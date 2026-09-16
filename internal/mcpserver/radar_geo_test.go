package mcpserver

// Focused tests for the published geographic tools (plan §8.2). They run
// without a Postgres server and without the real Radar API: the transport is
// an httptest fake, so a tool call can be proven to make exactly one HTTP
// request with the configured bearer token.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apsisvictor/imot-cli/internal/radarclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const radarGeoTestToken = "radar-geo-test-token-0123456789"

const radarGeoSearchBody = `{
  "contractVersion": "radar-geo-1",
  "observedAt": "2026-09-14T13:20:00Z",
  "totals": {"supported": 1, "possible": 1, "excluded": 0},
  "groups": {
    "supported": [{
      "id": "cmeq7x2k10000q4m9k8v3n2p1",
      "advId": "1b1786c0a4d9e27",
      "url": "https://www.imot.bg/obiava-1b1786c0a4d9e27",
      "neighborhood": "yavorov",
      "neighborhoodBg": "Яворов",
      "sourceNeighborhoodBg": "Яворов",
      "propertyType": "2-СТАЕН",
      "city": "София",
      "title": null,
      "priceEur": 95000,
      "pricePerSqm": 1187.5,
      "areaSqm": 80,
      "initialPriceEur": 99000,
      "finalSeenPriceEur": null,
      "priceChangeCount": 1,
      "floor": "4-ти от 6",
      "totalFloors": 6,
      "constructionType": "Тухла",
      "yearRange": "2000 - 2009",
      "heatingTec": "ДА",
      "heatingGas": "НЕ",
      "heating": "ТЕЦ",
      "features": ["затворен комплекс"],
      "agentName": null,
      "agentPhone": null,
      "phones": null,
      "sellerType": "Агенция",
      "isAgency": true,
      "agencyUrl": null,
      "firstSeenAt": "2026-09-02T07:04:11.000Z",
      "lastSeenAt": "2026-09-14T07:03:52.000Z",
      "disappearedAt": null,
      "daysListed": 12,
      "detailFetched": true,
      "firstMissingAt": null,
      "missCount": 0,
      "lastPriceChangeAt": null,
      "photo": null,
      "photoCount": 0,
      "detailState": "complete",
      "mediaState": "complete",
      "detailLastSuccessAt": null,
      "mediaLastSuccessAt": null,
      "location": {
        "method": "source_pin",
        "precision": "building",
        "verification": "source_asserted",
        "displayPoint": {"lat": 42.6901, "lng": 23.3226},
        "sourcePin": {"lat": 42.6901, "lng": 23.3226},
        "supportKind": "unknown",
        "warnings": []
      }
    }],
    "possible": [],
    "excluded": []
  },
  "coverage": {
    "inventoryComplete": true,
    "locationStates": {"pending": 0, "in_progress": 0, "retry_due": 0, "complete": 1, "blocked": 0, "unlocated": 0},
    "unresolvedFloorCount": 0
  },
  "listingContractVersion": "radar-v1-read-1",
  "page": 1,
  "pageSize": 50,
  "hasMore": {"supported": false, "possible": false, "excluded": false}
}`

const radarGeoLocationBody = `{
  "contractVersion": "radar-geo-1",
  "observedAt": "2026-09-14T13:20:00Z",
  "locations": [{
    "listingId": "listing-1",
    "advId": "adv-1",
    "method": "geocoded",
    "precision": "street",
    "verification": "derived",
    "displayPoint": {"lat": 42.68, "lng": 23.31},
    "sourcePin": {"lat": 42.681, "lng": 23.311},
    "sourcePinObservedAt": "2026-09-14T07:03:52.000Z",
    "support": {"type": "LineString", "coordinates": [[23.31, 42.68], [23.32, 42.69]]},
    "supportKind": "bounded_evidence",
    "evidence": [{"sourceField": "description", "quote": "ул. Витоша 10", "relation": "address", "featureIds": ["f1"]}],
    "alternatives": [{"precision": "neighbourhood", "displayPoint": null, "support": null, "supportKind": "locality_only", "evidence": []}],
    "warnings": ["missing_geometry"],
    "outcome": "located",
    "state": "complete",
    "processedAt": "2026-09-14T13:00:00Z"
  }],
  "missingIds": []
}`

