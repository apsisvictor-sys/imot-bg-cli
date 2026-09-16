package radarclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const radarTestToken = "radar-test-token-0123456789"

// searchEnvelopeBody is the shape the API publishes for /v1/geo/search: the
// committed fixture's field set, in wire order.
const searchEnvelopeBody = `{
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
  "hasMore": {"supported": false, "possible": true, "excluded": false}
}`

// locationBatchBody carries one located listing with full evidence.
const locationBatchBody = `{
  "contractVersion": "radar-geo-1",
  "observedAt": "2026-09-14T13:20:00Z",
  "locations": [{
    "listingId": "listing-1",
    "advId": "adv-1",
    "method": "geocoded",
    "precision": "street",
    "verification": "derived",
    "displayPoint": {"lat": 42.68, "lng": 23.31},
    "sourcePin": null,
    "sourcePinObservedAt": null,
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

const mapEnvelopeBody = `{
  "contractVersion": "radar-geo-1",
  "observedAt": "2026-09-14T13:20:00Z",
  "totals": {"supported": 2, "possible": 0, "excluded": 0},
  "features": [
    {"kind": "point", "listingId": "listing-1", "advId": "adv-1", "displayPoint": {"lat": 42.69, "lng": 23.32}, "method": "source_pin", "precision": "building", "verification": "source_asserted"},
    {"kind": "cluster", "center": {"lat": 42.69, "lng": 23.32}, "count": 3, "nativeCount": 2, "derivedCount": 1, "coLocateKey": "42.690|23.320"}
  ],
  "coarseCount": 0,
  "unlocatedCount": 0,
  "unresolvedFloorCount": 0,
  "truncated": false
}`

func newRadarTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(Config{BaseURL: server.URL, Token: radarTestToken})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func writeBody(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write([]byte(body)); err != nil {
		t.Errorf("writing response: %v", err)
	}
}

func floatPointer(value float64) *float64 { return &value }

func intPointer(value int) *int { return &value }

func TestGeoSearchRoundTrip(t *testing.T) {
	var (
		method, path, authorization, contentType string
		request                                  GeoSearchInput
	)
	client := newRadarTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		authorization, contentType = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		writeBody(t, w, searchEnvelopeBody)
	})

	response, err := client.GeoSearch(context.Background(), GeoSearchInput{
		Population: GeoPopulation{
			Neighborhoods: []string{"yavorov"},
			PropertyTypes: []string{"2-СТАЕН"},
			PriceMax:      floatPointer(120000),
		},
		Geo:                GeoScope{Anchor: &GeoPoint{Lat: 42.6901, Lng: 23.3226}, RadiusMeters: intPointer(1500)},
		MissingFieldPolicy: PolicyPossible,
		Groups:             []string{GroupSupported, GroupPossible},
	})
	if err != nil {
		t.Fatalf("GeoSearch: %v", err)
	}

	if method != http.MethodPost || path != pathGeoSearch {
		t.Errorf("request = %s %s, want POST %s", method, path, pathGeoSearch)
	}
	if authorization != "Bearer "+radarTestToken {
		t.Errorf("Authorization = %q, want the bearer token", authorization)
	}
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q", contentType)
	}
	if len(request.Population.Neighborhoods) != 1 || request.Population.Neighborhoods[0] != "yavorov" {
		t.Errorf("neighborhoods = %#v", request.Population.Neighborhoods)
	}
	if len(request.Population.PropertyTypes) != 1 || request.Population.PropertyTypes[0] != "2-СТАЕН" {
		t.Errorf("propertyTypes = %#v", request.Population.PropertyTypes)
	}
	if request.Geo.Anchor == nil || request.Geo.Anchor.Lat != 42.6901 || request.Geo.RadiusMeters == nil || *request.Geo.RadiusMeters != 1500 {
		t.Errorf("geo = %#v", request.Geo)
	}
	if len(request.Groups) != 2 || request.Groups[0] != GroupSupported {
		t.Errorf("groups = %#v", request.Groups)
	}
	if request.Page != 1 || request.PageSize != DefaultPageSize {
		t.Errorf("page/pageSize = %d/%d", request.Page, request.PageSize)
	}

	if response.ContractVersion != ContractVersion {
		t.Errorf("contractVersion = %q", response.ContractVersion)
	}
	if response.ListingContractVersion != ListingContractVersion {
		t.Errorf("listingContractVersion = %q", response.ListingContractVersion)
	}
	if response.Totals.Supported != 1 || response.Totals.Possible != 1 || response.Totals.Excluded != 0 {
		t.Errorf("totals = %#v", response.Totals)
	}
	if len(response.Groups.Supported) != 1 {
		t.Fatalf("supported rows = %d", len(response.Groups.Supported))
	}
	row := response.Groups.Supported[0]
	if row.PriceEur == nil || *row.PriceEur != 95000 {
		t.Errorf("priceEur = %v", row.PriceEur)
	}
	if row.Location.Method == nil || *row.Location.Method != "source_pin" {
		t.Errorf("location.method = %v", row.Location.Method)
	}
	if row.Location.DisplayPoint == nil || row.Location.DisplayPoint.Lat != 42.6901 {
		t.Errorf("location.displayPoint = %#v", row.Location.DisplayPoint)
	}
	if response.Coverage.InventoryComplete == nil || !*response.Coverage.InventoryComplete {
		t.Errorf("coverage.inventoryComplete = %v", response.Coverage.InventoryComplete)
	}
	if response.HasMore.Supported || !response.HasMore.Possible {
		t.Errorf("hasMore = %#v", response.HasMore)
	}
}

func TestGeoSearchRefusesForeignEnvelopes(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"listing contract instead of geo contract", `{"contractVersion":"radar-v1-read-1","observedAt":"2026-09-14T13:20:00Z","totals":{"supported":0,"possible":0,"excluded":0},"groups":{"supported":[],"possible":[],"excluded":[]},"coverage":{"inventoryComplete":null,"locationStates":{"pending":0,"in_progress":0,"retry_due":0,"complete":0,"blocked":0,"unlocated":0},"unresolvedFloorCount":0},"listingContractVersion":"radar-v1-read-1","page":1,"pageSize":50,"hasMore":{"supported":false,"possible":false,"excluded":false}}`},
		{"future listing projection", `{"contractVersion":"radar-geo-1","observedAt":"2026-09-14T13:20:00Z","totals":{"supported":0,"possible":0,"excluded":0},"groups":{"supported":[],"possible":[],"excluded":[]},"coverage":{"inventoryComplete":null,"locationStates":{"pending":0,"in_progress":0,"retry_due":0,"complete":0,"blocked":0,"unlocated":0},"unresolvedFloorCount":0},"listingContractVersion":"radar-v1-read-2","page":1,"pageSize":50,"hasMore":{"supported":false,"possible":false,"excluded":false}}`},
		{"unknown field", `{"contractVersion":"radar-geo-1","observedAt":"2026-09-14T13:20:00Z","totals":{"supported":0,"possible":0,"excluded":0},"groups":{"supported":[],"possible":[],"excluded":[]},"coverage":{"inventoryComplete":null,"locationStates":{"pending":0,"in_progress":0,"retry_due":0,"complete":0,"blocked":0,"unlocated":0},"unresolvedFloorCount":0},"listingContractVersion":"radar-v1-read-1","page":1,"pageSize":50,"hasMore":{"supported":false,"possible":false,"excluded":false},"unexpected":1}`},
		{"html error page", `<!doctype html><html><body><h1>502 Bad Gateway</h1></body></html>`},
		{"json error page without the geo contract", `{"error":"upstream unavailable"}`},
		{"empty body", ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newRadarTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writeBody(t, w, tc.body)
			})
			_, err := client.GeoSearch(context.Background(), GeoSearchInput{})
			assertAPIError(t, err, CodeRadarUnavailable, http.StatusOK)
		})
	}
}

func TestGeoErrorMapping(t *testing.T) {
	const errorEnvelope = `{"contractVersion":"radar-geo-1","observedAt":"2026-09-14T13:20:00Z","error":{"code":"%s","message":"bounded message"}}`
	cases := []struct {
		name       string
		status     int
		body       string
		wantCode   string
		wantStatus int
	}{
		{"unauthorized envelope", http.StatusUnauthorized, strings.Replace(errorEnvelope, "%s", CodeUnauthorized, 1), CodeUnauthorized, http.StatusUnauthorized},
		{"unauthorized without an envelope", http.StatusUnauthorized, `<!doctype html><html>401</html>`, CodeUnauthorized, http.StatusUnauthorized},
		{"forbidden maps to unauthorized", http.StatusForbidden, ``, CodeUnauthorized, http.StatusForbidden},
		{"invalid query envelope", http.StatusBadRequest, strings.Replace(errorEnvelope, "%s", CodeInvalidQuery, 1), CodeInvalidQuery, http.StatusBadRequest},
		{"not found envelope", http.StatusNotFound, strings.Replace(errorEnvelope, "%s", CodeNotFound, 1), CodeNotFound, http.StatusNotFound},
		{"too large envelope", http.StatusRequestEntityTooLarge, strings.Replace(errorEnvelope, "%s", CodeQueryTooLarge, 1), CodeQueryTooLarge, http.StatusRequestEntityTooLarge},
		{"unavailable envelope", http.StatusInternalServerError, strings.Replace(errorEnvelope, "%s", CodeRadarUnavailable, 1), CodeRadarUnavailable, http.StatusInternalServerError},
		{"gateway html", http.StatusBadGateway, `<html>bad gateway</html>`, CodeRadarUnavailable, http.StatusBadGateway},
		{"unknown code in envelope", http.StatusTeapot, strings.Replace(errorEnvelope, "%s", "made_up", 1), CodeRadarUnavailable, http.StatusTeapot},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newRadarTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				writeBody(t, w, tc.body)
			})
			_, err := client.GeoSearch(context.Background(), GeoSearchInput{})
			apiErr := assertAPIError(t, err, tc.wantCode, tc.wantStatus)
			if strings.Contains(err.Error(), radarTestToken) {
				t.Fatalf("the read token must never appear in an error: %v", err)
			}
			// Only the envelope case promises a specific bounded message. The
			// no-envelope unauthorized cases intentionally carry the generic
			// status message, which is caller-irrelevant by contract.
			envelopeCase := strings.Contains(tc.body, "\"error\":")
			if envelopeCase && tc.wantCode == CodeUnauthorized && apiErr.Message != "" && !strings.Contains(apiErr.Message, "bounded message") {
				t.Errorf("message = %q", apiErr.Message)
			}
		})
	}
}

func assertAPIError(t *testing.T, err error, wantCode string, wantStatus int) *APIError {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v (%T) is not an *APIError", err, err)
	}
	if apiErr.Code != wantCode {
		t.Errorf("code = %q, want %q", apiErr.Code, wantCode)
	}
	if apiErr.Status != wantStatus {
		t.Errorf("status = %d, want %d", apiErr.Status, wantStatus)
	}
	return apiErr
}

func TestGeoLocationsBatchRoundTrip(t *testing.T) {
	var got []string
	client := newRadarTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != pathGeoLocationsBatch {
			t.Errorf("path = %s", r.URL.Path)
		}
		var body struct {
			ListingIDs []string `json:"listingIds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		got = body.ListingIDs
		writeBody(t, w, locationBatchBody)
	})

	response, err := client.GeoLocationsBatch(context.Background(), []string{"listing-1", " listing-1 "})
	if err != nil {
		t.Fatalf("GeoLocationsBatch: %v", err)
	}
	if len(got) != 1 || got[0] != "listing-1" {
		t.Errorf("requested ids = %#v, want one de-duplicated id", got)
	}
	if response.ContractVersion != ContractVersion || len(response.Locations) != 1 {
		t.Fatalf("response = %#v", response)
	}
	if len(response.MissingIDs) != 0 {
		t.Errorf("missingIds = %#v", response.MissingIDs)
	}
}

