// Package radarclient is the typed HTTP client for Market Radar's published
// geographic read API.
//
// The API is implemented by
// apps/market-radar/src/api/geo-{contracts,queries,routes}.ts and is versioned
// independently of the listing read API: every /v1/geo/* response carries
// contractVersion "radar-geo-1", and a geo search names the frozen listing
// projection it embeds as listingContractVersion "radar-v1-read-1". The client
// refuses any response whose version fields do not match those constants, so a
// proxy HTML page, a JSON error page or a future contract can never be read as
// current data.
//
// Configuration comes from the environment: IMOT_RADAR_API_BASE_URL names the
// API origin and IMOT_RADAR_API_READ_TOKEN carries the read bearer token. The
// token is sent only in the Authorization header; it is never logged, echoed
// in an error or placed in a URL. The package is stdlib-only and only reads
// already-published Radar data; it never contacts imot.bg.
package radarclient

// Contract versions of every response this client accepts.
const (
	// ContractVersion is the geo contract every /v1/geo/* response carries.
	ContractVersion = "radar-geo-1"
	// ListingContractVersion is the frozen listing projection a geo search
	// embeds; a search response that names a different projection is refused.
	ListingContractVersion = "radar-v1-read-1"
)

// Error codes of the geo-versioned error envelope. They mirror
// ERROR_CODES in the Radar API contract exactly; a code outside this set is
// never trusted, and the HTTP status decides the code instead.
const (
	CodeInvalidQuery     = "invalid_query"
	CodeUnauthorized     = "unauthorized"
	CodeNotFound         = "not_found"
	CodeRadarUnavailable = "radar_unavailable"
	CodeQueryTooLarge    = "query_too_large"
)

// ── Request shapes ──────────────────────────────────────────────────────────

// GeoPoint is a named WGS84 point: the anchor of a search circle and the
// display/source points of a listing location share this wire shape.
type GeoPoint struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// GeoIDPredicate is the population's optional id filter. Mode is "include" or
// "exclude" and IDs are listing ids.
type GeoIDPredicate struct {
	Mode string   `json:"mode"`
	IDs  []string `json:"ids"`
}

// GeoPopulation is the property-filter subset a geographic request carries.
// Every field is optional; omitted fields do not narrow the population.
// The wire schema is strict, so no field beyond these may be sent.
type GeoPopulation struct {
	Status        string          `json:"status,omitempty"`
	Neighborhoods []string        `json:"neighborhoods,omitempty"`
	PropertyTypes []string        `json:"propertyTypes,omitempty"`
	AreaMin       *float64        `json:"areaMin,omitempty"`
	AreaMax       *float64        `json:"areaMax,omitempty"`
	FloorMin      *int            `json:"floorMin,omitempty"`
	FloorMax      *int            `json:"floorMax,omitempty"`
	PriceMin      *float64        `json:"priceMin,omitempty"`
	PriceMax      *float64        `json:"priceMax,omitempty"`
	IDPredicate   *GeoIDPredicate `json:"idPredicate,omitempty"`
}

// GeoScope is the optional search circle. Anchor is always carried: an
// explicit null means "no geographic narrowing" (every candidate then stays
// possible rather than silently outside), and a radius never travels without
// an anchor.
type GeoScope struct {
	Anchor       *GeoPoint `json:"anchor"`
	RadiusMeters *int      `json:"radiusMeters,omitempty"`
}

// GeoViewport is a map viewport. It is validated and reported but is never a
// hard exclusion filter.
type GeoViewport struct {
	MinLat float64 `json:"minLat"`
	MaxLat float64 `json:"maxLat"`
	MinLng float64 `json:"minLng"`
	MaxLng float64 `json:"maxLng"`
}

