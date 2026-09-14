package mcpserver

// Market Radar read path (Phase U1 of
// docs/plans/market-radar-mcp-unified-serving-plan-2026-09-13.md, owned by
// broker-essentials).
//
// The MCP serves market data from two places and never mixes them silently:
//
//   - the authoritative Market Radar Postgres store, read through a dedicated
//     least-privilege read-only connection (RadarReader). Requests inside the
//     Radar catalogue scope are answered from there with zero source requests.
//   - the pre-existing live imot.bg fallback, kept for rentals and scopes
//     outside the Radar catalogue until the shared scope replaces it. Every
//     fallback answer is labelled through source, coverage and notes.
//
// Every response carries source, coverage, readiness and observed_at so a model
// can tell a verified empty market from a scope that was never collected, is
// stale, is only partially covered, or could not be read at all. An unavailable
// Radar store is never presented as an empty result.
//
// No SQL text, database URL, role or path ever comes from the model. The reader
// exposes named operations only; the Postgres implementation binds every value
// as a query parameter.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/apsisvictor/imot-cli/internal/scraper"
)

// Source values carried in SearchListingsOutput.Source and
// GetListingOutput.Source.
const (
	// SourceRadar: the answer was read from the authoritative Radar store and
	// the scope coverage is complete, partial or never_collected.
	SourceRadar = "radar"
	// SourceRadarStale: the answer was read from Radar but its scope coverage
	// is older than the configured freshness window.
	SourceRadarStale = "radar_stale"
	// SourceCache: the answer came from the MCP's own non-authoritative SQLite
	// cache. Coverage and observed_at are the ones stored with the payload.
	SourceCache = "cache"
	// SourceLive: the answer came from the live imot.bg fallback.
	SourceLive = "live"
	// SourceNone: no read happened, for example when detail was not requested.
	SourceNone = "none"
)

// Coverage values carried in SearchListingsOutput.Coverage and
// GetListingOutput.Coverage.
const (
	// CoverageComplete: the scope's inventory collection completed and is
	// fresh. Only then may a zero-match answer be read as an empty market.
	CoverageComplete = "complete"
	// CoveragePartial: collection ran but complete coverage is not proven, so
	// listings may be missing.
	CoveragePartial = "partial"
	// CoverageStale: a complete observation exists but is older than the
	// configured freshness window.
	CoverageStale = "stale"
	// CoverageNeverCollected: no completed collection exists for this scope.
	CoverageNeverCollected = "never_collected"
	// CoverageOutOfScope: the request is outside the shared Radar scope and
	// was served by the labelled live fallback.
	CoverageOutOfScope = "out_of_scope"
	// CoverageUnavailable: Radar could not be read, so the answer is the
	// labelled live fallback and is not Radar coverage.
	CoverageUnavailable = "unavailable"
)

// Reasons a request is answered outside Radar coverage. They stay a bounded set
// so notes and tests cannot drift into arbitrary prose.
const (
	ScopeReasonRadarNotConfigured   = "radar_not_configured"
	ScopeReasonRentalNotSupported   = "rental_not_supported"
	ScopeReasonCityNotCovered       = "city_not_covered"
	ScopeReasonCitywideNotCovered   = "citywide_not_covered"
	ScopeReasonNotInCatalogue       = "neighborhood_not_in_catalogue"
	ScopeReasonNeighborhoodInactive = "neighborhood_inactive"
	ScopeReasonNotInRadar           = "not_in_radar_catalogue"
)

// radarCoverageCity is the city the Radar catalogue currently covers. The
// catalogue's own "city" column is authoritative; this constant only lets the
// reader reject another city before spending a query, and it changes when the
// Radar catalogue expands.
const radarCoverageCity = "София"

// radarEnrichmentStates mirrors the RadarEnrichmentState enum in the canonical
// Radar schema (apps/market-radar/prisma/radar.prisma). The order is the enum's
// own order and the Postgres readiness query selects its columns in this order.
var radarEnrichmentStates = []string{
	"pending",
	"in_progress",
	"retry_due",
	"complete",
	"source_limited",
	"source_unavailable",
	"blocked",
}

