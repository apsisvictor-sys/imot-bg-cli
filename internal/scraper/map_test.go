package scraper

import (
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// mapRoundTripFunc fakes one HTTP transport for the map tests. It is local to
// this file so the map tests do not depend on another test file's helpers.
type mapRoundTripFunc func(*http.Request) (*http.Response, error)

func (f mapRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// mapHTMLResponse returns one UTF-8 HTML response; the map body is valid UTF-8
// and DecodeHTMLBytes passes it through unchanged.
func mapHTMLResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func readMapFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return DecodeHTMLBytes(raw)
}

// quoteJoin renders values as the source's own single-quoted array elements.
func quoteJoin(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = "'" + v + "'"
	}
	return strings.Join(quoted, ",")
}

// mapBody builds the smallest page that carries the same initGoogleMapS call
// shape the source serves.
func mapBody(lats, lngs, ids []string) string {
	return `<html><body onload="javascript:initGoogleMapS(new Array(` + quoteJoin(lats) +
		`),new Array(` + quoteJoin(lngs) +
		`),new Array(` + quoteJoin(ids) +
		`),'sell',false);"></body></html>`
}

func mapErrorKind(t *testing.T, err error) string {
	t.Helper()
	var mapErr *MapPinsError
	if !errors.As(err, &mapErr) {
		t.Fatalf("expected a typed *MapPinsError, got %T: %v", err, err)
	}
	return mapErr.Kind
}

func hasMapWarning(warnings []string, code string) bool {
	return slices.Contains(warnings, code)
}

// TestParseMapPinsReal170Fixture parses the captured 170-pin replay fixture:
// three strict arrays of 170 quoted strings, 170 canonical ids, 138 distinct
// 6dp points, and one specific advert at its source coordinate.
func TestParseMapPinsReal170Fixture(t *testing.T) {
	body := readMapFixture(t, "map-search-real-170.html")

	parsed, err := parseMapPinsBody(body)
	if err != nil {
		t.Fatalf("parseMapPinsBody returned error: %v", err)
	}

	if len(parsed.ArrayLengths) != 3 {
		t.Fatalf("expected three arrays, got %v", parsed.ArrayLengths)
	}
	if want := []int{170, 170, 170}; !slices.Equal(parsed.ArrayLengths, want) {
		t.Fatalf("unexpected array lengths: got %v want %v", parsed.ArrayLengths, want)
	}
	if len(parsed.Pins) != 170 {
		t.Fatalf("expected 170 pins, got %d", len(parsed.Pins))
	}

	ids := make(map[string]bool, len(parsed.Pins))
	for _, p := range parsed.Pins {
		if ids[p.AdvID] {
			t.Fatalf("duplicate advert id in fixture: %s", p.AdvID)
		}
		ids[p.AdvID] = true
	}
	if len(ids) != 170 {
		t.Fatalf("expected 170 unique advert ids, got %d", len(ids))
	}
	if parsed.DistinctPoints != 138 {
		t.Fatalf("expected 138 distinct points, got %d", parsed.DistinctPoints)
	}

	// The replay fixture was captured without the page heading, so coverage is
	// unproven and must be unknown — never complete.
	if parsed.ReportedMappedCount != nil {
		t.Fatalf("fixture carries no heading; expected nil reported count, got %d", *parsed.ReportedMappedCount)
	}
	if parsed.Completeness != MapCompletenessUnknown {
		t.Fatalf("expected unknown completeness without a heading, got %q", parsed.Completeness)
	}
	if len(parsed.Warnings) != 0 {
		t.Fatalf("expected no warnings for the fixture, got %v", parsed.Warnings)
	}

	var found bool
	for _, p := range parsed.Pins {
		if p.AdvID != "1c178833344908368" {
			continue
		}
		found = true
		if math.Abs(p.Lat-42.6880035400391) > 1e-9 || math.Abs(p.Lng-23.330545425415) > 1e-9 {
			t.Fatalf("advert 1c178833344908368 has unexpected coordinates: lat=%v lng=%v", p.Lat, p.Lng)
		}
	}
	if !found {
		t.Fatal("advert 1c178833344908368 is missing from the fixture pins")
	}
}

