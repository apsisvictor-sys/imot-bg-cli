package scraper

// Map-pin acquisition for one source map scope.
//
// imot.bg renders a scope's pins in a same-origin iframe named mapgfix. The
// parent map page carries a search form named mapgfixparams whose hidden inputs
// are the scope's own parameters; submitting that form to its own action URL
// (documented and observed as /pcgi/mapgfix.cgi) returns the iframe body, whose
// <body onload> calls
//
//	initGoogleMapS(new Array(<lat strings>), new Array(<lng strings>),
//	               new Array(<advert-id strings>), 'sell', false)
//
// The three arrays are positionally aligned: index i of each array describes
// the same advert. This file therefore treats alignment as the primary
// invariant. Unequal arrays fail the payload instead of shifting one advert's
// coordinate onto another; a single bad record is dropped with a bounded
// warning code, and the raw array lengths stay in the payload so a consumer can
// see exactly what the page carried before validation.
//
// The form's hidden inputs are submitted exactly as the page carries them,
// including its own requester/IP field (f0). That value is never fabricated or
// replayed from a fixture: it is read from the fetched page on every run.

import (
	"errors"
	"fmt"
	"html"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/encoding/charmap"
)

// MapBaseURL is the source root of the map pages. Their path mirrors the
// listings path: <rubric>/<city-slug>[/<neighborhood-slug>][/<type-slug>].
const MapBaseURL = "https://www.imot.bg/obiavimap"

// MapPinsContractVersion is the version tag on the map-pin producer payload. It
// changes only when the meaning of the payload changes, so a consumer can
// refuse a shape it does not understand instead of guessing.
const MapPinsContractVersion = "imot-map-pins-v1"

// Completeness of one map dataset. Zero parsed pins never means complete.
const (
	// MapCompletenessComplete: the source reported a mapped count and the
	// validated pins carry exactly that many adverts.
	MapCompletenessComplete = "complete"
	// MapCompletenessPartial: the source itself marked the batch as truncated.
	// No such marker has been verified on the map body, so this producer never
	// publishes it today; the value exists so a proven marker is not forced into
	// "complete" or "unknown" when one is found.
	MapCompletenessPartial = "partial"
	// MapCompletenessUnknown: no verified claim covers the whole scope (no
	// heading, a count that disagrees with the parsed pins, or rejected records).
	MapCompletenessUnknown = "unknown"
)

// Map page failure kinds carried in MapPinsError.Kind. A caller branches on the
// kind instead of the error text, the same way detail and search failures are
// read.
const (
	// MapPinsErrorFetchFailed: the map request failed (non-200 or network
	// error), including the form POST.
	MapPinsErrorFetchFailed = "fetch_failed"
	// MapPinsErrorChallengePage: the response is a bot/captcha interstitial, not
	// a map page. It must never be read as a scope with zero pins.
	MapPinsErrorChallengePage = "challenge_page"
	// MapPinsErrorUnreadablePage: HTTP 200 carried no usable map form or no
	// initGoogleMapS call with three strict arrays (changed layout, block page).
	MapPinsErrorUnreadablePage = "unreadable_page"
	// MapPinsErrorMisalignedArrays: the map arrays have different lengths, so no
	// advert can be paired with a coordinate without guessing.
	MapPinsErrorMisalignedArrays = "misaligned_arrays"
)

// Bounded warning codes carried in MapPinsResult.Warnings. They are identifiers,
// not prose: a machine-read field never carries a sentence.
const (
	// MapWarningBadAdvertID: one array position held a string that is not a
	// canonical advert id; that position was dropped.
	MapWarningBadAdvertID = "bad_advert_id"
	// MapWarningCoordinateUnparseable: one array position held a coordinate that
	// is not a float64; that position was dropped.
	MapWarningCoordinateUnparseable = "coordinate_unparseable"
	// MapWarningCoordinateOutOfBounds: one coordinate fell outside Bulgaria's
	// bounding box (lat 41.0-44.0, lng 21.0-27.0); that position was dropped.
	MapWarningCoordinateOutOfBounds = "coordinate_out_of_bounds"
	// MapWarningDuplicateAdvertID: the same advert id appeared more than once;
	// the later position was dropped so one advert never claims two points.
	MapWarningDuplicateAdvertID = "duplicate_advert_id"
	// MapWarningSourceCountMismatch: the source's own heading count disagrees
	// with the number of validated pins.
	MapWarningSourceCountMismatch = "source_count_mismatch"
)