// newRadarGeoSession builds a real MCP server on an httptest endpoint and
// connects an SDK client to it, so tool registration, input schemas and
// structured output validation are all exercised.
func newRadarGeoSession(t *testing.T, cfg Config) *mcp.ClientSession {
	t.Helper()
	server, err := New(cfg, testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	client := mcp.NewClient(&mcp.Implementation{Name: "radar-test", Version: "0.0.1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp/" + testSecret}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func callRadarToolRaw(t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return result
}

func callRadarTool[T any](t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, args map[string]any) T {
	t.Helper()
	var out T
	result := callRadarToolRaw(t, ctx, session, name, args)
	if result.IsError {
		t.Fatalf("%s returned an error result: %s", name, radarToolErrorText(t, result))
	}
	if result.StructuredContent == nil {
		t.Fatalf("%s returned no structured content", name)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("%s: marshaling structured content: %v", name, err)
	}
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("%s: decoding structured content: %v", name, err)
	}
	return out
}

func radarToolErrorText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func TestLoadConfigValidatesRadarGeoPair(t *testing.T) {
	t.Setenv("IMOT_MCP_TOKENS", "pilot:"+testSecret)
	t.Setenv("IMOT_MCP_DB", filepath.Join(t.TempDir(), "cache.db"))

	t.Setenv(EnvRadarGeoBaseURL, "https://radar.example.com")
	t.Setenv(EnvRadarGeoToken, "")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), EnvRadarGeoToken) {
		t.Fatalf("a base URL without a token must be refused: %v", err)
	}

	t.Setenv(EnvRadarGeoBaseURL, "")
	t.Setenv(EnvRadarGeoToken, "geo-read-token")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), EnvRadarGeoBaseURL) {
		t.Fatalf("a token without a base URL must be refused: %v", err)
	}

	t.Setenv(EnvRadarGeoBaseURL, "radar.example.com")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), EnvRadarGeoBaseURL) {
		t.Fatalf("a base URL without a scheme must be refused: %v", err)
	}

	t.Setenv(EnvRadarGeoBaseURL, "https://radar.example.com")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig with a complete pair: %v", err)
	}
	if cfg.RadarGeoBaseURL != "https://radar.example.com" || cfg.RadarGeoToken != "geo-read-token" {
		t.Fatalf("geo config = %q / %q", cfg.RadarGeoBaseURL, cfg.RadarGeoToken)
	}

	t.Setenv(EnvRadarGeoBaseURL, "")
	t.Setenv(EnvRadarGeoToken, "")
	cfg, err = LoadConfig()
	if err != nil {
		t.Fatalf("an unset geographic API is valid configuration: %v", err)
	}
	if cfg.RadarGeoBaseURL != "" || cfg.RadarGeoToken != "" {
		t.Fatalf("unset geo config = %q / %q", cfg.RadarGeoBaseURL, cfg.RadarGeoToken)
	}
}