// TestJoinMapPinsRejectsMisalignedArrays proves unequal arrays fail the payload
// instead of pairing some advert with another advert's coordinate.
func TestJoinMapPinsRejectsMisalignedArrays(t *testing.T) {
	body := mapBody(
		[]string{"42.7", "42.71"},
		[]string{"23.32"},
		[]string{"1c178833344908368"},
	)
	_, err := parseMapPinsBody(body)
	if got := mapErrorKind(t, err); got != MapPinsErrorMisalignedArrays {
		t.Fatalf("expected kind %q, got %q", MapPinsErrorMisalignedArrays, got)
	}

	var mapErr *MapPinsError
	_ = errors.As(err, &mapErr)
	if !strings.Contains(mapErr.Message, "2 latitudes") || !strings.Contains(mapErr.Message, "1 longitudes") {
		t.Fatalf("misalignment message should carry the observed lengths, got %q", mapErr.Message)
	}
}

// TestJoinMapPinsRejectsBadAdvertIDKeepsOthers drops only the invalid position
// and warns, so one malformed id cannot discard an otherwise usable batch.
func TestJoinMapPinsRejectsBadAdvertIDKeepsOthers(t *testing.T) {
	body := mapBody(
		[]string{"42.7", "42.71", "42.72"},
		[]string{"23.32", "23.33", "23.34"},
		[]string{"1c178833344908368", "not-an-advert-id", "1c178533723433796"},
	)
	parsed, err := parseMapPinsBody(body)
	if err != nil {
		t.Fatalf("parseMapPinsBody returned error: %v", err)
	}
	if len(parsed.Pins) != 2 {
		t.Fatalf("expected the two valid pins to survive, got %d", len(parsed.Pins))
	}
	for _, p := range parsed.Pins {
		if p.AdvID == "not-an-advert-id" {
			t.Fatal("the invalid advert id was kept")
		}
	}
	if !hasMapWarning(parsed.Warnings, MapWarningBadAdvertID) {
		t.Fatalf("expected warning %q, got %v", MapWarningBadAdvertID, parsed.Warnings)
	}
	if len(parsed.Warnings) != 1 {
		t.Fatalf("expected exactly one unique warning code, got %v", parsed.Warnings)
	}
	// The raw array lengths stay visible even though one position was refused.
	if want := []int{3, 3, 3}; !slices.Equal(parsed.ArrayLengths, want) {
		t.Fatalf("unexpected array lengths: got %v want %v", parsed.ArrayLengths, want)
	}
	if parsed.Completeness != MapCompletenessUnknown {
		t.Fatalf("a batch with a refused record cannot be complete, got %q", parsed.Completeness)
	}
}

func TestJoinMapPinsAcceptsAdvertKindVariants(t *testing.T) {
	parsed, err := parseMapPinsBody(mapBody(
		[]string{"42.7", "42.71", "42.72", "42.73"},
		[]string{"23.32", "23.33", "23.34", "23.35"},
		[]string{"1b178833344908368", "1c178533723433796", "1d178181550164279", "1g178903594660305"},
	))
	if err != nil {
		t.Fatalf("parseMapPinsBody returned error: %v", err)
	}
	if len(parsed.Pins) != 4 {
		t.Fatalf("expected all advert-kind variants to survive, got %d", len(parsed.Pins))
	}
	if len(parsed.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", parsed.Warnings)
	}
}

// TestJoinMapPinsRejectsDuplicateAdvertID keeps the first point for an advert
// that appears twice, so one advert never claims two coordinates.
func TestJoinMapPinsRejectsDuplicateAdvertID(t *testing.T) {
	parsed, err := parseMapPinsBody(mapBody(
		[]string{"42.7", "42.9"},
		[]string{"23.32", "23.39"},
		[]string{"1c178833344908368", "1c178833344908368"},
	))
	if err != nil {
		t.Fatalf("parseMapPinsBody returned error: %v", err)
	}
	if len(parsed.Pins) != 1 || parsed.Pins[0].Lat != 42.7 || parsed.Pins[0].Lng != 23.32 {
		t.Fatalf("expected the first occurrence to survive, got %#v", parsed.Pins)
	}
	if !hasMapWarning(parsed.Warnings, MapWarningDuplicateAdvertID) {
		t.Fatalf("expected warning %q, got %v", MapWarningDuplicateAdvertID, parsed.Warnings)
	}
}