// Coordinate bounds for one source pin. imot.bg lists Bulgarian property, so a
// point outside Bulgaria's national bounding box cannot be a coordinate this
// source produced for a listing; it is dropped per record rather than plotted.
const (
	mapPinLatMin = 41.0
	mapPinLatMax = 44.0
	mapPinLngMin = 21.0
	mapPinLngMax = 27.0
)

// MapPin is one source-asserted advert coordinate: the advert id the source
// printed plus its latitude/longitude. Coordinates are WGS84 as served.
type MapPin struct {
	AdvID string  `json:"advId"`
	Lat   float64 `json:"lat"`
	Lng   float64 `json:"lng"`
}

// MapPinsRequest identifies the scope a map dataset was requested for. The
// source applies these dimensions server-side; they are not client filters.
type MapPinsRequest struct {
	City         string `json:"city"`
	Neighborhood string `json:"neighborhood"`
	Type         string `json:"type"`
	Rent         bool   `json:"rent"`
}

// MapPinsResult is one map scope's validated pin payload.
//
// ArrayLengths reports the lengths of the three positional arrays as the page
// carried them, before per-record validation; Pins is the validated join. The
// two differ when a record was rejected, and that difference together with
// Warnings is how a consumer sees that the source page held something this
// parser refused.
type MapPinsResult struct {
	ContractVersion     string         `json:"contract_version"`
	Requested           MapPinsRequest `json:"requested"`
	ObservedAt          string         `json:"observed_at"`
	Pins                []MapPin       `json:"pins"`
	ArrayLengths        []int          `json:"array_lengths"`
	DistinctPoints      int            `json:"distinct_points"`
	ReportedMappedCount *int           `json:"reported_mapped_count"`
	Completeness        string         `json:"completeness"`
	Warnings            []string       `json:"warnings"`
}

// MapPinsError is the typed failure returned when a map scope cannot be
// acquired or parsed. Its JSON shape follows the detail command's typed error
// metadata (kind, requested URL, HTTP evidence, message), so a caller can tell
// a challenge from a non-200 without parsing error strings.
type MapPinsError struct {
	Kind              string `json:"kind"`
	RequestedURL      string `json:"requested_url,omitempty"`
	EffectiveURL      string `json:"effective_url,omitempty"`
	HTTPStatus        int    `json:"http_status,omitempty"`
	RetryAfterSeconds *int   `json:"retry_after_seconds,omitempty"`
	Message           string `json:"error"`
}

// Error keeps the kind in the text so logs and wrapped messages stay legible.
func (e *MapPinsError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", e.Kind, e.Message)
}

// mapForm is the source's own map search form as fetched: its action URL plus
// the hidden fields, in page order, exactly as carried.
type mapForm struct {
	Action string
	Fields url.Values
}

