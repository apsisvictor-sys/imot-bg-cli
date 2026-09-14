package mcpserver

// Market Radar collection-request boundary (Phase U2 of
// docs/plans/market-radar-mcp-unified-serving-plan-2026-09-13.md).
//
// This is the MCP's constrained write surface: two fixed database functions
// (radar_enqueue_collection_request, radar_collection_request_status) owned by
// the Radar schema. The connected role carries no table privileges, so the only
// operations it can perform are these validated calls. No SQL, URL, scope text
// or advert id is ever interpolated: values travel as bind parameters and the
// function itself re-validates kind, readiness, slug and advert shape.
//
// The pool is separate from the read-only RadarReader pool on purpose: reads
// stay read-only at the session level, while enqueue reaches a SECURITY
// DEFINER function that performs the only permitted INSERT. With
// IMOT_MCP_RADAR_ENQUEUE_DSN unset the requester is nil and every serving
// path stays exactly as it is today.

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Request kinds and readiness values the enqueue function accepts. They mirror
// the database-side whitelist; sending anything else is a caller error, not a
// database round-trip.
const (
	RequestKindScopeInventory = "scope_inventory"
	RequestKindListingDetail  = "listing_detail"
	RequestKindListingMedia   = "listing_media"

	RequestReadinessInventory = "inventory"
	RequestReadinessDetail    = "detail"
	RequestReadinessMedia     = "media"
)

// Request states the status function can report. They are the plan's bounded
// vocabulary: none | pending | running | retry_due | complete | blocked |
// failed | deferred.
const (
	RequestStateNone     = "none"
	RequestStatePending  = "pending"
	RequestStateRunning  = "running"
	RequestStateRetryDue = "retry_due"
	RequestStateComplete = "complete"
	RequestStateBlocked  = "blocked"
	RequestStateFailed   = "failed"
	RequestStateDeferred = "deferred"
)

// RadarEnqueueInput is one validated acquisition wish. Scope requests use the
// catalogue slug; advert requests use the imot advert id and leave the slug
// empty only when the kind is listing-scoped.
type RadarEnqueueInput struct {
	Kind             string
	NeighborhoodSlug string
	AdvID            string
	Readiness        string
	RequestsRefresh  bool
}

// RadarEnqueueResult reports which durable request now covers the wish and
// whether this call created it. Deduplication means a second caller receives
// created=false and the same RequestID.
type RadarEnqueueResult struct {
	RequestID         string
	State             string
	Created           bool
	NextAttemptAt     time.Time
	RetryAfterSeconds int
}

// RadarRequestStatus is the raw, unclassified fact set for one request. The
// MCP's Go layer owns any coverage classification; the database returns data,
// never prose.
type RadarRequestStatus struct {
	RequestID           string
	State               string
	Kind                string
	NeighborhoodSlug    string
	AdvID               string
	RequestedReadiness  string
	AttemptCount        int
	FailureCount        int
	LastFailureKind     string
	NextAttemptAt       time.Time
	LeaseExpiresAt      time.Time
	ExpiresAt           time.Time
	CompletedAt         time.Time
	ObservedAt          time.Time
	ResultRevision      int64
	ListingDetailState  string
	ListingMediaState   string
	DetailLastSuccessAt time.Time
	MediaLastSuccessAt  time.Time
	CommittedPhotoCount int
}

// RadarRequester is the MCP-side contract for the constrained enqueue/status
// boundary. Implementations must never send SQL text or accept caller-supplied
// scope text beyond the validated fields.
type RadarRequester interface {
	// EnqueueCollectionRequest creates or reuses the one durable request for a
	// normalized scope or advert key.
	EnqueueCollectionRequest(ctx context.Context, in RadarEnqueueInput) (RadarEnqueueResult, error)
	// CollectionRequestStatus reports raw facts for up to a bounded number of
	// request ids. Unknown ids are simply absent from the result.
	CollectionRequestStatus(ctx context.Context, requestIDs []string) ([]RadarRequestStatus, error)
	// Close releases the pool.
	Close() error
}