// RadarReader is the read-only contract the MCP tool layer uses. It exposes
// named operations over the canonical Radar schema; it accepts no SQL, URL,
// role or path from the caller. Implementations must never write.
type RadarReader interface {
	// ResolveScope decides whether a normalized request is inside the Radar
	// catalogue. A request outside it returns InScope false with a bounded
	// Reason and no error, so the caller can serve the labelled live fallback.
	// A reader that cannot answer returns an error, which is never treated as
	// an empty or out-of-scope result.
	ResolveScope(ctx context.Context, city, neighborhood string, rent bool) (RadarScope, error)

	// SearchListings returns the bounded page rows plus the query population's
	// readiness and observation time.
	SearchListings(ctx context.Context, q RadarSearchQuery) (RadarSearchResult, error)

	// GetListing returns one listing by its imot.bg advert id (Radar's advId).
	// found is false only when the listing is genuinely absent; an unreadable
	// store returns an error.
	GetListing(ctx context.Context, listingID string) (listing RadarListing, found bool, err error)

	// Close releases the reader's connection pool.
	Close() error
}

// RadarScope is the resolved coverage context for one request. Coverage refers
// to the neighbourhood's inventory collection, not to the caller's filters.
type RadarScope struct {
	InScope       bool
	Reason        string
	Slug          string
	NameBg        string
	SearchLabel   string
	City          string
	Active        bool
	Coverage      string
	LastScrapedAt time.Time
	CompleteAt    time.Time
	LastRunStatus string
	ObservedAt    time.Time
}

// RadarSearchQuery is a fully normalized, already scoped Radar query. The
// service applies no SQL of its own; every value here is bound as a parameter.
type RadarSearchQuery struct {
	Scope        RadarScope
	PropertyType string // normalized imot type, for example "2-стаен"
	MinPriceEUR  int
	MaxPriceEUR  int
	MinSizeSqM   int
	MaxSizeSqM   int
	Limit        int
}

// RadarSearchResult is one bounded page plus the matching population's
// readiness and last observation time.
type RadarSearchResult struct {
	Listings   []RadarListing
	Total      int
	Readiness  RadarReadiness
	ObservedAt time.Time
}

// RadarReadiness reports how complete detail and media enrichment is for the
// listings a result covers. Counts are over the whole matching population, not
// only the returned page, so committed and expected are comparable.
type RadarReadiness struct {
	Scope            string         `json:"scope" jsonschema:"neighborhood or listing"`
	DetailState      string         `json:"detail_state" jsonschema:"aggregate detail state: complete, partial, pending, retry_due, in_progress, source_limited, source_unavailable, blocked, or none when nothing matched"`
	MediaState       string         `json:"media_state" jsonschema:"aggregate media state using the same vocabulary as detail_state"`
	Matched          int            `json:"matched" jsonschema:"listings matching the reader query"`
	DetailComplete   int            `json:"detail_complete" jsonschema:"matching listings whose detail standard is met"`
	MediaComplete    int            `json:"media_complete" jsonschema:"matching listings whose media standard is met"`
	DetailStates     map[string]int `json:"detail_states,omitempty" jsonschema:"exact per-state counts for detail enrichment"`
	MediaStates      map[string]int `json:"media_states,omitempty" jsonschema:"exact per-state counts for media enrichment"`
	CommittedPhotos  int            `json:"committed_photos" jsonschema:"committed Radar photo rows for the matching listings"`
	ExpectedPhotos   int            `json:"expected_photos,omitempty" jsonschema:"photos expected from the collected source manifests, when recorded"`
	DetailObservedAt string         `json:"detail_observed_at,omitempty"`
	MediaObservedAt  string         `json:"media_observed_at,omitempty"`
	Notes            []string       `json:"notes,omitempty"`
}

