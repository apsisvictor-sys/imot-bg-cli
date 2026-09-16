package cli

// Tests for the published Radar command group. They never touch the network
// except through an httptest fake, and they never execute a cobra command that
// would read the environment, so they run without Radar credentials.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apsisvictor/imot-cli/internal/radarclient"
)

func radarTestFlags() radarSearchFlags {
	return radarSearchFlags{
		groups:             radarclient.GroupSupported + "," + radarclient.GroupPossible,
		missingFieldPolicy: radarclient.PolicyPossible,
	}
}

func TestRadarSearchFlagsInput(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*radarSearchFlags)
		wantErr string
		check   func(*testing.T, radarclient.GeoSearchInput)
	}{
		{
			name: "canonicalizes type labels case-insensitively",
			mutate: func(f *radarSearchFlags) {
				f.types = []string{"2-стаен", "2-СТАЕН", "ателие", " таван"}
			},
			check: func(t *testing.T, input radarclient.GeoSearchInput) {
				if !radarTestStringsEqual(input.Population.PropertyTypes, []string{"2-СТАЕН", "АТЕЛИЕ"}) {
					t.Errorf("propertyTypes = %#v", input.Population.PropertyTypes)
				}
				if !radarTestStringsEqual(input.Groups, []string{radarclient.GroupSupported, radarclient.GroupPossible}) {
					t.Errorf("groups = %#v", input.Groups)
				}
				if input.PageSize != radarclient.DefaultPageSize {
					t.Errorf("pageSize = %d", input.PageSize)
				}
				if input.MissingFieldPolicy != radarclient.PolicyPossible {
					t.Errorf("missingFieldPolicy = %q", input.MissingFieldPolicy)
				}
			},
		},
		{
			name:   "a garage label resolves to both stored canonical values",
			mutate: func(f *radarSearchFlags) { f.types = []string{"гараж"} },
			check: func(t *testing.T, input radarclient.GeoSearchInput) {
				if !radarTestStringsEqual(input.Population.PropertyTypes, []string{"ГАРАЖ", "ПАРКОМЯСТО"}) {
					t.Errorf("propertyTypes = %#v", input.Population.PropertyTypes)
				}
			},
		},
		{
			name:    "an unknown type label is refused",
			mutate:  func(f *radarSearchFlags) { f.types = []string{"няма такъв"} },
			wantErr: "unknown property type",
		},
		{
			name: "a Bulgarian neighbourhood label becomes a slug and explicit slugs stay verbatim",
			mutate: func(f *radarSearchFlags) {
				f.neighborhoods = []string{"Лозенец"}
				f.neighborhoodSlugs = []string{"mladost-1", "lozenets"}
			},
			check: func(t *testing.T, input radarclient.GeoSearchInput) {
				if !radarTestStringsEqual(input.Population.Neighborhoods, []string{"lozenets", "mladost-1"}) {
					t.Errorf("neighborhoods = %#v", input.Population.Neighborhoods)
				}
			},
		},
		{
			name: "bounds become wire pointers",
			mutate: func(f *radarSearchFlags) {
				f.minSqm, f.maxSqm = 80, 120
				f.minPrice, f.maxPrice = 50000, 120000
				f.floorMax = 0
			},
			check: func(t *testing.T, input radarclient.GeoSearchInput) {
				population := input.Population
				if population.AreaMin == nil || *population.AreaMin != 80 || population.AreaMax == nil || *population.AreaMax != 120 {
					t.Errorf("area bounds = %v / %v", population.AreaMin, population.AreaMax)
				}
				if population.PriceMin == nil || *population.PriceMin != 50000 || population.PriceMax == nil || *population.PriceMax != 120000 {
					t.Errorf("price bounds = %v / %v", population.PriceMin, population.PriceMax)
				}
				if population.FloorMax == nil || *population.FloorMax != 0 {
					t.Errorf("floorMax = %v", population.FloorMax)
				}
			},
		},
		{
			name:    "a reversed size range is refused",
			mutate:  func(f *radarSearchFlags) { f.minSqm, f.maxSqm = 120, 80 },
			wantErr: "--min-sqm",
		},
		{
			name:    "a reversed price range is refused",
			mutate:  func(f *radarSearchFlags) { f.minPrice, f.maxPrice = 120000, 50000 },
			wantErr: "--min-price",
		},
		{
			name:    "an anchor missing its radius is refused",
			mutate:  func(f *radarSearchFlags) { f.latSet, f.lat = true, 42.69 },
			wantErr: "--lat, --lng and --radius",
		},
		{
			name:    "a radius without an anchor is refused",
			mutate:  func(f *radarSearchFlags) { f.radiusSet, f.radiusMeters = true, 1500 },
			wantErr: "--lat, --lng and --radius",
		},
		{
			name: "a complete anchor is carried",
			mutate: func(f *radarSearchFlags) {
				f.latSet, f.lngSet, f.radiusSet = true, true, true
				f.lat, f.lng, f.radiusMeters = 42.6901, 23.3226, 1500
			},
			check: func(t *testing.T, input radarclient.GeoSearchInput) {
				if input.Geo.Anchor == nil || input.Geo.Anchor.Lat != 42.6901 || input.Geo.Anchor.Lng != 23.3226 {
					t.Errorf("anchor = %#v", input.Geo.Anchor)
				}
				if input.Geo.RadiusMeters == nil || *input.Geo.RadiusMeters != 1500 {
					t.Errorf("radiusMeters = %v", input.Geo.RadiusMeters)
				}
			},
		},
		{
			name: "an out-of-range radius is refused",
			mutate: func(f *radarSearchFlags) {
				f.latSet, f.lngSet, f.radiusSet = true, true, true
				f.lat, f.lng, f.radiusMeters = 42.69, 23.32, 50001
			},
			wantErr: "--radius",
		},
		{
			name:    "an unknown missing-field policy is refused",
			mutate:  func(f *radarSearchFlags) { f.missingFieldPolicy = "maybe" },
			wantErr: "--missing-field-policy",
		},
		{
			name:    "an unknown group is refused",
			mutate:  func(f *radarSearchFlags) { f.groups = "supported,everything" },
			wantErr: "--groups",
		},
		{
			name:    "an empty group list is refused",
			mutate:  func(f *radarSearchFlags) { f.groups = "" },
			wantErr: "--groups",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flags := radarTestFlags()
			tc.mutate(&flags)
			input, err := flags.input()
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error containing %q", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("input: %v", err)
			}
			if tc.check != nil {
				tc.check(t, input)
			}
		})
	}
}

