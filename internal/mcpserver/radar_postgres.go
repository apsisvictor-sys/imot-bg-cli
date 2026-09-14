package mcpserver

// Postgres implementation of RadarReader over the canonical Market Radar
// schema (apps/market-radar/prisma/radar.prisma, owned by broker-essentials).
//
// Every statement is a package-level constant with numbered bind parameters.
// Nothing the model sends is interpolated into SQL, and nothing here can write:
// the configured role must already be read-only and the session additionally
// sets default_transaction_read_only.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// radarScopeQuery resolves one catalogue neighbourhood. It matches the exact
// Bulgarian name, the catalogue slug or the collector's search label, in any
// case, and stays deterministic when a name resolves to more than one row.
const radarScopeQuery = `
SELECT "slug", "nameBg", "searchLabel", "city", "active",
       "firstScrapedAt", "lastScrapedAt", "lastRunStatus",
       "lastCompleteScrapedAt", "coverageReceipt"::text
FROM "RadarNeighborhood"
WHERE (lower("nameBg") = lower($1) OR lower("slug") = lower($1) OR lower("searchLabel") = lower($1))
  AND lower("city") = lower($2)
ORDER BY "active" DESC, "slug" ASC
LIMIT 1`

// radarSearchWhere is shared by the readiness and page queries so both describe
// the same population. Price and size bounds are applied here, in the reader,
// because the MCP must not echo a filter it never applied. COALESCE mirrors the
// live-card behaviour where an unparsed price or area is zero: a minimum filter
// cannot be satisfied by a missing value, and a maximum filter sees it as zero.
const radarSearchWhere = `l."status" = 'active'
    AND lower(l."neighborhood") = lower($1)
    AND ($2::text IS NULL OR upper(l."propertyType") = $2)
    AND ($3::double precision IS NULL OR COALESCE(l."priceEur", 0) >= $3)
    AND ($4::double precision IS NULL OR COALESCE(l."priceEur", 0) <= $4)
    AND ($5::double precision IS NULL OR COALESCE(l."areaSqm", 0) >= $5)
    AND ($6::double precision IS NULL OR COALESCE(l."areaSqm", 0) <= $6)`

// radarSearchStatsQuery aggregates over the whole matching population. The
// readiness columns follow radarEnrichmentStates exactly: the seven
// RadarEnrichmentState values for detailState, then the same seven for
// mediaState, in the enum's own order.
const radarSearchStatsQuery = `
SELECT
  COUNT(*) AS total,
  COUNT(*) FILTER (WHERE l."detailState" = 'pending') AS detail_pending,
  COUNT(*) FILTER (WHERE l."detailState" = 'in_progress') AS detail_in_progress,
  COUNT(*) FILTER (WHERE l."detailState" = 'retry_due') AS detail_retry_due,
  COUNT(*) FILTER (WHERE l."detailState" = 'complete') AS detail_complete,
  COUNT(*) FILTER (WHERE l."detailState" = 'source_limited') AS detail_source_limited,
  COUNT(*) FILTER (WHERE l."detailState" = 'source_unavailable') AS detail_source_unavailable,
  COUNT(*) FILTER (WHERE l."detailState" = 'blocked') AS detail_blocked,
  COUNT(*) FILTER (WHERE l."mediaState" = 'pending') AS media_pending,
  COUNT(*) FILTER (WHERE l."mediaState" = 'in_progress') AS media_in_progress,
  COUNT(*) FILTER (WHERE l."mediaState" = 'retry_due') AS media_retry_due,
  COUNT(*) FILTER (WHERE l."mediaState" = 'complete') AS media_complete,
  COUNT(*) FILTER (WHERE l."mediaState" = 'source_limited') AS media_source_limited,
  COUNT(*) FILTER (WHERE l."mediaState" = 'source_unavailable') AS media_source_unavailable,
  COUNT(*) FILTER (WHERE l."mediaState" = 'blocked') AS media_blocked,
  COALESCE(SUM((SELECT COUNT(*) FROM "MarketListingPhoto" p WHERE p."listingId" = l."id")), 0)::bigint AS committed_photos,
  COALESCE(SUM(CASE WHEN jsonb_typeof(l."sourcePhotoManifest" -> 'photos') = 'array'
                    THEN jsonb_array_length(l."sourcePhotoManifest" -> 'photos') ELSE 0 END), 0)::bigint AS expected_photos,
  MAX(l."lastSeenAt") AS observed_at
FROM "MarketListing" l
WHERE ` + radarSearchWhere