var (
	// reMapChallengePage is the map path's own copy of the block/challenge
	// vocabulary. It is deliberately local: this file must not depend on
	// another lane's parser state.
	reMapChallengePage = regexp.MustCompile(`(?i)(cf-chl|__cf_chl|challenge-platform|cf_chl_opt|cf_chl_tk|just a moment|checking your browser|enable javascript and cookies|attention required|g-recaptcha|hcaptcha|recaptcha/api|достъпът е ограничен)`)

	// reMapFormBlock captures the opening tag and body of
	// form[name=mapgfixparams]. The name attribute may be quoted or bare.
	reMapFormBlock = regexp.MustCompile(`(?is)(<form\b[^>]*(?:^|\s)name\s*=\s*["']?mapgfixparams["']?[^>]*>)(.*?)</form\s*>`)
	// reMapFormAction reads the action attribute from that opening tag.
	reMapFormAction = regexp.MustCompile(`(?is)(?:^|\s)action\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	// reMapHiddenInput matches one hidden input inside the form body.
	reMapHiddenInput = regexp.MustCompile(`(?is)<input\b[^>]*(?:^|\s)type\s*=\s*["']?hidden["']?[^>]*>`)
	// reMapAttrName / reMapAttrValue read one attribute's raw value.
	reMapAttrName  = regexp.MustCompile(`(?is)(?:^|\s)name\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	reMapAttrValue = regexp.MustCompile(`(?is)(?:^|\s)value\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)

	// reMapBodyOnload captures the body's onload attribute value.
	reMapBodyOnload = regexp.MustCompile(`(?is)<body\b[^>]*(?:^|\s)onload\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	// reInitGoogleMapS captures the argument list of the init call. The map
	// body's only parenthesis-bearing values are the new Array(...) groups, so
	// the capture runs from the call's open parenthesis to the last close.
	reInitGoogleMapS = regexp.MustCompile(`(?is)initGoogleMapS\s*\((.*)\)`)
	// reNewArray captures one new Array(...) group's contents.
	reNewArray = regexp.MustCompile(`(?is)new\s+Array\s*\(([^()]*)\)`)
	// reQuotedStringElement accepts exactly one quoted string and nothing else,
	// so unquoted junk in a coordinate array is a parse failure, not a value.
	reQuotedStringElement = regexp.MustCompile(`^\s*(?:'([^']*)'|"([^"]*)")\s*$`)

	// reMapAdvertID is the canonical advert-id shape. The second character is
	// an imot.bg advert-kind variant (e.g. 1b/1c/1d/1g/1l), not a property type.
	reMapAdvertID = regexp.MustCompile(`^1[a-z][0-9]{15}$`)
	// reReportedMappedCount reads the source's own mapped-advert heading.
	// \s is ASCII-only in RE2, so the non-breaking space the page may use is
	// included explicitly; no other character is accepted in its place.
	reReportedMappedCount = regexp.MustCompile("(\\d+)[\\s\u00a0]+имота с точно местоположение")
)

// mapFirstNonEmpty returns the first non-empty value, used to read the quoted
// or bare alternative of a regexp attribute capture.
func mapFirstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// buildMapPinsURL constructs the map scope URL. The neighborhood slug is
// resolved by the same evidence path listings use; the type name is resolved
// through the shared type map, and a known slug is also accepted so a caller
// holding the source's own vocabulary is not forced back through a label.
func buildMapPinsURL(params SearchParams, neighborhoodSlug string) (string, error) {
	if strings.TrimSpace(params.City) == "" {
		return "", fmt.Errorf("city is required; a map scope names one city")
	}
	citySlug := resolveCitySlug(params.City)
	if citySlug == "" {
		return "", fmt.Errorf("unknown city %q: no imot.bg city slug is known for it", params.City)
	}

	rubric := "prodazhbi"
	if params.Rent {
		rubric = "naemi"
	}

	parts := []string{MapBaseURL, rubric, citySlug}
	if neighborhoodSlug != "" {
		parts = append(parts, neighborhoodSlug)
	}
	if params.Type != "" {
		typeSlug := resolveMapTypeSlug(params.Type)
		if typeSlug == "" {
			return "", fmt.Errorf("unknown property type %q: no imot.bg map slug is known for it", params.Type)
		}
		parts = append(parts, typeSlug)
	}
	return strings.Join(parts, "/"), nil
}

// resolveMapTypeSlug accepts either a property-type label known to TypeMap or a
// slug TypeMap itself maps to. Anything else is unknown, so the scope is never
// silently widened by dropping the type segment.
func resolveMapTypeSlug(propType string) string {
	if slug := resolveTypeSlug(propType); slug != "" {
		return slug
	}
	for _, slug := range TypeMap {
		if slug == propType {
			return slug
		}
	}
	return ""
}

// FetchMapPins fetches and validates one map scope's native source pins.
//
// It resolves the neighborhood slug the same way listings do, GETs the map
// page, submits that page's own mapgfixparams form (hidden fields preserved,
// including the page's f0 requester value), and parses the iframe body. Map
// acquisition is scope-level and spaced like search: one GET plus one form POST
// separated by the existing search-page delay.
func (c *Client) FetchMapPins(params SearchParams) (MapPinsResult, error) {
	result := MapPinsResult{
		ContractVersion: MapPinsContractVersion,
		Requested: MapPinsRequest{
			City:         params.City,
			Neighborhood: params.Neighborhood,
			Type:         params.Type,
			Rent:         params.Rent,
		},
		ObservedAt:   FormatTimestamp(time.Now().UTC()),
		Pins:         []MapPin{},
		ArrayLengths: []int{},
		Warnings:     []string{},
		Completeness: MapCompletenessUnknown,
	}

	neighborhoodSlug := ""
	if params.Neighborhood != "" {
		neighborhoodSlug = c.resolveNeighborhoodSlug(params)
	}

	pageURL, err := buildMapPinsURL(params, neighborhoodSlug)
	if err != nil {
		return result, err
	}

	pageHTML, effectivePageURL, err := c.fetchPageMeta(pageURL)
	if err != nil {
		return result, newMapPinsFetchError(pageURL, effectivePageURL, err)
	}

	form, err := parseMapForm(pageHTML, effectivePageURL)
	if err != nil {
		// A page whose form is missing is either a bot interstitial or a page
		// that is not a usable map scope; only the challenge vocabulary tells
		// them apart. The form check runs first, so a genuine map page that
		// merely mentions a captcha widget is never rejected for it.
		if reMapChallengePage.MatchString(pageHTML) {
			return result, &MapPinsError{
				Kind:         MapPinsErrorChallengePage,
				RequestedURL: pageURL,
				EffectiveURL: effectivePageURL,
				Message:      "map search page is a bot challenge, not a map scope page",
			}
		}
		return result, &MapPinsError{
			Kind:         MapPinsErrorUnreadablePage,
			RequestedURL: pageURL,
			EffectiveURL: effectivePageURL,
			Message:      err.Error(),
		}
	}

	jitteredSleep(SearchPageDelay)
	body, err := c.postMapForm(form, effectivePageURL)
	if err != nil {
		return result, newMapPinsFetchError(form.Action, "", err)
	}

	parsed, err := parseMapPinsBody(body)
	if err != nil {
		return result, withMapPinsURLs(err, form.Action, effectivePageURL)
	}

	result.Pins = parsed.Pins
	result.ArrayLengths = parsed.ArrayLengths
	result.DistinctPoints = parsed.DistinctPoints
	result.ReportedMappedCount = parsed.ReportedMappedCount
	result.Completeness = parsed.Completeness
	result.Warnings = parsed.Warnings

	return result, nil
}

// mapPinsParse is the pure result of parsing one map response body.
type mapPinsParse struct {
	Pins                []MapPin
	ArrayLengths        []int
	DistinctPoints      int
	ReportedMappedCount *int
	Completeness        string
	Warnings            []string
}

// parseMapPinsBody parses one fetched map body: it extracts the three aligned
// arrays, joins them into pins, counts distinct points and reads the source's
// own mapped count. It performs no I/O, so the fixture tests exercise exactly
// the path a live fetch uses.
func parseMapPinsBody(body string) (mapPinsParse, error) {
	arrays, err := extractMapPinsArrays(body)
	if err != nil {
		return mapPinsParse{}, err
	}

	warnings := []string{}
	pins, err := joinMapPins(arrays, &warnings)
	if err != nil {
		return mapPinsParse{}, err
	}

	reported := parseReportedMappedCount(body)
	if reported != nil && *reported != len(pins) {
		addMapWarning(&warnings, MapWarningSourceCountMismatch)
	}

	return mapPinsParse{
		Pins:                pins,
		ArrayLengths:        []int{len(arrays.Latitudes), len(arrays.Longitudes), len(arrays.AdvIDs)},
		DistinctPoints:      countDistinctPoints(pins),
		ReportedMappedCount: reported,
		Completeness:        mapPinsCompleteness(reported, len(pins)),
		Warnings:            warnings,
	}, nil
}

// postMapForm submits the fetched form to its own action URL through the
// client that owns the session/proxy conventions, and returns the decoded
// response body.
//
// The source page is windows-1251, and the browser submits the form with the
// page's own charset: non-ASCII field values (град София, Център+) travel as
// cp1251 bytes. URL.Values.Encode() percent-encodes the UTF-8 bytes, which the
// server decodes as mojibake and answers with an EMPTY map (three zero-length
// arrays, no error) — verified live on 2026-09-15. The values are therefore
// encoded to windows-1251 before the standard URL encoding.
func (c *Client) postMapForm(form mapForm, referer string) (string, error) {
	req, err := http.NewRequest("POST", form.Action, strings.NewReader(encodeWin1251Form(form.Fields)))
	if err != nil {
		return "", fmt.Errorf("creating map form request: %w", err)
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "bg-BG,bg;q=0.9,en;q=0.8")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("posting map form: %w", err)
	}
	defer resp.Body.Close()

	effectiveURL := form.Action
	if resp.Request != nil && resp.Request.URL != nil && resp.Request.URL.String() != "" {
		effectiveURL = resp.Request.URL.String()
	}
	if resp.StatusCode != http.StatusOK {
		return "", &HTTPError{
			StatusCode:        resp.StatusCode,
			URL:               form.Action,
			EffectiveURL:      effectiveURL,
			RetryAfterSeconds: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
		}
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading map response: %w", err)
	}
	return DecodeHTMLBytes(raw), nil
}

// newMapPinsFetchError converts a request failure into typed metadata, keeping
// the HTTP evidence the detail path keeps.
func newMapPinsFetchError(requestedURL, effectiveURL string, err error) *MapPinsError {
	e := &MapPinsError{
		Kind:         MapPinsErrorFetchFailed,
		RequestedURL: requestedURL,
		EffectiveURL: effectiveURL,
		Message:      err.Error(),
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		e.HTTPStatus = httpErr.StatusCode
		if httpErr.EffectiveURL != "" {
			e.EffectiveURL = httpErr.EffectiveURL
		}
		e.RetryAfterSeconds = httpErr.RetryAfterSeconds
	}
	return e
}

// withMapPinsURLs fills request evidence into a typed error raised by the pure
// parsers, which do not know the URLs involved.
func withMapPinsURLs(err error, requestedURL, effectiveURL string) error {
	var mapErr *MapPinsError
	if errors.As(err, &mapErr) {
		if mapErr.RequestedURL == "" {
			mapErr.RequestedURL = requestedURL
		}
		if mapErr.EffectiveURL == "" {
			mapErr.EffectiveURL = effectiveURL
		}
	}
	return err
}

// parseMapForm reads the source's map form: its action URL and every hidden
// input, in page order, exactly as carried. A page without the form or without
// hidden fields is not a usable map page; no action URL is invented. The action
// is resolved against the page URL that served it, so a root-relative action
// keeps the source's host.
func parseMapForm(htmlText, pageURL string) (mapForm, error) {
	m := reMapFormBlock.FindStringSubmatch(htmlText)
	if m == nil {
		return mapForm{}, fmt.Errorf("map page carries no form named mapgfixparams")
	}
	openingTag, body := m[1], m[2]

	action := ""
	if a := reMapFormAction.FindStringSubmatch(openingTag); a != nil {
		action = mapFirstNonEmpty(a[1:]...)
	}
	action = html.UnescapeString(strings.TrimSpace(action))
	if action == "" {
		return mapForm{}, fmt.Errorf("map form carries no action URL to submit to")
	}
	resolved, err := resolveFormAction(action, pageURL)
	if err != nil {
		return mapForm{}, fmt.Errorf("map form action %q is not a usable URL: %w", action, err)
	}

	fields := url.Values{}
	for _, input := range reMapHiddenInput.FindAllString(body, -1) {
		name := ""
		if nm := reMapAttrName.FindStringSubmatch(input); nm != nil {
			name = mapFirstNonEmpty(nm[1:]...)
		}
		name = html.UnescapeString(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		value := ""
		if vm := reMapAttrValue.FindStringSubmatch(input); vm != nil {
			value = mapFirstNonEmpty(vm[1:]...)
		}
		// Add, not Set: a repeated field name is sent as often as the page
		// carries it, exactly as a browser would submit the form.
		fields.Add(name, html.UnescapeString(value))
	}
	if len(fields) == 0 {
		return mapForm{}, fmt.Errorf("map form carries no hidden inputs to submit")
	}

	return mapForm{Action: resolved, Fields: fields}, nil
}

// resolveFormAction resolves a form action against the page that served it,
// falling back to the source root only when no page URL is available. The map
// form's action is root-relative, the way the page serves it.
func resolveFormAction(action, pageURL string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(pageURL))
	if err != nil || base.Scheme == "" {
		base, err = url.Parse("https://www.imot.bg/")
		if err != nil {
			return "", err
		}
	}
	ref, err := url.Parse(action)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(ref).String(), nil
}

// extractMapPinsArrays reads the three positional arrays out of the map body's
// initGoogleMapS call. A body without the call, or a call whose arrays are not
// exactly three strict quoted-string groups, is a typed page failure.
func extractMapPinsArrays(body string) (mapPinsArrays, error) {
	onload := ""
	if m := reMapBodyOnload.FindStringSubmatch(body); m != nil {
		onload = mapFirstNonEmpty(m[1:]...)
	}
	if strings.TrimSpace(onload) == "" {
		if reMapChallengePage.MatchString(body) {
			return mapPinsArrays{}, &MapPinsError{
				Kind:    MapPinsErrorChallengePage,
				Message: "map response is a bot challenge, not a map result page",
			}
		}
		return mapPinsArrays{}, &MapPinsError{
			Kind:    MapPinsErrorUnreadablePage,
			Message: "map response carries no body onload handler",
		}
	}

	call := reInitGoogleMapS.FindStringSubmatch(onload)
	if call == nil {
		if reMapChallengePage.MatchString(onload) {
			return mapPinsArrays{}, &MapPinsError{
				Kind:    MapPinsErrorChallengePage,
				Message: "map response is a bot challenge, not a map result page",
			}
		}
		return mapPinsArrays{}, &MapPinsError{
			Kind:    MapPinsErrorUnreadablePage,
			Message: "map body onload does not call initGoogleMapS",
		}
	}

	groups := reNewArray.FindAllStringSubmatch(call[1], -1)
	if len(groups) != 3 {
		return mapPinsArrays{}, &MapPinsError{
			Kind:    MapPinsErrorUnreadablePage,
			Message: fmt.Sprintf("initGoogleMapS carries %d coordinate arrays, expected exactly 3", len(groups)),
		}
	}

	arrays := make([][]string, 3)
	for i, g := range groups {
		values, err := parseQuotedArray(g[1])
		if err != nil {
			return mapPinsArrays{}, &MapPinsError{
				Kind:    MapPinsErrorUnreadablePage,
				Message: fmt.Sprintf("coordinate array %d is not a strict quoted-string array: %v", i+1, err),
			}
		}
		arrays[i] = values
	}

	return mapPinsArrays{
		Latitudes:  arrays[0],
		Longitudes: arrays[1],
		AdvIDs:     arrays[2],
	}, nil
}

// parseQuotedArray turns one new Array(...) body into its quoted strings. An
// empty array is valid (a true empty map is a result, not an error); an element
// that is not a single quoted string fails the array.
func parseQuotedArray(content string) ([]string, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return []string{}, nil
	}
	parts := strings.Split(trimmed, ",")
	values := make([]string, 0, len(parts))
	for _, p := range parts {
		m := reQuotedStringElement.FindStringSubmatch(p)
		if m == nil {
			return nil, fmt.Errorf("element %q is not a quoted string", strings.TrimSpace(p))
		}
		values = append(values, mapFirstNonEmpty(m[1:]...))
	}
	return values, nil
}