func TestRadarCommandGroupIsLocalAndRegistered(t *testing.T) {
	root := NewRootCommand()

	radar, _, err := root.Find([]string{"radar"})
	if err != nil {
		t.Fatalf("radar command: %v", err)
	}
	if radar.Flags().Lookup("json") != nil {
		t.Error("the group itself must not define the search flags")
	}

	search, _, err := root.Find([]string{"radar", "search"})
	if err != nil {
		t.Fatalf("radar search: %v", err)
	}
	for _, flag := range []string{
		"neighborhood", "neighborhood-slug", "type", "min-sqm", "max-sqm", "min-price",
		"max-price", "floor-max", "lat", "lng", "radius", "missing-field-policy", "groups", "all", "json",
	} {
		if search.Flags().Lookup(flag) == nil {
			t.Errorf("radar search is missing --%s", flag)
		}
	}
	if search.Flags().Lookup("pages") != nil || search.Flags().Lookup("city") != nil {
		t.Error("radar search must not inherit the legacy search flags")
	}

	location, _, err := root.Find([]string{"radar", "location"})
	if err != nil {
		t.Fatalf("radar location: %v", err)
	}
	if location.Flags().Lookup("json") == nil {
		t.Error("radar location needs --json")
	}

	legacy, _, err := root.Find([]string{"search"})
	if err != nil {
		t.Fatalf("legacy search: %v", err)
	}
	if legacy.Flags().Lookup("radius") != nil || legacy.Flags().Lookup("neighborhood-slug") != nil {
		t.Error("the radar flags must stay local to the radar group")
	}
}

func TestRadarHasMoreHonorsRequestedGroups(t *testing.T) {
	hasMore := radarclient.GeoGroupHasMore{Supported: false, Possible: false, Excluded: true}
	if radarHasMore(hasMore, []string{radarclient.GroupSupported, radarclient.GroupPossible}) {
		t.Error("an unrequested group must not extend pagination")
	}
	if !radarHasMore(hasMore, []string{radarclient.GroupExcluded}) {
		t.Error("a requested group with more rows must extend pagination")
	}
}