// GeoSearchInput is the body of POST /v1/geo/search.
type GeoSearchInput struct {
	Population         GeoPopulation `json:"population"`
	Geo                GeoScope      `json:"geo"`
	MissingFieldPolicy string        `json:"missingFieldPolicy,omitempty"`
	Groups             []string      `json:"groups,omitempty"`
	Sort               string        `json:"sort,omitempty"`
	SortDir            string        `json:"sortDir,omitempty"`
	Page               int           `json:"page,omitempty"`
	PageSize           int           `json:"pageSize,omitempty"`
	WithTotal          *bool         `json:"withTotal,omitempty"`
}

// GeoMapInput is the body of POST /v1/geo/map.
type GeoMapInput struct {
	Population         GeoPopulation `json:"population"`
	Geo                GeoScope      `json:"geo"`
	MissingFieldPolicy string        `json:"missingFieldPolicy,omitempty"`
	Viewport           *GeoViewport  `json:"viewport,omitempty"`
	Zoom               int           `json:"zoom"`
}

// ── Search response ─────────────────────────────────────────────────────────

// GeoPhotoCover is the cover photo summary carried by a listing summary.
type GeoPhotoCover struct {
	URL    string `json:"url"`
	Width  *int   `json:"width"`
	Height *int   `json:"height"`
}

// GeoListingLocation is the compact location fact set carried alongside every
// geo listing row. Null method/precision/verification means "not processed
// yet"; the full evidence lives in a locations/batch answer.
type GeoListingLocation struct {
	Method       *string   `json:"method"`
	Precision    *string   `json:"precision"`
	Verification *string   `json:"verification"`
	DisplayPoint *GeoPoint `json:"displayPoint"`
	SourcePin    *GeoPoint `json:"sourcePin"`
	SupportKind  *string   `json:"supportKind"`
	Warnings     []string  `json:"warnings"`
}

// GeoListingRow is one frozen listing-summary projection row (the exact
// radar-v1-read-1 field set) plus its compact location facts.
type GeoListingRow struct {
	ID                   string             `json:"id"`
	AdvID                string             `json:"advId"`
	URL                  string             `json:"url"`
	Neighborhood         string             `json:"neighborhood"`
	NeighborhoodBg       string             `json:"neighborhoodBg"`
	SourceNeighborhoodBg *string            `json:"sourceNeighborhoodBg"`
	PropertyType         string             `json:"propertyType"`
	City                 string             `json:"city"`
	Title                *string            `json:"title"`
	PriceEur             *float64           `json:"priceEur"`
	PricePerSqm          *float64           `json:"pricePerSqm"`
	AreaSqm              *float64           `json:"areaSqm"`
	InitialPriceEur      *float64           `json:"initialPriceEur"`
	FinalSeenPriceEur    *float64           `json:"finalSeenPriceEur"`
	PriceChangeCount     int                `json:"priceChangeCount"`
	Floor                *string            `json:"floor"`
	TotalFloors          *int               `json:"totalFloors"`
	ConstructionType     *string            `json:"constructionType"`
	YearRange            *string            `json:"yearRange"`
	HeatingTec           *string            `json:"heatingTec"`
	HeatingGas           *string            `json:"heatingGas"`
	Heating              *string            `json:"heating"`
	Features             []string           `json:"features"`
	AgentName            *string            `json:"agentName"`
	AgentPhone           *string            `json:"agentPhone"`
	Phones               *string            `json:"phones"`
	SellerType           *string            `json:"sellerType"`
	IsAgency             bool               `json:"isAgency"`
	AgencyURL            *string            `json:"agencyUrl"`
	FirstSeenAt          string             `json:"firstSeenAt"`
	LastSeenAt           string             `json:"lastSeenAt"`
	DisappearedAt        *string            `json:"disappearedAt"`
	DaysListed           *int               `json:"daysListed"`
	DetailFetched        bool               `json:"detailFetched"`
	FirstMissingAt       *string            `json:"firstMissingAt"`
	MissCount            int                `json:"missCount"`
	LastPriceChangeAt    *string            `json:"lastPriceChangeAt"`
	Photo                *GeoPhotoCover     `json:"photo"`
	PhotoCount           int                `json:"photoCount"`
	DetailState          string             `json:"detailState"`
	MediaState           string             `json:"mediaState"`
	DetailLastSuccessAt  *string            `json:"detailLastSuccessAt"`
	MediaLastSuccessAt   *string            `json:"mediaLastSuccessAt"`
	Location             GeoListingLocation `json:"location"`
}

