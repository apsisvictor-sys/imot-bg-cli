package scraper

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"

	"github.com/apsisvictor/imot-cli/internal/translit"
)

const (
	BaseURL      = "https://www.imot.bg/obiavi"
	FormURL      = "https://www.imot.bg/pcgi/imot.cgi"
	UserAgent    = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	RequestDelay = 1500 * time.Millisecond
	PerPage      = 40
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

// FetchPage fetches a page from imot.bg and returns UTF-8 decoded HTML
func (c *Client) FetchPage(pageURL string) (string, error) {
	req, err := http.NewRequest("GET", pageURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "bg-BG,bg;q=0.9,en;q=0.8")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching page: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d for %s", resp.StatusCode, pageURL)
	}

	// Decode windows-1251 to UTF-8
	decoder := charmap.Windows1251.NewDecoder()
	utf8Reader := transform.NewReader(resp.Body, decoder)
	body, err := io.ReadAll(utf8Reader)
	if err != nil {
		return "", fmt.Errorf("decoding response: %w", err)
	}

	return string(body), nil
}

// resolveNeighborhoodSlug converts a Bulgarian neighborhood name to a URL slug.
// First tries transliteration. If the transliterated URL returns 404, falls back
// to POST form resolution via f40 parameter.
func (c *Client) resolveNeighborhoodSlug(params SearchParams) string {
	if params.Neighborhood == "" {
		return ""
	}

	// Try transliteration first
	slug := translit.ToSlug(params.Neighborhood)
	if slug == "" {
		return ""
	}

	// Verify the transliterated slug works by probing the URL
	testURL := buildURLWithSlug(params, slug, 1)
	_, err := c.FetchPage(testURL)
	if err == nil {
		return slug
	}

	// Transliterated URL failed — try POST form to resolve correct slug
	resolvedSlug := c.resolveSlugViaPost(params)
	if resolvedSlug != "" {
		return resolvedSlug
	}

	// Fall back to transliteration even if unverified
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

	// Don't follow redirects — we just want the Location header
	checkClient := &http.Client{
		Timeout: 15 * time.Second,
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

// Search fetches and parses listings from imot.bg.
// When params.Pages == 0, it auto-detects the total count from the first page
// and scrapes all pages. When params.Pages > 0, it scrapes exactly that many pages.
func (c *Client) Search(params SearchParams) ([]Listing, error) {
	// Resolve neighborhood slug if neighborhood is specified
	neighborhoodSlug := ""
	if params.Neighborhood != "" {
		neighborhoodSlug = c.resolveNeighborhoodSlug(params)
	}

	// Fetch page 1
	url1 := buildURLWithSlug(params, neighborhoodSlug, 1)
	html, err := c.FetchPage(url1)
	if err != nil {
		return nil, fmt.Errorf("page 1: %w", err)
	}

	listings := ParseListings(html)
	allListings := listings

	// Determine total pages
	totalPages := params.Pages
	if params.Pages == 0 {
		totalCount := ParseTotalCount(html)
		if totalCount > 0 {
			totalPages = (totalCount + PerPage - 1) / PerPage
		} else {
			// Couldn't parse total count (e.g., "1000+ обяви").
			// Use a safety cap and rely on break-on-empty-listings.
			if len(listings) >= PerPage {
				totalPages = reMaxPages
			} else {
				totalPages = 1
			}
		}
	}

	// Fetch remaining pages
	for page := 2; page <= totalPages; page++ {
		time.Sleep(RequestDelay)

		pageURL := buildURLWithSlug(params, neighborhoodSlug, page)
		pageHTML, err := c.FetchPage(pageURL)
		if err != nil {
			// Stop on error — return what we have
			break
		}

		pageListings := ParseListings(pageHTML)
		if len(pageListings) == 0 {
			// No more listings — we've reached the end
			break
		}
		allListings = append(allListings, pageListings...)
	}

	return allListings, nil
}