// mapPinsArrays is the positional data one map body carried before validation.
type mapPinsArrays struct {
	Latitudes  []string
	Longitudes []string
	AdvIDs     []string
}

// joinMapPins pairs the positional arrays into pins.
//
// Unequal lengths are a hard failure: no record can be paired without guessing
// which advert a coordinate belongs to. Once lengths agree, each position is
// validated independently; a rejected position is dropped with a bounded
// warning while every other position survives. Duplicate advert ids keep the
// first point so one advert never claims two.
func joinMapPins(arrays mapPinsArrays, warnings *[]string) ([]MapPin, error) {
	n := len(arrays.Latitudes)
	if len(arrays.Longitudes) != n || len(arrays.AdvIDs) != n {
		return nil, &MapPinsError{
			Kind: MapPinsErrorMisalignedArrays,
			Message: fmt.Sprintf("map arrays are not aligned: %d latitudes, %d longitudes, %d advert ids",
				len(arrays.Latitudes), len(arrays.Longitudes), len(arrays.AdvIDs)),
		}
	}

	pins := make([]MapPin, 0, n)
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		advID := strings.TrimSpace(arrays.AdvIDs[i])
		if !reMapAdvertID.MatchString(advID) {
			addMapWarning(warnings, MapWarningBadAdvertID)
			continue
		}

		lat, latErr := strconv.ParseFloat(strings.TrimSpace(arrays.Latitudes[i]), 64)
		lng, lngErr := strconv.ParseFloat(strings.TrimSpace(arrays.Longitudes[i]), 64)
		if latErr != nil || lngErr != nil {
			addMapWarning(warnings, MapWarningCoordinateUnparseable)
			continue
		}
		// Written as a positive range test so NaN and infinities fail it: a
		// NaN coordinate can never satisfy the bounds.
		if !(lat >= mapPinLatMin && lat <= mapPinLatMax) || !(lng >= mapPinLngMin && lng <= mapPinLngMax) {
			addMapWarning(warnings, MapWarningCoordinateOutOfBounds)
			continue
		}

		if seen[advID] {
			addMapWarning(warnings, MapWarningDuplicateAdvertID)
			continue
		}
		seen[advID] = true
		pins = append(pins, MapPin{AdvID: advID, Lat: lat, Lng: lng})
	}

	return pins, nil
}

