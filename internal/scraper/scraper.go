package scraper

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"

	"github.com/apsisvictor/imot-cli/internal/translit"
)

const (
	BaseURL         = "https://www.imot.bg/obiavi"
	SearchBaseURL   = "https://www.imot.bg/search"
	FormURL         = "https://www.imot.bg/pcgi/imot.cgi"
	UserAgent       = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	SearchPageDelay = 3000 * time.Millisecond // 3s between search result pages
	DetailPageDelay = 8000 * time.Millisecond // 8s between detail page fetches (single-detail path)
	PerPage         = 40

	// Polite concurrency defaults for search --full detail enrichment.
	// Baked in on purpose: callers (and models) never pass these as flags.
	DetailWorkers         = 3                       // simultaneous detail fetches
	ConcurrentDetailDelay = 2000 * time.Millisecond // base spacing per worker, +-30% jitter
)

// Client is the HTTP scraper for imot.bg
type Client struct {
	httpClient *http.Client
}

// NewClient creates a new scraper client
func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// jitteredSleep sleeps for base +- 30% random jitter to mimic human browsing.
func jitteredSleep(base time.Duration) {
	jitter := time.Duration(float64(base) * (0.7 + 0.6*rand.Float64()))
	time.Sleep(jitter)
}

// HTTPError is the typed error for a non-200 source response. It lets a caller
// treat a verified 404 (the advert is gone) differently from a 403 or 429 (the
// source refused the request) without parsing error strings.
type HTTPError struct {
	StatusCode int
	URL        string
	// EffectiveURL is the URL the source finally served after redirects.
	EffectiveURL string
	// RetryAfterSeconds is the parsed Retry-After header; nil when the source
	// sent no such header or sent an unparseable value.
	RetryAfterSeconds *int
}

// Error keeps the original "HTTP <status> for <url>" wording.
func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d for %s", e.StatusCode, e.URL)
}

// DecodeHTMLBytes converts saved page bytes to UTF-8. imot.bg serves
// windows-1251: fixtures captured through the same path arrive as cp1251 bytes,
// while fixtures saved after decoding are already valid UTF-8. Valid UTF-8 is
// passed through unchanged, so the offline parse path needs no encoding flag.
func DecodeHTMLBytes(raw []byte) string {
	if utf8.Valid(raw) {
		return string(raw)
	}
	decoded, err := io.ReadAll(transform.NewReader(bytes.NewReader(raw), charmap.Windows1251.NewDecoder()))
	if err != nil {
		return string(raw)
	}
	return string(decoded)
}

// FetchPage fetches a page from imot.bg and returns UTF-8 decoded HTML
func (c *Client) FetchPage(pageURL string) (string, error) {
	html, _, err := c.fetchPageMeta(pageURL)
	return html, err
}

// parseRetryAfter reads an HTTP Retry-After header, the source's own pause
// instruction. It accepts the delta-seconds form and the HTTP-date form, and
// returns nil when the header is absent or does not parse, so an unprovable
// value is omitted rather than guessed.
func parseRetryAfter(header string, now time.Time) *int {
	v := strings.TrimSpace(header)
	if v == "" {
		return nil
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return nil
		}
		return &secs
	}
	if when, err := http.ParseTime(v); err == nil {
		secs := int(when.Sub(now).Seconds())
		if secs < 0 {
			secs = 0
		}
		return &secs
	}
	return nil
}

// fetchPageMeta is FetchPage plus the URL the source finally served. The
// effective URL is error evidence: a redirect can land on a challenge page or
// on another advert, and the requested URL alone would hide that.
func (c *Client) fetchPageMeta(pageURL string) (string, string, error) {
	req, err := http.NewRequest("GET", pageURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "bg-BG,bg;q=0.9,en;q=0.8")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("fetching page: %w", err)
	}
	defer resp.Body.Close()

	effectiveURL := pageURL
	if resp.Request != nil && resp.Request.URL != nil && resp.Request.URL.String() != "" {
		effectiveURL = resp.Request.URL.String()
	}

	if resp.StatusCode != http.StatusOK {
		return "", effectiveURL, &HTTPError{
			StatusCode:        resp.StatusCode,
			URL:               pageURL,
			EffectiveURL:      effectiveURL,
			RetryAfterSeconds: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
		}
	}

	// Decode windows-1251 to UTF-8
	decoder := charmap.Windows1251.NewDecoder()
	utf8Reader := transform.NewReader(resp.Body, decoder)
	body, err := io.ReadAll(utf8Reader)
	if err != nil {
		return "", effectiveURL, fmt.Errorf("decoding response: %w", err)
	}

	return string(body), effectiveURL, nil
}