// TestJoinMapPinsRejectsOutOfBoundsCoordinatesKeepsOthers rejects
// out-of-country and swapped pairs per record, warning while keeping the rest.
func TestJoinMapPinsRejectsOutOfBoundsCoordinatesKeepsOthers(t *testing.T) {
	parsed, err := parseMapPinsBody(mapBody(
		[]string{"42.7", "0.0", "23.3219"},
		[]string{"23.32", "23.33", "42.6977"},
		[]string{"1c178833344908368", "1c178533723433796", "1c178046727816545"},
	))
	if err != nil {
		t.Fatalf("parseMapPinsBody returned error: %v", err)
	}
	if len(parsed.Pins) != 1 {
		t.Fatalf("expected one in-bounds pin, got %d: %#v", len(parsed.Pins), parsed.Pins)
	}
	if parsed.Pins[0].AdvID != "1c178833344908368" {
		t.Fatalf("the wrong pin survived: %#v", parsed.Pins[0])
	}
	if !hasMapWarning(parsed.Warnings, MapWarningCoordinateOutOfBounds) {
		t.Fatalf("expected warning %q, got %v", MapWarningCoordinateOutOfBounds, parsed.Warnings)
	}
	if len(parsed.Warnings) != 1 {
		t.Fatalf("expected one unique warning code, got %v", parsed.Warnings)
	}
}

// TestJoinMapPinsRejectsUnparseableCoordinates warns per record instead of
// letting strconv's zero value become a real coordinate.
func TestJoinMapPinsRejectsUnparseableCoordinates(t *testing.T) {
	parsed, err := parseMapPinsBody(mapBody(
		[]string{"not-a-number", "42.7"},
		[]string{"23.32", "23.32"},
		[]string{"1c178833344908368", "1c178533723433796"},
	))
	if err != nil {
		t.Fatalf("parseMapPinsBody returned error: %v", err)
	}
	if len(parsed.Pins) != 1 {
		t.Fatalf("expected one valid pin, got %d", len(parsed.Pins))
	}
	if !hasMapWarning(parsed.Warnings, MapWarningCoordinateUnparseable) {
		t.Fatalf("expected warning %q, got %v", MapWarningCoordinateUnparseable, parsed.Warnings)
	}
}

// TestMapPinsChallengeAndUnreadablePages separates a bot interstitial from a
// page that simply is not the map body, and never returns zero pins for either.
func TestMapPinsChallengeAndUnreadablePages(t *testing.T) {
	t.Run("challenge fixture", func(t *testing.T) {
		body := readMapFixture(t, "map-search-challenge.html")
		_, err := parseMapPinsBody(body)
		if got := mapErrorKind(t, err); got != MapPinsErrorChallengePage {
			t.Fatalf("expected kind %q, got %q", MapPinsErrorChallengePage, got)
		}
	})

	t.Run("empty onload", func(t *testing.T) {
		_, err := parseMapPinsBody(`<html><body onload=""></body></html>`)
		if got := mapErrorKind(t, err); got != MapPinsErrorUnreadablePage {
			t.Fatalf("expected kind %q, got %q", MapPinsErrorUnreadablePage, got)
		}
	})

	t.Run("onload without initGoogleMapS", func(t *testing.T) {
		_, err := parseMapPinsBody(`<html><body onload="javascript:doSomethingElse();"></body></html>`)
		if got := mapErrorKind(t, err); got != MapPinsErrorUnreadablePage {
			t.Fatalf("expected kind %q, got %q", MapPinsErrorUnreadablePage, got)
		}
	})

	t.Run("two arrays", func(t *testing.T) {
		_, err := parseMapPinsBody(`<html><body onload="javascript:initGoogleMapS(new Array('42.7'),new Array('23.32'));"></body></html>`)
		if got := mapErrorKind(t, err); got != MapPinsErrorUnreadablePage {
			t.Fatalf("expected kind %q, got %q", MapPinsErrorUnreadablePage, got)
		}
	})

	t.Run("unquoted array element", func(t *testing.T) {
		_, err := parseMapPinsBody(`<html><body onload="javascript:initGoogleMapS(new Array(42.7),new Array('23.32'),new Array('1c178833344908368'));"></body></html>`)
		if got := mapErrorKind(t, err); got != MapPinsErrorUnreadablePage {
			t.Fatalf("expected kind %q, got %q", MapPinsErrorUnreadablePage, got)
		}
	})
}

