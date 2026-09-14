package mcpserver

// Focused Radar reader and serving contract tests (Phase U1).
//
// They run without a Postgres server: the reader is faked at the RadarReader
// boundary and the service's live source calls are injected counters, so a
// Radar hit can be proven to make zero imot.bg requests. The SQL itself is
// checked against the canonical schema contract in
// TestRadarQueriesUseCanonicalSchema.

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/apsisvictor/imot-cli/internal/scraper"
)

// fakeRadarReader records the calls the service makes and returns fixtures.
type fakeRadarReader struct {
	scope      RadarScope
	scopeErr   error
	search     RadarSearchResult
	searchErr  error
	listing    RadarListing
	found      bool
	listingErr error

	scopeCalls  int
	searchCalls int
	detailCalls int
	lastQuery   RadarSearchQuery
	lastCity    string
	lastHood    string
	lastRent    bool
	lastID      string
	closed      bool
}

func (f *fakeRadarReader) ResolveScope(_ context.Context, city, neighborhood string, rent bool) (RadarScope, error) {
	f.scopeCalls++
	f.lastCity, f.lastHood, f.lastRent = city, neighborhood, rent
	return f.scope, f.scopeErr
}

func (f *fakeRadarReader) SearchListings(_ context.Context, q RadarSearchQuery) (RadarSearchResult, error) {
	f.searchCalls++
	f.lastQuery = q
	return f.search, f.searchErr
}

func (f *fakeRadarReader) GetListing(_ context.Context, listingID string) (RadarListing, bool, error) {
	f.detailCalls++
	f.lastID = listingID
	return f.listing, f.found, f.listingErr
}

func (f *fakeRadarReader) Close() error {
	f.closed = true
	return nil
}

func radarTestObservation() time.Time {
	return time.Now().UTC().Truncate(time.Second).Add(-2 * time.Hour)
}

func completeRadarScope(observed time.Time) RadarScope {
	return RadarScope{
		InScope:       true,
		Slug:          "lozenets",
		NameBg:        "Лозенец",
		SearchLabel:   "Лозенец",
		City:          radarCoverageCity,
		Active:        true,
		Coverage:      CoverageComplete,
		LastScrapedAt: observed,
		CompleteAt:    observed,
		LastRunStatus: "ok",
		ObservedAt:    observed,
	}
}

func radarFixtureListing(id string, price, area float64, observed time.Time) RadarListing {
	return RadarListing{
		AdvID:          id,
		URL:            "https://www.imot.bg/obiava-" + id,
		City:           radarCoverageCity,
		Neighborhood:   "lozenets",
		NeighborhoodBg: "Лозенец",
		PropertyType:   "2-СТАЕН",
		PriceEUR:       price,
		PricePerSqm:    price / area,
		AreaSqm:        area,
		Floor:          "4 от 7",
		YearRange:      "2007",
		IsAgency:       true,
		AgentName:      "iHOME",
		Status:         "active",
		LastSeenAt:     observed,
		DetailState:    "complete",
		MediaState:     "complete",
	}
}

// newRadarTestService builds a service whose live source calls are injected. A
// nil live function makes any accidental source call a test failure.
func newRadarTestService(t *testing.T, cfg Config, cache *Cache, radar RadarReader, live func(scraper.SearchParams) (scraper.SearchResult, error)) *Service {
	t.Helper()
	svc := newService(cfg, cache, NewLimiter(1, 0), radar, "pilot", testLogger())
	if live == nil {
		live = func(scraper.SearchParams) (scraper.SearchResult, error) {
			return scraper.SearchResult{}, errors.New("unexpected live source call")
		}
	}
	svc.searchLive = live
	svc.fetchLiveDetail = func([]scraper.Listing) []error {
		return []error{errors.New("unexpected live detail call")}
	}
	return svc
}