func TestWriteRadarLocationJSONShape(t *testing.T) {
	ambiguous := true
	detail := radarclient.GeoLocationDetail{
		ListingID:    "listing-1",
		AdvID:        "adv-1",
		Method:       radarTestStringPointer("geocoded"),
		Precision:    radarTestStringPointer("street"),
		Verification: radarTestStringPointer("derived"),
		DisplayPoint: &radarclient.GeoPoint{Lat: 42.68, Lng: 23.31},
		SourcePin:    &radarclient.GeoPoint{Lat: 42.681, Lng: 23.311},
		SupportKind:  radarTestStringPointer("bounded_evidence"),
		Evidence: []radarclient.GeoEvidence{{
			SourceField: "description",
			Quote:       "ул. Витоша 10",
			Relation:    "address",
			FeatureIDs:  []string{"f1"},
			Ambiguous:   &ambiguous,
		}},
		Warnings:    []string{"missing_geometry"},
		Outcome:     radarTestStringPointer("located"),
		State:       radarTestStringPointer("complete"),
		ProcessedAt: radarTestStringPointer("2026-09-14T13:00:00Z"),
	}

	var buffer bytes.Buffer
	if err := writeRadarLocationJSON(&buffer, detail); err != nil {
		t.Fatalf("writeRadarLocationJSON: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(buffer.Bytes(), &decoded); err != nil {
		t.Fatalf("decoding output: %v", err)
	}
	for _, key := range []string{
		"listingId", "advId", "method", "precision", "verification", "displayPoint",
		"sourcePin", "sourcePinObservedAt", "support", "supportKind", "evidence",
		"alternatives", "warnings", "outcome", "state", "processedAt",
	} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("location JSON is missing %q", key)
		}
	}
	if decoded["method"] != "geocoded" || decoded["precision"] != "street" {
		t.Errorf("method/precision = %v/%v", decoded["method"], decoded["precision"])
	}
	evidence, ok := decoded["evidence"].([]any)
	if !ok || len(evidence) != 1 {
		t.Fatalf("evidence = %#v", decoded["evidence"])
	}
	entry, ok := evidence[0].(map[string]any)
	if !ok || entry["quote"] != "ул. Витоша 10" || entry["sourceField"] != "description" {
		t.Errorf("evidence[0] = %#v", evidence[0])
	}
}

func TestPrintRadarLocationHuman(t *testing.T) {
	detail := radarclient.GeoLocationDetail{
		ListingID:    "listing-1",
		AdvID:        "adv-1",
		Method:       radarTestStringPointer("geocoded"),
		Precision:    radarTestStringPointer("street"),
		Verification: radarTestStringPointer("derived"),
		DisplayPoint: &radarclient.GeoPoint{Lat: 42.68, Lng: 23.31},
		SourcePin:    &radarclient.GeoPoint{Lat: 42.681, Lng: 23.311},
		SupportKind:  radarTestStringPointer("bounded_evidence"),
		Evidence: []radarclient.GeoEvidence{{
			SourceField: "description",
			Quote:       "ул. Витоша 10",
			Relation:    "address",
		}},
		Warnings:    []string{"missing_geometry"},
		ProcessedAt: radarTestStringPointer("2026-09-14T13:00:00Z"),
	}

	var buffer bytes.Buffer
	if err := printRadarLocationHuman(&buffer, detail); err != nil {
		t.Fatalf("printRadarLocationHuman: %v", err)
	}
	text := buffer.String()
	for _, want := range []string{
		"listing: listing-1", "adv: adv-1", "method: geocoded", "precision: street",
		"verification: derived", "display point: 42.680000, 23.310000",
		"source pin: 42.681000, 23.311000", `"ул. Витоша 10"`, "relation=address",
		"warnings: missing_geometry", "processed at: 2026-09-14T13:00:00Z",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("human location output does not mention %q:\n%s", want, text)
		}
	}
}

func TestPrintRadarSearchHuman(t *testing.T) {
	response := radarclient.GeoSearchResponse{
		ContractVersion: radarclient.ContractVersion,
		ObservedAt:      "2026-09-14T13:20:00Z",
		Totals:          radarclient.GeoGroupTotals{Supported: 1, Possible: 0, Excluded: 0},
		Groups: radarclient.GeoGroups{
			Supported: []radarclient.GeoListingRow{{
				URL:          "https://www.imot.bg/obiava-1",
				PriceEur:     radarTestFloatPointer(95000),
				AreaSqm:      radarTestFloatPointer(80),
				Floor:        radarTestStringPointer("4-ти от 6"),
				PropertyType: "2-СТАЕН",
				Location: radarclient.GeoListingLocation{
					Method:    radarTestStringPointer("source_pin"),
					Precision: radarTestStringPointer("building"),
					Warnings:  []string{"missing_geometry"},
				},
			}},
		},
		Coverage: radarclient.GeoCoverage{
			InventoryComplete: radarTestBoolPointer(true),
			LocationStates:    radarclient.GeoLocationStates{Complete: 1},
		},
	}

	var buffer bytes.Buffer
	if err := printRadarSearchHuman(&buffer, response); err != nil {
		t.Fatalf("printRadarSearchHuman: %v", err)
	}
	text := buffer.String()
	for _, want := range []string{
		"totals: supported 1 | possible 0 | excluded 0",
		"inventory complete: true",
		"source_pin/building",
		"https://www.imot.bg/obiava-1",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("human search output does not mention %q:\n%s", want, text)
		}
	}
}