func TestRadarGeoToolsRegisteredAndUnavailable(t *testing.T) {
	t.Setenv(EnvRadarGeoBaseURL, "")
	t.Setenv(EnvRadarGeoToken, "")
	session := newRadarGeoSession(t, testConfig(t))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	registered := make(map[string]bool, len(tools.Tools))
	descriptions := make(map[string]string, len(tools.Tools))
	for _, tool := range tools.Tools {
		registered[tool.Name] = true
		descriptions[tool.Name] = tool.Description
		if tool.InputSchema == nil {
			t.Errorf("tool %s has no input schema", tool.Name)
		}
	}
	for _, want := range []string{ToolRadarGeoSearch, ToolRadarLocation, ToolSearchListings, ToolGetListing, ToolListFilters} {
		if !registered[want] {
			t.Errorf("tool %s was not registered", want)
		}
	}

	// The descriptions carry the contract a model cannot infer: how to use the
	// tool, what a possible match means and that source and derived precision
	// are different facts.
	searchDescription := descriptions[ToolRadarGeoSearch]
	for _, want := range []string{"Use this", "supported", "possible", "source_pin", "locality_only", "derived"} {
		if !strings.Contains(searchDescription, want) {
			t.Errorf("radar_geo_search description must mention %q", want)
		}
	}
	locationDescription := descriptions[ToolRadarLocation]
	for _, want := range []string{"Use this", "source_asserted", "derived", "locality_only", "not_found"} {
		if !strings.Contains(locationDescription, want) {
			t.Errorf("radar_location description must mention %q", want)
		}
	}

	calls := []struct {
		name string
		args map[string]any
	}{
		{ToolRadarGeoSearch, map[string]any{"population": map[string]any{}, "geo": map[string]any{}}},
		{ToolRadarLocation, map[string]any{"listing_id": "listing-1"}},
	}
	for _, call := range calls {
		result := callRadarToolRaw(t, ctx, session, call.name, call.args)
		if !result.IsError {
			t.Fatalf("%s should report an unavailable capability", call.name)
		}
		text := radarToolErrorText(t, result)
		for _, want := range []string{"not configured", EnvRadarGeoBaseURL, EnvRadarGeoToken} {
			if !strings.Contains(text, want) {
				t.Errorf("%s error %q must mention %q", call.name, text, want)
			}
		}
	}
}

