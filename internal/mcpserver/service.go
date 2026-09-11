package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/apsisvictor/imot-cli/internal/scraper"
)

// Tool names, kept as constants so logs, tests, and docs cannot drift.
const (
	ToolSearchListings = "search_listings"
	ToolGetListing     = "get_listing"
	ToolListFilters    = "list_supported_filters"
)

// ListingSummary is the compact per-listing shape returned to the model. It is
// the MCP surface's own projection: agency and building year matter to working
// agents, so they are included even though the CLI's quiet projection omits them.
type ListingSummary struct {
	ID           string  `json:"id" jsonschema:"imot.bg listing id; pass it to get_listing for full detail"`
	Type         string  `json:"type" jsonschema:"property type, uppercase Bulgarian, for example 2-СТАЕН"`
	Neighborhood string  `json:"neighborhood" jsonschema:"neighborhood or quarter as listed"`
	PriceEUR     int     `json:"price_eur" jsonschema:"asking price in EUR (monthly rent when the query was a rental search)"`
	SizeSqM      int     `json:"size_sqm" jsonschema:"size in square metres"`
	PricePerSqM  float64 `json:"price_per_sqm" jsonschema:"price per square metre in EUR"`
	Floor        string  `json:"floor,omitempty" jsonschema:"floor, for example 4 от 7"`
	YearBuilt    string  `json:"year_built,omitempty" jsonschema:"year of construction, may be a range or empty"`
	Seller       string  `json:"seller" jsonschema:"agency or private"`
	Agency       string  `json:"agency,omitempty" jsonschema:"listing agency name; empty for a private seller"`
	Phone        string  `json:"phone,omitempty" jsonschema:"contact phone as shown on the listing card"`
	Snippet      string  `json:"snippet,omitempty" jsonschema:"truncated listing description"`
	URL          string  `json:"url" jsonschema:"canonical imot.bg listing URL"`
}

// SearchListingsInput is the argument shape for search_listings.
type SearchListingsInput struct {
	City         string `json:"city" jsonschema:"Bulgarian city name, for example София, Пловдив, Варна. Required."`
	Neighborhood string `json:"neighborhood,omitempty" jsonschema:"Neighborhood or quarter, for example Лозенец, Яворов, Младост 1. Transliteration such as lozenets also works. Matched partially."`
	PropertyType string `json:"property_type,omitempty" jsonschema:"One of: 1-стаен, 2-стаен, 3-стаен, 4-стаен, многостаен, мезонет, къща, вила, офис, магазин, заведение, склад, гараж, парцел, ателие."`
	Rent         bool   `json:"rent,omitempty" jsonschema:"True for rentals, false or omitted for sales."`
	MinPriceEUR  int    `json:"min_price_eur,omitempty" jsonschema:"Minimum asking price in EUR."`
	MaxPriceEUR  int    `json:"max_price_eur,omitempty" jsonschema:"Maximum asking price in EUR."`
	MinSizeSqM   int    `json:"min_size_sqm,omitempty" jsonschema:"Minimum size in square metres."`
	MaxSizeSqM   int    `json:"max_size_sqm,omitempty" jsonschema:"Maximum size in square metres."`
	Pages        int    `json:"pages,omitempty" jsonschema:"Result pages to fetch, 1 to 3. Each page holds about 40 listings. Default 1."`
	Limit        int    `json:"limit,omitempty" jsonschema:"Maximum listings to return. Default 40."`
	Refresh      bool   `json:"refresh,omitempty" jsonschema:"Force a fresh scrape instead of reusing the cached answer. Consumes the shared live-fetch budget; use only when the cached answer is too old."`
}

// QuerySummary echoes the interpreted query so the model can confirm what ran.
type QuerySummary struct {
	City         string `json:"city"`
	Neighborhood string `json:"neighborhood,omitempty"`
	PropertyType string `json:"property_type,omitempty"`
	Rent         bool   `json:"rent"`
	MinPriceEUR  int    `json:"min_price_eur,omitempty"`
	MaxPriceEUR  int    `json:"max_price_eur,omitempty"`
	MinSizeSqM   int    `json:"min_size_sqm,omitempty"`
	MaxSizeSqM   int    `json:"max_size_sqm,omitempty"`
	Pages        int    `json:"pages"`
}