// TestMapPinsTrueEmptyMap proves a genuinely empty map is a result, not a
// parse failure: the source's own zero heading makes it complete.
func TestMapPinsTrueEmptyMap(t *testing.T) {
	body := `<html><body onload="javascript:initGoogleMapS(new Array(),new Array(),new Array(),'sell',false);">` +
		`<h1>0 имота с точно местоположение</h1></body></html>`

	parsed, err := parseMapPinsBody(body)
	if err != nil {
		t.Fatalf("parseMapPinsBody returned error: %v", err)
	}
	if len(parsed.Pins) != 0 || parsed.DistinctPoints != 0 {
		t.Fatalf("expected zero pins and points, got %d pins / %d points", len(parsed.Pins), parsed.DistinctPoints)
	}
	if want := []int{0, 0, 0}; !slices.Equal(parsed.ArrayLengths, want) {
		t.Fatalf("unexpected array lengths: got %v want %v", parsed.ArrayLengths, want)
	}
	if parsed.ReportedMappedCount == nil || *parsed.ReportedMappedCount != 0 {
		t.Fatalf("expected reported mapped count 0, got %#v", parsed.ReportedMappedCount)
	}
	if parsed.Completeness != MapCompletenessComplete {
		t.Fatalf("a zero heading matching zero pins is complete, got %q", parsed.Completeness)
	}
}

// TestParseReportedMappedCount reads the source's own heading, including the
// non-breaking space the page may use, and leaves unproven counts as nil.
func TestParseReportedMappedCount(t *testing.T) {
	cases := []struct {
		name string
		body string
		want *int
	}{
		{name: "heading present", body: "<h2>170 имота с точно местоположение</h2>", want: intPtr(170)},
		{name: "non-breaking space", body: "<h2>42\u00a0имота с точно местоположение</h2>", want: intPtr(42)},
		{name: "heading absent", body: "<h2>170 обяви в Център</h2>", want: nil},
		{name: "other wording", body: "<h2>170 имота на картата</h2>", want: nil},
		{name: "no number", body: "<h2>имота с точно местоположение</h2>", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseReportedMappedCount(tc.body)
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("expected nil, got %d", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("expected %d, got nil", *tc.want)
			case tc.want != nil && got != nil && *got != *tc.want:
				t.Fatalf("expected %d, got %d", *tc.want, *got)
			}
		})
	}
}

// TestMapPinsCompleteness proves the heading count is the only thing that can
// promote a batch to complete, and a mismatch stays unknown with a warning.
func TestMapPinsCompleteness(t *testing.T) {
	body := mapBody(
		[]string{"42.7", "42.71"},
		[]string{"23.32", "23.33"},
		[]string{"1c178833344908368", "1c178533723433796"},
	)

	complete := strings.Replace(body, "</body>", "<h2>2 имота с точно местоположение</h2></body>", 1)
	parsed, err := parseMapPinsBody(complete)
	if err != nil {
		t.Fatalf("parseMapPinsBody returned error: %v", err)
	}
	if parsed.Completeness != MapCompletenessComplete {
		t.Fatalf("expected complete when the heading matches the pins, got %q", parsed.Completeness)
	}

	mismatch := strings.Replace(body, "</body>", "<h2>170 имота с точно местоположение</h2></body>", 1)
	parsed, err = parseMapPinsBody(mismatch)
	if err != nil {
		t.Fatalf("parseMapPinsBody returned error: %v", err)
	}
	if parsed.Completeness != MapCompletenessUnknown {
		t.Fatalf("a count mismatch must stay unknown, got %q", parsed.Completeness)
	}
	if !hasMapWarning(parsed.Warnings, MapWarningSourceCountMismatch) {
		t.Fatalf("expected warning %q, got %v", MapWarningSourceCountMismatch, parsed.Warnings)
	}
}