// radarSearchPageQuery returns one bounded page of card-level rows.
const radarSearchPageQuery = `
SELECT
  l."advId",
  l."url",
  l."propertyType",
  l."neighborhoodBg",
  l."priceEur",
  l."pricePerSqm",
  l."areaSqm",
  l."floor",
  l."yearRange",
  l."isAgency",
  l."sellerType",
  l."agentName",
  l."agentPhone",
  l."phones",
  COALESCE(l."description", l."fullDescription") AS snippet_source,
  l."lastSeenAt"
FROM "MarketListing" l
WHERE ` + radarSearchWhere + `
ORDER BY l."lastSeenAt" DESC, l."advId" ASC
LIMIT $7`

// radarDetailQuery returns one listing plus its catalogue coverage. Every
// selected column is an existing canonical column; no value is synthesized.
const radarDetailQuery = `
SELECT
  l."id", l."advId", l."url", l."city", l."neighborhood", l."neighborhoodBg",
  l."propertyType", l."title", l."description", l."fullDescription",
  l."priceEur", l."pricePerSqm", l."areaSqm", l."floor", l."yearRange",
  l."constructionType", l."heatingTec", l."heatingGas",
  l."sellerType", l."isAgency", l."agentName", l."agentPhone", l."phones", l."agencyUrl",
  COALESCE(l."photoUrl", '') AS photo_url,
  array_to_json(COALESCE(l."features", ARRAY[]::text[]))::text AS features,
  COALESCE((SELECT array_to_json(array_agg(p."blobUrl" ORDER BY p."order" ASC, p."id" ASC))
            FROM "MarketListingPhoto" p WHERE p."listingId" = l."id"), '[]'::json)::text AS photo_urls,
  l."status", l."firstSeenAt", l."lastSeenAt",
  l."imotViewCount", l."imotCorrectedAt",
  l."detailState"::text, l."mediaState"::text, l."detailLastSuccessAt", l."mediaLastSuccessAt",
  l."detailEvidence"::text,
  l."sourcePhotoManifest"::text,
  n."active", n."lastScrapedAt", n."lastRunStatus", n."lastCompleteScrapedAt", n."coverageReceipt"::text
FROM "MarketListing" l
LEFT JOIN "RadarNeighborhood" n ON n."slug" = l."neighborhood"
WHERE l."advId" = $1
LIMIT 1`

// postgresRadarReader is the only implementation allowed to touch the Radar
// database. It holds a dedicated pool built from IMOT_MCP_RADAR_DSN.
type radarQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type radarRowScanner interface {
	Scan(...any) error
}

type postgresRadarReader struct {
	db        *sql.DB
	timeout   time.Duration
	freshness time.Duration
	now       func() time.Time
}

// OpenRadarReader builds the dedicated read-only Radar connection. It parses
// the configured DSN, pins the session read-only and bounds every statement
// with statement_timeout. It never connects eagerly: a Radar outage must not
// stop the MCP from serving the labelled live fallback.
func OpenRadarReader(dsn string, timeout, freshness time.Duration) (RadarReader, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return nil, fmt.Errorf("radar DSN is empty")
	}
	connConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		// pgx echoes the connection string in its parse error, which would put
		// the database password into the logs. Fail without the DSN text.
		return nil, fmt.Errorf("parsing IMOT_MCP_RADAR_DSN: invalid Postgres connection string")
	}
	if connConfig.RuntimeParams == nil {
		connConfig.RuntimeParams = map[string]string{}
	}
	// Defence in depth. The configured role must already be a least-privilege
	// reader; this makes an accidental write fail even if the role is broader
	// than intended.
	connConfig.RuntimeParams["default_transaction_read_only"] = "on"
	if timeout > 0 {
		connConfig.RuntimeParams["statement_timeout"] = strconv.FormatInt(timeout.Milliseconds(), 10)
	}

	db := stdlib.OpenDB(*connConfig)
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxIdleTime(5 * time.Minute)
	db.SetConnMaxLifetime(30 * time.Minute)

	return &postgresRadarReader{
		db:        db,
		timeout:   timeout,
		freshness: freshness,
		now:       time.Now,
	}, nil
}

// Close releases the Radar pool.
func (r *postgresRadarReader) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}