// RadarListing is one Radar row in the shape the MCP projection needs. It is
// used both for search rows (summary fields only) and one full detail row.
type RadarListing struct {
	ID     string
	AdvID  string
	URL    string
	City   string
	Status string

	Neighborhood   string
	NeighborhoodBg string
	PropertyType   string

	Title           string
	Description     string
	FullDescription string

	PriceEUR    float64
	PricePerSqm float64
	AreaSqm     float64
	Floor       string
	YearRange   string

	ConstructionType string
	HeatingTEC       string
	HeatingGas       string
	SellerType       string
	IsAgency         bool
	AgentName        string
	AgentPhone       string
	Phones           string
	AgencyURL        string

	Features []string

	PhotoURL  string
	PhotoURLs []string

	FirstSeenAt time.Time
	LastSeenAt  time.Time
	ViewCount   int
	CorrectedAt *time.Time

	DetailState         string
	MediaState          string
	DetailLastSuccessAt *time.Time
	MediaLastSuccessAt  *time.Time
	DetailEvidence      *RadarDetailEvidence

	CommittedPhotos int
	ExpectedPhotos  int

	// Coverage is the resolved coverage of the listing's neighbourhood, filled
	// by the reader for detail rows.
	Coverage string
}

// RadarDetailEvidence mirrors the collector's detailEvidence JSON on
// MarketListing. Keeping it preserves the producer's own presence contract
// instead of inventing values for fields the producer marked unknown.
type RadarDetailEvidence struct {
	Version          int                                    `json:"version"`
	Standard         string                                 `json:"standard"`
	ProducerContract string                                 `json:"producerContract"`
	AdvertID         string                                 `json:"advertId"`
	CanonicalURL     string                                 `json:"canonicalUrl"`
	ObservedAt       string                                 `json:"observedAt"`
	Fields           map[string]scraper.DetailFieldEvidence `json:"fields"`
}

// classifyRadarCoverage turns one RadarNeighborhood row into a coverage value.
// It is deliberately conservative: complete requires a finished attempt marked
// ok, a receipt that does not say incomplete, and an observation inside the
// freshness window. Everything else reports a weaker state rather than
// claiming an empty market.
func classifyRadarCoverage(
	active bool,
	lastScraped, lastComplete *time.Time,
	lastRunStatus string,
	receiptComplete *bool,
	freshness time.Duration,
	now time.Time,
) string {
	if !active {
		return CoverageOutOfScope
	}
	if lastComplete == nil {
		if lastScraped == nil {
			return CoverageNeverCollected
		}
		// Collection ran but no complete coverage was ever recorded.
		return CoveragePartial
	}
	// The collector writes "ok" only for a completed successful sweep. A
	// missing status is not evidence of completion, even when a timestamp and
	// receipt happen to be present.
	if lastRunStatus != "ok" {
		return CoveragePartial
	}
	// A completion timestamp without a valid receipt is insufficient proof of
	// complete coverage. The receipt is the collector's explicit completeness
	// assertion; missing or malformed JSON must remain conservative.
	if receiptComplete == nil || !*receiptComplete {
		return CoveragePartial
	}
	if freshness > 0 && now.Sub(*lastComplete) > freshness {
		return CoverageStale
	}
	return CoverageComplete
}

// coverageReceiptQuery is the JSON contract owned by the Radar collector.
// Pointers distinguish a required zero value from a missing field, while
// TypeSlug/FinishedAt/TaxonomyHash retain their explicit null values.
type coverageReceiptQuery struct {
	TypeSlug                 json.RawMessage `json:"typeSlug"`
	ResolvedNeighborhoodSlug *string         `json:"resolvedNeighborhoodSlug"`
	PagesPlanned             *int            `json:"pagesPlanned"`
	PagesFetched             *int            `json:"pagesFetched"`
	ReportedCount            *int            `json:"reportedCount"`
	UniqueIDs                *int            `json:"uniqueIds"`
	Partial                  *bool           `json:"partial"`
	EmptyVerified            *bool           `json:"emptyVerified"`
	Saturated                *bool           `json:"saturated"`
}

type coverageReceipt struct {
	Version            *int                   `json:"version"`
	RunID              *string                `json:"runId"`
	StartedAt          *string                `json:"startedAt"`
	FinishedAt         json.RawMessage        `json:"finishedAt"`
	Scope              *string                `json:"scope"`
	TaxonomyHash       json.RawMessage        `json:"taxonomyHash"`
	ExpectedTypeSlugs  []string               `json:"expectedTypeSlugs"`
	AttemptedTypeSlugs []string               `json:"attemptedTypeSlugs"`
	Complete           *bool                  `json:"complete"`
	Reasons            []string               `json:"reasons"`
	Queries            []coverageReceiptQuery `json:"queries"`
}