func TestRadarGeoToolsUseConfiguredAPI(t *testing.T) {
	var (
		searchInput   radarclient.GeoSearchInput
		locationIDs   []string
		authorization string
	)

	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/v1/geo/search":
			if err := json.NewDecoder(r.Body).Decode(&searchInput); err != nil {
				t.Errorf("decoding search request: %v", err)
			}
			_, _ = w.Write([]byte(radarGeoSearchBody))
		case "/v1/geo/locations/batch":
			var body struct {
				ListingIDs []string `json:"listingIds"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding batch request: %v", err)
			}
			locationIDs = body.ListingIDs
			_, _ = w.Write([]byte(radarGeoLocationBody))
		default:
			http.NotFound(w, r)
		}
	}))
	defer fake.Close()

	t.Setenv(EnvRadarGeoBaseURL, fake.URL)
	t.Setenv(EnvRadarGeoToken, radarGeoTestToken)
	session := newRadarGeoSession(t, testConfig(t))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	search := callRadarTool[RadarGeoSearchOutput](t, ctx, session, ToolRadarGeoSearch, map[string]any{
		"population": map[string]any{
			"neighborhoods": []string{"yavorov"},
			"propertyTypes": []string{"2-стаен", "офис"},
			"areaMin":       80,
		},
		"geo": map[string]any{
			"anchor":       map[string]any{"lat": 42.6901, "lng": 23.3226},
			"radiusMeters": 1500,
		},
	})
	if authorization != "Bearer "+radarGeoTestToken {
		t.Fatalf("Authorization = %q, want the configured bearer token", authorization)
	}
	if !stringListsEqual(searchInput.Population.PropertyTypes, []string{"2-СТАЕН", "ОФИС"}) {
		t.Errorf("propertyTypes reached the API as %#v", searchInput.Population.PropertyTypes)
	}
	if !stringListsEqual(searchInput.Groups, []string{radarclient.GroupSupported, radarclient.GroupPossible}) {
		t.Errorf("groups = %#v, want the supported+possible default", searchInput.Groups)
	}
	if searchInput.Geo.Anchor == nil || searchInput.Geo.RadiusMeters == nil || *searchInput.Geo.RadiusMeters != 1500 {
		t.Errorf("geo = %#v", searchInput.Geo)
	}
	if len(searchInput.Population.Neighborhoods) != 1 || searchInput.Population.Neighborhoods[0] != "yavorov" {
		t.Errorf("neighborhoods = %#v", searchInput.Population.Neighborhoods)
	}
	if searchInput.Population.AreaMin == nil || *searchInput.Population.AreaMin != 80 {
		t.Errorf("areaMin = %v", searchInput.Population.AreaMin)
	}

	if search.ContractVersion != radarclient.ContractVersion {
		t.Errorf("contractVersion = %q", search.ContractVersion)
	}
	if search.Totals.Supported != 1 || search.Totals.Possible != 1 {
		t.Errorf("totals = %#v", search.Totals)
	}
	if len(search.Groups.Supported) != 1 {
		t.Fatalf("supported rows = %d", len(search.Groups.Supported))
	}
	row := search.Groups.Supported[0]
	if row.URL == "" || row.Location.Method == nil || *row.Location.Method != "source_pin" {
		t.Errorf("row = %#v", row)
	}
	if search.Coverage.InventoryComplete == nil || !*search.Coverage.InventoryComplete {
		t.Errorf("coverage = %#v", search.Coverage)
	}

	location := callRadarTool[RadarLocationOutput](t, ctx, session, ToolRadarLocation, map[string]any{
		"listing_id": "listing-1",
	})
	if location.ContractVersion != radarclient.ContractVersion {
		t.Errorf("location contractVersion = %q", location.ContractVersion)
	}
	detail := location.Location
	if detail.ListingID != "listing-1" || detail.AdvID != "adv-1" {
		t.Errorf("identity = %s/%s", detail.ListingID, detail.AdvID)
	}
	if detail.Method == nil || *detail.Method != "geocoded" || detail.Precision == nil || *detail.Precision != "street" {
		t.Errorf("method/precision = %v/%v", detail.Method, detail.Precision)
	}
	if detail.Verification == nil || *detail.Verification != "derived" {
		t.Errorf("verification = %v", detail.Verification)
	}
	if detail.DisplayPoint == nil || detail.SourcePin == nil || detail.SourcePinObservedAt == nil {
		t.Errorf("points = %#v / %#v / %v", detail.DisplayPoint, detail.SourcePin, detail.SourcePinObservedAt)
	}
	if detail.Support == nil || detail.Support.Type != "LineString" || detail.SupportKind == nil || *detail.SupportKind != "bounded_evidence" {
		t.Errorf("support = %#v / %v", detail.Support, detail.SupportKind)
	}
	if len(detail.Evidence) != 1 || detail.Evidence[0].Quote != "ул. Витоша 10" {
		t.Errorf("evidence = %#v", detail.Evidence)
	}
	if len(detail.Warnings) != 1 || detail.Warnings[0] != "missing_geometry" {
		t.Errorf("warnings = %#v", detail.Warnings)
	}
	if len(locationIDs) != 1 || locationIDs[0] != "listing-1" {
		t.Errorf("batch ids = %#v", locationIDs)
	}
}

func TestRadarGeoSearchRefusesUnknownPropertyType(t *testing.T) {
	requests := 0
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(radarGeoSearchBody))
	}))
	defer fake.Close()

	t.Setenv(EnvRadarGeoBaseURL, fake.URL)
	t.Setenv(EnvRadarGeoToken, radarGeoTestToken)
	session := newRadarGeoSession(t, testConfig(t))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result := callRadarToolRaw(t, ctx, session, ToolRadarGeoSearch, map[string]any{
		"population": map[string]any{"propertyTypes": []string{"няма такъв"}},
		"geo":        map[string]any{},
	})
	if !result.IsError {
		t.Fatal("an unknown property type must be refused")
	}
	if text := radarToolErrorText(t, result); !strings.Contains(text, "unknown property type") {
		t.Errorf("error = %q", text)
	}
	if requests != 0 {
		t.Fatalf("an invalid filter reached the API %d time(s)", requests)
	}
}

func stringListsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