// maxStatusRequestIDs bounds one status call. It mirrors the database-side
// array cap so a caller error stays a caller error.
const maxStatusRequestIDs = 20

// OpenRadarRequester builds the dedicated enqueue/status pool. Like the reader,
// it never connects eagerly: an enqueue-path outage must not stop the MCP from
// serving the labelled fallback paths.
func OpenRadarRequester(dsn string, timeout time.Duration) (RadarRequester, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return nil, fmt.Errorf("radar enqueue DSN is empty")
	}
	connConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		// pgx echoes the connection string in its parse error, which would put
		// the database password into the logs. Fail without the DSN text.
		return nil, fmt.Errorf("parsing IMOT_MCP_RADAR_ENQUEUE_DSN: invalid Postgres connection string")
	}
	if connConfig.RuntimeParams == nil {
		connConfig.RuntimeParams = map[string]string{}
	}
	if timeout > 0 {
		connConfig.RuntimeParams["statement_timeout"] = strconv.FormatInt(timeout.Milliseconds(), 10)
	}

	db := stdlib.OpenDB(*connConfig)
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxIdleTime(5 * time.Minute)
	db.SetConnMaxLifetime(30 * time.Minute)

	return &postgresRadarRequester{db: db}, nil
}

type postgresRadarRequester struct {
	db *sql.DB
}

// Close releases the enqueue pool.
func (r *postgresRadarRequester) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}

// EnqueueCollectionRequest calls the fixed enqueue function. Validation on this
// side only prevents obvious mistakes from reaching the database; the function
// remains the authority.
func (r *postgresRadarRequester) EnqueueCollectionRequest(ctx context.Context, in RadarEnqueueInput) (RadarEnqueueResult, error) {
	kind := strings.TrimSpace(in.Kind)
	readiness := strings.TrimSpace(in.Readiness)
	if !validRequestKind(kind) {
		return RadarEnqueueResult{}, fmt.Errorf("unsupported collection request kind %q", kind)
	}
	if !validRequestReadiness(readiness) {
		return RadarEnqueueResult{}, fmt.Errorf("unsupported requested readiness %q", readiness)
	}
	if kind == RequestKindScopeInventory && strings.TrimSpace(in.NeighborhoodSlug) == "" {
		return RadarEnqueueResult{}, fmt.Errorf("a scope request requires a neighbourhood slug")
	}
	if kind != RequestKindScopeInventory && strings.TrimSpace(in.AdvID) == "" {
		return RadarEnqueueResult{}, fmt.Errorf("a listing request requires an advert id")
	}

	ctx, cancel := context.WithTimeout(ctx, enqueueCallTimeout)
	defer cancel()

	var (
		requestID, state string
		created          bool
		nextAttempt      sql.NullTime
		retryAfter       sql.NullInt64
	)
	err := r.db.QueryRowContext(ctx,
		`SELECT request_id, state, created, next_attempt_at, retry_after_seconds
		   FROM radar_enqueue_collection_request($1::text, $2::text, $3::text, $4::text, $5::boolean)`,
		kind, strings.TrimSpace(in.NeighborhoodSlug), nullableRequestText(in.AdvID), readiness, in.RequestsRefresh,
	).Scan(&requestID, &state, &created, &nextAttempt, &retryAfter)
	if err != nil {
		return RadarEnqueueResult{}, fmt.Errorf("enqueueing radar collection request: %w", err)
	}

	result := RadarEnqueueResult{
		RequestID: requestID,
		State:     state,
		Created:   created,
	}
	if nextAttempt.Valid {
		result.NextAttemptAt = nextAttempt.Time
	}
	if retryAfter.Valid {
		result.RetryAfterSeconds = int(retryAfter.Int64)
	}
	return result, nil
}