// GeoGroupTotals is the full-population count for each result group.
type GeoGroupTotals struct {
	Supported int `json:"supported"`
	Possible  int `json:"possible"`
	Excluded  int `json:"excluded"`
}

// GeoGroups carries the rows of the three result groups. A group that was not
// requested is an empty array, never absent.
type GeoGroups struct {
	Supported []GeoListingRow `json:"supported"`
	Possible  []GeoListingRow `json:"possible"`
	Excluded  []GeoListingRow `json:"excluded"`
}

// GeoLocationStates counts the matching adverts by location-process state.
// Unlocated is not a persisted state: it is the population with no location
// row at all.
type GeoLocationStates struct {
	Pending    int `json:"pending"`
	InProgress int `json:"in_progress"`
	RetryDue   int `json:"retry_due"`
	Complete   int `json:"complete"`
	Blocked    int `json:"blocked"`
	Unlocated  int `json:"unlocated"`
}

// GeoCoverage reports what the answer can and cannot prove. InventoryComplete
// is null when the request named no neighbourhood, true only when every
// requested scope carries an ok run with a valid complete receipt, and false
// otherwise; it is never derived from the result count.
type GeoCoverage struct {
	InventoryComplete    *bool             `json:"inventoryComplete"`
	LocationStates       GeoLocationStates `json:"locationStates"`
	UnresolvedFloorCount int               `json:"unresolvedFloorCount"`
}

// GeoGroupHasMore reports, per requested group, whether a later page exists.
// A group that was not requested is always false.
type GeoGroupHasMore struct {
	Supported bool `json:"supported"`
	Possible  bool `json:"possible"`
	Excluded  bool `json:"excluded"`
}

// GeoSearchResponse is the answer of POST /v1/geo/search.
type GeoSearchResponse struct {
	ContractVersion        string          `json:"contractVersion"`
	ObservedAt             string          `json:"observedAt"`
	Totals                 GeoGroupTotals  `json:"totals"`
	Groups                 GeoGroups       `json:"groups"`
	Coverage               GeoCoverage     `json:"coverage"`
	ListingContractVersion string          `json:"listingContractVersion"`
	Page                   int             `json:"page"`
	PageSize               int             `json:"pageSize"`
	HasMore                GeoGroupHasMore `json:"hasMore"`
}

// ── Location evidence ───────────────────────────────────────────────────────

// GeoJSONCoordinates carries a geometry's positions unchanged. It is a
// decoded JSON array rather than json.RawMessage so a consumer that reflects
// over these types (the MCP server derives an output schema from them) sees an
// array of positions instead of an array of bytes.
type GeoJSONCoordinates []any

// GeoJSONGeometry is a support geometry: GeoJSON WGS84 with
// [longitude, latitude] positions.
type GeoJSONGeometry struct {
	Type        string             `json:"type"`
	Coordinates GeoJSONCoordinates `json:"coordinates"`
}

// GeoEvidence is one quoted property-text clue. SourceField is a bounded
// source-field code, Quote is the exact property text, and Ambiguous marks a
// sentence whose ownership is not decidable.
type GeoEvidence struct {
	SourceField string   `json:"sourceField"`
	Quote       string   `json:"quote"`
	Relation    string   `json:"relation"`
	FeatureIDs  []string `json:"featureIds"`
	Ambiguous   *bool    `json:"ambiguous,omitempty"`
}

// GeoAlternative is a rejected but recorded alternative placement.
type GeoAlternative struct {
	Precision    string           `json:"precision"`
	DisplayPoint *GeoPoint        `json:"displayPoint"`
	Support      *GeoJSONGeometry `json:"support"`
	SupportKind  string           `json:"supportKind"`
	Evidence     []GeoEvidence    `json:"evidence"`
}