// SearchListingsOutput is the result of search_listings.
type SearchListingsOutput struct {
	Query         QuerySummary        `json:"query"`
	Stats         scraper.SearchStats `json:"stats" jsonschema:"price distribution for the returned listings"`
	Listings      []ListingSummary    `json:"listings"`
	TotalMatching int                 `json:"total_matching" jsonschema:"listings imot.bg reports for this query, which may exceed the returned count"`
	Returned      int                 `json:"returned"`
	PagesFetched  int                 `json:"pages_fetched"`
	Sampled       bool                `json:"sampled" jsonschema:"true when fewer listings were returned than match the query"`
	Partial       bool                `json:"partial" jsonschema:"true when some result pages failed to load"`
	Source        string              `json:"source" jsonschema:"cache or live"`
	FetchedAt     string              `json:"fetched_at" jsonschema:"RFC3339 timestamp of when the data was scraped"`
	AgeSeconds    int                 `json:"age_seconds" jsonschema:"age of the data in seconds"`
	Notes         []string            `json:"notes,omitempty"`
}

// GetListingInput is the argument shape for get_listing.
type GetListingInput struct {
	ListingID     string `json:"listing_id" jsonschema:"The imot.bg listing id, the code in obiava-<id>-... in the listing URL. Required."`
	IncludeDetail *bool  `json:"include_detail,omitempty" jsonschema:"Include the full description, feature tags, broker contact and gallery photos. Default true."`
	Refresh       bool   `json:"refresh,omitempty" jsonschema:"Force a fresh page fetch instead of using the cached copy."`
}

// GetListingOutput is the result of get_listing.
type GetListingOutput struct {
	ListingID  string                 `json:"listing_id"`
	URL        string                 `json:"url"`
	Detail     *scraper.DetailListing `json:"detail,omitempty"`
	Source     string                 `json:"source"`
	FetchedAt  string                 `json:"fetched_at"`
	AgeSeconds int                    `json:"age_seconds"`
	Notes      []string               `json:"notes,omitempty"`
}

// ListFiltersInput takes no arguments.
type ListFiltersInput struct{}

// ListFiltersOutput lists the vocabulary the search tool understands.
type ListFiltersOutput struct {
	Cities        []string `json:"cities"`
	PropertyTypes []string `json:"property_types"`
	Notes         []string `json:"notes"`
}

// Service implements the MCP tools for one identity.
type Service struct {
	cfg      Config
	cache    *Cache
	limiter  *Limiter
	client   *scraper.Client
	logger   *slog.Logger
	identity string
}

// newService builds the tool layer for one identity.
func newService(cfg Config, cache *Cache, limiter *Limiter, identity string, logger *slog.Logger) *Service {
	return &Service{
		cfg:      cfg,
		cache:    cache,
		limiter:  limiter,
		client:   scraper.NewClient(),
		logger:   logger,
		identity: identity,
	}
}