// CollectionRequestStatus calls the fixed status function with a bounded id set.
func (r *postgresRadarRequester) CollectionRequestStatus(ctx context.Context, requestIDs []string) ([]RadarRequestStatus, error) {
	ids := make([]string, 0, len(requestIDs))
	for _, id := range requestIDs {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			ids = append(ids, trimmed)
		}
		if len(ids) == maxStatusRequestIDs {
			break
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(ctx, enqueueCallTimeout)
	defer cancel()

	rows, err := r.db.QueryContext(ctx,
		`SELECT request_id, state, kind, neighborhood_slug, adv_id, requested_readiness,
		        attempt_count, failure_count, last_failure_kind, next_attempt_at,
		        lease_expires_at, expires_at, completed_at, observed_at,
		        result_revision, detail_state, media_state,
		        detail_last_success_at, media_last_success_at, committed_photo_count
		   FROM radar_collection_request_status($1::text[])`,
		pqTextArray(ids),
	)
	if err != nil {
		return nil, fmt.Errorf("reading radar collection request status: %w", err)
	}
	defer rows.Close()

	out := make([]RadarRequestStatus, 0, len(ids))
	for rows.Next() {
		var (
			status                                              RadarRequestStatus
			neighborhoodSlug, advID, readiness, lastFailureKind sql.NullString
			detailState, mediaState                             sql.NullString
			nextAttempt, leaseExpires, expires, completed       sql.NullTime
			observed, detailSuccess, mediaSuccess               sql.NullTime
			resultRevision                                      sql.NullInt64
			photoCount                                          sql.NullInt64
		)
		if err := rows.Scan(
			&status.RequestID, &status.State, &status.Kind,
			&neighborhoodSlug, &advID, &readiness,
			&status.AttemptCount, &status.FailureCount, &lastFailureKind,
			&nextAttempt, &leaseExpires, &expires, &completed, &observed,
			&resultRevision, &detailState, &mediaState,
			&detailSuccess, &mediaSuccess, &photoCount,
		); err != nil {
			return nil, fmt.Errorf("scanning radar request status: %w", err)
		}
		status.NeighborhoodSlug = neighborhoodSlug.String
		status.AdvID = advID.String
		status.RequestedReadiness = readiness.String
		status.LastFailureKind = lastFailureKind.String
		status.ListingDetailState = detailState.String
		status.ListingMediaState = mediaState.String
		status.NextAttemptAt = nullTimeValue(nextAttempt)
		status.LeaseExpiresAt = nullTimeValue(leaseExpires)
		status.ExpiresAt = nullTimeValue(expires)
		status.CompletedAt = nullTimeValue(completed)
		status.ObservedAt = nullTimeValue(observed)
		status.DetailLastSuccessAt = nullTimeValue(detailSuccess)
		status.MediaLastSuccessAt = nullTimeValue(mediaSuccess)
		status.ResultRevision = resultRevision.Int64
		status.CommittedPhotoCount = int(photoCount.Int64)
		out = append(out, status)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading radar request status rows: %w", err)
	}
	return out, nil
}

// enqueueCallTimeout bounds one function call when the caller's context does
// not carry its own deadline. It matches the configured statement timeout
// default so a hung enqueue cannot pin a pool slot.
const enqueueCallTimeout = 10 * time.Second

func validRequestKind(kind string) bool {
	switch kind {
	case RequestKindScopeInventory, RequestKindListingDetail, RequestKindListingMedia:
		return true
	}
	return false
}

func validRequestReadiness(readiness string) bool {
	switch readiness {
	case RequestReadinessInventory, RequestReadinessDetail, RequestReadinessMedia:
		return true
	}
	return false
}

func nullableRequestText(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}

func nullTimeValue(value sql.NullTime) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time
}

// pqTextArray renders a Postgres text[] literal. The ids themselves were
// trimmed and length-bounded above; the encoding only escapes quotes so the
// array travels safely as one parameter value.
func pqTextArray(values []string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, v := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(strings.ReplaceAll(strings.ReplaceAll(v, "\\", "\\\\"), "\"", "\\\""))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}