// GeoLocationDetail is one listing's full location evidence.
type GeoLocationDetail struct {
	ListingID           string           `json:"listingId"`
	AdvID               string           `json:"advId"`
	Method              *string          `json:"method"`
	Precision           *string          `json:"precision"`
	Verification        *string          `json:"verification"`
	DisplayPoint        *GeoPoint        `json:"displayPoint"`
	SourcePin           *GeoPoint        `json:"sourcePin"`
	SourcePinObservedAt *string          `json:"sourcePinObservedAt"`
	Support             *GeoJSONGeometry `json:"support"`
	SupportKind         *string          `json:"supportKind"`
	Evidence            []GeoEvidence    `json:"evidence"`
	Alternatives        []GeoAlternative `json:"alternatives"`
	Warnings            []string         `json:"warnings"`
	Outcome             *string          `json:"outcome"`
	State               *string          `json:"state"`
	ProcessedAt         *string          `json:"processedAt"`
}

// GeoLocationsBatchResponse is the answer of POST /v1/geo/locations/batch.
// MissingIDs lists requested listings the Radar store has no location row for;
// an empty locations array plus a missing id is never a located property.
type GeoLocationsBatchResponse struct {
	ContractVersion string              `json:"contractVersion"`
	ObservedAt      string              `json:"observedAt"`
	Locations       []GeoLocationDetail `json:"locations"`
	MissingIDs      []string            `json:"missingIds"`
}

// ── Map response ────────────────────────────────────────────────────────────

// GeoMapFeature is one map feature: a point or a cluster. The wire type is a
// discriminated union on Kind; the unused side's fields stay empty.
type GeoMapFeature struct {
	Kind         string    `json:"kind"`
	ListingID    *string   `json:"listingId,omitempty"`
	AdvID        *string   `json:"advId,omitempty"`
	DisplayPoint *GeoPoint `json:"displayPoint,omitempty"`
	Method       *string   `json:"method,omitempty"`
	Precision    *string   `json:"precision,omitempty"`
	Verification *string   `json:"verification,omitempty"`
	Center       *GeoPoint `json:"center,omitempty"`
	Count        *int      `json:"count,omitempty"`
	NativeCount  *int      `json:"nativeCount,omitempty"`
	DerivedCount *int      `json:"derivedCount,omitempty"`
	CoLocateKey  *string   `json:"coLocateKey,omitempty"`
}

// GeoMapResponse is the answer of POST /v1/geo/map. Truncated is true when the
// individual-point cap forced the coarsest aggregation; it is never a silent
// limit.
type GeoMapResponse struct {
	ContractVersion      string          `json:"contractVersion"`
	ObservedAt           string          `json:"observedAt"`
	Totals               GeoGroupTotals  `json:"totals"`
	Features             []GeoMapFeature `json:"features"`
	CoarseCount          int             `json:"coarseCount"`
	UnlocatedCount       int             `json:"unlocatedCount"`
	UnresolvedFloorCount int             `json:"unresolvedFloorCount"`
	Truncated            bool            `json:"truncated"`
}

// envelopeMeta is implemented by every geo response type. The transport reads
// the version fields through it before any data is returned.
type envelopeMeta interface {
	envelopeContractVersion() string
	envelopeListingContractVersion() string
}

func (r GeoSearchResponse) envelopeContractVersion() string { return r.ContractVersion }

func (r GeoSearchResponse) envelopeListingContractVersion() string {
	return r.ListingContractVersion
}

func (r GeoLocationsBatchResponse) envelopeContractVersion() string { return r.ContractVersion }

func (r GeoLocationsBatchResponse) envelopeListingContractVersion() string { return "" }

func (r GeoMapResponse) envelopeContractVersion() string { return r.ContractVersion }

func (r GeoMapResponse) envelopeListingContractVersion() string { return "" }
