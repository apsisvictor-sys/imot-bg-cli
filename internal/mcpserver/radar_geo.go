package mcpserver

// Published Radar geographic tools (plan §8.2).
//
// radar_geo_search and radar_location are thin, read-only wrappers over the
// versioned geographic read API (radar-geo-1) implemented in
// broker-essentials/apps/market-radar. Transport lives in
// internal/radarclient, so this package owns no SQL and no second HTTP client.
//
// The two tools are registered unconditionally. With the API unconfigured they
// answer with a clear unavailable-capability error naming both environment
// variables; they never fall back to the live imot.bg source. The existing
// non-geographic tools are untouched.

import (
	"context"
	"fmt"
	"strings"

	"github.com/apsisvictor/imot-cli/internal/radarclient"
)

// Tool names, kept as constants so logs, tests and docs cannot drift.
const (
	ToolRadarGeoSearch = "radar_geo_search"
	ToolRadarLocation  = "radar_location"
)

// Tool descriptions explain the two things a model gets wrong without help:
// what a result group means, and that a source-asserted pin and a derived
// point are different facts rather than two spellings of "the location".
const (
	toolRadarGeoSearchDescription = "Search the shared Market Radar store by geography and property filters, grouped by location evidence. Use this when asked which properties are near a place, or to separate listings whose position the evidence backs (supported) from listings where the location is only possible (possible) or explicitly excluded. A possible row is not proven, not disproven: an advert with no resolved location stays possible rather than disappearing. Each row's location.method and location.precision say how the point was produced: source_pin is the source's own pin and source_asserted; geocoded and llm_assisted are derived positions; locality_only is a neighbourhood approximation, never a building. Read coverage.inventoryComplete, locationStates and unresolvedFloorCount before calling a result complete or empty. This returns one page; hasMore tells you whether more rows exist."
	toolRadarLocationDescription  = "Fetch one listing's full Radar location evidence: the method that produced the display point, its precision (building, street_segment, street, neighbourhood or unknown), whether it is source_asserted or derived, the source pin and when it was observed, the support geometry kind, the exact property text quoted as evidence, alternative placements and warning codes. Use this when asked why a listing is placed where it is, or how certain its location is. method and verification are the honest labels: source_pin is the source's own assertion, geocoded is a derived position, locality_only is coarse by construction, and unresolved means no display point exists at all. Requires a listing_id from radar_geo_search. A listing the Radar store does not know returns not_found, never an empty location."
)

// RadarGeoSearchInput mirrors the wire's geo search request. Field names and
// nesting follow POST /v1/geo/search, so the tool schema and the contract are
// the same vocabulary.
type RadarGeoSearchInput struct {
	Population         RadarGeoPopulationInput `json:"population" jsonschema:"Property and status filter; every field inside is optional."`
	Geo                RadarGeoScopeInput      `json:"geo" jsonschema:"Geographic scope: an anchor point with a radius, or an empty object for no geographic narrowing. Without an anchor every candidate stays possible rather than being treated as outside."`
	MissingFieldPolicy string                  `json:"missingFieldPolicy,omitempty" jsonschema:"possible (default) keeps a listing with an unparseable floor discoverable in the possible group; exclude drops it."`
	Groups             []string                `json:"groups,omitempty" jsonschema:"Result groups to return: supported, possible, excluded. Default supported and possible."`
	Page               int                     `json:"page,omitempty" jsonschema:"1-based page number. Default 1."`
	PageSize           int                     `json:"pageSize,omitempty" jsonschema:"Rows per group on one page, 1 to 200. Default 50."`
}

// RadarGeoPopulationInput is the wire's population filter.
type RadarGeoPopulationInput struct {
	Status        string   `json:"status,omitempty" jsonschema:"active (default) or disappeared."`
	Neighborhoods []string `json:"neighborhoods,omitempty" jsonschema:"Radar catalogue neighbourhood slugs, for example lozenets or yavorov, as published in a search row. Omit to search every collected neighbourhood; an unknown slug is an invalid_query error, not an empty result."`
	PropertyTypes []string `json:"propertyTypes,omitempty" jsonschema:"Property type labels, case-insensitive, for example 2-стаен, 3-СТАЕН, ателие, парцел, земеделска земя, гараж. An unknown label is refused."`
	AreaMin       *float64 `json:"areaMin,omitempty" jsonschema:"Minimum size in square metres."`
	AreaMax       *float64 `json:"areaMax,omitempty" jsonschema:"Maximum size in square metres."`
	FloorMin      *int     `json:"floorMin,omitempty" jsonschema:"Minimum floor; 0 is the ground floor."`
	FloorMax      *int     `json:"floorMax,omitempty" jsonschema:"Maximum floor; 0 is the ground floor."`
	PriceMin      *float64 `json:"priceMin,omitempty" jsonschema:"Minimum asking price in EUR."`
	PriceMax      *float64 `json:"priceMax,omitempty" jsonschema:"Maximum asking price in EUR."`
}

// RadarGeoScopeInput is the wire's optional search circle.
type RadarGeoScopeInput struct {
	Anchor       *RadarGeoAnchorInput `json:"anchor,omitempty" jsonschema:"WGS84 anchor point. Omit or set null for no geographic narrowing."`
	RadiusMeters *int                 `json:"radiusMeters,omitempty" jsonschema:"Search radius in metres, 1 to 50000. Required with an anchor and not allowed without one."`
}