// FetchDetail fetches and parses a single listing's detail page.
// Applies rate limiting before the request to mimic human browsing.
func (c *Client) FetchDetail(listingURL string) (DetailListing, error) {
	jitteredSleep(DetailPageDelay)
	return c.fetchDetailNow(listingURL)
}

// fetchDetailNow fetches and parses a detail page without the pre-request
// politeness sleep; the concurrent pool applies its own spacing.
//
// The page must prove it is an advert page and that it belongs to the requested
// advert; a challenge page, a removal notice, an unreadable page or a page with
// absent/wrong advert identity is returned as a typed *DetailError, never as a
// DetailListing with substituted or empty fields.
func (c *Client) fetchDetailNow(listingURL string) (DetailListing, error) {
	html, effectiveURL, err := c.fetchPageMeta(listingURL)
	if err != nil {
		fetchErr := &DetailError{
			Kind:              DetailErrorFetchFailed,
			RequestedURL:      listingURL,
			RequestedAdvertID: AdvertIDFromURL(listingURL),
			EffectiveURL:      effectiveURL,
			Message:           err.Error(),
		}
		var httpErr *HTTPError
		if errors.As(err, &httpErr) {
			fetchErr.HTTPStatus = httpErr.StatusCode
			if httpErr.EffectiveURL != "" {
				fetchErr.EffectiveURL = httpErr.EffectiveURL
			}
			fetchErr.RetryAfterSeconds = httpErr.RetryAfterSeconds
		}
		return DetailListing{}, fetchErr
	}
	return ParseDetailPageWithMeta(html, listingURL, effectiveURL)
}

// FetchDetailsConcurrent enriches listings with detail-page data using a
// bounded, polite worker pool (DetailWorkers x ConcurrentDetailDelay +-30%);
// effective spacing stays under ~0.7 requests/second. No flags required.
// Returns one error per listing index (nil when that listing enriched fine);
// individual failures never abort the whole run.
func (c *Client) FetchDetailsConcurrent(listings []Listing) []error {
	errs := make([]error, len(listings))
	if len(listings) == 0 {
		return errs
	}

	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < DetailWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				jitteredSleep(ConcurrentDetailDelay)
				detail, err := c.fetchDetailNow(listings[i].URL)
				if err != nil {
					errs[i] = err
					continue
				}
				listings[i].Detail = &detail
			}
		}()
	}
	for i := range listings {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return errs
}

// resolveNeighborhoodSlugEvidence converts a Bulgarian neighborhood name to a
// URL slug and reports how the slug was obtained, so a caller can tell a
// source-confirmed slug from an unverified transliteration guess. The returned
// evidence is one of the NeighborhoodResolution* constants in types.go.
//
// First tries transliteration. If the transliterated URL returns 404, falls back
// to POST form resolution via f40 parameter. When neither confirms the slug it
// still returns the transliteration (the query has to address something), but
// the evidence records that the slug is unproven.
func (c *Client) resolveNeighborhoodSlugEvidence(params SearchParams) (string, string) {
	if params.Neighborhood == "" {
		return "", ""
	}

	// Try transliteration first
	slug := translit.ToSlug(params.Neighborhood)
	if slug == "" {
		return "", NeighborhoodResolutionUnresolved
	}

	// Verify the transliterated slug works by probing the URL
	testURL := buildURLWithSlug(params, slug, 1)
	if _, err := c.FetchPage(testURL); err == nil {
		return slug, NeighborhoodResolutionProbe
	}

	// Transliterated URL failed — try POST form to resolve correct slug
	if resolvedSlug := c.resolveSlugViaPost(params); resolvedSlug != "" {
		return resolvedSlug, NeighborhoodResolutionRedirect
	}

	// Fall back to transliteration even if unverified
	return slug, NeighborhoodResolutionUnverified
}

// resolveNeighborhoodSlug keeps its original signature for callers that only
// need the slug.
func (c *Client) resolveNeighborhoodSlug(params SearchParams) string {
	slug, _ := c.resolveNeighborhoodSlugEvidence(params)
	return slug
}

