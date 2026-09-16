package radarclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Environment names a direct caller (the CLI) reads when it has no explicit
// configuration. The MCP server maps its own environment onto Config instead.
const (
	EnvBaseURL   = "IMOT_RADAR_API_BASE_URL"
	EnvReadToken = "IMOT_RADAR_API_READ_TOKEN"
)

// DefaultTimeout bounds one geo request when Config.Timeout is not set. The
// API answers a bounded page from one read-only snapshot, so five seconds is
// already generous; a longer wait is an outage, not a slow query.
const DefaultTimeout = 5 * time.Second

// Limits mirrored from the geo contract. They are validated before a request
// leaves the process, so an obviously invalid filter is a caller error rather
// than a round trip.
const (
	MaxPageSize     = 200
	DefaultPageSize = 50
	MinRadiusMeters = 1
	MaxRadiusMeters = 50_000
	MaxBatchIDs     = 500
	MaxIDArray      = 50_000
	MinZoom         = 1
	MaxZoom         = 22
)

// Paths of the geographic read operations, relative to the configured origin.
const (
	pathGeoSearch         = "/v1/geo/search"
	pathGeoMap            = "/v1/geo/map"
	pathGeoLocationsBatch = "/v1/geo/locations/batch"
)

// maxResponseBytes bounds one response body. A search page carries at most 200
// rows and a map answer at most a few hundred features, so a larger body is a
// misconfigured endpoint (an HTML page, a proxy error document), not data.
const maxResponseBytes = 8 << 20

// sortKeys and sortDirections mirror the wire vocabulary.
var (
	sortKeys       = []string{"lastSeenAt", "firstSeenAt", "priceEur", "pricePerSqm", "areaSqm", "daysListed", "floor"}
	sortDirections = []string{"asc", "desc"}
	statuses       = []string{"active", "disappeared"}
	idModes        = []string{"include", "exclude"}
)

// Config configures a Client. BaseURL and Token are required; Token is a
// bearer secret and is never logged or echoed.
type Config struct {
	BaseURL string
	Token   string
	// Timeout bounds one request. Zero uses DefaultTimeout.
	Timeout time.Duration
	// HTTPClient overrides the transport; tests use it, production does not.
	HTTPClient *http.Client
}

// Client talks to the published Radar geographic API. A Client is safe for
// concurrent use; it never mutates state after construction.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New builds a client from explicit configuration. The base URL must be an
// http(s) origin without a query, fragment or embedded credentials.
func New(cfg Config) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if err := ValidateBaseURL(base); err != nil {
		return nil, err
	}
	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		return nil, fmt.Errorf("a radar read token is required")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	return &Client{baseURL: base, token: token, http: httpClient}, nil
}

// FromEnv builds a client from IMOT_RADAR_API_BASE_URL and
// IMOT_RADAR_API_READ_TOKEN. A missing variable is reported by name; its value
// never is.
func FromEnv() (*Client, error) {
	base := os.Getenv(EnvBaseURL)
	token := os.Getenv(EnvReadToken)
	if strings.TrimSpace(base) == "" {
		return nil, fmt.Errorf("%s is not set; this command reads published Radar data and never scrapes imot.bg", EnvBaseURL)
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("%s is not set; the radar read token is required", EnvReadToken)
	}
	return New(Config{BaseURL: base, Token: token})
}

// ValidateBaseURL checks an API origin. http is allowed so a local deployment
// can be inspected without TLS termination; production uses https.
func ValidateBaseURL(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fmt.Errorf("the radar API base URL is empty")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("the radar API base URL is not a valid URL")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return fmt.Errorf("the radar API base URL must use http or https")
	}
	if parsed.Host == "" {
		return fmt.Errorf("the radar API base URL has no host")
	}
	if parsed.User != nil {
		return fmt.Errorf("the radar API base URL must not carry credentials; the read token travels in the Authorization header")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("the radar API base URL must not carry a query or fragment")
	}
	return nil
}

// APIError is a typed Radar failure. Code is one of the radar-geo-1 error
// codes, Status is the HTTP status (0 when the request never reached the API),
// and Message is the API's own bounded message when it sent one. A bearer
// token can never appear in any field.
type APIError struct {
	Code    string
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("radar error %s (status %d)", e.Code, e.Status)
	}
	return fmt.Sprintf("radar error %s (status %d): %s", e.Code, e.Status, e.Message)
}

// GeoSearch runs POST /v1/geo/search.
func (c *Client) GeoSearch(ctx context.Context, input GeoSearchInput) (GeoSearchResponse, error) {
	normalized, err := normalizeSearchInput(input)
	if err != nil {
		return GeoSearchResponse{}, err
	}
	var out GeoSearchResponse
	if err := c.call(ctx, pathGeoSearch, normalized, &out); err != nil {
		return GeoSearchResponse{}, err
	}
	return out, nil
}

