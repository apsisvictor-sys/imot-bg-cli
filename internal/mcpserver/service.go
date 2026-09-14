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
	Query            QuerySummary        `json:"query"`
	Stats            scraper.SearchStats `json:"stats" jsonschema:"price distribution for the returned listings"`
	Listings         []ListingSummary    `json:"listings"`
	TotalMatching    int                 `json:"total_matching" jsonschema:"the source population for this query before any client-side price/size filters, so it can exceed the returned count"`
	FilteredMatching int                 `json:"filtered_matching,omitempty" jsonschema:"when client-side price/size filters were applied, the count that satisfies them"`
	Returned         int                 `json:"returned"`
	PagesFetched     int                 `json:"pages_fetched"`
	Sampled          bool                `json:"sampled" jsonschema:"true when fewer listings were returned than match the query"`
	Partial          bool                `json:"partial" jsonschema:"true when some result pages failed to load"`
	Source           string              `json:"source" jsonschema:"radar, radar_stale, cache or live. live is the labelled fallback outside Radar coverage."`
	Coverage         string              `json:"coverage" jsonschema:"complete, partial, stale, never_collected, out_of_scope or unavailable. Never read a non-complete coverage as a verified empty market."`
	Readiness        *RadarReadiness     `json:"readiness,omitempty" jsonschema:"detail and media enrichment state with committed and expected counts; present for Radar answers"`
	ObservedAt       string              `json:"observed_at" jsonschema:"RFC3339 time the market data was observed; empty when unknown"`
	EmptyVerified    bool                `json:"empty_verified" jsonschema:"true only when a complete source explicitly verified zero matches"`
	ClientFilters    []string            `json:"client_filters,omitempty" jsonschema:"price/size filters applied to downloaded listings because the source URL cannot carry them"`
	FetchedAt        string              `json:"fetched_at" jsonschema:"RFC3339 timestamp of when this answer was produced"`
	AgeSeconds       int                 `json:"age_seconds" jsonschema:"age of the data in seconds"`
	Notes            []string            `json:"notes,omitempty"`
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
	Source     string                 `json:"source" jsonschema:"radar, radar_stale, cache, live or none"`
	Coverage   string                 `json:"coverage" jsonschema:"complete, partial, stale, never_collected, out_of_scope or unavailable"`
	Readiness  *RadarReadiness        `json:"readiness,omitempty" jsonschema:"detail and media enrichment state with committed and expected counts; present for Radar answers"`
	ObservedAt string                 `json:"observed_at" jsonschema:"RFC3339 time the listing was last observed; empty when unknown"`
	FetchedAt  string                 `json:"fetched_at"`
	AgeSeconds int                    `json:"age_seconds"`
	Notes      []string               `json:"notes,omitempty"`

	// ScopeSlug records the Radar catalogue slug a Radar-served detail belongs
	// to, so a cached answer can rebase its coverage claim against the current
	// catalogue state without refetching the listing. Empty for live answers.
	ScopeSlug string `json:"scope_slug,omitempty"`
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
	radar    RadarReader
	logger   *slog.Logger
	identity string

	// Test seams: the live source calls are injectable so a test can prove a
	// Radar answer made zero imot.bg requests without touching the network.
	searchLive      func(scraper.SearchParams) (scraper.SearchResult, error)
	fetchLiveDetail func(listings []scraper.Listing) []error
	now             func() time.Time
}