// countDistinctPoints counts unique coordinate pairs at 6 decimal places. Two
// adverts in the same building share one point; the pins still each keep their
// own coordinate. This is a diagnostic count, never a substitute for pins.
func countDistinctPoints(pins []MapPin) int {
	seen := make(map[string]bool, len(pins))
	for _, p := range pins {
		key := strconv.FormatFloat(round6(p.Lat), 'f', 6, 64) + "," + strconv.FormatFloat(round6(p.Lng), 'f', 6, 64)
		seen[key] = true
	}
	return len(seen)
}

// round6 rounds one coordinate to 6 decimal places, the precision at which two
// source pins are treated as the same point.
func round6(v float64) float64 {
	return math.Round(v*1e6) / 1e6
}

// parseReportedMappedCount reads the source's own heading count, or nil when
// the heading is absent or does not parse. A missing count is unknown coverage,
// never zero.
func parseReportedMappedCount(body string) *int {
	m := reReportedMappedCount.FindStringSubmatch(body)
	if m == nil {
		return nil
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return nil
	}
	return &n
}

// mapPinsCompleteness reports how much of the requested scope the payload
// covers. "complete" needs the source's own count to equal the validated pins;
// anything unproven is "unknown". "partial" is reserved for a source-provided
// truncation marker, which this parser has not verified on the map body.
func mapPinsCompleteness(reported *int, pinCount int) string {
	if reported == nil {
		return MapCompletenessUnknown
	}
	if *reported == pinCount {
		return MapCompletenessComplete
	}
	return MapCompletenessUnknown
}

// addMapWarning appends a bounded warning code once. Duplicate codes carry no
// extra meaning and a consumer validates the list as a set.
func addMapWarning(warnings *[]string, code string) {
	if warnings == nil {
		return
	}
	for _, existing := range *warnings {
		if existing == code {
			return
		}
	}
	*warnings = append(*warnings, code)
}

// encodeWin1251Form renders a url.Values body the way the source's own
// windows-1251 form submits it: every value is first encoded from UTF-8 to
// cp1251 bytes, then the standard application/x-www-form-urlencoded encoding
// is applied. Key order and repetitions are preserved exactly.
func encodeWin1251Form(fields url.Values) string {
	encoder := charmap.Windows1251.NewEncoder()
	out := url.Values{}
	for key, values := range fields {
		encodedKey, err := encoder.Bytes([]byte(key))
		if err != nil {
			// A value that cannot be represented stays as-is: the request then
			// fails the honest way instead of silently widening the scope.
			encodedKey = []byte(key)
		}
		for _, value := range values {
			encoded, err := encoder.Bytes([]byte(value))
			if err != nil {
				encoded = []byte(value)
			}
			out.Add(string(encodedKey), string(encoded))
		}
	}
	return out.Encode()
}