// GeoLocationsBatch runs POST /v1/geo/locations/batch.
func (c *Client) GeoLocationsBatch(ctx context.Context, listingIDs []string) (GeoLocationsBatchResponse, error) {
	ids, err := normalizeListingIDs(listingIDs)
	if err != nil {
		return GeoLocationsBatchResponse{}, err
	}
	var out GeoLocationsBatchResponse
	if err := c.call(ctx, pathGeoLocationsBatch, geoLocationsBatchRequest{ListingIDs: ids}, &out); err != nil {
		return GeoLocationsBatchResponse{}, err
	}
	return out, nil
}

// GeoLocation fetches one listing's full location evidence. A listing absent
// from the answer is a typed not_found error, never an empty location: an
// advert the Radar store does not know cannot be reported as unlocated.
func (c *Client) GeoLocation(ctx context.Context, listingID string) (GeoLocationDetail, error) {
	id := strings.TrimSpace(listingID)
	if id == "" {
		return GeoLocationDetail{}, fmt.Errorf("a radar location lookup needs a non-empty listing id")
	}
	response, err := c.GeoLocationsBatch(ctx, []string{id})
	if err != nil {
		return GeoLocationDetail{}, err
	}
	for _, location := range response.Locations {
		if location.ListingID == id {
			return location, nil
		}
	}
	return GeoLocationDetail{}, &APIError{
		Code:    CodeNotFound,
		Status:  http.StatusNotFound,
		Message: "the Radar store has no location row for this listing id",
	}
}

// GeoMap runs POST /v1/geo/map.
func (c *Client) GeoMap(ctx context.Context, input GeoMapInput) (GeoMapResponse, error) {
	normalized, err := normalizeMapInput(input)
	if err != nil {
		return GeoMapResponse{}, err
	}
	var out GeoMapResponse
	if err := c.call(ctx, pathGeoMap, normalized, &out); err != nil {
		return GeoMapResponse{}, err
	}
	return out, nil
}

// geoLocationsBatchRequest is the batch operation's body.
type geoLocationsBatchRequest struct {
	ListingIDs []string `json:"listingIds"`
}

// call performs one POST and validates the envelope before returning data.
func (c *Client) call(ctx context.Context, path string, payload any, out envelopeMeta) error {
	body, status, err := c.post(ctx, path, payload)
	if err != nil {
		return err
	}
	if err := decodeStrict(body, out); err != nil {
		return &APIError{
			Code:    CodeRadarUnavailable,
			Status:  status,
			Message: "the radar response is not a " + ContractVersion + " JSON document",
		}
	}
	if got := out.envelopeContractVersion(); got != ContractVersion {
		return &APIError{
			Code:    CodeRadarUnavailable,
			Status:  status,
			Message: fmt.Sprintf("unexpected contract version %q (want %q)", oneLine(got), ContractVersion),
		}
	}
	if got := out.envelopeListingContractVersion(); got != "" && got != ListingContractVersion {
		return &APIError{
			Code:    CodeRadarUnavailable,
			Status:  status,
			Message: fmt.Sprintf("unexpected listing contract version %q (want %q)", oneLine(got), ListingContractVersion),
		}
	}
	return nil
}

// post sends one JSON request. The token travels only in the Authorization
// header; the URL and every error message built here are token-free.
func (c *Client) post(ctx context.Context, path string, payload any) ([]byte, int, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, fmt.Errorf("encoding the radar request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return nil, 0, fmt.Errorf("building the radar request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.token)

	response, err := c.http.Do(request)
	if err != nil {
		return nil, 0, &APIError{
			Code:    CodeRadarUnavailable,
			Status:  0,
			Message: "the radar API could not be reached: " + oneLine(err.Error()),
		}
	}
	defer response.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if readErr != nil {
		return nil, response.StatusCode, &APIError{
			Code:    codeFromStatus(response.StatusCode),
			Status:  response.StatusCode,
			Message: "the radar response could not be read",
		}
	}
	if len(body) > maxResponseBytes {
		return nil, response.StatusCode, &APIError{
			Code:    CodeRadarUnavailable,
			Status:  response.StatusCode,
			Message: "the radar response exceeded the accepted size",
		}
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, response.StatusCode, errorFromResponse(response.StatusCode, body)
	}
	return body, response.StatusCode, nil
}

// decodeStrict decodes one geo response body. The wire schemas are strict, so
// an unknown field means the document is not the contract this client speaks;
// refusing it is what keeps a JSON error document from masquerading as data.
func decodeStrict(body []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
}