func TestGeoLocationParsesFullEvidence(t *testing.T) {
	client := newRadarTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeBody(t, w, locationBatchBody)
	})
	detail, err := client.GeoLocation(context.Background(), "listing-1")
	if err != nil {
		t.Fatalf("GeoLocation: %v", err)
	}
	if detail.ListingID != "listing-1" || detail.AdvID != "adv-1" {
		t.Errorf("identity = %s/%s", detail.ListingID, detail.AdvID)
	}
	if detail.Method == nil || *detail.Method != "geocoded" {
		t.Errorf("method = %v", detail.Method)
	}
	if detail.Precision == nil || *detail.Precision != "street" {
		t.Errorf("precision = %v", detail.Precision)
	}
	if detail.Verification == nil || *detail.Verification != "derived" {
		t.Errorf("verification = %v", detail.Verification)
	}
	if detail.DisplayPoint == nil || detail.DisplayPoint.Lat != 42.68 || detail.DisplayPoint.Lng != 23.31 {
		t.Errorf("displayPoint = %#v", detail.DisplayPoint)
	}
	if detail.SourcePin != nil {
		t.Errorf("sourcePin = %#v, want null", detail.SourcePin)
	}
	if detail.SupportKind == nil || *detail.SupportKind != "bounded_evidence" {
		t.Errorf("supportKind = %v", detail.SupportKind)
	}
	if detail.Support == nil || detail.Support.Type != "LineString" {
		t.Fatalf("support = %#v", detail.Support)
	}
	if len(detail.Support.Coordinates) != 2 {
		t.Errorf("support coordinates = %#v", detail.Support.Coordinates)
	}
	if len(detail.Evidence) != 1 || detail.Evidence[0].Quote != "ул. Витоша 10" {
		t.Errorf("evidence = %#v", detail.Evidence)
	}
	if detail.Evidence[0].Relation != "address" || detail.Evidence[0].SourceField != "description" {
		t.Errorf("evidence[0] = %#v", detail.Evidence[0])
	}
	if len(detail.Alternatives) != 1 || detail.Alternatives[0].SupportKind != "locality_only" {
		t.Errorf("alternatives = %#v", detail.Alternatives)
	}
	if len(detail.Warnings) != 1 || detail.Warnings[0] != "missing_geometry" {
		t.Errorf("warnings = %#v", detail.Warnings)
	}
	if detail.Outcome == nil || *detail.Outcome != "located" || detail.ProcessedAt == nil {
		t.Errorf("outcome/processedAt = %v/%v", detail.Outcome, detail.ProcessedAt)
	}
}