// TestBuildMapPinsURLRubric proves the map path mirrors the listings path for
// both rubrics, the resolved slugs, and both accepted type vocabularies.
func TestBuildMapPinsURLRubric(t *testing.T) {
	cases := []struct {
		name             string
		params           SearchParams
		neighborhoodSlug string
		want             string
		wantErr          bool
	}{
		{
			name:             "sales with neighborhood and type label",
			params:           SearchParams{City: "София", Type: "3-стаен"},
			neighborhoodSlug: "tsentar",
			want:             "https://www.imot.bg/obiavimap/prodazhbi/grad-sofiya/tsentar/tristaen",
		},
		{
			name:             "rents",
			params:           SearchParams{City: "София", Rent: true},
			neighborhoodSlug: "tsentar",
			want:             "https://www.imot.bg/obiavimap/naemi/grad-sofiya/tsentar",
		},
		{
			name:   "sales city-wide without neighborhood",
			params: SearchParams{City: "София"},
			want:   "https://www.imot.bg/obiavimap/prodazhbi/grad-sofiya",
		},
		{
			name:   "source slug accepted as type",
			params: SearchParams{City: "София", Type: "ofis"},
			want:   "https://www.imot.bg/obiavimap/prodazhbi/grad-sofiya/ofis",
		},
		{
			name:    "unknown type is refused",
			params:  SearchParams{City: "София", Type: "замък"},
			wantErr: true,
		},
		{
			name:    "unknown city is refused",
			params:  SearchParams{City: "Мордор"},
			wantErr: true,
		},
		{
			name:    "missing city is refused",
			params:  SearchParams{},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildMapPinsURL(tc.params, tc.neighborhoodSlug)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got URL %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("unexpected URL: got %q want %q", got, tc.want)
			}
		})
	}
}

// TestParseMapFormPreservesPageFields reads the form's own hidden fields,
// including the requester/IP field, and refuses a page without a usable form.
func TestParseMapFormPreservesPageFields(t *testing.T) {
	page := `<html><body>
<form name="mapgfixparams" action="/pcgi/mapgfix.cgi" method="post" target="mapgfix">
<input type="hidden" name="f0" value="203.0.113.7">
<input type="hidden" name="rub" value="1">
<input type="text" name="ignored" value="not-submitted">
<input type="hidden" name="f40" value="Център+">
</form>
</body></html>`

	form, err := parseMapForm(page, "https://www.imot.bg/obiavimap/prodazhbi/grad-sofiya/tsentar/tristaen")
	if err != nil {
		t.Fatalf("parseMapForm returned error: %v", err)
	}
	if form.Action != "https://www.imot.bg/pcgi/mapgfix.cgi" {
		t.Fatalf("unexpected resolved action: %q", form.Action)
	}
	if got := form.Fields.Get("f0"); got != "203.0.113.7" {
		t.Fatalf("requester field must come from the fetched page, got %q", got)
	}
	if got := form.Fields.Get("f40"); got != "Център+" {
		t.Fatalf("unexpected f40 value: %q", got)
	}
	if form.Fields.Has("ignored") {
		t.Fatal("a non-hidden input must not be submitted")
	}

	missingForm := `<html><body><form name="other" action="/pcgi/mapgfix.cgi"></form></body></html>`
	if _, err := parseMapForm(missingForm, "https://www.imot.bg/obiavimap/prodazhbi/grad-sofiya"); err == nil {
		t.Fatal("expected an error for a page without the mapgfixparams form")
	}

	noAction := `<html><body><form name="mapgfixparams"><input type="hidden" name="f0" value="1"></form></body></html>`
	if _, err := parseMapForm(noAction, "https://www.imot.bg/obiavimap/prodazhbi/grad-sofiya"); err == nil {
		t.Fatal("expected an error for a form without an action URL")
	}

	noHidden := `<html><body><form name="mapgfixparams" action="/pcgi/mapgfix.cgi"><input type="submit" name="go" value="1"></form></body></html>`
	if _, err := parseMapForm(noHidden, "https://www.imot.bg/obiavimap/prodazhbi/grad-sofiya"); err == nil {
		t.Fatal("expected an error for a form with no hidden fields")
	}
}