// SearchListings answers a listing query, cache first.
func (s *Service) SearchListings(ctx context.Context, in SearchListingsInput) (SearchListingsOutput, error) {
	started := time.Now()

	city := strings.TrimSpace(in.City)
	if city == "" {
		return SearchListingsOutput{}, fmt.Errorf("city is required")
	}
	city = normalizeCity(city)

	propertyType := ""
	if trimmed := strings.TrimSpace(in.PropertyType); trimmed != "" {
		propertyType = normalizeType(trimmed)
	}

	pages := clampInt(in.Pages, s.cfg.DefaultPages, 1, s.cfg.MaxPages)
	limit := clampInt(in.Limit, s.cfg.DefaultLimit, 1, s.cfg.MaxLimit)

	params := scraper.SearchParams{
		City:         city,
		Neighborhood: strings.TrimSpace(in.Neighborhood),
		Type:         propertyType,
		Rent:         in.Rent,
		MinPrice:     in.MinPriceEUR,
		MaxPrice:     in.MaxPriceEUR,
		MinSqM:       in.MinSizeSqM,
		MaxSqM:       in.MaxSizeSqM,
		Pages:        pages,
	}
	query := QuerySummary{
		City:         params.City,
		Neighborhood: params.Neighborhood,
		PropertyType: params.Type,
		Rent:         params.Rent,
		MinPriceEUR:  params.MinPrice,
		MaxPriceEUR:  params.MaxPrice,
		MinSizeSqM:   params.MinSqM,
		MaxSizeSqM:   params.MaxSqM,
		Pages:        params.Pages,
	}
	key := searchKey(params)

	if !in.Refresh {
		if payload, createdAt, ok, err := s.cache.GetSearch(key, s.cfg.SearchCacheTTL); err != nil {
			s.logger.Warn("search cache read failed", "error", err)
		} else if ok {
			var cached SearchListingsOutput
			if err := json.Unmarshal(payload, &cached); err == nil {
				s.record(ToolSearchListings, true, time.Since(started), len(cached.Listings))
				cached.Source = "cache"
				cached.AgeSeconds = int(time.Since(createdAt).Seconds())
				cached.FetchedAt = createdAt.UTC().Format(time.RFC3339)
				cached.Listings = truncateListings(cached.Listings, limit)
				cached.Returned = len(cached.Listings)
				cached.Notes = refreshNotes(cached, true)
				return cached, nil
			}
			s.logger.Warn("search cache payload was unreadable; refetching")
		}
	}

	if err := s.authorizeLive(ToolSearchListings); err != nil {
		return SearchListingsOutput{}, err
	}
	release, err := s.limiter.Acquire(ctx)
	if err != nil {
		return SearchListingsOutput{}, err
	}
	defer release()

	result, err := s.client.SearchWithMeta(params)
	if err != nil {
		s.record(ToolSearchListings, false, time.Since(started), 0)
		return SearchListingsOutput{}, fmt.Errorf("live search failed: %w", err)
	}

	out := SearchListingsOutput{
		Query:         query,
		Listings:      toSummaries(result.Listings),
		Stats:         scraper.ComputeStats(result.Listings),
		TotalMatching: result.TotalCount,
		PagesFetched:  result.PagesFetched,
		Partial:       result.Partial,
		Source:        "live",
		FetchedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	out.Listings = truncateListings(out.Listings, limit)
	out.Returned = len(out.Listings)
	out.Sampled = out.TotalMatching > out.Returned
	out.Notes = refreshNotes(out, false)

	if payload, err := json.Marshal(out); err == nil {
		if err := s.cache.PutSearch(key, payload, len(out.Listings)); err != nil {
			s.logger.Warn("search cache write failed", "error", err)
		}
	}
	s.record(ToolSearchListings, false, time.Since(started), out.Returned)
	return out, nil
}

// GetListing returns one listing's detail page data, cache first.
func (s *Service) GetListing(ctx context.Context, in GetListingInput) (GetListingOutput, error) {
	started := time.Now()

	id := strings.TrimSpace(in.ListingID)
	if id == "" {
		return GetListingOutput{}, fmt.Errorf("listing_id is required")
	}
	// Accept a full imot.bg URL as well as a bare id, because models often have
	// one and not the other.
	if strings.Contains(id, "://") {
		id = listingIDFromURL(id)
		if id == "" {
			return GetListingOutput{}, fmt.Errorf("could not read a listing id from that URL")
		}
	}
	url := "https://www.imot.bg/obiava-" + id

	includeDetail := true
	if in.IncludeDetail != nil {
		includeDetail = *in.IncludeDetail
	}
	if !includeDetail {
		return GetListingOutput{
			ListingID: id,
			URL:       url,
			Source:    "none",
			FetchedAt: time.Now().UTC().Format(time.RFC3339),
			Notes:     []string{"Detail was not requested; call again with include_detail true for description, features and contacts."},
		}, nil
	}

	if !in.Refresh {
		if payload, fetchedAt, ok, err := s.cache.GetDetail(id, s.cfg.DetailCacheTTL); err != nil {
			s.logger.Warn("detail cache read failed", "error", err)
		} else if ok {
			var detail scraper.DetailListing
			if err := json.Unmarshal(payload, &detail); err == nil {
				s.record(ToolGetListing, true, time.Since(started), 1)
				return GetListingOutput{
					ListingID:  id,
					URL:        firstNonEmpty(detail.URL, url),
					Detail:     &detail,
					Source:     "cache",
					FetchedAt:  fetchedAt.UTC().Format(time.RFC3339),
					AgeSeconds: int(time.Since(fetchedAt).Seconds()),
				}, nil
			}
			s.logger.Warn("detail cache payload was unreadable; refetching")
		}
	}

	if err := s.authorizeLive(ToolGetListing); err != nil {
		return GetListingOutput{}, err
	}
	release, err := s.limiter.Acquire(ctx)
	if err != nil {
		return GetListingOutput{}, err
	}
	defer release()

	// Reuse the batch enrichment path: it applies the polite detail pacing
	// (about 2s) rather than the 8s single-detail delay, which matters for an
	// interactive tool call.
	listings := []scraper.Listing{{ID: id, URL: url}}
	errs := s.client.FetchDetailsConcurrent(listings)
	s.record(ToolGetListing, false, time.Since(started), 1)

	if len(errs) > 0 && errs[0] != nil {
		return GetListingOutput{}, fmt.Errorf("could not load listing %s: %w", id, errs[0])
	}
	if listings[0].Detail == nil {
		return GetListingOutput{}, fmt.Errorf("listing %s returned no detail page data", id)
	}

	detail := *listings[0].Detail
	now := time.Now().UTC()
	if payload, err := json.Marshal(detail); err == nil {
		if err := s.cache.PutDetail(id, payload); err != nil {
			s.logger.Warn("detail cache write failed", "error", err)
		}
	}
	return GetListingOutput{
		ListingID: id,
		URL:       firstNonEmpty(detail.URL, url),
		Detail:    &detail,
		Source:    "live",
		FetchedAt: now.Format(time.RFC3339),
	}, nil
}

// ListSupportedFilters returns the vocabulary the search tool accepts.
func (s *Service) ListSupportedFilters(ctx context.Context, _ ListFiltersInput) (ListFiltersOutput, error) {
	cities := make([]string, 0, len(scraper.CityMap))
	for name := range scraper.CityMap {
		cities = append(cities, name)
	}
	sort.Strings(cities)

	types := make([]string, 0, len(scraper.TypeMap))
	for name := range scraper.TypeMap {
		types = append(types, name)
	}
	sort.Strings(types)

	return ListFiltersOutput{
		Cities:        cities,
		PropertyTypes: types,
		Notes: []string{
			"Prices are in EUR. For rental searches the price is monthly rent.",
			"search_listings returns the first pages only; check the sampled flag before presenting a result as the whole market.",
			"Neighborhood accepts Bulgarian names or transliteration and matches partially.",
		},
	}, nil
}

// authorizeLive enforces the per-identity and shared live-fetch budgets. Cached
// answers are unaffected, so a busy period degrades new scraping rather than
// blocking all use.
func (s *Service) authorizeLive(tool string) error {
	since := time.Now().Add(-s.cfg.QuotaWindow)

	total, err := s.cache.LiveCountAll(since)
	if err == nil && total >= s.cfg.GlobalQuotaPerWindow {
		s.logger.Warn("shared live budget exhausted", "tool", tool, "identity", s.identity, "used", total)
		return fmt.Errorf("the shared live-scrape budget is exhausted for this %s window; cached results are still available, or ask again later", humanDuration(s.cfg.QuotaWindow))
	}

	mine, err := s.cache.LiveCount(s.identity, since)
	if err == nil && mine >= s.cfg.LiveQuotaPerWindow {
		return fmt.Errorf("your live-fetch limit is reached (%d per %s); cached results remain available, or ask again later", s.cfg.LiveQuotaPerWindow, humanDuration(s.cfg.QuotaWindow))
	}
	return nil
}

func (s *Service) record(tool string, cacheHit bool, took time.Duration, results int) {
	if err := s.cache.RecordUsage(s.identity, tool, cacheHit); err != nil {
		s.logger.Warn("usage record failed", "error", err)
	}
	s.logger.Info("tool call",
		"tool", tool,
		"identity", s.identity,
		"cache_hit", cacheHit,
		"duration_ms", took.Milliseconds(),
		"results", results,
	)
}

// searchKey builds a stable cache key for a normalized query.
func searchKey(p scraper.SearchParams) string {
	canonical := fmt.Sprintf("%s|%s|%s|%t|%d|%d|%d|%d|%d",
		p.City,
		strings.ToLower(strings.TrimSpace(p.Neighborhood)),
		p.Type,
		p.Rent,
		p.MinPrice, p.MaxPrice, p.MinSqM, p.MaxSqM,
		p.Pages,
	)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// toSummaries projects scraped listings onto the model-facing shape.
func toSummaries(listings []scraper.Listing) []ListingSummary {
	out := make([]ListingSummary, 0, len(listings))
	for _, l := range listings {
		seller := "private"
		if strings.TrimSpace(l.Agency) != "" {
			seller = "agency"
		}
		summary := ListingSummary{
			ID:           l.ID,
			Type:         l.Type,
			Neighborhood: l.Neighborhood,
			PriceEUR:     l.PriceEUR,
			SizeSqM:      l.SizeSqM,
			Floor:        l.Floor,
			YearBuilt:    l.YearBuilt,
			Seller:       seller,
			Agency:       strings.TrimSpace(l.Agency),
			Phone:        strings.TrimSpace(l.Phone),
			Snippet:      snippetOf(l.Description, 140),
			URL:          l.URL,
		}
		if l.SizeSqM > 0 && l.PriceEUR > 0 {
			summary.PricePerSqM = float64(l.PriceEUR) / float64(l.SizeSqM)
		}
		out = append(out, summary)
	}
	return out
}

// refreshNotes explains the freshness and coverage caveats attached to a result.
func refreshNotes(out SearchListingsOutput, fromCache bool) []string {
	var notes []string
	if out.Sampled {
		notes = append(notes, fmt.Sprintf(
			"Sampled result: showing %d of %d matching listings at imot.bg. Raise pages or narrow the filters for fuller coverage.",
			out.Returned, out.TotalMatching))
	}
	if out.Partial {
		notes = append(notes, "Some result pages failed to load, so counts and statistics may be incomplete.")
	}
	if fromCache && out.AgeSeconds > 600 {
		notes = append(notes, fmt.Sprintf(
			"Cached answer scraped %s ago. Set refresh true for a fresh fetch.",
			humanDuration(time.Duration(out.AgeSeconds)*time.Second)))
	}
	if out.Query.Rent {
		notes = append(notes, "Rental prices are monthly rent as advertised; utilities and fees are not included.")
	}
	if len(out.Listings) == 0 {
		notes = append(notes, "No listings matched. Try a wider neighborhood match or remove price and size filters.")
	}
	return notes
}

func truncateListings(listings []ListingSummary, limit int) []ListingSummary {
	if limit > 0 && len(listings) > limit {
		return listings[:limit]
	}
	return listings
}

// normalizeCity maps a city name to the exact key the scraper recognises, so a
// lower-case or differently-cased name still resolves.
func normalizeCity(raw string) string {
	for name := range scraper.CityMap {
		if strings.EqualFold(name, raw) {
			return name
		}
	}
	return raw
}

// normalizeType does the same for property types.
func normalizeType(raw string) string {
	for name := range scraper.TypeMap {
		if strings.EqualFold(name, raw) {
			return name
		}
	}
	return raw
}

// listingIDFromURL extracts the listing id from an imot.bg listing URL.
func listingIDFromURL(raw string) string {
	marker := "obiava-"
	idx := strings.Index(raw, marker)
	if idx < 0 {
		return ""
	}
	rest := raw[idx+len(marker):]
	if cut := strings.IndexAny(rest, "-/?#"); cut >= 0 {
		rest = rest[:cut]
	}
	return strings.TrimSpace(rest)
}

func snippetOf(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

func clampInt(v, def, min, max int) int {
	if v <= 0 {
		return def
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func humanDuration(d time.Duration) string {
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}