// newService builds the tool layer for one identity.
func newService(cfg Config, cache *Cache, limiter *Limiter, radar RadarReader, identity string, logger *slog.Logger) *Service {
	client := scraper.NewClient()
	return &Service{
		cfg:             cfg,
		cache:           cache,
		limiter:         limiter,
		client:          client,
		radar:           radar,
		logger:          logger,
		identity:        identity,
		searchLive:      client.SearchWithMeta,
		fetchLiveDetail: client.FetchDetailsConcurrent,
		now:             time.Now,
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
	// Resolve the serving scope before the cache lookup so a Radar answer and a
	// live fallback answer can never share a cache entry.
	scope := RadarScope{Reason: ScopeReasonRadarNotConfigured}
	var scopeErr error
	if s.radar != nil {
		scope, scopeErr = s.radar.ResolveScope(ctx, city, params.Neighborhood, params.Rent)
		if scopeErr != nil {
			s.logger.Warn("radar scope resolution failed; serving the labelled live fallback", "error", scopeErr)
		}
	}
	key := searchKey(servingMode(scope, scopeErr, params.Neighborhood), params, limit)

	if !in.Refresh {
		if cached, createdAt, ok := s.cachedSearch(key, s.cfg.SearchCacheTTL); ok {
			out := s.serveCachedSearch(cached, createdAt, limit)
			// Scope metadata is authoritative at read time. A cached Radar
			// payload must not preserve a formerly complete/empty claim after
			// the catalogue receipt has become partial or invalid.
			if scopeErr == nil && scope.InScope {
				out.Coverage = scope.Coverage
				out.EmptyVerified = scope.Coverage == CoverageComplete && out.TotalMatching == 0
			}
			s.record(ToolSearchListings, true, time.Since(started), out.Returned)
			return out, nil
		}
	}

	if s.radar != nil && scopeErr == nil && scope.InScope {
		full, err := s.radarSearch(ctx, query, scope, params, limit)
		if err == nil {
			// Store the full envelope, never a truncated one: a later caller with
			// a larger limit must be able to recover every fetched listing.
			s.storeSearch(key, full)
			out := truncateSearchOutput(full, limit)
			out.Notes = append(out.Notes, refreshNotes(out, false)...)
			s.record(ToolSearchListings, true, time.Since(started), out.Returned)
			return out, nil
		}
		s.logger.Warn("radar search failed; trying the cached Radar answer before the live fallback", "error", err, "scope", scope.Slug)
		if cached, createdAt, ok := s.cachedSearch(key, 0); ok {
			out := s.serveCachedSearch(cached, createdAt, limit)
			out.Source = SourceCache
			out.Coverage = CoverageStale
			out.EmptyVerified = false
			out.Notes = append(out.Notes, "Market Radar is currently unreachable; serving the last cached Radar answer.")
			s.record(ToolSearchListings, true, time.Since(started), out.Returned)
			return out, nil
		}
		scopeErr = err
	}

	full, err := s.liveSearch(ctx, query, scope, scopeErr, params)
	if err != nil {
		s.record(ToolSearchListings, false, time.Since(started), 0)
		return SearchListingsOutput{}, err
	}
	// If Radar was unavailable, keep the live fallback in its own cache
	// namespace. Otherwise a later Radar refresh failure could replay this live
	// payload as if it were stale Radar data.
	storeKey := key
	if scopeErr != nil {
		storeKey = searchKey(servingMode(scope, scopeErr, params.Neighborhood), params, limit)
	}
	s.storeSearch(storeKey, full)
	out := truncateSearchOutput(full, limit)
	out.Notes = append(out.Notes, refreshNotes(out, false)...)
	s.record(ToolSearchListings, false, time.Since(started), out.Returned)
	return out, nil
}

// radarSearch serves an in-scope query from the authoritative store. It never
// touches imot.bg, so a fresh Radar hit costs zero source requests.
func (s *Service) radarSearch(ctx context.Context, query QuerySummary, scope RadarScope, params scraper.SearchParams, limit int) (SearchListingsOutput, error) {
	result, err := s.radar.SearchListings(ctx, RadarSearchQuery{
		Scope:        scope,
		PropertyType: params.Type,
		MinPriceEUR:  params.MinPrice,
		MaxPriceEUR:  params.MaxPrice,
		MinSizeSqM:   params.MinSqM,
		MaxSizeSqM:   params.MaxSqM,
		Limit:        limit,
	})
	if err != nil {
		return SearchListingsOutput{}, err
	}
	now := s.now().UTC()
	source := SourceRadar
	if scope.Coverage == CoverageStale {
		source = SourceRadarStale
	}
	out := SearchListingsOutput{
		Query:         query,
		Stats:         scraper.ComputeStats(toRadarListings(result.Listings)),
		Listings:      toRadarSummaries(result.Listings),
		TotalMatching: result.Total,
		Returned:      len(result.Listings),
		PagesFetched:  1,
		Sampled:       result.Total > len(result.Listings),
		Source:        source,
		Coverage:      scope.Coverage,
		Readiness:     &result.Readiness,
		ObservedAt:    formatObservedAt(result.ObservedAt),
		EmptyVerified: scope.Coverage == CoverageComplete && result.Total == 0,
		FetchedAt:     now.Format(time.RFC3339),
	}
	return out, nil
}

// liveSearch is the legacy path, kept for rentals and scopes outside the Radar
// catalogue (and as a labelled last resort when Radar is unreachable). Price
// and area bounds are applied here in Go because the imot.bg request URL carries
// no such parameters: the previous code echoed them without applying them.
func (s *Service) liveSearch(ctx context.Context, query QuerySummary, scope RadarScope, scopeErr error, params scraper.SearchParams) (SearchListingsOutput, error) {
	if err := s.authorizeLive(ToolSearchListings); err != nil {
		return SearchListingsOutput{}, err
	}
	release, err := s.limiter.Acquire(ctx)
	if err != nil {
		return SearchListingsOutput{}, err
	}
	defer release()

	result, err := s.searchLive(params)
	if err != nil {
		return SearchListingsOutput{}, fmt.Errorf("live search failed: %w", err)
	}

	listings := filterListingsByRange(result.Listings, params)
	now := s.now().UTC()
	coverage := CoverageOutOfScope
	var notes []string
	if scopeErr != nil {
		coverage = CoverageUnavailable
		notes = append(notes, "Market Radar is currently unreachable; this answer is the live imot.bg fallback and is not Radar coverage.")
	} else if note := scopeReasonNote(scope.Reason); note != "" {
		notes = append(notes, note)
	}
	filtersApplied := len(liveClientFilterNames(params)) > 0
	filteredMatching := 0
	if filtersApplied {
		filteredMatching = len(listings)
	}

	out := SearchListingsOutput{
		Query:            query,
		Stats:            scraper.ComputeStats(listings),
		Listings:         toSummaries(listings),
		TotalMatching:    result.TotalCount,
		FilteredMatching: filteredMatching,
		Returned:         len(listings),
		PagesFetched:     result.PagesFetched,
		Sampled:          result.TotalCount > len(listings),
		Partial:          result.Partial,
		Source:           SourceLive,
		Coverage:         coverage,
		ObservedAt:       now.Format(time.RFC3339),
		EmptyVerified:    result.EmptyVerified,
		ClientFilters:    liveClientFilterNames(params),
		FetchedAt:        now.Format(time.RFC3339),
	}
	// Only the scope explanation is stored; freshness and sampling notes are
	// recomputed for whichever limit serves this payload.
	out.Notes = notes
	return out, nil
}

// GetListing returns one listing's detail, Radar first and the labelled live
// page as the fallback when Radar does not have the advert.
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
	now := s.now().UTC()
	if !includeDetail {
		return GetListingOutput{
			ListingID:  id,
			URL:        url,
			Source:     SourceNone,
			Coverage:   CoverageOutOfScope,
			ObservedAt: now.Format(time.RFC3339),
			FetchedAt:  now.Format(time.RFC3339),
			Notes:      []string{"Detail was not requested; call again with include_detail true for description, features and contacts."},
		}, nil
	}

	// A recent Radar answer stays authoritative and cheap.
	if s.radar != nil && !in.Refresh {
		if payload, fetchedAt, ok, err := s.cache.GetDetail(radarDetailKey(id), s.cfg.DetailCacheTTL); err != nil {
			s.logger.Warn("radar detail cache read failed", "error", err)
		} else if ok {
			if cached, ok := decodeDetailOutput(payload); ok {
				s.rebaseDetailCoverage(ctx, &cached)
				s.record(ToolGetListing, true, time.Since(started), 1)
				return serveCachedDetail(cached, fetchedAt, now), nil
			}
			s.logger.Warn("radar detail cache payload was unreadable; refetching")
		}
	}

	radarUnavailable := false
	if s.radar != nil {
		listing, found, err := s.radar.GetListing(ctx, id)
		switch {
		case err != nil:
			radarUnavailable = true
			s.logger.Warn("radar detail read failed; trying the cached Radar copy", "error", err)
			if payload, fetchedAt, ok, _ := s.cache.GetDetail(radarDetailKey(id), 0); ok {
				if cached, ok := decodeDetailOutput(payload); ok {
					out := serveCachedDetail(cached, fetchedAt, now)
					out.Coverage = CoverageStale
					out.Notes = append(out.Notes, "Market Radar is currently unreachable; serving the last cached Radar detail.")
					s.record(ToolGetListing, true, time.Since(started), 1)
					return out, nil
				}
			}
		case found:
			out := radarDetailOutput(listing, id, url, now)
			s.storeDetail(radarDetailKey(id), out)
			s.record(ToolGetListing, true, time.Since(started), 1)
			return out, nil
		}
	}

	// A recent live answer remains valid and keeps repeat calls off both
	// networks. It keeps the source and coverage recorded when it was fetched.
	if !in.Refresh {
		if payload, fetchedAt, ok, err := s.cache.GetDetail(liveDetailKey(id), s.cfg.DetailCacheTTL); err != nil {
			s.logger.Warn("detail cache read failed", "error", err)
		} else if ok {
			if cached, ok := decodeDetailOutput(payload); ok {
				s.record(ToolGetListing, true, time.Since(started), 1)
				return serveCachedDetail(cached, fetchedAt, now), nil
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
	errs := s.fetchLiveDetail(listings)
	s.record(ToolGetListing, false, time.Since(started), 1)

	if len(errs) > 0 && errs[0] != nil {
		return GetListingOutput{}, fmt.Errorf("could not load listing %s: %w", id, errs[0])
	}
	if listings[0].Detail == nil {
		return GetListingOutput{}, fmt.Errorf("listing %s returned no detail page data", id)
	}

	detail := *listings[0].Detail
	out := s.liveDetailOutput(id, url, detail, now, radarUnavailable)
	s.storeDetail(liveDetailKey(id), out)
	return out, nil
}

// rebaseDetailCoverage renews a cached Radar detail's coverage claim against
// the current catalogue state. Scope metadata is authoritative at read time: a
// receipt that has since become invalid or stale must not keep advertising a
// formerly complete listing as fresh. An unreachable reader keeps the cached
// label untouched rather than turning a served answer into an error.
func (s *Service) rebaseDetailCoverage(ctx context.Context, cached *GetListingOutput) {
	if s.radar == nil || strings.TrimSpace(cached.ScopeSlug) == "" {
		return
	}
	scope, err := s.radar.ResolveScope(ctx, radarCoverageCity, cached.ScopeSlug, false)
	if err != nil {
		s.logger.Warn("radar scope rebase failed; keeping the cached coverage label", "error", err)
		return
	}
	if scope.InScope {
		cached.Coverage = scope.Coverage
		return
	}
	cached.Coverage = CoverageOutOfScope
	cached.Notes = append(cached.Notes, "This listing's neighbourhood is no longer in the active Market Radar catalogue; coverage is out of scope.")
}

// radarDetailOutput builds the MCP envelope for an authoritative Radar detail.
func radarDetailOutput(listing RadarListing, requestedID, fallbackURL string, now time.Time) GetListingOutput {
	detail := listing.toDetailListing()
	source := SourceRadar
	if listing.Coverage == CoverageStale {
		source = SourceRadarStale
	}
	readiness := readinessForListing(listing)
	out := GetListingOutput{
		ListingID:  firstNonEmpty(listing.AdvID, requestedID),
		URL:        firstNonEmpty(detail.URL, fallbackURL),
		Detail:     &detail,
		Source:     source,
		Coverage:   listing.Coverage,
		Readiness:  &readiness,
		ObservedAt: formatObservedAt(listing.LastSeenAt),
		FetchedAt:  now.Format(time.RFC3339),
		ScopeSlug:  listing.Neighborhood,
	}
	switch {
	case readiness.DetailState != "complete":
		out.Notes = append(out.Notes, "Market Radar has this listing, but its detail enrichment is "+readiness.DetailState+"; description, features or contacts may be incomplete.")
	case readiness.MediaState != "complete":
		out.Notes = append(out.Notes, "Market Radar has this listing, but its photo enrichment is "+readiness.MediaState+"; the gallery may be incomplete.")
	}
	if listing.Coverage != CoverageComplete && listing.Coverage != "" {
		out.Notes = append(out.Notes, "This listing's neighbourhood coverage is "+listing.Coverage+"; the listing may not reflect the current market.")
	}
	return out
}

// liveDetailOutput builds the MCP envelope for a live imot.bg detail, labelled
// as the fallback it is.
func (s *Service) liveDetailOutput(id, fallbackURL string, detail scraper.DetailListing, now time.Time, radarUnavailable bool) GetListingOutput {
	out := GetListingOutput{
		ListingID:  id,
		URL:        firstNonEmpty(detail.URL, fallbackURL),
		Detail:     &detail,
		Source:     SourceLive,
		Coverage:   CoverageOutOfScope,
		ObservedAt: now.Format(time.RFC3339),
		FetchedAt:  now.Format(time.RFC3339),
	}
	switch {
	case radarUnavailable:
		out.Coverage = CoverageUnavailable
		out.Notes = []string{"Market Radar was unreachable; this detail is the live imot.bg page, not Radar data."}
	case s.radar != nil:
		out.Notes = []string{"This listing is not in the Market Radar catalogue; this detail is the live imot.bg page."}
	}
	return out
}

// serveCachedDetail relabels a cached detail envelope without rewriting its
// stored coverage, readiness or observation time.
func serveCachedDetail(cached GetListingOutput, fetchedAt, now time.Time) GetListingOutput {
	cached.Source = SourceCache
	cached.AgeSeconds = int(now.Sub(fetchedAt).Seconds())
	cached.FetchedAt = fetchedAt.UTC().Format(time.RFC3339)
	return cached
}

// cachedSearch returns a decoded search envelope. ttl 0 means any age, which is
// how an unreachable Radar reuses its last known answer instead of returning an
// empty market.
func (s *Service) cachedSearch(key string, ttl time.Duration) (SearchListingsOutput, time.Time, bool) {
	payload, createdAt, ok, err := s.cache.GetSearch(key, ttl)
	if err != nil {
		s.logger.Warn("search cache read failed", "error", err)
		return SearchListingsOutput{}, time.Time{}, false
	}
	if !ok {
		return SearchListingsOutput{}, time.Time{}, false
	}
	var cached SearchListingsOutput
	if err := json.Unmarshal(payload, &cached); err != nil {
		s.logger.Warn("search cache payload was unreadable; refetching")
		return SearchListingsOutput{}, time.Time{}, false
	}
	return cached, createdAt, true
}

// serveCachedSearch projects a cached payload for one request. It only ever
// truncates the response copy and recomputes the fields derived from the
// returned rows; the stored payload and its total are untouched.
func (s *Service) serveCachedSearch(cached SearchListingsOutput, createdAt time.Time, limit int) SearchListingsOutput {
	prefix := cached.Notes
	out := cached
	out.Source = SourceCache
	out.AgeSeconds = int(s.now().Sub(createdAt).Seconds())
	out.FetchedAt = createdAt.UTC().Format(time.RFC3339)
	if out.Coverage == CoverageComplete && s.observationStale(out.ObservedAt) {
		out.Coverage = CoverageStale
		// A cached complete zero-match response is no longer a verified empty
		// market once its observation has gone stale.
		out.EmptyVerified = false
	}
	out = truncateSearchOutput(out, limit)
	out.Notes = append(prefix, refreshNotes(out, true)...)
	return out
}

// storeSearch writes the full, untruncated envelope so pagination or a smaller
// limit can never shrink the cached population.
func (s *Service) storeSearch(key string, out SearchListingsOutput) {
	payload, err := json.Marshal(out)
	if err != nil {
		s.logger.Warn("search cache marshal failed", "error", err)
		return
	}
	if err := s.cache.PutSearch(key, payload, len(out.Listings)); err != nil {
		s.logger.Warn("search cache write failed", "error", err)
	}
}

func (s *Service) storeDetail(key string, out GetListingOutput) {
	payload, err := json.Marshal(out)
	if err != nil {
		s.logger.Warn("detail cache marshal failed", "error", err)
		return
	}
	if err := s.cache.PutDetail(key, payload); err != nil {
		s.logger.Warn("detail cache write failed", "error", err)
	}
}

func decodeDetailOutput(payload []byte) (GetListingOutput, bool) {
	var out GetListingOutput
	if err := json.Unmarshal(payload, &out); err != nil {
		return GetListingOutput{}, false
	}
	return out, true
}

// truncateSearchOutput applies the caller's limit to the response copy and
// recomputes every field derived from the returned rows. total_matching is
// never derived from the returned listing array.
func truncateSearchOutput(out SearchListingsOutput, limit int) SearchListingsOutput {
	out.Listings = truncateListings(out.Listings, limit)
	out.Returned = len(out.Listings)
	out.Sampled = out.TotalMatching > out.Returned
	out.Stats = summaryStats(out.Listings)
	return out
}

// summaryStats mirrors scraper.ComputeStats over the model-facing projection so
// the statistics always describe exactly the returned listings.
func summaryStats(listings []ListingSummary) scraper.SearchStats {
	rows := make([]scraper.Listing, 0, len(listings))
	for _, l := range listings {
		rows = append(rows, scraper.Listing{PriceEUR: l.PriceEUR, SizeSqM: l.SizeSqM, Agency: l.Agency})
	}
	return scraper.ComputeStats(rows)
}

// servingMode keeps Radar, live-fallback and unavailable answers in separate
// cache entries. A scope change (or recovery) therefore cannot serve an answer
// produced under different semantics.
func servingMode(scope RadarScope, scopeErr error, neighborhood string) string {
	switch {
	case scope.InScope && scopeErr == nil:
		slug := strings.TrimSpace(scope.Slug)
		if slug == "" {
			slug = strings.ToLower(strings.TrimSpace(neighborhood))
		}
		return "radar:" + slug
	case scopeErr != nil:
		return "unavailable:" + strings.ToLower(strings.TrimSpace(neighborhood))
	default:
		return "legacy"
	}
}

// filterListingsByRange applies the price and size bounds the imot.bg request
// URL cannot carry, matching the CLI's own filter semantics.
func filterListingsByRange(listings []scraper.Listing, params scraper.SearchParams) []scraper.Listing {
	if params.MinPrice == 0 && params.MaxPrice == 0 && params.MinSqM == 0 && params.MaxSqM == 0 {
		return listings
	}
	out := make([]scraper.Listing, 0, len(listings))
	for _, l := range listings {
		if params.MinPrice > 0 && l.PriceEUR < params.MinPrice {
			continue
		}
		if params.MaxPrice > 0 && l.PriceEUR > params.MaxPrice {
			continue
		}
		if params.MinSqM > 0 && l.SizeSqM < params.MinSqM {
			continue
		}
		if params.MaxSqM > 0 && l.SizeSqM > params.MaxSqM {
			continue
		}
		out = append(out, l)
	}
	return out
}

// liveClientFilterNames names the bounds that were applied to downloaded rows
// rather than at the source, so the envelope does not imply server-side
// filtering the source never performed.
func liveClientFilterNames(params scraper.SearchParams) []string {
	var names []string
	if params.MinPrice > 0 {
		names = append(names, "min_price")
	}
	if params.MaxPrice > 0 {
		names = append(names, "max_price")
	}
	if params.MinSqM > 0 {
		names = append(names, "min_sqm")
	}
	if params.MaxSqM > 0 {
		names = append(names, "max_sqm")
	}
	return names
}

// scopeReasonNote explains, in bounded prose, why a request was served outside
// Radar coverage.
func scopeReasonNote(reason string) string {
	switch reason {
	case ScopeReasonRadarNotConfigured:
		return "Market Radar is not configured on this server, so this answer is the live imot.bg fallback, not Radar coverage."
	case ScopeReasonRentalNotSupported:
		return "Market Radar does not cover rentals yet, so this answer is the live imot.bg fallback, not Radar coverage. Rental prices are monthly rent as advertised."
	case ScopeReasonCityNotCovered:
		return "Market Radar currently covers София only, so this answer is the live imot.bg fallback, not Radar coverage."
	case ScopeReasonCitywideNotCovered:
		return "Market Radar coverage is per neighbourhood, so a query without a neighbourhood is served by the live imot.bg fallback, not Radar coverage."
	case ScopeReasonNotInCatalogue:
		return "This neighbourhood is not in the Market Radar coverage catalogue, so this answer is the live imot.bg fallback, not Radar coverage."
	case ScopeReasonNeighborhoodInactive:
		return "This neighbourhood is not actively collected by Market Radar, so this answer is the live imot.bg fallback, not Radar coverage."
	default:
		return ""
	}
}

// observationStale reports whether a stored RFC3339 observation is older than
// the configured Radar freshness window.
func (s *Service) observationStale(observedAt string) bool {
	if s.cfg.RadarFreshness <= 0 || strings.TrimSpace(observedAt) == "" {
		return false
	}
	observed, err := time.Parse(time.RFC3339, observedAt)
	if err != nil {
		return false
	}
	return s.now().Sub(observed) > s.cfg.RadarFreshness
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

// record logs one tool call. servedWithoutLiveFetch is true for cache hits and
// Radar reads: neither touches imot.bg, so neither may consume the shared live
// budget that guards the scraping egress.
func (s *Service) record(tool string, servedWithoutLiveFetch bool, took time.Duration, results int) {
	if err := s.cache.RecordUsage(s.identity, tool, servedWithoutLiveFetch); err != nil {
		s.logger.Warn("usage record failed", "error", err)
	}
	s.logger.Info("tool call",
		"tool", tool,
		"identity", s.identity,
		"served_without_live_fetch", servedWithoutLiveFetch,
		"duration_ms", took.Milliseconds(),
		"results", results,
	)
}

// Cache projection versions. A payload is only ever served to a request built
// with the same projection, so changing the model-facing shape cannot smuggle
// an old envelope into a new one.
const (
	searchProjectionVersion = "imot-mcp-search-v3"
	detailProjectionVersion = "imot-mcp-detail-v3"
)

// searchKey builds a stable cache key for a normalized query. The serving mode
// keeps a Radar answer and a live fallback answer apart; the projection, pages
// and limit are part of the key so a payload produced for one request can never
// be replayed as another.
func searchKey(mode string, p scraper.SearchParams, limit int) string {
	canonical := fmt.Sprintf("%s|%s|%s|%s|%s|%t|%d|%d|%d|%d|%d|%d",
		searchProjectionVersion,
		mode,
		p.City,
		strings.ToLower(strings.TrimSpace(p.Neighborhood)),
		p.Type,
		p.Rent,
		p.MinPrice, p.MaxPrice, p.MinSqM, p.MaxSqM,
		p.Pages, limit,
	)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// radarDetailKey and liveDetailKey keep the two detail sources in separate
// cache entries under the current projection.
func radarDetailKey(id string) string {
	return detailProjectionVersion + "|radar|" + id
}

func liveDetailKey(id string) string {
	return detailProjectionVersion + "|live|" + id
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

// refreshNotes explains the freshness and coverage caveats attached to a
// result. It is the single place that keeps a non-complete scope from reading
// as a verified empty market.
func refreshNotes(out SearchListingsOutput, fromCache bool) []string {
	var notes []string
	if out.Sampled {
		if len(out.ClientFilters) > 0 {
			notes = append(notes, fmt.Sprintf(
				"Sampled result: showing %d of the %d listings the source reported for the unfiltered query; the price/size filters narrowed the downloaded rows.",
				out.Returned, out.TotalMatching))
		} else {
			notes = append(notes, fmt.Sprintf(
				"Sampled result: showing %d of %d matching listings. Raise the limit or narrow the filters for fuller coverage.",
				out.Returned, out.TotalMatching))
		}
	}
	if out.Partial {
		notes = append(notes, "Some result pages failed to load, so counts and statistics may be incomplete.")
	}
	if len(out.ClientFilters) > 0 {
		notes = append(notes, "imot.bg has no URL filter for price or size, so those filters were applied only to the listings downloaded for this query; total_matching still counts the source's unfiltered result.")
	}
	switch out.Coverage {
	case CoverageStale:
		notes = append(notes, "Market Radar coverage for this scope is stale: the listings are last observations, not a verified current market.")
	case CoveragePartial:
		notes = append(notes, "Market Radar reports partial coverage for this scope, so listings may be missing. This is not a verified empty or complete market.")
	case CoverageNeverCollected:
		notes = append(notes, "Market Radar has not completed a collection for this scope, so an empty or short result must not be read as an empty market.")
	case CoverageUnavailable:
		notes = append(notes, "Market Radar was unavailable, so this is the labelled live imot.bg fallback and not Radar coverage.")
	}
	if fromCache && out.AgeSeconds > 600 {
		notes = append(notes, fmt.Sprintf(
			"Cached answer produced %s ago. Set refresh true for a fresh fetch.",
			humanDuration(time.Duration(out.AgeSeconds)*time.Second)))
	}
	if out.Query.Rent {
		notes = append(notes, "Rental prices are monthly rent as advertised; utilities and fees are not included.")
	}
	if len(out.Listings) == 0 {
		switch {
		case out.Coverage == CoverageComplete:
			notes = append(notes, "Market Radar coverage for this scope is complete and no active listings match these filters; this is a verified empty result.")
		case out.Coverage == CoverageNeverCollected || out.Coverage == CoveragePartial || out.Coverage == CoverageStale:
			notes = append(notes, "No listings were returned, but this scope's Market Radar coverage is not complete; do not read this as a verified empty market.")
		case len(out.ClientFilters) > 0:
			notes = append(notes, "No downloaded listings matched your price or size filters; the source query may still have matches outside them.")
		case out.EmptyVerified:
			notes = append(notes, "imot.bg explicitly reported no matching listings for this source query.")
		default:
			notes = append(notes, "No listings matched. Try a wider neighborhood match or remove price and size filters.")
		}
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