func TestRadarSearchHitMakesZeroSourceCalls(t *testing.T) {
	cfg := testConfig(t)
	cache, err := OpenCache(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	defer cache.Close()

	observed := radarTestObservation()
	reader := &fakeRadarReader{
		scope: completeRadarScope(observed),
		search: RadarSearchResult{
			Total:      2,
			Listings:   []RadarListing{radarFixtureListing("a1", 150000, 75, observed), radarFixtureListing("a2", 180000, 90, observed)},
			ObservedAt: observed,
			Readiness: RadarReadiness{
				Scope: "neighborhood", DetailState: "complete", MediaState: "complete",
				Matched: 2, DetailComplete: 2, MediaComplete: 2,
				CommittedPhotos: 4, ExpectedPhotos: 4,
			},
		},
	}
	sourceCalls := 0
	svc := newRadarTestService(t, cfg, cache, reader, func(scraper.SearchParams) (scraper.SearchResult, error) {
		sourceCalls++
		return scraper.SearchResult{}, errors.New("a Radar hit must not call imot.bg")
	})

	in := SearchListingsInput{City: radarCoverageCity, Neighborhood: "Лозенец", PropertyType: "2-стаен"}
	out, err := svc.SearchListings(context.Background(), in)
	if err != nil {
		t.Fatalf("SearchListings from Radar: %v", err)
	}
	if sourceCalls != 0 {
		t.Fatalf("source calls = %d, want 0 for a Radar hit", sourceCalls)
	}
	if reader.searchCalls != 1 {
		t.Fatalf("Radar search calls = %d, want 1", reader.searchCalls)
	}
	if out.Source != SourceRadar || out.Coverage != CoverageComplete {
		t.Fatalf("source/coverage = %q/%q, want radar/complete", out.Source, out.Coverage)
	}
	if out.TotalMatching != 2 || out.Returned != 2 || len(out.Listings) != 2 {
		t.Fatalf("total/returned/listings = %d/%d/%d, want 2/2/2", out.TotalMatching, out.Returned, len(out.Listings))
	}
	if out.Listings[0].ID != "a1" || out.Listings[0].Neighborhood != "Лозенец" {
		t.Fatalf("listing projection = %+v", out.Listings[0])
	}
	if out.ObservedAt != observed.Format(time.RFC3339) {
		t.Fatalf("observed_at = %q, want %q", out.ObservedAt, observed.Format(time.RFC3339))
	}
	if out.Readiness == nil || out.Readiness.DetailComplete != 2 || out.Readiness.MediaComplete != 2 {
		t.Fatalf("readiness = %+v", out.Readiness)
	}
	// A Radar read is not a live fetch and must not consume the shared budget.
	if count, err := cache.LiveCountAll(time.Now().Add(-time.Hour)); err != nil || count != 0 {
		t.Fatalf("live count = %d, err = %v; a Radar hit must not consume the live budget", count, err)
	}

	// A repeat inside the cache TTL must not re-read Radar or touch imot.bg.
	repeated, err := svc.SearchListings(context.Background(), in)
	if err != nil {
		t.Fatalf("repeated SearchListings: %v", err)
	}
	if reader.searchCalls != 1 {
		t.Fatalf("Radar re-read for a cached answer: %d calls", reader.searchCalls)
	}
	if sourceCalls != 0 {
		t.Fatalf("cached answer made %d source calls", sourceCalls)
	}
	if repeated.Source != SourceCache {
		t.Fatalf("cached source = %q, want cache", repeated.Source)
	}
	if repeated.Readiness == nil || repeated.Readiness.DetailComplete != 2 {
		t.Fatalf("cached readiness was lost: %+v", repeated.Readiness)
	}
}

func TestRadarCompleteEmptyIsVerifiedEmpty(t *testing.T) {
	cfg := testConfig(t)
	cache, err := OpenCache(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	defer cache.Close()

	observed := radarTestObservation()
	reader := &fakeRadarReader{
		scope: completeRadarScope(observed),
		search: RadarSearchResult{
			Total:      0,
			Listings:   []RadarListing{},
			ObservedAt: observed,
			Readiness:  RadarReadiness{Scope: "neighborhood", DetailState: "none", MediaState: "none"},
		},
	}
	sourceCalls := 0
	svc := newRadarTestService(t, cfg, cache, reader, func(scraper.SearchParams) (scraper.SearchResult, error) {
		sourceCalls++
		return scraper.SearchResult{}, nil
	})

	out, err := svc.SearchListings(context.Background(), SearchListingsInput{City: radarCoverageCity, Neighborhood: "Лозенец"})
	if err != nil {
		t.Fatalf("SearchListings: %v", err)
	}
	if sourceCalls != 0 {
		t.Fatalf("source calls = %d, want 0", sourceCalls)
	}
	if out.Source != SourceRadar || out.Coverage != CoverageComplete {
		t.Fatalf("source/coverage = %q/%q, want radar/complete", out.Source, out.Coverage)
	}
	if !out.EmptyVerified {
		t.Fatal("a complete zero-match Radar answer must be marked empty_verified")
	}
	if len(out.Listings) != 0 || out.TotalMatching != 0 {
		t.Fatalf("listings/total = %d/%d, want 0/0", len(out.Listings), out.TotalMatching)
	}
	if notes := strings.Join(out.Notes, " | "); !strings.Contains(notes, "verified empty") {
		t.Fatalf("empty complete result is not explained: %s", notes)
	}
}

func TestRadarUnavailableIsNotAnEmptyResult(t *testing.T) {
	cfg := testConfig(t)
	cache, err := OpenCache(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	defer cache.Close()

	reader := &fakeRadarReader{
		scope:     completeRadarScope(radarTestObservation()),
		searchErr: errors.New("dial tcp 10.0.0.5:5434: connect: connection refused"),
	}
	liveCalls := 0
	svc := newRadarTestService(t, cfg, cache, reader, func(scraper.SearchParams) (scraper.SearchResult, error) {
		liveCalls++
		return scraper.SearchResult{
			Listings:     []scraper.Listing{{ID: "live-1", Type: "2-СТАЕН", PriceEUR: 100000, SizeSqM: 60, Neighborhood: "Лозенец"}},
			TotalCount:   1,
			PagesFetched: 1,
		}, nil
	})

	out, err := svc.SearchListings(context.Background(), SearchListingsInput{City: radarCoverageCity, Neighborhood: "Лозенец"})
	if err != nil {
		t.Fatalf("SearchListings with Radar down: %v", err)
	}
	if liveCalls != 1 {
		t.Fatalf("live fallback calls = %d, want 1", liveCalls)
	}
	if out.Source != SourceLive || out.Coverage != CoverageUnavailable {
		t.Fatalf("source/coverage = %q/%q, want live/unavailable", out.Source, out.Coverage)
	}
	if out.Readiness != nil {
		t.Fatalf("a live fallback must not claim Radar readiness: %+v", out.Readiness)
	}
	if len(out.Listings) != 1 || out.TotalMatching != 1 {
		t.Fatalf("listings/total = %d/%d, want 1/1: an unavailable Radar must not look empty", len(out.Listings), out.TotalMatching)
	}
	if notes := strings.Join(out.Notes, " | "); !strings.Contains(notes, "not Radar coverage") {
		t.Fatalf("unavailable fallback is not labelled: %s", notes)
	}

	// Radar down and the live source down too: a tool error is returned, never a
	// zero-listing success.
	downCfg := testConfig(t)
	downCache, err := OpenCache(downCfg.DBPath)
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	defer downCache.Close()
	down := newRadarTestService(t, downCfg, downCache, &fakeRadarReader{
		scope:     completeRadarScope(radarTestObservation()),
		searchErr: errors.New("connection refused"),
	}, func(scraper.SearchParams) (scraper.SearchResult, error) {
		return scraper.SearchResult{}, errors.New("source unreachable as well")
	})
	if _, err := down.SearchListings(context.Background(), SearchListingsInput{City: radarCoverageCity, Neighborhood: "Лозенец"}); err == nil {
		t.Fatal("expected an error when neither Radar nor the live source can answer")
	}
}

func TestSearchCachePaginationStaysTruthful(t *testing.T) {
	cfg := testConfig(t)
	cache, err := OpenCache(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	defer cache.Close()

	scope := completeRadarScope(radarTestObservation())
	reader := &fakeRadarReader{scope: scope}
	svc := newRadarTestService(t, cfg, cache, reader, nil)

	params := scraper.SearchParams{City: radarCoverageCity, Neighborhood: "Лозенец", Pages: 1}
	key := searchKey(servingMode(scope, nil, params.Neighborhood), params, 5)

	// Simulate a payload written by a larger request: 40 listings, 120 matches.
	wide := SearchListingsOutput{
		Query:         QuerySummary{City: radarCoverageCity, Neighborhood: "Лозенец", Pages: 1},
		TotalMatching: 120,
		Source:        SourceRadar,
		Coverage:      CoverageComplete,
		ObservedAt:    scope.ObservedAt.Format(time.RFC3339),
	}
	wide.Listings = make([]ListingSummary, 40)
	for i := range wide.Listings {
		wide.Listings[i] = ListingSummary{ID: "a", PriceEUR: 100000, SizeSqM: 60, Agency: "iHOME"}
	}
	wide.Returned = len(wide.Listings)
	payload, err := json.Marshal(wide)
	if err != nil {
		t.Fatalf("marshal cached payload: %v", err)
	}
	if err := cache.PutSearch(key, payload, len(wide.Listings)); err != nil {
		t.Fatalf("PutSearch: %v", err)
	}

	out, err := svc.SearchListings(context.Background(), SearchListingsInput{
		City: radarCoverageCity, Neighborhood: "Лозенец", Limit: 5,
	})
	if err != nil {
		t.Fatalf("SearchListings from cache: %v", err)
	}
	if reader.searchCalls != 0 {
		t.Fatalf("cached answer re-read Radar: %d calls", reader.searchCalls)
	}
	if out.Returned != 5 || len(out.Listings) != 5 {
		t.Fatalf("returned = %d (listings %d), want 5", out.Returned, len(out.Listings))
	}
	if out.TotalMatching != 120 {
		t.Fatalf("total_matching = %d, want 120: pagination must not shrink the total", out.TotalMatching)
	}
	if !out.Sampled {
		t.Fatal("sampled must be true when 5 of 120 matches are shown")
	}
	if out.Source != SourceCache {
		t.Fatalf("source = %q, want cache", out.Source)
	}
	if out.Stats.Count != 5 {
		t.Fatalf("stats.count = %d, want 5: statistics must describe the returned rows", out.Stats.Count)
	}

	// The stored payload must still hold the full population.
	storedPayload, _, ok, err := cache.GetSearch(key, time.Hour)
	if err != nil || !ok {
		t.Fatalf("GetSearch after serving: ok=%v err=%v", ok, err)
	}
	var stored SearchListingsOutput
	if err := json.Unmarshal(storedPayload, &stored); err != nil {
		t.Fatalf("unmarshal stored payload: %v", err)
	}
	if len(stored.Listings) != 40 || stored.TotalMatching != 120 {
		t.Fatalf("cached payload was rewritten to %d listings / total %d", len(stored.Listings), stored.TotalMatching)
	}
}

func TestSearchKeyIncludesModePaginationAndProjection(t *testing.T) {
	params := scraper.SearchParams{City: radarCoverageCity, Neighborhood: "Лозенец", Pages: 1}
	base := searchKey("legacy", params, 40)
	if base == searchKey("legacy", params, 5) {
		t.Fatal("a different limit must produce a different cache key")
	}
	morePages := params
	morePages.Pages = 2
	if base == searchKey("legacy", morePages, 40) {
		t.Fatal("a different page count must produce a different cache key")
	}
	if base == searchKey("radar:lozenets", params, 40) {
		t.Fatal("Radar and live answers must not share a cache key")
	}
	filtered := params
	filtered.MinPrice = 100000
	if base == searchKey("legacy", filtered, 40) {
		t.Fatal("a different price filter must produce a different cache key")
	}
}

func TestRadarSearchAppliesFiltersInTheReader(t *testing.T) {
	cfg := testConfig(t)
	cache, err := OpenCache(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	defer cache.Close()

	observed := radarTestObservation()
	reader := &fakeRadarReader{
		scope: completeRadarScope(observed),
		search: RadarSearchResult{
			Total:      1,
			Listings:   []RadarListing{radarFixtureListing("a1", 150000, 75, observed)},
			ObservedAt: observed,
		},
	}
	svc := newRadarTestService(t, cfg, cache, reader, nil)

	_, err = svc.SearchListings(context.Background(), SearchListingsInput{
		City:         radarCoverageCity,
		Neighborhood: "Лозенец",
		PropertyType: "2-стаен",
		MinPriceEUR:  100000,
		MaxPriceEUR:  200000,
		MinSizeSqM:   60,
		MaxSizeSqM:   120,
	})
	if err != nil {
		t.Fatalf("SearchListings: %v", err)
	}
	got := reader.lastQuery
	if got.MinPriceEUR != 100000 || got.MaxPriceEUR != 200000 || got.MinSizeSqM != 60 || got.MaxSizeSqM != 120 {
		t.Fatalf("reader query filters = %+v, want the submitted bounds", got)
	}
	if got.Scope.Slug != "lozenets" || got.PropertyType != "2-стаен" {
		t.Fatalf("reader query scope/type = %+v, want lozenets / 2-стаен", got)
	}

	args := radarSearchArgs(got)
	if args[0] != "lozenets" {
		t.Fatalf("scope arg = %#v, want lozenets", args[0])
	}
	if args[1] != "2-СТАЕН" {
		t.Fatalf("type arg = %#v, want 2-СТАЕН", args[1])
	}
	if args[2] != float64(100000) || args[3] != float64(200000) || args[4] != float64(60) || args[5] != float64(120) {
		t.Fatalf("bound args = %#v, want the submitted bounds", args[2:])
	}

	// The filter predicates live in the reader's own SQL, not in prose.
	for _, want := range []string{
		`COALESCE(l."priceEur", 0) >= $3`,
		`COALESCE(l."priceEur", 0) <= $4`,
		`COALESCE(l."areaSqm", 0) >= $5`,
		`COALESCE(l."areaSqm", 0) <= $6`,
	} {
		if !strings.Contains(radarSearchWhere, want) {
			t.Errorf("search where clause is missing %q", want)
		}
	}
}

func TestLegacyLiveFallbackAppliesFiltersAndIsLabelled(t *testing.T) {
	cfg := testConfig(t)
	cache, err := OpenCache(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	defer cache.Close()

	reader := &fakeRadarReader{scope: RadarScope{InScope: false, Reason: ScopeReasonRentalNotSupported}}
	svc := newRadarTestService(t, cfg, cache, reader, func(scraper.SearchParams) (scraper.SearchResult, error) {
		return scraper.SearchResult{
			Listings: []scraper.Listing{
				{ID: "cheap", Type: "2-СТАЕН", PriceEUR: 500, SizeSqM: 60, Neighborhood: "Лозенец"},
				{ID: "in-band", Type: "2-СТАЕН", PriceEUR: 1500, SizeSqM: 70, Neighborhood: "Лозенец"},
				{ID: "expensive", Type: "2-СТАЕН", PriceEUR: 2500, SizeSqM: 70, Neighborhood: "Лозенец"},
				{ID: "small", Type: "2-СТАЕН", PriceEUR: 1500, SizeSqM: 40, Neighborhood: "Лозенец"},
			},
			TotalCount:   4,
			PagesFetched: 1,
		}, nil
	})

	out, err := svc.SearchListings(context.Background(), SearchListingsInput{
		City:         radarCoverageCity,
		Neighborhood: "Лозенец",
		Rent:         true,
		MinPriceEUR:  1000,
		MaxPriceEUR:  2000,
		MinSizeSqM:   50,
	})
	if err != nil {
		t.Fatalf("SearchListings: %v", err)
	}
	if out.Source != SourceLive || out.Coverage != CoverageOutOfScope {
		t.Fatalf("source/coverage = %q/%q, want live/out_of_scope", out.Source, out.Coverage)
	}
	if len(out.Listings) != 1 || out.Listings[0].ID != "in-band" {
		t.Fatalf("client-side filters were not applied: %+v", out.Listings)
	}
	if !reflect.DeepEqual(out.ClientFilters, []string{"min_price", "max_price", "min_sqm"}) {
		t.Fatalf("client_filters = %v", out.ClientFilters)
	}
	notes := strings.Join(out.Notes, " | ")
	if !strings.Contains(notes, "not Radar coverage") {
		t.Fatalf("rental fallback is not labelled as outside Radar coverage: %s", notes)
	}
	if !strings.Contains(notes, "applied only to the listings downloaded") {
		t.Fatalf("client-side filtering is not explained: %s", notes)
	}
}

func TestRadarDetailServedWithoutLiveFetch(t *testing.T) {
	cfg := testConfig(t)
	cache, err := OpenCache(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	defer cache.Close()

	observed := radarTestObservation()
	listing := radarFixtureListing("a1", 150000, 75, observed)
	listing.FullDescription = "Просторен апартамент в Лозенец"
	listing.Phones = "0888 123 456"
	listing.PhotoURLs = []string{"https://media.pixelautomate.com/a1/0.webp"}
	listing.CommittedPhotos = 1
	listing.ExpectedPhotos = 1
	listing.Coverage = CoverageComplete

	reader := &fakeRadarReader{listing: listing, found: true}
	liveDetailCalls := 0
	svc := newRadarTestService(t, cfg, cache, reader, nil)
	svc.fetchLiveDetail = func([]scraper.Listing) []error {
		liveDetailCalls++
		return []error{errors.New("a Radar detail must not fetch imot.bg")}
	}

	out, err := svc.GetListing(context.Background(), GetListingInput{ListingID: "a1"})
	if err != nil {
		t.Fatalf("GetListing from Radar: %v", err)
	}
	if liveDetailCalls != 0 {
		t.Fatalf("live detail calls = %d, want 0", liveDetailCalls)
	}
	if reader.detailCalls != 1 {
		t.Fatalf("Radar detail calls = %d, want 1", reader.detailCalls)
	}
	if out.Source != SourceRadar || out.Coverage != CoverageComplete {
		t.Fatalf("source/coverage = %q/%q, want radar/complete", out.Source, out.Coverage)
	}
	if out.Detail == nil || out.Detail.FullDescription != "Просторен апартамент в Лозенец" {
		t.Fatalf("detail = %+v", out.Detail)
	}
	if out.Readiness == nil || out.Readiness.DetailState != "complete" || out.Readiness.MediaState != "complete" {
		t.Fatalf("readiness = %+v", out.Readiness)
	}
	if out.ObservedAt != observed.Format(time.RFC3339) {
		t.Fatalf("observed_at = %q", out.ObservedAt)
	}

	// A repeat is served from the MCP cache: no Radar read, no source call.
	repeated, err := svc.GetListing(context.Background(), GetListingInput{ListingID: "a1"})
	if err != nil {
		t.Fatalf("repeated GetListing: %v", err)
	}
	if reader.detailCalls != 1 {
		t.Fatalf("cached detail re-read Radar: %d calls", reader.detailCalls)
	}
	if liveDetailCalls != 0 {
		t.Fatalf("cached detail made %d live calls", liveDetailCalls)
	}
	if repeated.Source != SourceCache || repeated.Readiness == nil {
		t.Fatalf("cached detail lost its metadata: %+v", repeated)
	}
}

func TestRadarDetailFallbackIsLabelled(t *testing.T) {
	cfg := testConfig(t)
	cache, err := OpenCache(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	defer cache.Close()

	served := 0
	reader := &fakeRadarReader{found: false}
	svc := newRadarTestService(t, cfg, cache, reader, nil)
	svc.fetchLiveDetail = func(listings []scraper.Listing) []error {
		served++
		listings[0].Detail = &scraper.DetailListing{URL: "https://www.imot.bg/obiava-x1", FullDescription: "live detail"}
		return nil
	}

	out, err := svc.GetListing(context.Background(), GetListingInput{ListingID: "x1"})
	if err != nil {
		t.Fatalf("GetListing fallback: %v", err)
	}
	if served != 1 {
		t.Fatalf("live detail calls = %d, want 1", served)
	}
	if out.Source != SourceLive || out.Coverage != CoverageOutOfScope {
		t.Fatalf("source/coverage = %q/%q, want live/out_of_scope", out.Source, out.Coverage)
	}
	if out.Readiness != nil {
		t.Fatalf("a live fallback must not claim Radar readiness: %+v", out.Readiness)
	}
	if notes := strings.Join(out.Notes, " | "); !strings.Contains(notes, "not in the Market Radar catalogue") {
		t.Fatalf("live fallback is not labelled: %s", notes)
	}

	// Radar unreachable: the live page is still served, but labelled unavailable.
	downCfg := testConfig(t)
	downCache, err := OpenCache(downCfg.DBPath)
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	defer downCache.Close()

	downServed := 0
	down := newRadarTestService(t, downCfg, downCache, &fakeRadarReader{listingErr: errors.New("connection refused")}, nil)
	down.fetchLiveDetail = func(listings []scraper.Listing) []error {
		downServed++
		listings[0].Detail = &scraper.DetailListing{URL: "https://www.imot.bg/obiava-y1"}
		return nil
	}
	out, err = down.GetListing(context.Background(), GetListingInput{ListingID: "y1"})
	if err != nil {
		t.Fatalf("GetListing with Radar down: %v", err)
	}
	if downServed != 1 {
		t.Fatalf("live detail calls = %d, want 1", downServed)
	}
	if out.Source != SourceLive || out.Coverage != CoverageUnavailable {
		t.Fatalf("source/coverage = %q/%q, want live/unavailable", out.Source, out.Coverage)
	}
	if notes := strings.Join(out.Notes, " | "); !strings.Contains(notes, "not Radar data") {
		t.Fatalf("unavailable detail fallback is not labelled: %s", notes)
	}
}

func TestRadarDetailUnavailableServesStaleCache(t *testing.T) {
	cfg := testConfig(t)
	cache, err := OpenCache(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	defer cache.Close()

	observed := radarTestObservation()
	listing := radarFixtureListing("a1", 150000, 75, observed)
	listing.Coverage = CoverageComplete
	reader := &fakeRadarReader{listing: listing, found: true}

	liveDetailCalls := 0
	svc := newRadarTestService(t, cfg, cache, reader, nil)
	svc.fetchLiveDetail = func([]scraper.Listing) []error {
		liveDetailCalls++
		return []error{errors.New("must not be called")}
	}

	if _, err := svc.GetListing(context.Background(), GetListingInput{ListingID: "a1"}); err != nil {
		t.Fatalf("first GetListing: %v", err)
	}

	reader.listingErr = errors.New("connection refused")
	reader.found = false
	out, err := svc.GetListing(context.Background(), GetListingInput{ListingID: "a1", Refresh: true})
	if err != nil {
		t.Fatalf("GetListing with Radar down: %v", err)
	}
	if liveDetailCalls != 0 {
		t.Fatalf("stale-cache path made %d live calls", liveDetailCalls)
	}
	if out.Source != SourceCache || out.Coverage != CoverageStale {
		t.Fatalf("source/coverage = %q/%q, want cache/stale", out.Source, out.Coverage)
	}
	if notes := strings.Join(out.Notes, " | "); !strings.Contains(notes, "unreachable") {
		t.Fatalf("stale Radar detail is not explained: %s", notes)
	}
}

func TestClassifyRadarCoverage(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Hour)
	old := now.Add(-72 * time.Hour)
	scrapedOnly := now.Add(-time.Hour)
	complete := now.Add(-time.Hour)
	yes, no := true, false
	ptr := func(v time.Time) *time.Time { return &v }

	cases := []struct {
		name         string
		active       bool
		lastScraped  *time.Time
		lastComplete *time.Time
		status       string
		receipt      *bool
		want         string
	}{
		{name: "inactive scope", active: false, lastScraped: &fresh, lastComplete: &fresh, status: "ok", want: CoverageOutOfScope},
		{name: "no attempts", active: true, want: CoverageNeverCollected},
		{name: "attempt without complete coverage", active: true, lastScraped: &scrapedOnly, status: "incomplete", want: CoveragePartial},
		{name: "finished incomplete", active: true, lastScraped: ptr(scrapedOnly), lastComplete: &complete, status: "incomplete", want: CoveragePartial},
		{name: "receipt says incomplete", active: true, lastScraped: ptr(scrapedOnly), lastComplete: &complete, status: "ok", receipt: &no, want: CoveragePartial},
		{name: "missing run status is not completion", active: true, lastScraped: ptr(scrapedOnly), lastComplete: &complete, status: "", receipt: &yes, want: CoveragePartial},
		{name: "complete but old", active: true, lastScraped: ptr(scrapedOnly), lastComplete: &old, status: "ok", receipt: &yes, want: CoverageStale},
		{name: "complete and fresh", active: true, lastScraped: ptr(scrapedOnly), lastComplete: &complete, status: "ok", receipt: &yes, want: CoverageComplete},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyRadarCoverage(tc.active, tc.lastScraped, tc.lastComplete, tc.status, tc.receipt, 26*time.Hour, now)
			if got != tc.want {
				t.Fatalf("classifyRadarCoverage = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRadarQueriesUseCanonicalSchema is the schema-drift guard: every quoted
// identifier in the static SQL must be a table or column that exists in the
// canonical Radar schema (apps/market-radar/prisma/radar.prisma).
func TestRadarQueriesUseCanonicalSchema(t *testing.T) {
	allowed := map[string]bool{}
	for _, name := range []string{
		// Tables
		"MarketListing", "MarketListingPhoto", "RadarNeighborhood",
		// RadarNeighborhood columns
		"slug", "nameBg", "searchLabel", "city", "active",
		"firstScrapedAt", "lastScrapedAt", "lastRunStatus",
		"lastCompleteScrapedAt", "coverageReceipt",
		// MarketListing columns
		"id", "advId", "url", "neighborhood", "neighborhoodBg",
		"sourceLocationSlug", "sourceNeighborhoodBg", "propertyType",
		"title", "priceEur", "pricePerSqm", "areaSqm", "floor", "yearRange",
		"constructionType", "heatingTec", "heatingGas", "description", "fullDescription",
		"photoUrl", "features", "agentName", "agentPhone", "phones", "isAgency",
		"sellerType", "agencyUrl", "status", "firstSeenAt", "lastSeenAt",
		"imotViewCount", "imotCorrectedAt", "sourcePhotoManifest", "detailEvidence",
		"detailState", "mediaState", "detailLastSuccessAt", "mediaLastSuccessAt",
		// MarketListingPhoto columns
		"listingId", "blobUrl", "order",
	} {
		allowed[name] = true
	}

	identifier := regexp.MustCompile(`"([A-Za-z][A-Za-z0-9]*)"`)
	queries := map[string]string{
		"radarScopeQuery":       radarScopeQuery,
		"radarSearchStatsQuery": radarSearchStatsQuery,
		"radarSearchPageQuery":  radarSearchPageQuery,
		"radarDetailQuery":      radarDetailQuery,
	}
	for name, query := range queries {
		for _, match := range identifier.FindAllStringSubmatch(query, -1) {
			if !allowed[match[1]] {
				t.Errorf("%s references %q, which is not a canonical Radar table or column", name, match[1])
			}
		}
	}

	// The readiness columns follow the enum's own order, and every state is
	// counted once for detail and once for media.
	wantStates := []string{"pending", "in_progress", "retry_due", "complete", "source_limited", "source_unavailable", "blocked"}
	if !reflect.DeepEqual(radarEnrichmentStates, wantStates) {
		t.Fatalf("radarEnrichmentStates = %v, want the canonical enum order %v", radarEnrichmentStates, wantStates)
	}
	for _, state := range wantStates {
		if count := strings.Count(radarSearchStatsQuery, "'"+state+"'"); count != 2 {
			t.Errorf("state %q appears %d times in the readiness query, want 2 (detail and media)", state, count)
		}
	}

	// Legacy tenant-state columns exist on the same MarketListing row but are
	// tenant-private CRM state, not market data: they must never be selected.
	for _, forbidden := range []string{"calledAt", "callOutcome", "salePrice", "notes", "starredAt"} {
		if allowed[forbidden] {
			t.Errorf("%q is tenant-private state and must not be an allowed Radar column", forbidden)
		}
		for name, query := range queries {
			if strings.Contains(query, `"`+forbidden+`"`) {
				t.Errorf("%s reads tenant-private column %q", name, forbidden)
			}
		}
	}
}

func TestOpenRadarReaderValidatesDSN(t *testing.T) {
	if _, err := OpenRadarReader("", time.Second, time.Hour); err == nil {
		t.Fatal("an empty Radar DSN must be rejected")
	}
	if _, err := OpenRadarReader("http://example.com/radar", time.Second, time.Hour); err == nil {
		t.Fatal("a non-Postgres DSN must be rejected")
	}

	reader, err := OpenRadarReader("postgresql://radar_ro:secret@127.0.0.1:5434/radar?sslmode=disable", 3*time.Second, 12*time.Hour)
	if err != nil {
		t.Fatalf("OpenRadarReader with a valid DSN: %v", err)
	}
	defer reader.Close()

	fake := &fakeRadarReader{}
	if err := fake.Close(); err != nil {
		t.Fatalf("fake Close: %v", err)
	}
	if !fake.closed {
		t.Fatal("fake reader did not record Close")
	}
}

func TestLoadConfigRadarSettings(t *testing.T) {
	t.Setenv("IMOT_MCP_TOKENS", "pilot:"+testSecret)
	t.Setenv("IMOT_MCP_RADAR_DSN", "postgresql://radar_ro:secret@radar-db.example:5434/radar?sslmode=verify-full")
	t.Setenv("IMOT_MCP_RADAR_TIMEOUT", "3s")
	t.Setenv("IMOT_MCP_RADAR_FRESHNESS", "12h")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.RadarDSN != "postgresql://radar_ro:secret@radar-db.example:5434/radar?sslmode=verify-full" {
		t.Fatalf("RadarDSN = %q", cfg.RadarDSN)
	}
	if cfg.RadarQueryTimeout != 3*time.Second || cfg.RadarFreshness != 12*time.Hour {
		t.Fatalf("radar timeout/freshness = %v/%v", cfg.RadarQueryTimeout, cfg.RadarFreshness)
	}

	t.Setenv("IMOT_MCP_RADAR_TIMEOUT", "-1s")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("a non-positive Radar timeout must be rejected")
	}
}