func TestGeoLocationMissingIDIsNotFound(t *testing.T) {
	client := newRadarTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeBody(t, w, `{"contractVersion":"radar-geo-1","observedAt":"2026-09-14T13:20:00Z","locations":[],"missingIds":["listing-9"]}`)
	})
	_, err := client.GeoLocation(context.Background(), "listing-9")
	assertAPIError(t, err, CodeNotFound, http.StatusNotFound)
}

func TestGeoMapRoundTrip(t *testing.T) {
	var request GeoMapInput
	client := newRadarTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != pathGeoMap {
			t.Errorf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		writeBody(t, w, mapEnvelopeBody)
	})

	response, err := client.GeoMap(context.Background(), GeoMapInput{
		Population: GeoPopulation{PropertyTypes: []string{"2-СТАЕН"}},
		Viewport:   &GeoViewport{MinLat: 42.6, MaxLat: 42.7, MinLng: 23.2, MaxLng: 23.4},
		Zoom:       15,
	})
	if err != nil {
		t.Fatalf("GeoMap: %v", err)
	}
	if request.Viewport == nil || request.Viewport.MaxLat != 42.7 || request.Zoom != 15 {
		t.Errorf("request = %#v", request)
	}
	if response.ContractVersion != ContractVersion || len(response.Features) != 2 {
		t.Fatalf("response = %#v", response)
	}
	if response.Features[0].Kind != "point" || response.Features[0].DisplayPoint == nil {
		t.Errorf("features[0] = %#v", response.Features[0])
	}
	if response.Features[1].Kind != "cluster" || response.Features[1].Count == nil || *response.Features[1].Count != 3 {
		t.Errorf("features[1] = %#v", response.Features[1])
	}
	if response.Truncated {
		t.Error("truncated should be false in the fixture")
	}
}