// TestFetchMapPinsSubmitsFetchedFormAndParsesBody exercises the live path
// offline: the GET page's form is submitted to its own action URL with the
// page's own field values, and the POST response body becomes the parsed
// payload.
func TestFetchMapPinsSubmitsFetchedFormAndParsesBody(t *testing.T) {
	page := `<html><body>
<form name="mapgfixparams" action="/pcgi/mapgfix.cgi" method="post" target="mapgfix">
<input type="hidden" name="f0" value="203.0.113.7">
<input type="hidden" name="rub" value="1">
</form>
</body></html>`
	body := mapBody(
		[]string{"42.6880035400391"},
		[]string{"23.330545425415"},
		[]string{"1c178833344908368"},
	)

	var postedBody url.Values
	client := &Client{httpClient: &http.Client{Transport: mapRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.Method {
		case http.MethodGet:
			if req.URL.String() != "https://www.imot.bg/obiavimap/prodazhbi/grad-sofiya/tristaen" {
				t.Errorf("unexpected map page URL: %s", req.URL.String())
			}
			return mapHTMLResponse(http.StatusOK, page), nil
		case http.MethodPost:
			if req.URL.String() != "https://www.imot.bg/pcgi/mapgfix.cgi" {
				t.Errorf("unexpected form action: %s", req.URL.String())
			}
			raw, err := io.ReadAll(req.Body)
			if err != nil {
				t.Errorf("reading posted form: %v", err)
			}
			postedBody, err = url.ParseQuery(string(raw))
			if err != nil {
				t.Errorf("parsing posted form: %v", err)
			}
			return mapHTMLResponse(http.StatusOK, body), nil
		}
		return mapHTMLResponse(http.StatusBadRequest, ""), nil
	})}}

	result, err := client.FetchMapPins(SearchParams{City: "София", Type: "3-стаен"})
	if err != nil {
		t.Fatalf("FetchMapPins returned error: %v", err)
	}

	if got := postedBody.Get("f0"); got != "203.0.113.7" {
		t.Fatalf("the page's own requester field was not submitted: %q", got)
	}
	if got := postedBody.Get("rub"); got != "1" {
		t.Fatalf("unexpected posted rub value: %q", got)
	}
	if result.ContractVersion != MapPinsContractVersion {
		t.Fatalf("unexpected contract version: %q", result.ContractVersion)
	}
	if result.Requested.City != "София" || result.Requested.Type != "3-стаен" || result.Requested.Rent {
		t.Fatalf("unexpected requested scope: %#v", result.Requested)
	}
	if len(result.Pins) != 1 || result.Pins[0].AdvID != "1c178833344908368" {
		t.Fatalf("unexpected pins: %#v", result.Pins)
	}
	if result.DistinctPoints != 1 {
		t.Fatalf("expected one distinct point, got %d", result.DistinctPoints)
	}
	if result.ReportedMappedCount != nil {
		t.Fatalf("expected nil reported count without a heading, got %d", *result.ReportedMappedCount)
	}
	if result.Completeness != MapCompletenessUnknown {
		t.Fatalf("expected unknown completeness, got %q", result.Completeness)
	}
}

func intPtr(v int) *int { return &v }