// parseCoverageReceiptComplete validates the fields that make a collector
// receipt authoritative before allowing it to establish complete coverage.
// The canonical validator lives with the Radar producer; this strict mirror
// prevents a malformed or schema-drifted JSON value from turning a scope into
// a verified empty market at the read boundary.
func parseCoverageReceiptComplete(raw string) *bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var receipt coverageReceipt
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return nil
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil
	}
	if receipt.Version == nil || *receipt.Version != 1 || receipt.RunID == nil || strings.TrimSpace(*receipt.RunID) == "" ||
		receipt.StartedAt == nil || !validReceiptInstant(*receipt.StartedAt) || receipt.Scope == nil ||
		(*receipt.Scope != "catalogue" && *receipt.Scope != "neighbourhood") || receipt.Complete == nil ||
		receipt.FinishedAt == nil || receipt.TaxonomyHash == nil || receipt.ExpectedTypeSlugs == nil ||
		receipt.AttemptedTypeSlugs == nil || receipt.Reasons == nil || receipt.Queries == nil {
		return nil
	}
	if !validNullableReceiptString(receipt.FinishedAt, false) || !validNullableReceiptString(receipt.TaxonomyHash, true) ||
		!validReceiptSlugs(receipt.ExpectedTypeSlugs) || !validReceiptSlugs(receipt.AttemptedTypeSlugs) ||
		!uniqueReceiptStrings(receipt.ExpectedTypeSlugs) || !uniqueReceiptStrings(receipt.AttemptedTypeSlugs) {
		return nil
	}

	expected := make(map[string]bool, len(receipt.ExpectedTypeSlugs))
	for _, slug := range receipt.ExpectedTypeSlugs {
		expected[slug] = true
	}
	attempted := make(map[string]bool, len(receipt.AttemptedTypeSlugs))
	for _, slug := range receipt.AttemptedTypeSlugs {
		attempted[slug] = true
		if !expected[slug] {
			return nil
		}
	}
	started, _ := time.Parse(time.RFC3339, *receipt.StartedAt)
	if *receipt.Complete {
		if !validNullableReceiptString(receipt.FinishedAt, false) || string(receipt.FinishedAt) == "null" ||
			!validReceiptInstantValue(receipt.FinishedAt, started) || string(receipt.TaxonomyHash) == "null" ||
			len(receipt.ExpectedTypeSlugs) == 0 || len(receipt.Queries) == 0 {
			return nil
		}
	}

	queryKeys := make(map[string]bool, len(receipt.Queries))
	for _, query := range receipt.Queries {
		if query.TypeSlug == nil || query.ResolvedNeighborhoodSlug == nil || !validReceiptSlug(*query.ResolvedNeighborhoodSlug) ||
			query.PagesPlanned == nil || *query.PagesPlanned < 0 || query.PagesFetched == nil || *query.PagesFetched < 0 ||
			query.ReportedCount == nil || *query.ReportedCount < 0 || query.UniqueIDs == nil || *query.UniqueIDs < 0 ||
			query.Partial == nil || query.EmptyVerified == nil || query.Saturated == nil {
			return nil
		}
		typeSlug, ok := nullableReceiptSlug(query.TypeSlug)
		if !ok || (typeSlug != "" && !attempted[typeSlug]) {
			return nil
		}
		key := typeSlug + "|" + *query.ResolvedNeighborhoodSlug
		if queryKeys[key] {
			return nil
		}
		queryKeys[key] = true
		if *query.EmptyVerified && (*query.ReportedCount != 0 || *query.UniqueIDs != 0) {
			return nil
		}
		if *receipt.Complete {
			if *query.Partial || (*query.Saturated && typeSlug != "") || *query.PagesPlanned < 1 ||
				*query.PagesFetched < *query.PagesPlanned ||
				(!*query.Saturated && *query.UniqueIDs != *query.ReportedCount) ||
				(*query.UniqueIDs == 0 && !*query.EmptyVerified) {
				return nil
			}
		}
	}
	if *receipt.Complete {
		wholeScope, wholeScopeSaturated := false, false
		for _, query := range receipt.Queries {
			typeSlug, _ := nullableReceiptSlug(query.TypeSlug)
			if typeSlug == "" {
				wholeScope = true
				wholeScopeSaturated = *query.Saturated
			}
		}
		// A non-saturated whole-scope query covers the catalogue without
		// requiring redundant child-type queries. Saturated or split coverage
		// must prove every expected type, matching the producer contract.
		if !wholeScope || wholeScopeSaturated {
			for _, slug := range receipt.ExpectedTypeSlugs {
				if !attempted[slug] {
					return nil
				}
			}
		}
	}
	return receipt.Complete
}