func TestRunRadarSearchAllFollowsPages(t *testing.T) {
	var pages []int
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Page int `json:"page"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request: %v", err)
		}
		pages = append(pages, body.Page)
		_, _ = w.Write([]byte(radarSearchPageBody(body.Page, body.Page < 2)))
	}))
	defer fake.Close()

	client, err := radarclient.New(radarclient.Config{BaseURL: fake.URL, Token: "cli-test-token-0123456789"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	input := radarclient.GeoSearchInput{
		PageSize: radarclient.DefaultPageSize,
		Groups:   []string{radarclient.GroupSupported, radarclient.GroupPossible},
	}

	var buffer bytes.Buffer
	if err := runRadarSearchAll(context.Background(), client, input, true, &buffer); err != nil {
		t.Fatalf("runRadarSearchAll: %v", err)
	}
	var output radarAllOutput
	if err := json.Unmarshal(buffer.Bytes(), &output); err != nil {
		t.Fatalf("decoding --all output: %v", err)
	}
	if output.PagesFetched != 2 || len(output.Envelopes) != 2 {
		t.Fatalf("output = %#v", output)
	}
	if output.Truncated {
		t.Error("a completed --all run must not be marked truncated")
	}
	if !radarTestIntsEqual(pages, []int{1, 2}) {
		t.Errorf("requested pages = %#v", pages)
	}
}

func TestRunRadarSearchAllReportsInterruption(t *testing.T) {
	requests := 0
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			_, _ = w.Write([]byte(radarSearchPageBody(1, true)))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"contractVersion":"radar-geo-1","observedAt":"2026-09-14T13:20:00Z","error":{"code":"radar_unavailable","message":"the radar store could not be read"}}`))
	}))
	defer fake.Close()

	client, err := radarclient.New(radarclient.Config{BaseURL: fake.URL, Token: "cli-test-token-0123456789"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	input := radarclient.GeoSearchInput{
		PageSize: radarclient.DefaultPageSize,
		Groups:   []string{radarclient.GroupSupported},
	}

	var buffer bytes.Buffer
	err = runRadarSearchAll(context.Background(), client, input, true, &buffer)
	if err == nil {
		t.Fatal("an interrupted --all run must exit non-zero")
	}
	var output radarAllOutput
	if decodeErr := json.Unmarshal(buffer.Bytes(), &output); decodeErr != nil {
		t.Fatalf("decoding --all output: %v", decodeErr)
	}
	if output.PagesFetched != 1 || len(output.Envelopes) != 1 {
		t.Fatalf("partial output = %#v", output)
	}
	if !output.Truncated || output.TruncationReason != radarclient.CodeRadarUnavailable {
		t.Fatalf("truncation = %t/%q", output.Truncated, output.TruncationReason)
	}
}

func radarSearchPageBody(page int, hasMore bool) string {
	return fmt.Sprintf(`{
  "contractVersion": "radar-geo-1",
  "observedAt": "2026-09-14T13:20:00Z",
  "totals": {"supported": 2, "possible": 0, "excluded": 0},
  "groups": {"supported": [], "possible": [], "excluded": []},
  "coverage": {"inventoryComplete": true, "locationStates": {"pending": 0, "in_progress": 0, "retry_due": 0, "complete": 0, "blocked": 0, "unlocated": 0}, "unresolvedFloorCount": 0},
  "listingContractVersion": "radar-v1-read-1",
  "page": %d,
  "pageSize": 50,
  "hasMore": {"supported": %t, "possible": false, "excluded": false}
}`, page, hasMore)
}

func radarTestStringPointer(value string) *string { return &value }

func radarTestFloatPointer(value float64) *float64 { return &value }

func radarTestBoolPointer(value bool) *bool { return &value }

func radarTestStringsEqual(a, b []string) bool {
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

func radarTestIntsEqual(a, b []int) bool {
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