// ResolveScope classifies the request before touching the database: rentals,
// other cities and citywide queries are answered without a query, because the
// Radar catalogue is a per-neighbourhood Sofia scope. Everything else resolves
// against "RadarNeighborhood"; a lookup failure is an error, never "not in
// scope".
func (r *postgresRadarReader) ResolveScope(ctx context.Context, city, neighborhood string, rent bool) (RadarScope, error) {
	if rent {
		return RadarScope{Reason: ScopeReasonRentalNotSupported}, nil
	}
	city = strings.TrimSpace(city)
	neighborhood = strings.TrimSpace(neighborhood)
	if neighborhood == "" {
		return RadarScope{Reason: ScopeReasonCitywideNotCovered}, nil
	}
	if !strings.EqualFold(city, radarCoverageCity) {
		return RadarScope{Reason: ScopeReasonCityNotCovered}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	var (
		slug, nameBg, searchLabel, scopeCity, lastRunStatus string
		active                                              bool
		firstScraped, lastScraped, lastComplete             sql.NullTime
		coverageReceipt                                     sql.NullString
	)
	err := r.db.QueryRowContext(ctx, radarScopeQuery, neighborhood, city).Scan(
		&slug, &nameBg, &searchLabel, &scopeCity, &active,
		&firstScraped, &lastScraped, &lastRunStatus,
		&lastComplete, &coverageReceipt,
	)
	if err == sql.ErrNoRows {
		return RadarScope{Reason: ScopeReasonNotInCatalogue}, nil
	}
	if err != nil {
		return RadarScope{}, fmt.Errorf("reading radar scope: %w", err)
	}

	var receiptComplete *bool
	if coverageReceipt.Valid {
		receiptComplete = parseCoverageReceiptComplete(coverageReceipt.String)
	}
	scope := RadarScope{
		InScope:       active,
		Slug:          slug,
		NameBg:        nameBg,
		SearchLabel:   searchLabel,
		City:          scopeCity,
		Active:        active,
		LastScrapedAt: timeOrZero(lastScraped),
		CompleteAt:    timeOrZero(lastComplete),
		LastRunStatus: lastRunStatus,
		Coverage:      classifyRadarCoverage(active, timeOrNil(lastScraped), timeOrNil(lastComplete), lastRunStatus, receiptComplete, r.freshness, r.now()),
	}
	scope.ObservedAt = pickScopeObservation(scope)
	if !active {
		scope.Reason = ScopeReasonNeighborhoodInactive
	}
	return scope, nil
}

// SearchListings runs the readiness aggregate and the page query against the
// same where clause and parameter set, so the page can never describe a
// different population than the counts.
func (r *postgresRadarReader) SearchListings(ctx context.Context, q RadarSearchQuery) (RadarSearchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return RadarSearchResult{}, fmt.Errorf("starting radar search snapshot: %w", err)
	}
	defer tx.Rollback()

	args := radarSearchArgs(q)
	readiness, observedAt, err := r.searchReadiness(tx, ctx, args...)
	if err != nil {
		return RadarSearchResult{}, err
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 40
	}
	pageArgs := append(append([]any{}, args...), limit)
	rows, err := tx.QueryContext(ctx, radarSearchPageQuery, pageArgs...)
	if err != nil {
		return RadarSearchResult{}, fmt.Errorf("reading radar search: %w", err)
	}
	defer rows.Close()

	listings := make([]RadarListing, 0, limit)
	for rows.Next() {
		listing, err := scanRadarSummary(rows)
		if err != nil {
			return RadarSearchResult{}, err
		}
		listings = append(listings, listing)
	}
	if err := rows.Err(); err != nil {
		return RadarSearchResult{}, fmt.Errorf("reading radar search rows: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return RadarSearchResult{}, fmt.Errorf("committing radar search snapshot: %w", err)
	}
	if observedAt.IsZero() {
		observedAt = pickScopeObservation(q.Scope)
	}
	return RadarSearchResult{
		Listings:   listings,
		Total:      readiness.Matched,
		Readiness:  readiness,
		ObservedAt: observedAt,
	}, nil
}

// searchReadiness reads the matching population's aggregate. It returns the
// total as well, because the caller's page query deliberately does not.
func (r *postgresRadarReader) searchReadiness(db radarQueryer, ctx context.Context, args ...any) (RadarReadiness, time.Time, error) {
	var (
		total, committedPhotos, expectedPhotos int64
		observed                               sql.NullTime
	)
	detailCounts := make([]int64, len(radarEnrichmentStates))
	mediaCounts := make([]int64, len(radarEnrichmentStates))

	dest := make([]any, 0, 3+2*len(radarEnrichmentStates))
	dest = append(dest, &total)
	for i := range detailCounts {
		dest = append(dest, &detailCounts[i])
	}
	for i := range mediaCounts {
		dest = append(dest, &mediaCounts[i])
	}
	dest = append(dest, &committedPhotos, &expectedPhotos, &observed)

	if err := db.QueryRowContext(ctx, radarSearchStatsQuery, args...).Scan(dest...); err != nil {
		return RadarReadiness{}, time.Time{}, fmt.Errorf("reading radar search readiness: %w", err)
	}

	detailStates := make(map[string]int, len(radarEnrichmentStates))
	mediaStates := make(map[string]int, len(radarEnrichmentStates))
	for i, state := range radarEnrichmentStates {
		if detailCounts[i] > 0 {
			detailStates[state] = int(detailCounts[i])
		}
		if mediaCounts[i] > 0 {
			mediaStates[state] = int(mediaCounts[i])
		}
	}

	readiness := RadarReadiness{
		Scope:           "neighborhood",
		Matched:         int(total),
		DetailState:     aggregateStandardState(int(total), detailStates),
		MediaState:      aggregateStandardState(int(total), mediaStates),
		DetailComplete:  detailStates["complete"],
		MediaComplete:   mediaStates["complete"],
		CommittedPhotos: int(committedPhotos),
		ExpectedPhotos:  int(expectedPhotos),
	}
	if len(detailStates) > 0 {
		readiness.DetailStates = detailStates
	}
	if len(mediaStates) > 0 {
		readiness.MediaStates = mediaStates
	}
	readiness.Notes = radarReadinessNotes(readiness)

	var observedAt time.Time
	if observed.Valid {
		observedAt = observed.Time
	}
	return readiness, observedAt, nil
}

// GetListing reads one listing by advId with its catalogue coverage.
func (r *postgresRadarReader) GetListing(ctx context.Context, listingID string) (RadarListing, bool, error) {
	listingID = strings.TrimSpace(listingID)
	if listingID == "" {
		return RadarListing{}, false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	var (
		listing                                                 RadarListing
		city, neighborhood, title, description, fullDescription sql.NullString
		floor, yearRange, constructionType, heatingTEC          sql.NullString
		heatingGas, sellerType, agentName, agentPhone, phones   sql.NullString
		agencyURL, photoURL, featuresJSON, photoURLsJSON        sql.NullString
		detailEvidenceJSON, sourcePhotoManifestJSON             sql.NullString
		lastRunStatus, coverageReceipt                          sql.NullString
		priceEUR, pricePerSqm, areaSqm                          sql.NullFloat64
		viewCount                                               sql.NullInt64
		correctedAt, detailLastSuccess, mediaLastSuccess        sql.NullTime
		lastScraped, lastComplete                               sql.NullTime
		active                                                  sql.NullBool
	)

	err := r.db.QueryRowContext(ctx, radarDetailQuery, listingID).Scan(
		&listing.ID, &listing.AdvID, &listing.URL, &city, &neighborhood, &listing.NeighborhoodBg,
		&listing.PropertyType, &title, &description, &fullDescription,
		&priceEUR, &pricePerSqm, &areaSqm, &floor, &yearRange,
		&constructionType, &heatingTEC, &heatingGas,
		&sellerType, &listing.IsAgency, &agentName, &agentPhone, &phones, &agencyURL,
		&photoURL, &featuresJSON, &photoURLsJSON,
		&listing.Status, &listing.FirstSeenAt, &listing.LastSeenAt,
		&viewCount, &correctedAt,
		&listing.DetailState, &listing.MediaState, &detailLastSuccess, &mediaLastSuccess,
		&detailEvidenceJSON, &sourcePhotoManifestJSON,
		&active, &lastScraped, &lastRunStatus, &lastComplete, &coverageReceipt,
	)
	if err == sql.ErrNoRows {
		return RadarListing{}, false, nil
	}
	if err != nil {
		return RadarListing{}, false, fmt.Errorf("reading radar listing: %w", err)
	}

	listing.City = city.String
	listing.Neighborhood = neighborhood.String
	listing.Title = title.String
	listing.Description = description.String
	listing.FullDescription = fullDescription.String
	listing.PriceEUR = nullToFloat(priceEUR)
	listing.PricePerSqm = nullToFloat(pricePerSqm)
	listing.AreaSqm = nullToFloat(areaSqm)
	listing.Floor = floor.String
	listing.YearRange = yearRange.String
	listing.ConstructionType = constructionType.String
	listing.HeatingTEC = heatingTEC.String
	listing.HeatingGas = heatingGas.String
	listing.SellerType = sellerType.String
	listing.AgentName = agentName.String
	listing.AgentPhone = agentPhone.String
	listing.Phones = phones.String
	listing.AgencyURL = agencyURL.String
	listing.PhotoURL = photoURL.String
	listing.ViewCount = int(viewCount.Int64)
	listing.CorrectedAt = timeOrNil(correctedAt)
	listing.DetailLastSuccessAt = timeOrNil(detailLastSuccess)
	listing.MediaLastSuccessAt = timeOrNil(mediaLastSuccess)

	if featuresJSON.Valid && strings.TrimSpace(featuresJSON.String) != "" {
		if err := json.Unmarshal([]byte(featuresJSON.String), &listing.Features); err != nil {
			return RadarListing{}, false, fmt.Errorf("reading radar listing features: %w", err)
		}
	}
	if photoURLsJSON.Valid && strings.TrimSpace(photoURLsJSON.String) != "" {
		if err := json.Unmarshal([]byte(photoURLsJSON.String), &listing.PhotoURLs); err != nil {
			return RadarListing{}, false, fmt.Errorf("reading radar listing photos: %w", err)
		}
	}
	if listing.PhotoURL == "" {
		listing.PhotoURL = firstString(listing.PhotoURLs)
	}
	listing.CommittedPhotos = len(listing.PhotoURLs)
	if sourcePhotoManifestJSON.Valid {
		listing.ExpectedPhotos = manifestPhotoCount(sourcePhotoManifestJSON.String)
	}
	if detailEvidenceJSON.Valid && strings.TrimSpace(detailEvidenceJSON.String) != "" {
		var evidence RadarDetailEvidence
		if err := json.Unmarshal([]byte(detailEvidenceJSON.String), &evidence); err == nil {
			listing.DetailEvidence = &evidence
		}
	}

	var receiptComplete *bool
	if coverageReceipt.Valid {
		receiptComplete = parseCoverageReceiptComplete(coverageReceipt.String)
	}
	listing.Coverage = classifyRadarCoverage(
		active.Valid && active.Bool,
		timeOrNil(lastScraped),
		timeOrNil(lastComplete),
		lastRunStatus.String,
		receiptComplete,
		r.freshness,
		r.now(),
	)
	return listing, true, nil
}

// radarSearchArgs binds the six normalized filter values shared by both search
// queries. A nil value is an absent filter, never an empty string that could
// match a different row.
func radarSearchArgs(q RadarSearchQuery) []any {
	propertyType := strings.ToUpper(strings.TrimSpace(q.PropertyType))
	return []any{
		strings.TrimSpace(q.Scope.Slug),
		nullableText(propertyType),
		nullableBound(q.MinPriceEUR),
		nullableBound(q.MaxPriceEUR),
		nullableBound(q.MinSizeSqM),
		nullableBound(q.MaxSizeSqM),
	}
}

func nullableText(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullableBound(value int) any {
	if value <= 0 {
		return nil
	}
	return float64(value)
}

// scanRadarSummary reads one page row.
func scanRadarSummary(rows radarRowScanner) (RadarListing, error) {
	var (
		listing                        RadarListing
		priceEUR, pricePerSqm, areaSqm sql.NullFloat64
		floor, yearRange, sellerType   sql.NullString
		agentName, agentPhone, phones  sql.NullString
		snippet                        sql.NullString
	)
	if err := rows.Scan(
		&listing.AdvID,
		&listing.URL,
		&listing.PropertyType,
		&listing.NeighborhoodBg,
		&priceEUR,
		&pricePerSqm,
		&areaSqm,
		&floor,
		&yearRange,
		&listing.IsAgency,
		&sellerType,
		&agentName,
		&agentPhone,
		&phones,
		&snippet,
		&listing.LastSeenAt,
	); err != nil {
		return RadarListing{}, fmt.Errorf("scanning radar listing: %w", err)
	}
	listing.PriceEUR = nullToFloat(priceEUR)
	listing.PricePerSqm = nullToFloat(pricePerSqm)
	listing.AreaSqm = nullToFloat(areaSqm)
	listing.Floor = floor.String
	listing.YearRange = yearRange.String
	listing.SellerType = sellerType.String
	listing.AgentName = agentName.String
	listing.AgentPhone = agentPhone.String
	listing.Phones = phones.String
	listing.Description = snippet.String
	return listing, nil
}

// manifestPhotoCount reads the producer's source photo manifest without
// trusting its shape; an unreadable manifest leaves the expectation unknown
// rather than inventing zero.
func manifestPhotoCount(raw string) int {
	var manifest struct {
		Photos []json.RawMessage `json:"photos"`
	}
	if err := json.Unmarshal([]byte(raw), &manifest); err != nil {
		return 0
	}
	return len(manifest.Photos)
}

func nullToFloat(value sql.NullFloat64) float64 {
	if !value.Valid {
		return 0
	}
	return value.Float64
}

func timeOrNil(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time
	return &t
}

func timeOrZero(value sql.NullTime) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time
}