func TestGeoSearchValidatesBeforeSending(t *testing.T) {
	requests := 0
	client := newRadarTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		writeBody(t, w, searchEnvelopeBody)
	})
	cases := []struct {
		name  string
		input GeoSearchInput
	}{
		{"radius without an anchor", GeoSearchInput{Geo: GeoScope{RadiusMeters: intPointer(1000)}}},
		{"anchor without a radius", GeoSearchInput{Geo: GeoScope{Anchor: &GeoPoint{Lat: 42.69, Lng: 23.32}}}},
		{"latitude out of range", GeoSearchInput{Geo: GeoScope{Anchor: &GeoPoint{Lat: 91, Lng: 23.32}, RadiusMeters: intPointer(1000)}}},
		{"longitude out of range", GeoSearchInput{Geo: GeoScope{Anchor: &GeoPoint{Lat: 42.69, Lng: 181}, RadiusMeters: intPointer(1000)}}},
		{"radius below the minimum", GeoSearchInput{Geo: GeoScope{Anchor: &GeoPoint{Lat: 42.69, Lng: 23.32}, RadiusMeters: intPointer(0)}}},
		{"radius above the maximum", GeoSearchInput{Geo: GeoScope{Anchor: &GeoPoint{Lat: 42.69, Lng: 23.32}, RadiusMeters: intPointer(50001)}}},
		{"page size above the cap", GeoSearchInput{PageSize: MaxPageSize + 1}},
		{"page below one", GeoSearchInput{Page: -1}},
		{"unknown group", GeoSearchInput{Groups: []string{"maybe"}}},
		{"unknown policy", GeoSearchInput{MissingFieldPolicy: "sure"}},
		{"unknown status", GeoSearchInput{Population: GeoPopulation{Status: "sold"}}},
		{"negative price", GeoSearchInput{Population: GeoPopulation{PriceMin: floatPointer(-1)}}},
		{"reversed area bounds", GeoSearchInput{Population: GeoPopulation{AreaMin: floatPointer(120), AreaMax: floatPointer(80)}}},
		{"reversed floor bounds", GeoSearchInput{Population: GeoPopulation{FloorMin: intPointer(5), FloorMax: intPointer(1)}}},
		{"empty neighbourhood", GeoSearchInput{Population: GeoPopulation{Neighborhoods: []string{" "}}}},
		{"unknown id predicate mode", GeoSearchInput{Population: GeoPopulation{IDPredicate: &GeoIDPredicate{Mode: "only", IDs: []string{"a"}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := client.GeoSearch(context.Background(), tc.input); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
	if requests != 0 {
		t.Fatalf("invalid filters reached the network %d time(s)", requests)
	}
}

func TestGeoLocationsBatchValidatesBeforeSending(t *testing.T) {
	requests := 0
	client := newRadarTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		writeBody(t, w, locationBatchBody)
	})
	if _, err := client.GeoLocationsBatch(context.Background(), nil); err == nil {
		t.Fatal("expected an error for an empty id list")
	}
	if _, err := client.GeoLocationsBatch(context.Background(), []string{strings.Repeat("a", MaxIdentifierLength+1)}); err == nil {
		t.Fatal("expected an error for an over-long id")
	}
	tooMany := make([]string, MaxBatchIDs+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("listing-%d", i)
	}
	if _, err := client.GeoLocationsBatch(context.Background(), tooMany); err == nil {
		t.Fatal("expected an error above the batch id cap")
	}
	if requests != 0 {
		t.Fatalf("invalid batches reached the network %d time(s)", requests)
	}
}

func TestUnreachableAPIIsTyped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	client, err := New(Config{BaseURL: server.URL, Token: radarTestToken})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	server.Close()

	_, err = client.GeoSearch(context.Background(), GeoSearchInput{})
	apiErr := assertAPIError(t, err, CodeRadarUnavailable, 0)
	if strings.Contains(err.Error(), radarTestToken) {
		t.Fatalf("the read token must never appear in an error: %v", err)
	}
	if apiErr.Message == "" {
		t.Error("an unreachable API should say so")
	}
}

func TestFromEnvRequiresBothVariables(t *testing.T) {
	t.Setenv(EnvBaseURL, "")
	t.Setenv(EnvReadToken, "")
	_, err := FromEnv()
	if err == nil || !strings.Contains(err.Error(), EnvBaseURL) {
		t.Fatalf("empty base URL error = %v", err)
	}

	t.Setenv(EnvBaseURL, "https://radar.example.com")
	_, err = FromEnv()
	if err == nil || !strings.Contains(err.Error(), EnvReadToken) {
		t.Fatalf("empty token error = %v", err)
	}

	t.Setenv(EnvReadToken, radarTestToken)
	client, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if client.baseURL != "https://radar.example.com" {
		t.Errorf("baseURL = %q", client.baseURL)
	}
	if client.http.Timeout != DefaultTimeout {
		t.Errorf("timeout = %s, want %s", client.http.Timeout, DefaultTimeout)
	}
	if client.token != radarTestToken {
		t.Error("the configured token should reach the client")
	}
}

func TestValidateBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"https origin", "https://radar.example.com", false},
		{"http local origin", "http://127.0.0.1:8099", false},
		{"https with a path prefix", "https://example.com/radar", false},
		{"empty", "", true},
		{"not a URL", "radar.example.com", true},
		{"wrong scheme", "ftp://radar.example.com", true},
		{"credentials in the URL", "https://user:secret@radar.example.com", true},
		{"query", "https://radar.example.com?token=x", true},
		{"fragment", "https://radar.example.com#frag", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBaseURL(tc.value)
			if tc.wantErr && err == nil {
				t.Fatal("expected an error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestNewRequiresTokenAndTrimsBaseURL(t *testing.T) {
	if _, err := New(Config{BaseURL: "https://radar.example.com", Token: "  "}); err == nil {
		t.Fatal("expected an error for an empty token")
	}
	client, err := New(Config{BaseURL: "https://radar.example.com/", Token: radarTestToken, Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if client.baseURL != "https://radar.example.com" {
		t.Errorf("baseURL = %q", client.baseURL)
	}
	if client.http.Timeout != time.Second {
		t.Errorf("timeout = %s", client.http.Timeout)
	}
}