// errorFromResponse maps a non-2xx answer. A valid geo error envelope is
// trusted only under the exact contract version; anything else (an HTML proxy
// page, a bare JSON error document) is classified by its HTTP status and its
// body is never echoed.
func errorFromResponse(status int, body []byte) error {
	var envelope struct {
		ContractVersion string `json:"contractVersion"`
		Error           *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil &&
		envelope.Error != nil && envelope.ContractVersion == ContractVersion && validErrorCode(envelope.Error.Code) {
		return &APIError{Code: envelope.Error.Code, Status: status, Message: oneLine(envelope.Error.Message)}
	}
	return &APIError{
		Code:    codeFromStatus(status),
		Status:  status,
		Message: fmt.Sprintf("the radar API answered status %d without a %s error envelope", status, ContractVersion),
	}
}

// codeFromStatus classifies a failure whose body is not a geo error envelope.
// The mapping follows the API's own ApiError statuses.
func codeFromStatus(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return CodeUnauthorized
	case http.StatusNotFound:
		return CodeNotFound
	case http.StatusRequestEntityTooLarge:
		return CodeQueryTooLarge
	case http.StatusBadRequest:
		return CodeInvalidQuery
	default:
		return CodeRadarUnavailable
	}
}

func validErrorCode(code string) bool {
	switch code {
	case CodeInvalidQuery, CodeUnauthorized, CodeNotFound, CodeRadarUnavailable, CodeQueryTooLarge:
		return true
	default:
		return false
	}
}

// ── Input normalization ─────────────────────────────────────────────────────

func normalizeSearchInput(input GeoSearchInput) (GeoSearchInput, error) {
	out := input
	if err := normalizePopulation(&out.Population); err != nil {
		return GeoSearchInput{}, err
	}
	if err := normalizeScope(&out.Geo); err != nil {
		return GeoSearchInput{}, err
	}
	policy, err := NormalizeMissingFieldPolicy(out.MissingFieldPolicy)
	if err != nil {
		return GeoSearchInput{}, err
	}
	out.MissingFieldPolicy = policy
	groups, err := NormalizeGroups(out.Groups)
	if err != nil {
		return GeoSearchInput{}, err
	}
	out.Groups = groups
	if out.Sort != "" && !contains(sortKeys, out.Sort) {
		return GeoSearchInput{}, fmt.Errorf("unsupported radar sort key %q", oneLine(out.Sort))
	}
	if out.SortDir != "" && !contains(sortDirections, out.SortDir) {
		return GeoSearchInput{}, fmt.Errorf("unsupported radar sort direction %q; use asc or desc", oneLine(out.SortDir))
	}
	if out.Page == 0 {
		out.Page = 1
	}
	if out.Page < 1 {
		return GeoSearchInput{}, fmt.Errorf("the radar page number must be at least 1")
	}
	if out.PageSize == 0 {
		out.PageSize = DefaultPageSize
	}
	if out.PageSize < 1 || out.PageSize > MaxPageSize {
		return GeoSearchInput{}, fmt.Errorf("the radar pageSize must be between 1 and %d", MaxPageSize)
	}
	return out, nil
}

func normalizeMapInput(input GeoMapInput) (GeoMapInput, error) {
	out := input
	if err := normalizePopulation(&out.Population); err != nil {
		return GeoMapInput{}, err
	}
	if err := normalizeScope(&out.Geo); err != nil {
		return GeoMapInput{}, err
	}
	policy, err := NormalizeMissingFieldPolicy(out.MissingFieldPolicy)
	if err != nil {
		return GeoMapInput{}, err
	}
	out.MissingFieldPolicy = policy
	if out.Viewport != nil {
		viewport := *out.Viewport
		if viewport.MinLat > viewport.MaxLat || viewport.MinLng > viewport.MaxLng {
			return GeoMapInput{}, fmt.Errorf("the map viewport bounds must be ordered min <= max")
		}
		if err := validateCoordinates(viewport.MinLat, viewport.MinLng); err != nil {
			return GeoMapInput{}, err
		}
		if err := validateCoordinates(viewport.MaxLat, viewport.MaxLng); err != nil {
			return GeoMapInput{}, err
		}
	}
	if out.Zoom < MinZoom || out.Zoom > MaxZoom {
		return GeoMapInput{}, fmt.Errorf("the map zoom must be between %d and %d", MinZoom, MaxZoom)
	}
	return out, nil
}