// RadarGeoAnchorInput is a named WGS84 point.
type RadarGeoAnchorInput struct {
	Lat float64 `json:"lat" jsonschema:"WGS84 latitude, -90 to 90."`
	Lng float64 `json:"lng" jsonschema:"WGS84 longitude, -180 to 180."`
}

// RadarLocationInput is the argument shape for radar_location.
type RadarLocationInput struct {
	ListingID string `json:"listing_id" jsonschema:"The listing id field from a radar_geo_search row. Required."`
}

// RadarGeoSearchOutput is the radar-geo-1 search envelope, unchanged. It is an
// alias rather than a copy so the tool answer and the wire contract cannot
// drift.
type RadarGeoSearchOutput = radarclient.GeoSearchResponse

// RadarLocationOutput is one listing's full location evidence with the
// contract version that produced it.
type RadarLocationOutput struct {
	ContractVersion string                        `json:"contractVersion" jsonschema:"The geo contract version this evidence was read under."`
	Location        radarclient.GeoLocationDetail `json:"location" jsonschema:"Full evidence: method, precision, verification, display and source points, support kind, quoted clues, alternatives and warnings."`
}

// radarGeoClient builds the geographic API client. An unconfigured pair is a
// capability error quoting both variable names; a live imot.bg fallback does
// not exist for these tools.
func (s *Server) radarGeoClient() (*radarclient.Client, error) {
	base := strings.TrimSpace(s.cfg.RadarGeoBaseURL)
	token := strings.TrimSpace(s.cfg.RadarGeoToken)
	if base == "" || token == "" {
		return nil, fmt.Errorf(
			"the Radar geographic API is not configured on this server: set %s and %s to enable %s and %s. These tools read published Radar data only and have no live-source fallback; use %s or %s for the non-geographic Radar store",
			EnvRadarGeoBaseURL, EnvRadarGeoToken, ToolRadarGeoSearch, ToolRadarLocation, ToolSearchListings, ToolGetListing,
		)
	}
	client, err := radarclient.New(radarclient.Config{BaseURL: base, Token: token})
	if err != nil {
		return nil, fmt.Errorf("the Radar geographic API configuration is invalid: %w", err)
	}
	return client, nil
}

// radarGeoSearch runs one geographic search through the published API.
func (s *Server) radarGeoSearch(ctx context.Context, in RadarGeoSearchInput) (RadarGeoSearchOutput, error) {
	client, err := s.radarGeoClient()
	if err != nil {
		return RadarGeoSearchOutput{}, err
	}
	input, err := in.toClientInput()
	if err != nil {
		return RadarGeoSearchOutput{}, err
	}
	return client.GeoSearch(ctx, input)
}

// radarLocation fetches one listing's full location evidence.
func (s *Server) radarLocation(ctx context.Context, in RadarLocationInput) (RadarLocationOutput, error) {
	client, err := s.radarGeoClient()
	if err != nil {
		return RadarLocationOutput{}, err
	}
	id := strings.TrimSpace(in.ListingID)
	if id == "" {
		return RadarLocationOutput{}, fmt.Errorf("listing_id is required")
	}
	detail, err := client.GeoLocation(ctx, id)
	if err != nil {
		return RadarLocationOutput{}, err
	}
	return RadarLocationOutput{
		ContractVersion: radarclient.ContractVersion,
		Location:        detail,
	}, nil
}

// toClientInput maps the tool schema onto the wire request: property labels are
// canonicalized (an unknown label is refused rather than silently matching
// nothing), and the default groups are supported plus possible.
func (in RadarGeoSearchInput) toClientInput() (radarclient.GeoSearchInput, error) {
	propertyTypes, err := radarclient.CanonicalPropertyTypeList(in.Population.PropertyTypes)
	if err != nil {
		return radarclient.GeoSearchInput{}, err
	}

	groups := in.Groups
	if len(groups) == 0 {
		groups = []string{radarclient.GroupSupported, radarclient.GroupPossible}
	}
	groups, err = radarclient.NormalizeGroups(groups)
	if err != nil {
		return radarclient.GeoSearchInput{}, err
	}

	input := radarclient.GeoSearchInput{
		Population: radarclient.GeoPopulation{
			Status:        strings.TrimSpace(in.Population.Status),
			Neighborhoods: in.Population.Neighborhoods,
			PropertyTypes: propertyTypes,
			AreaMin:       in.Population.AreaMin,
			AreaMax:       in.Population.AreaMax,
			FloorMin:      in.Population.FloorMin,
			FloorMax:      in.Population.FloorMax,
			PriceMin:      in.Population.PriceMin,
			PriceMax:      in.Population.PriceMax,
		},
		MissingFieldPolicy: strings.TrimSpace(in.MissingFieldPolicy),
		Groups:             groups,
		Page:               in.Page,
		PageSize:           in.PageSize,
	}

	if in.Geo.Anchor != nil {
		input.Geo.Anchor = &radarclient.GeoPoint{Lat: in.Geo.Anchor.Lat, Lng: in.Geo.Anchor.Lng}
		input.Geo.RadiusMeters = in.Geo.RadiusMeters
	} else if in.Geo.RadiusMeters != nil {
		return radarclient.GeoSearchInput{}, fmt.Errorf("radiusMeters was given without an anchor; an anchor and its radius travel together")
	}
	return input, nil
}