func validReceiptInstant(value string) bool {
	_, err := time.Parse(time.RFC3339, value)
	return err == nil
}

func validReceiptInstantValue(raw json.RawMessage, started time.Time) bool {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	finished, err := time.Parse(time.RFC3339, value)
	return err == nil && !finished.Before(started)
}

func validNullableReceiptString(raw json.RawMessage, hash bool) bool {
	if string(raw) == "null" {
		return true
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value) == "" {
		return false
	}
	if hash {
		if len(value) != 64 {
			return false
		}
		for _, ch := range value {
			if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
				return false
			}
		}
	}
	return validReceiptInstant(value) || hash
}

func validReceiptSlugs(slugs []string) bool {
	for _, slug := range slugs {
		if !validReceiptSlug(slug) {
			return false
		}
	}
	return true
}

func validReceiptSlug(slug string) bool {
	if len(slug) < 1 || len(slug) > 64 {
		return false
	}
	for i, ch := range slug {
		if (i == 0 && !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9'))) ||
			(i > 0 && !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-')) {
			return false
		}
	}
	return true
}

func uniqueReceiptStrings(values []string) bool {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}

func nullableReceiptSlug(raw json.RawMessage) (string, bool) {
	if string(raw) == "null" {
		return "", true
	}
	var slug string
	if json.Unmarshal(raw, &slug) != nil || !validReceiptSlug(slug) {
		return "", false
	}
	return slug, true
}

// aggregateStandardState reduces per-state counts to one model-facing state.
// A zero-match population reports "none": there is nothing to enrich, and the
// scope coverage already carries whether the emptiness is verified.
func aggregateStandardState(total int, states map[string]int) string {
	if total <= 0 {
		return "none"
	}
	if states["complete"] >= total {
		return "complete"
	}
	if states["complete"] > 0 {
		return "partial"
	}
	// No row attained the standard: report the most blocking state present.
	for _, state := range []string{"blocked", "source_unavailable", "source_limited", "retry_due", "in_progress"} {
		if states[state] > 0 {
			return state
		}
	}
	return "pending"
}

// readinessForListing builds the single-listing readiness shape.
func readinessForListing(l RadarListing) RadarReadiness {
	detailStates := map[string]int{}
	mediaStates := map[string]int{}
	if l.DetailState != "" {
		detailStates[l.DetailState] = 1
	}
	if l.MediaState != "" {
		mediaStates[l.MediaState] = 1
	}
	readiness := RadarReadiness{
		Scope:           "listing",
		Matched:         1,
		DetailState:     aggregateStandardState(1, detailStates),
		MediaState:      aggregateStandardState(1, mediaStates),
		DetailComplete:  detailStates["complete"],
		MediaComplete:   mediaStates["complete"],
		CommittedPhotos: l.CommittedPhotos,
		ExpectedPhotos:  l.ExpectedPhotos,
	}
	if l.DetailState != "" {
		readiness.DetailStates = detailStates
	}
	if l.MediaState != "" {
		readiness.MediaStates = mediaStates
	}
	readiness.DetailObservedAt = formatObservedAt(timePtr(l.DetailLastSuccessAt))
	readiness.MediaObservedAt = formatObservedAt(timePtr(l.MediaLastSuccessAt))
	readiness.Notes = radarReadinessNotes(readiness)
	return readiness
}

// radarReadinessNotes explains an incomplete enrichment state without turning
// it into an error: the card data is still readable.
func radarReadinessNotes(r RadarReadiness) []string {
	if r.Matched <= 0 {
		return nil
	}
	var notes []string
	if r.DetailComplete < r.Matched {
		notes = append(notes, fmt.Sprintf(
			"Detail enrichment is %s for %d of %d matched listings; descriptions, features or contacts may be incomplete.",
			r.DetailState, r.Matched-r.DetailComplete, r.Matched))
	}
	if r.MediaComplete < r.Matched {
		notes = append(notes, fmt.Sprintf(
			"Media enrichment is %s for %d of %d matched listings; photo galleries may be incomplete.",
			r.MediaState, r.Matched-r.MediaComplete, r.Matched))
	}
	if r.ExpectedPhotos > 0 && r.CommittedPhotos < r.ExpectedPhotos {
		notes = append(notes, fmt.Sprintf(
			"Committed Radar photos (%d) are fewer than the collected source manifest (%d).",
			r.CommittedPhotos, r.ExpectedPhotos))
	}
	return notes
}

// toRadarListings projects Radar rows onto the scraper's card shape so the
// existing summary, statistics and filter helpers stay the single projection
// owner for both sources.
func toRadarListings(rows []RadarListing) []scraper.Listing {
	out := make([]scraper.Listing, 0, len(rows))
	for _, row := range rows {
		agency := ""
		if row.IsAgency {
			agency = firstNonEmpty(row.AgentName, row.AgencyURL)
		}
		out = append(out, scraper.Listing{
			ID:           row.AdvID,
			Type:         row.PropertyType,
			City:         row.City,
			Neighborhood: row.NeighborhoodBg,
			PriceEUR:     roundToInt(row.PriceEUR),
			SizeSqM:      roundToInt(row.AreaSqm),
			Floor:        row.Floor,
			YearBuilt:    row.YearRange,
			Description:  firstNonEmpty(row.Description, row.FullDescription),
			Phone:        firstNonEmpty(row.AgentPhone, row.Phones),
			Agency:       agency,
			URL:          row.URL,
		})
	}
	return out
}

// toRadarSummaries is the model-facing projection of Radar rows. Radar's own
// pricePerSqm wins over the derived value when it is present.
func toRadarSummaries(rows []RadarListing) []ListingSummary {
	out := toSummaries(toRadarListings(rows))
	for i := range out {
		if i < len(rows) && rows[i].PricePerSqm > 0 {
			out[i].PricePerSqM = rows[i].PricePerSqm
		}
	}
	return out
}

// toDetailListing projects one Radar row onto the MCP detail shape. The
// producer's own field evidence is carried through when the collector recorded
// it, so presence semantics are not reinvented here.
func (l RadarListing) toDetailListing() scraper.DetailListing {
	detail := scraper.DetailListing{
		URL:              l.URL,
		FullDescription:  firstNonEmpty(l.FullDescription, l.Description),
		Floor:            l.Floor,
		YearBuilt:        l.YearRange,
		ConstructionType: l.ConstructionType,
		HeatingTEC:       l.HeatingTEC,
		HeatingGas:       l.HeatingGas,
		SellerType:       l.SellerType,
		Phones:           l.Phones,
		AgencyURL:        l.AgencyURL,
		ViewCount:        l.ViewCount,
		PhotoURL:         firstNonEmpty(l.PhotoURL, firstString(l.PhotoURLs)),
		PhotoURLs:        l.PhotoURLs,
		Features:         l.Features,
		BrokerName:       l.AgentName,
		BrokerPhone:      l.AgentPhone,
	}
	if l.CorrectedAt != nil {
		detail.CorrectedAt = l.CorrectedAt.UTC().Format(time.RFC3339)
	}
	if l.DetailEvidence != nil {
		detail.ContractVersion = l.DetailEvidence.ProducerContract
		detail.AdvertID = l.DetailEvidence.AdvertID
		detail.FieldEvidence = l.DetailEvidence.Fields
		if published, ok := l.DetailEvidence.Fields[scraper.DetailKeyPublishedAt]; ok {
			if raw, ok := published.Raw.(string); ok {
				detail.PublishedAt = raw
			}
		}
	}
	return detail
}

// formatObservedAt renders an observation timestamp, or "" when unknown.
func formatObservedAt(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// pickScopeObservation returns the newest scope-level observation, preferring
// the last complete collection because only it explains a zero-match answer.
func pickScopeObservation(scope RadarScope) time.Time {
	if !scope.CompleteAt.IsZero() {
		return scope.CompleteAt
	}
	return scope.LastScrapedAt
}

func timePtr(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func firstString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func roundToInt(v float64) int {
	if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return int(math.Round(v))
}