func normalizePopulation(population *GeoPopulation) error {
	if population.Status != "" && !contains(statuses, population.Status) {
		return fmt.Errorf("unsupported listing status %q; use active or disappeared", oneLine(population.Status))
	}
	neighborhoods, err := NormalizeNeighborhoods(population.Neighborhoods)
	if err != nil {
		return err
	}
	population.Neighborhoods = neighborhoods

	propertyTypes := make([]string, 0, len(population.PropertyTypes))
	seenTypes := make(map[string]bool, len(population.PropertyTypes))
	for _, raw := range population.PropertyTypes {
		value := strings.TrimSpace(raw)
		if value == "" {
			return fmt.Errorf("a radar property type must not be empty")
		}
		values := []string{value}
		if canonical, ok := CanonicalPropertyTypes(value); ok {
			values = canonical
		}
		for _, item := range values {
			if len(item) > MaxIdentifierLength {
				return fmt.Errorf("a radar property type must be at most %d characters", MaxIdentifierLength)
			}
			if seenTypes[item] {
				continue
			}
			seenTypes[item] = true
			propertyTypes = append(propertyTypes, item)
		}
	}
	population.PropertyTypes = propertyTypes

	if err := checkBound(population.AreaMin, "area minimum"); err != nil {
		return err
	}
	if err := checkBound(population.AreaMax, "area maximum"); err != nil {
		return err
	}
	if err := checkBound(population.PriceMin, "price minimum"); err != nil {
		return err
	}
	if err := checkBound(population.PriceMax, "price maximum"); err != nil {
		return err
	}
	if population.AreaMin != nil && population.AreaMax != nil && *population.AreaMin > *population.AreaMax {
		return fmt.Errorf("the radar area minimum must not exceed the area maximum")
	}
	if population.PriceMin != nil && population.PriceMax != nil && *population.PriceMin > *population.PriceMax {
		return fmt.Errorf("the radar price minimum must not exceed the price maximum")
	}
	if population.FloorMin != nil && population.FloorMax != nil && *population.FloorMin > *population.FloorMax {
		return fmt.Errorf("the radar floor minimum must not exceed the floor maximum")
	}
	if population.IDPredicate != nil {
		mode := strings.TrimSpace(population.IDPredicate.Mode)
		if !contains(idModes, mode) {
			return fmt.Errorf("unsupported id predicate mode %q; use include or exclude", oneLine(mode))
		}
		ids, err := normalizeIdentifiers(population.IDPredicate.IDs, "predicate id")
		if err != nil {
			return err
		}
		if len(ids) > MaxIDArray {
			return fmt.Errorf("the radar id predicate accepts at most %d ids", MaxIDArray)
		}
		population.IDPredicate = &GeoIDPredicate{Mode: mode, IDs: ids}
	}
	return nil
}

func normalizeScope(scope *GeoScope) error {
	if scope.Anchor == nil {
		if scope.RadiusMeters != nil {
			return fmt.Errorf("a radar radius needs an anchor: set anchor and radiusMeters together")
		}
		return nil
	}
	if err := validateCoordinates(scope.Anchor.Lat, scope.Anchor.Lng); err != nil {
		return err
	}
	if scope.RadiusMeters == nil {
		return fmt.Errorf("a radar anchor needs a radius: set radiusMeters together with the anchor")
	}
	if *scope.RadiusMeters < MinRadiusMeters || *scope.RadiusMeters > MaxRadiusMeters {
		return fmt.Errorf("the radar radius must be between %d and %d metres", MinRadiusMeters, MaxRadiusMeters)
	}
	return nil
}

func normalizeListingIDs(listingIDs []string) ([]string, error) {
	ids, err := normalizeIdentifiers(listingIDs, "listing id")
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("at least one listing id is required")
	}
	if len(ids) > MaxBatchIDs {
		return nil, fmt.Errorf("a radar location batch accepts at most %d listing ids", MaxBatchIDs)
	}
	return ids, nil
}

func normalizeIdentifiers(values []string, label string) ([]string, error) {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			return nil, fmt.Errorf("a radar %s must not be empty", label)
		}
		if len(value) > MaxIdentifierLength {
			return nil, fmt.Errorf("a radar %s must be at most %d characters", label, MaxIdentifierLength)
		}
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out, nil
}

func validateCoordinates(lat, lng float64) error {
	if lat < -90 || lat > 90 {
		return fmt.Errorf("a WGS84 latitude must be between -90 and 90")
	}
	if lng < -180 || lng > 180 {
		return fmt.Errorf("a WGS84 longitude must be between -180 and 180")
	}
	return nil
}

func checkBound(value *float64, label string) error {
	if value == nil {
		return nil
	}
	if *value < 0 {
		return fmt.Errorf("the radar %s must not be negative", label)
	}
	return nil
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