// resolveSlugViaPost uses the imot.bg search form (POST with f40) to resolve
// a neighborhood name to its canonical URL slug by following the redirect.
func (c *Client) resolveSlugViaPost(params SearchParams) string {
	category := "prodazhbi"
	if params.Rent {
		category = "naemi"
	}

	formData := url.Values{}
	formData.Set("act", "srch")
	formData.Set("rub", category)

	citySlug := resolveCitySlug(params.City)
	formData.Set("loc", citySlug)

	typeSlug := ""
	if params.Type != "" {
		typeSlug = resolveTypeSlug(params.Type)
	}
	if typeSlug != "" {
		formData.Set("type", typeSlug)
	}

	// f40 is the neighborhood filter: +-separated names with trailing +
	formData.Set("f40", params.Neighborhood+"+")

	req, err := http.NewRequest("POST", FormURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	// Don't follow redirects — we just want the Location header. The form client
	// reuses the caller's transport (and therefore its proxy/pacing settings)
	// rather than opening an unrelated connection path.
	transport := http.DefaultTransport
	if c.httpClient != nil && c.httpClient.Transport != nil {
		transport = c.httpClient.Transport
	}
	checkClient := &http.Client{
		Timeout:   15 * time.Second,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := checkClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusMovedPermanently || resp.StatusCode == http.StatusFound {
		loc := resp.Header.Get("Location")
		if loc != "" {
			return extractNeighborhoodFromURL(loc, citySlug, typeSlug)
		}
	}
	return ""
}

// extractNeighborhoodFromURL parses a redirect URL like
// "/obiavi/prodazhbi/grad-sofiya/banishora/dvustaen"
// and extracts the neighborhood slug.
func extractNeighborhoodFromURL(redirectURL, citySlug, typeSlug string) string {
	// Handle relative URLs
	u := redirectURL
	if strings.HasPrefix(u, "https://www.imot.bg") {
		u = strings.TrimPrefix(u, "https://www.imot.bg")
	} else if strings.HasPrefix(u, "http://www.imot.bg") {
		u = strings.TrimPrefix(u, "http://www.imot.bg")
	}

	parts := strings.Split(strings.Trim(u, "/"), "/")
	// Expected: ["obiavi", "prodazhbi"|"naemi", citySlug, neighborhoodSlug?, typeSlug?]
	// Find the part between citySlug and typeSlug
	cityIdx := -1
	for i, p := range parts {
		if p == citySlug {
			cityIdx = i
			break
		}
	}
	if cityIdx < 0 || cityIdx+1 >= len(parts) {
		return ""
	}

	// Check if there are enough parts for a neighborhood slug
	remaining := parts[cityIdx+1:]
	// Filter out pagination segments
	var filtered []string
	for _, p := range remaining {
		if strings.HasPrefix(p, "p-") || p == typeSlug {
			continue
		}
		filtered = append(filtered, p)
	}

	if len(filtered) == 1 {
		return filtered[0]
	}
	// Multiple non-type segments — join them
	return strings.Join(filtered, "-")
}

// buildURLWithSlug constructs the URL given a known neighborhood slug
func buildURLWithSlug(params SearchParams, neighborhoodSlug string, page int) string {
	var parts []string

	parts = append(parts, BaseURL)

	// Category: prodazhbi or naemi
	category := "prodazhbi"
	if params.Rent {
		category = "naemi"
	}
	parts = append(parts, category)

	// City slug
	citySlug := resolveCitySlug(params.City)
	if citySlug != "" {
		parts = append(parts, citySlug)
	}

	// Neighborhood slug
	if neighborhoodSlug != "" {
		parts = append(parts, neighborhoodSlug)
	}

	// Type slug
	typeSlug := ""
	if params.Type != "" {
		typeSlug = resolveTypeSlug(params.Type)
	}
	if typeSlug != "" {
		parts = append(parts, typeSlug)
	}

	u := strings.Join(parts, "/")

	// Pagination: /p-N path segment
	if page > 1 {
		u += fmt.Sprintf("/p-%d", page)
	} else {
		u += "/"
	}

	// These query parameters are the URLs produced by the source's own search
	// form (f31/f32). They are source-side filters, unlike the legacy price/size
	// flags which remain client-side row filters.
	floorQuery := url.Values{}
	if params.FloorFrom != nil {
		floorQuery.Set("floor_from", strconv.Itoa(*params.FloorFrom))
	}
	if params.FloorTo != nil {
		floorQuery.Set("floor_to", strconv.Itoa(*params.FloorTo))
	}
	if encoded := floorQuery.Encode(); encoded != "" {
		u += "?" + encoded
	}

	return u
}

// BuildURL constructs the imot.bg URL from search params
func BuildURL(params SearchParams, page int) string {
	return buildURLWithSlug(params, "", page)
}

func resolveCitySlug(city string) string {
	if slug, ok := CityMap[city]; ok {
		return slug
	}
	if slug, ok := OblastMap[city]; ok {
		return slug
	}
	return ""
}

func resolveTypeSlug(propType string) string {
	if slug, ok := TypeMap[propType]; ok {
		return slug
	}
	return ""
}

// CitySlug returns the imot.bg URL slug for a known city or oblast name, or ""
// when the name is not one this CLI can address. It is the exported twin of the
// internal resolver, for callers that need the slug without a live request.
func CitySlug(city string) string {
	return resolveCitySlug(city)
}

// CityPageURL returns the sales search page whose type filter advertises the
// property-type taxonomy, or "" for an unknown city. The search form, not the
// results page, is the source: the results page mixes type links with
// neighbourhood links of the same URL shape, while the form labels every type
// as a checkbox.
func CityPageURL(city string) string {
	slug := resolveCitySlug(city)
	if slug == "" {
		return ""
	}
	return SearchBaseURL + "/prodazhbi/" + slug
}

// FetchTaxonomy fetches the city search page and reads its advertised type
// taxonomy. It is the live twin of ParseTaxonomy and makes exactly one request.
func (c *Client) FetchTaxonomy(city string) (Taxonomy, error) {
	slug := resolveCitySlug(city)
	pageURL := CityPageURL(city)
	if pageURL == "" {
		return Taxonomy{}, fmt.Errorf("unknown city %q: no imot.bg city slug is known for it", city)
	}
	html, err := c.FetchPage(pageURL)
	if err != nil {
		return Taxonomy{}, fmt.Errorf("fetching taxonomy page: %w", err)
	}
	return ParseTaxonomy(html, TaxonomyParams{City: city, CitySlug: slug, SourceURL: pageURL})
}

// Search fetches and parses listings from imot.bg.
// When params.Pages == 0, it auto-detects the total count from the first page
// and scrapes all pages. When params.Pages > 0, it scrapes exactly that many pages.
func (c *Client) Search(params SearchParams) ([]Listing, error) {
	result, err := c.SearchWithMeta(params)
	if err != nil {
		return nil, err
	}
	// An unreadable first page yields no listings plus a typed error entry.
	// Surface it here too, so callers of Search (which discards the metadata)
	// cannot read a block/captcha/layout failure as an empty market.
	if len(result.Listings) == 0 && !result.EmptyVerified {
		for _, e := range result.Errors {
			if e.Kind == SearchErrorUnreadablePage {
				return nil, fmt.Errorf("%w: %s", ErrUnreadablePage, e.URL)
			}
		}
	}
	return result.Listings, nil
}

// SearchFilters documents, for one query, which filters the CLI applies in the
// imot.bg request URL (server_filters) and which it applies only to rows it has
// already downloaded (client_filters).
//
// The URL built by buildURLWithSlug carries category, city, neighborhood, type
// and the verified source-side floor bounds. Price and size remain client-side:
// their CLI flags narrow downloaded rows but do not narrow the source total.
// A caller must never use those client filters to claim cap decomposition.
// ServerFilterSupport names every filter this CLI can apply at the SOURCE,
// regardless of what one query actually used.
//
// This is deliberately a separate answer from SearchFilters. SearchFilters reports
// what a particular request narrowed, so an unfiltered query truthfully omits
// "type" and "floor". A client deciding which dimensions it may decompose along
// needs the capability instead.
func ServerFilterSupport() []string {
	return []string{"city", "neighborhood", "type", "floor"}
}

func SearchFilters(params SearchParams, neighborhoodSlug string) (server, client []string) {
	server = []string{}
	client = []string{}
	if resolveCitySlug(params.City) != "" {
		server = append(server, "city")
	}
	if neighborhoodSlug != "" {
		server = append(server, "neighborhood")
	}
	if params.Type != "" && resolveTypeSlug(params.Type) != "" {
		server = append(server, "type")
	}
	if params.FloorFrom != nil || params.FloorTo != nil {
		server = append(server, "floor")
	}
	if params.MinPrice > 0 {
		client = append(client, "min_price")
	}
	if params.MaxPrice > 0 {
		client = append(client, "max_price")
	}
	if params.MinSqM > 0 {
		client = append(client, "min_sqm")
	}
	if params.MaxSqM > 0 {
		client = append(client, "max_sqm")
	}
	return server, client
}

// SearchWithMeta fetches listings and returns scrape integrity metadata for automation.
func (c *Client) SearchWithMeta(params SearchParams) (SearchResult, error) {
	result := SearchResult{
		RequestedCity:         params.City,
		RequestedNeighborhood: params.Neighborhood,
		RequestedType:         params.Type,
		// Never nil: the envelope contract types listings as an array, and a
		// null array with total_count 0 is what consumers read as "verified
		// empty". EmptyVerified carries that meaning explicitly instead.
		Listings: []Listing{},
	}

	// Resolve neighborhood slug if neighborhood is specified
	neighborhoodSlug := ""
	if params.Neighborhood != "" {
		neighborhoodSlug, result.NeighborhoodResolution = c.resolveNeighborhoodSlugEvidence(params)
		result.ResolvedNeighborhoodSlug = neighborhoodSlug
	}
	result.ServerFilters, result.ClientFilters = SearchFilters(params, neighborhoodSlug)
	result.ServerFilterSupport = ServerFilterSupport()

	// Fetch page 1
	url1 := buildURLWithSlug(params, neighborhoodSlug, 1)
	html, err := c.FetchPage(url1)
	if err != nil {
		return result, fmt.Errorf("page 1: %w", err)
	}

	page1 := ScanListings(html)
	listings := page1.Listings
	result.Listings = append(result.Listings, listings...)
	result.absorbCardScan(page1)
	result.PagesFetched = 1
	result.TotalCount, result.TotalCountReported, result.TotalCountCapped = ParseTotalCountEvidence(html)
	noResultsMarker := HasNoResultsMarker(html)

	// A page with no cards, no source total and no explicit no-results marker is
	// not a search result page: a block page, a captcha or a changed layout.
	// Reporting it as total_count 0 would be indistinguishable from an empty
	// market, so surface it as a typed page failure instead.
	if len(listings) == 0 && result.TotalCount == 0 && !noResultsMarker {
		result.Partial = true
		result.PagesFetched = 0
		result.PagesPlanned = 1
		result.Errors = append(result.Errors, SearchError{
			Page:  1,
			URL:   url1,
			Kind:  SearchErrorUnreadablePage,
			Error: ErrUnreadablePage.Error(),
		})
		return result, nil
	}

	// Only imot.bg's own marker proves an empty result set.
	if noResultsMarker && len(listings) == 0 && result.TotalCount == 0 {
		result.EmptyVerified = true
	}

	// Determine total pages
	totalPages := params.Pages
	if params.Pages == 0 {
		if result.TotalCount > 0 {
			totalPages = (result.TotalCount + PerPage - 1) / PerPage
		} else {
			// The source printed no parseable total count. Use a safety cap and
			// rely on break-on-empty-listings.
			if len(listings) >= PerPage {
				totalPages = reMaxPages
				result.Partial = true
				result.Errors = append(result.Errors, SearchError{Page: 1, URL: url1, Kind: SearchErrorTotalCountUnknown, Error: "total count unavailable; using safety page cap"})
			} else {
				totalPages = 1
			}
		}
	}
	result.PagesPlanned = totalPages

	// Fetch remaining pages
	for page := 2; page <= totalPages; page++ {
		jitteredSleep(SearchPageDelay)

		pageURL := buildURLWithSlug(params, neighborhoodSlug, page)
		pageHTML, err := c.FetchPage(pageURL)
		if err != nil {
			result.Partial = true
			result.Errors = append(result.Errors, SearchError{Page: page, URL: pageURL, Kind: SearchErrorFetchFailed, Error: err.Error()})
			break
		}

		pageScan := ScanListings(pageHTML)
		result.absorbCardScan(pageScan)
		pageListings := pageScan.Listings
		if len(pageListings) == 0 {
			// Reaching the end is only credible when the source says so. A page
			// that is neither a result page nor the explicit no-results page is
			// a read failure when a later page was genuinely expected.
			if !HasNoResultsMarker(pageHTML) && (params.Pages > 0 || result.TotalCount > 0) {
				result.Partial = true
				result.Errors = append(result.Errors, SearchError{
					Page:  page,
					URL:   pageURL,
					Kind:  SearchErrorUnreadablePage,
					Error: ErrUnreadablePage.Error(),
				})
			}
			break
		}
		result.PagesFetched++
		result.Listings = append(result.Listings, pageListings...)
	}

	if result.TotalCount > 0 && len(result.Listings) < result.TotalCount && result.PagesFetched < result.PagesPlanned {
		result.Partial = true
	}

	return result, nil
}
