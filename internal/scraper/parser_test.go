package scraper

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	legitDetailURL = "https://www.imot.bg/obiava-1b177425523801314-dvustaen-apartament"
	otherDetailURL = "https://www.imot.bg/obiava-1a176754466608675-tristaen-apartament"
)

// readFixture loads one saved page from testdata. Fixtures are also the
// shared offline contract used by the Node fixture tests that run the built
// binary with `detail --file`, so they live at a stable path.
func readFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return DecodeHTMLBytes(raw)
}

func requireDetailError(t *testing.T, err error) *DetailError {
	t.Helper()
	if err == nil {
		t.Fatal("expected a typed detail error, got nil")
	}
	var de *DetailError
	if !errors.As(err, &de) {
		t.Fatalf("expected *DetailError, got %T: %v", err, err)
	}
	return de
}

func TestParseDetailPageAcceptsLegitimateDetail(t *testing.T) {
	detail, err := ParseDetailPage(readFixture(t, "detail-legit.html"), legitDetailURL)
	if err != nil {
		t.Fatalf("legitimate advert rejected: %v", err)
	}
	if detail.URL != legitDetailURL {
		t.Errorf("url = %q, want the page's own canonical URL %q", detail.URL, legitDetailURL)
	}
	if !strings.Contains(detail.FullDescription, "Лозенец") {
		t.Errorf("full_description was not extracted: %q", detail.FullDescription)
	}
	if detail.SellerType != "Агенция" {
		t.Errorf("seller_type = %q, want Агенция", detail.SellerType)
	}
	if detail.HeatingTEC != "ДА" || detail.HeatingGas != "НЕ" {
		t.Errorf("heating = %q/%q, want ДА/НЕ", detail.HeatingTEC, detail.HeatingGas)
	}
	if detail.ConstructionType != "Тухла" {
		t.Errorf("construction_type = %q, want Тухла", detail.ConstructionType)
	}
	if detail.YearBuilt != "2007" {
		t.Errorf("year_built = %q, want 2007", detail.YearBuilt)
	}
	if !strings.HasPrefix(detail.Phones, "0888") {
		t.Errorf("phones = %q, want a number starting 0888", detail.Phones)
	}
	if detail.PhotoURL == "" {
		t.Error("photo_url was not extracted from og:image")
	}
	if detail.ViewCount != 65 {
		t.Errorf("view_count = %d, want 65", detail.ViewCount)
	}
	if len(detail.Features) == 0 || detail.Features[0] != "Обзаведен" {
		t.Errorf("features = %#v, want Обзаведен first", detail.Features)
	}

	// The success payload carries the presence contract, and advert_id is the
	// page's own independently parsed identity.
	if detail.ContractVersion != DetailContractVersion {
		t.Errorf("contract_version = %q, want %q", detail.ContractVersion, DetailContractVersion)
	}
	if detail.AdvertID != "177425523801314" {
		t.Errorf("advert_id = %q, want the page's own advert number", detail.AdvertID)
	}
	if len(detail.FieldEvidence) != len(DetailKeys) {
		t.Errorf("field_evidence has %d entries, want %d", len(detail.FieldEvidence), len(DetailKeys))
	}
}

// A genuine advert with no photo and no feature tag is still a genuine advert.
func TestParseDetailPageAcceptsSparseLegitimateDetail(t *testing.T) {
	detail, err := ParseDetailPage(readFixture(t, "detail-legit-sparse.html"), "https://www.imot.bg/obiava-1c176754466608675-atelie-tavan")
	if err != nil {
		t.Fatalf("sparse advert rejected: %v", err)
	}
	if !strings.Contains(detail.FullDescription, "Ателие") {
		t.Errorf("full_description = %q", detail.FullDescription)
	}
	if detail.PhotoURL != "" || len(detail.PhotoURLs) != 0 {
		t.Errorf("expected no photos, got %q / %#v", detail.PhotoURL, detail.PhotoURLs)
	}
	if len(detail.Features) != 0 {
		t.Errorf("expected no features, got %#v", detail.Features)
	}
}

func TestParseDetailPageRejectsChallengePage(t *testing.T) {
	_, err := ParseDetailPage(readFixture(t, "detail-challenge.html"), legitDetailURL)
	de := requireDetailError(t, err)
	if de.Kind != DetailErrorChallengePage {
		t.Fatalf("kind = %q, want %q", de.Kind, DetailErrorChallengePage)
	}
	if de.RequestedAdvertID != "177425523801314" {
		t.Errorf("requested_advert_id = %q", de.RequestedAdvertID)
	}
}

func TestParseDetailPageRejectsRemovedAdvertNotice(t *testing.T) {
	_, err := ParseDetailPage(readFixture(t, "detail-removed.html"), legitDetailURL)
	de := requireDetailError(t, err)
	if de.Kind != DetailErrorRemovedAdvert {
		t.Fatalf("kind = %q, want %q", de.Kind, DetailErrorRemovedAdvert)
	}
}

func TestParseDetailPageRejectsUnreadablePage(t *testing.T) {
	_, err := ParseDetailPage("<html><body><p>Service temporarily unavailable</p></body></html>", legitDetailURL)
	de := requireDetailError(t, err)
	if de.Kind != DetailErrorUnreadablePage {
		t.Fatalf("kind = %q, want %q", de.Kind, DetailErrorUnreadablePage)
	}
}

func TestParseDetailPageRejectsWrongIdentity(t *testing.T) {
	_, err := ParseDetailPage(readFixture(t, "detail-wrong-identity.html"), legitDetailURL)
	de := requireDetailError(t, err)
	if de.Kind != DetailErrorWrongIdentity {
		t.Fatalf("kind = %q, want %q", de.Kind, DetailErrorWrongIdentity)
	}
	if de.RequestedAdvertID != "177425523801314" || de.ObservedAdvertID != "176754466608675" {
		t.Errorf("identity metadata = requested %q observed %q", de.RequestedAdvertID, de.ObservedAdvertID)
	}
	if de.ObservedURL != otherDetailURL {
		t.Errorf("observed_url = %q, want %q", de.ObservedURL, otherDetailURL)
	}
}

func TestParseDetailPageRejectsMissingIdentity(t *testing.T) {
	_, err := ParseDetailPage(readFixture(t, "detail-missing-identity.html"), legitDetailURL)
	de := requireDetailError(t, err)
	if de.Kind != DetailErrorMissingIdentity {
		t.Fatalf("kind = %q, want %q", de.Kind, DetailErrorMissingIdentity)
	}
	if de.ObservedAdvertID != "" {
		t.Errorf("observed_advert_id = %q, want empty: the requested URL must not be substituted", de.ObservedAdvertID)
	}
	if de.ObservedURL != "" {
		t.Errorf("observed_url = %q, want empty", de.ObservedURL)
	}
}

// The offline path may omit the expected URL, but identity is still required
// from the page itself; a fixture cannot pass merely because no comparison was
// requested.
func TestParseDetailPageWithoutExpectedURLStillRequiresIdentity(t *testing.T) {
	detail, err := ParseDetailPage(readFixture(t, "detail-legit.html"), "")
	if err != nil {
		t.Fatalf("legitimate advert rejected without --expect-url: %v", err)
	}
	if detail.URL != legitDetailURL {
		t.Errorf("url = %q, want %q", detail.URL, legitDetailURL)
	}

	_, err = ParseDetailPage(readFixture(t, "detail-missing-identity.html"), "")
	de := requireDetailError(t, err)
	if de.Kind != DetailErrorMissingIdentity {
		t.Fatalf("kind = %q, want %q", de.Kind, DetailErrorMissingIdentity)
	}
}

func TestClassifyDetailPage(t *testing.T) {
	cases := []struct {
		fixture string
		want    DetailPageKind
	}{
		{"detail-legit.html", DetailPageKindListing},
		{"detail-legit-sparse.html", DetailPageKindListing},
		{"detail-challenge.html", DetailPageKindChallenge},
		{"detail-removed.html", DetailPageKindRemoved},
		{"detail-wrong-identity.html", DetailPageKindListing},
		{"detail-missing-identity.html", DetailPageKindListing},
	}
	for _, tc := range cases {
		got := ClassifyDetailPage(readFixture(t, tc.fixture))
		if got != tc.want {
			t.Errorf("%s classified as %q, want %q", tc.fixture, got, tc.want)
		}
	}
}

func TestAdvertIDFromURL(t *testing.T) {
	cases := map[string]string{
		legitDetailURL:      "177425523801314",
		"1a176754466608675": "176754466608675",
		"https://www.imot.bg/obiava-1b177425523801314-x/p-2": "177425523801314",
		"https://www.imot.bg/obiavi/prodazhbi/grad-sofiya":   "",
		"": "",
	}
	for value, want := range cases {
		if got := AdvertIDFromURL(value); got != want {
			t.Errorf("AdvertIDFromURL(%q) = %q, want %q", value, got, want)
		}
	}
}

// ParseSearchPage is the offline twin of the live page-1 path. These fixtures
// are also the shared offline contract for the Node fixture tests that run the
// built binary with `search --file`.
func TestParseSearchPageParsesListings(t *testing.T) {
	result := ParseSearchPage(readFixture(t, "search-page.html"), "search-page.html")
	if result.Partial || len(result.Errors) != 0 {
		t.Fatalf("expected a clean parse, got partial=%v errors=%#v", result.Partial, result.Errors)
	}
	if result.TotalCount != 1 {
		t.Errorf("total_count = %d, want 1", result.TotalCount)
	}
	if len(result.Listings) != 1 {
		t.Fatalf("listings = %d, want 1", len(result.Listings))
	}
	l := result.Listings[0]
	if l.Type != "2-СТАЕН" || l.City != "София" || l.Neighborhood != "Лозенец" {
		t.Errorf("type/city/neighborhood = %q/%q/%q", l.Type, l.City, l.Neighborhood)
	}
	if l.PriceEUR != 120000 || l.SizeSqM != 65 {
		t.Errorf("price/size = %d/%d, want 120000/65", l.PriceEUR, l.SizeSqM)
	}
	if l.ID != "1b177425523801314" {
		t.Errorf("id = %q, want 1b177425523801314", l.ID)
	}
	if l.Floor != "3 от 5" || l.YearBuilt != "2007" {
		t.Errorf("floor/year = %q/%q, want 3 от 5 / 2007", l.Floor, l.YearBuilt)
	}
}

func TestParseSearchPageMarksVerifiedEmpty(t *testing.T) {
	result := ParseSearchPage(readFixture(t, "search-empty.html"), "search-empty.html")
	if !result.EmptyVerified {
		t.Fatal("expected empty_verified for the explicit no-results marker")
	}
	if result.Partial {
		t.Error("a verified empty page must not be partial")
	}
	if len(result.Listings) != 0 {
		t.Errorf("listings = %d, want 0", len(result.Listings))
	}
}

// A saved page with no cards, no total and no no-results marker must be an
// unreadable page, never a verified empty market.
func TestParseSearchPageMarksUnreadable(t *testing.T) {
	result := ParseSearchPage(readFixture(t, "search-unreadable.html"), "search-unreadable.html")
	if !result.Partial || result.EmptyVerified {
		t.Fatalf("partial = %v, empty_verified = %v; want true/false", result.Partial, result.EmptyVerified)
	}
	if len(result.Errors) != 1 || result.Errors[0].Kind != SearchErrorUnreadablePage {
		t.Fatalf("errors = %#v, want one unreadable_page error", result.Errors)
	}
	if result.Errors[0].URL != "search-unreadable.html" {
		t.Errorf("error url = %q, want the source name", result.Errors[0].URL)
	}
}

// requireDetailEvidence asserts one field's evidence entry. A present entry must
// carry a raw value; an unknown entry must not, so unknown can never be read as
// proof of absence.
func requireDetailEvidence(t *testing.T, d DetailListing, key, wantState, wantReason string) DetailFieldEvidence {
	t.Helper()
	ev, ok := d.FieldEvidence[key]
	if !ok {
		t.Fatalf("field_evidence[%q] missing", key)
	}
	if ev.State != wantState {
		t.Errorf("%s state = %q, want %q (reason %q)", key, ev.State, wantState, ev.Reason)
	}
	if wantReason != "" && ev.Reason != wantReason {
		t.Errorf("%s reason = %q, want %q", key, ev.Reason, wantReason)
	}
	if ev.State == DetailPresencePresent && ev.Raw == nil {
		t.Errorf("%s is present but raw is nil", key)
	}
	if ev.State == DetailPresenceUnknown && ev.Raw != nil {
		t.Errorf("%s is unknown but raw = %#v, want null", key, ev.Raw)
	}
	return ev
}

// Every named detail key must have exactly one evidence entry with a recognized
// state and a bounded reason code, whether or not the legacy flat field exists.
func TestDetailEvidenceCoversEveryNamedKey(t *testing.T) {
	for _, fixture := range []string{"detail-legit.html", "detail-legit-sparse.html"} {
		detail, err := ParseDetailPage(readFixture(t, fixture), "")
		if err != nil {
			t.Fatalf("%s rejected: %v", fixture, err)
		}
		if len(detail.FieldEvidence) != len(DetailKeys) {
			t.Errorf("%s: field_evidence has %d entries, want %d", fixture, len(detail.FieldEvidence), len(DetailKeys))
		}
		for _, key := range DetailKeys {
			ev, ok := detail.FieldEvidence[key]
			if !ok {
				t.Errorf("%s: field_evidence[%q] missing", fixture, key)
				continue
			}
			switch ev.State {
			case DetailPresencePresent, DetailPresenceVerifiedAbsent, DetailPresenceUnknown:
			default:
				t.Errorf("%s: %s has unrecognized state %q", fixture, key, ev.State)
			}
			if ev.Reason == "" {
				t.Errorf("%s: %s has no reason code", fixture, key)
			}
		}
	}
}

// A field the extractor observed is present with its extractor reason; a field
// no marker covered is unknown, not verified absence.
func TestDetailEvidenceDistinguishesPresentFromUnknown(t *testing.T) {
	detail, err := ParseDetailPage(readFixture(t, "detail-legit.html"), legitDetailURL)
	if err != nil {
		t.Fatalf("legitimate advert rejected: %v", err)
	}

	for _, tc := range []struct{ key, reason string }{
		{DetailKeyFullDescription, DetailReasonTextBlock},
		{DetailKeyFloor, DetailReasonParamsBlock},
		{DetailKeyYearBuilt, DetailReasonParamsBlock},
		{DetailKeyConstructionType, DetailReasonParamsBlock},
		{DetailKeyHeatingTEC, DetailReasonParamsBlock},
		{DetailKeyHeatingGas, DetailReasonParamsBlock},
		{DetailKeySellerType, DetailReasonParamsBlock},
		{DetailKeyPhones, DetailReasonPhoneBlock},
		{DetailKeyViewCount, DetailReasonViewCountMarker},
		{DetailKeyPhotoURLs, DetailReasonPhotoSelector},
		{DetailKeyFeatures, DetailReasonFeaturesBlock},
		{DetailKeyPublishedAt, DetailReasonPublishedAtMarker},
	} {
		requireDetailEvidence(t, detail, tc.key, DetailPresencePresent, tc.reason)
	}

	// The parser has no marker for these fields on this page, so they must be
	// unknown rather than verified absent.
	for _, key := range []string{
		DetailKeyAgencyURL, DetailKeyCorrectedAt, DetailKeyVatNote,
		DetailKeyBrokerName, DetailKeyBrokerPhone, DetailKeyAgencyOffice,
	} {
		requireDetailEvidence(t, detail, key, DetailPresenceUnknown, DetailReasonNoSelectorHit)
	}

	// A proven zero view count is a present value, and its raw value is numeric.
	ev := requireDetailEvidence(t, detail, DetailKeyViewCount, DetailPresencePresent, DetailReasonViewCountMarker)
	if ev.Raw != 65 {
		t.Errorf("view_count raw = %#v, want the number 65", ev.Raw)
	}
}

// A recognized source structure that omits a labelled params key (and a
// rendered, empty features block) proves absence; a bare token the parser does
// not recognize stays unknown.
func TestDetailEvidenceMarksVerifiedAbsenceNotUnknown(t *testing.T) {
	const url = "https://www.imot.bg/obiava-1d177425523801314-dvustaen-apartament"
	detail, err := ParseDetailPage(readFixture(t, "detail-presence-absent.html"), url)
	if err != nil {
		t.Fatalf("presence fixture rejected: %v", err)
	}

	for _, tc := range []struct{ key, reason string }{
		{DetailKeyFullDescription, DetailReasonTextBlockEmpty},
		{DetailKeyFloor, DetailReasonParamsKeyAbsent},
		{DetailKeyYearBuilt, DetailReasonParamsKeyAbsent},
		{DetailKeyHeatingTEC, DetailReasonParamsKeyAbsent},
		{DetailKeyHeatingGas, DetailReasonParamsKeyAbsent},
		{DetailKeyFeatures, DetailReasonFeaturesBlockEmpty},
	} {
		requireDetailEvidence(t, detail, tc.key, DetailPresenceVerifiedAbsent, tc.reason)
	}

	// The features block rendered with no tag: the state and reason carry the
	// absence proof; raw stays null because a non-present entry may not carry
	// a value the consumer could mistake for observed data.
	if ev := detail.FieldEvidence[DetailKeyFeatures]; ev.Raw != nil {
		t.Errorf("verified_absent features raw = %#v, want null", ev.Raw)
	}

	// A bare token the parser does not recognize could be a new source value,
	// so its absence stays unknown; the same holds for a phone block that
	// resolved no number.
	requireDetailEvidence(t, detail, DetailKeyConstructionType, DetailPresenceUnknown, DetailReasonParamsUnrecognized)
	requireDetailEvidence(t, detail, DetailKeyPhones, DetailPresenceUnknown, DetailReasonPhoneBlockUnresolved)
	requireDetailEvidence(t, detail, DetailKeyPhotoURLs, DetailPresenceUnknown, DetailReasonNoSelectorHit)
	requireDetailEvidence(t, detail, DetailKeySellerType, DetailPresencePresent, DetailReasonParamsBlock)

	// No unknown entry may carry a raw value that could be mistaken for a value.
	for _, key := range DetailKeys {
		if ev := detail.FieldEvidence[key]; ev.State == DetailPresenceUnknown && ev.Raw != nil {
			t.Errorf("%s is unknown but carries raw %#v", key, ev.Raw)
		}
	}
}

// A matched but empty params block proves nothing: it rendered no recognizable
// key, so the fields it omits stay unknown instead of clearing stored values.
func TestDetailEvidenceEmptyParamsBlockIsNotAbsence(t *testing.T) {
	const url = "https://www.imot.bg/obiava-1e177425523801314-dvustaen-apartament"
	html := `<meta property="og:url" content="` + url + `">` +
		`<div class="text">Просторен двустаен апартамент с достатъчно дълго описание за парсера.</div>` +
		`<div class="params"></div>` +
		`<div class="phone">тел.: 0888 123 456</div>`
	detail, err := ParseDetailPage(html, url)
	if err != nil {
		t.Fatalf("advert-shaped page rejected: %v", err)
	}
	for _, key := range []string{DetailKeyFloor, DetailKeyYearBuilt, DetailKeyHeatingTEC, DetailKeyHeatingGas} {
		requireDetailEvidence(t, detail, key, DetailPresenceUnknown, DetailReasonParamsUnrecognized)
	}
}

// An explicitly parsed zero view count is present, not missing: the evidence
// carries the number rather than treating zero as an absent field.
func TestDetailEvidenceProvenZeroViewCountIsPresent(t *testing.T) {
	const url = "https://www.imot.bg/obiava-1f177425523801314-dvustaen-apartament"
	html := `<meta property="og:url" content="` + url + `">` +
		`<div class="text">Просторен двустаен апартамент с достатъчно дълго описание за парсера.</div>` +
		`<div class="phone">тел.: 0888 123 456</div>` +
		`Обявата е посетена <span>0</span> пъти.`
	detail, err := ParseDetailPage(html, url)
	if err != nil {
		t.Fatalf("advert-shaped page rejected: %v", err)
	}
	ev := requireDetailEvidence(t, detail, DetailKeyViewCount, DetailPresencePresent, DetailReasonViewCountMarker)
	if ev.Raw != 0 {
		t.Errorf("zero view_count raw = %#v, want the number 0", ev.Raw)
	}
}

// The presence contract is additive: the flat success fields survive, and the
// new metadata serializes with the contract version, advert identity and all
// evidence entries.
func TestDetailSuccessJSONCarriesPresenceContract(t *testing.T) {
	detail, err := ParseDetailPage(readFixture(t, "detail-legit.html"), legitDetailURL)
	if err != nil {
		t.Fatalf("legitimate advert rejected: %v", err)
	}

	payload, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got := string(decoded["contract_version"]); got != `"`+DetailContractVersion+`"` {
		t.Errorf("contract_version = %s, want %q", got, DetailContractVersion)
	}
	if got := string(decoded["advert_id"]); got != `"177425523801314"` {
		t.Errorf("advert_id = %s, want the page's advert number", got)
	}
	for _, key := range []string{"url", "full_description", "floor", "year_built", "phones", "photo_urls", "features"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("legacy success field %q disappeared from the payload", key)
		}
	}

	var evidence map[string]DetailFieldEvidence
	if err := json.Unmarshal(decoded["field_evidence"], &evidence); err != nil {
		t.Fatalf("field_evidence unmarshal: %v", err)
	}
	if len(evidence) != len(DetailKeys) {
		t.Fatalf("field_evidence has %d entries, want %d", len(evidence), len(DetailKeys))
	}
	for _, key := range DetailKeys {
		ev, ok := evidence[key]
		if !ok {
			t.Errorf("field_evidence[%q] missing from JSON", key)
			continue
		}
		if ev.State == "" || ev.Reason == "" {
			t.Errorf("field_evidence[%q] = %+v, want state and reason", key, ev)
		}
	}
}

// Primary advert extraction (A1). The live imot.bg detail page renders the
// advert's own content under .ad2023 and shows recommendation cards for other
// adverts elsewhere. Photographs must come from the advert's primary gallery,
// its identity-matched offer list or og:image, never from a page-wide scan that
// mixes other adverts in.

// A valid land advert has no .carExtri "Особености" section. On a complete,
// identity-verified advert layout that omission is the source's own absence, so
// the listing completes and a consumer may clear stored features instead of
// keeping them forever because the field was unknown.
func TestDetailLandAdvertWithoutFeaturesCompletes(t *testing.T) {
	const url = "https://www.imot.bg/obiava-1r164785405040282-prodava-partsel-grad-sofiya-gotse-delchev"
	detail, err := ParseDetailPage(readFixture(t, "detail-land-no-features.html"), url)
	if err != nil {
		t.Fatalf("land advert rejected: %v", err)
	}
	if len(detail.Features) != 0 {
		t.Errorf("features = %#v, want none: the advert rendered no features section", detail.Features)
	}
	requireDetailEvidence(t, detail, DetailKeyFeatures, DetailPresenceVerifiedAbsent, DetailReasonFeaturesAbsent)
	requireDetailEvidence(t, detail, DetailKeyPhotoURLs, DetailPresencePresent, DetailReasonPhotoSelector)
	if !strings.Contains(detail.PhotoURL, "/big1/1r164785405040282_xn.jpg") {
		t.Errorf("photo_url = %q, want the source's fullscreen /big1/ gallery variant", detail.PhotoURL)
	}
	// The smaller social copy is retained as the advertised fallback instead of
	// being discarded with the variant it belongs to.
	if len(detail.PhotoURLs) != 2 || !strings.Contains(detail.PhotoURLs[1], "/big/1r164785405040282_xn.jpg") {
		t.Errorf("photo_urls = %#v, want the /big/ fallback after the /big1/ fullscreen variant", detail.PhotoURLs)
	}
}

// An advert that explicitly has no photographs renders the primary nophoto
// placeholder and no gallery. The placeholder is the source's own proof of
// absence, while the recommendation cards' focus.bg photographs belong to other
// adverts and must not reach this advert's list.
func TestDetailNoPhotoPlaceholderIsVerifiedAbsence(t *testing.T) {
	const url = "https://www.imot.bg/obiava-1c176600053014964-tristaen-apartament-grad-sofiya-banishora"
	detail, err := ParseDetailPage(readFixture(t, "detail-no-photo-placeholder.html"), url)
	if err != nil {
		t.Fatalf("no-photo advert rejected: %v", err)
	}
	if detail.PhotoURL != "" || len(detail.PhotoURLs) != 0 {
		t.Errorf("photos = %q / %#v, want none", detail.PhotoURL, detail.PhotoURLs)
	}
	ev := requireDetailEvidence(t, detail, DetailKeyPhotoURLs, DetailPresenceVerifiedAbsent, DetailReasonNoPhotoPlaceholder)
	if ev.Raw != nil {
		t.Errorf("verified_absent photos raw = %#v, want null", ev.Raw)
	}
	// The advert still completes with its real features and description.
	if len(detail.Features) != 2 || detail.Features[0] != "Тухла" {
		t.Errorf("features = %#v, want the advert's own two tags", detail.Features)
	}
	if detail.FullDescription == "" {
		t.Error("full_description was not extracted")
	}

	joined := strings.Join(detail.PhotoURLs, " ")
	for _, foreign := range []string{"1r169052714326239", "1r173307466150659", "1r166903703506895", "1r176902207606034"} {
		if strings.Contains(joined, foreign) {
			t.Errorf("recommendation photograph %s leaked into photo_urls", foreign)
		}
	}
}

// The primary gallery is read with its lazy and fullscreen attributes, its real
// formats and its advertised fallbacks. Each photograph's variants stay grouped
// with the largest advertised variant first, carousel clones collapse into it,
// and recommendation photographs outside the gallery are excluded.
func TestDetailGalleryIsScopedFullscreenFirst(t *testing.T) {
	const url = "https://www.imot.bg/obiava-1c176570442942924-prodava-tristaen-apartament-grad-sofiya-banishora"
	detail, err := ParseDetailPage(readFixture(t, "detail-gallery-eleven.html"), url)
	if err != nil {
		t.Fatalf("gallery advert rejected: %v", err)
	}
	const big1 = "https://imotstatic3.focus.bg/imot/photosimotbg/1/924/big1/1c176570442942924_"
	const plain = "https://imotstatic3.focus.bg/imot/photosimotbg/1/924/1c176570442942924_"
	want := []string{
		big1 + "pg.jpg",
		"https://imotstatic3.focus.bg/imot/photosimotbg/1/924/big/1c176570442942924_pg.jpg",
		plain + "pg.jpg",
		big1 + "h6.jpg",
		plain + "h6.jpg",
		big1 + "hs.jpg",
		big1 + "G7.jpg",
		big1 + "ge.jpg",
		big1 + "W0.jpg",
		big1 + "uU.jpg",
		big1 + "PX.jpg",
		big1 + "pN.jpg",
		big1 + "yA.jpg",
		big1 + "MT.png",
	}
	if len(detail.PhotoURLs) != len(want) {
		t.Fatalf("photo_urls = %#v, want %d advertised variants", detail.PhotoURLs, len(want))
	}
	for i, wantURL := range want {
		if detail.PhotoURLs[i] != wantURL {
			t.Errorf("photo_urls[%d] = %q, want %q", i, detail.PhotoURLs[i], wantURL)
		}
	}
	if detail.PhotoURL != big1+"pg.jpg" {
		t.Errorf("photo_url = %q, want the /big1/ fullscreen variant over the /big/ og:image", detail.PhotoURL)
	}
	// Eleven distinct photographs, each de-duplicated by identity across the
	// gallery, its clones and the social copy.
	identities := make(map[string]bool)
	for _, u := range detail.PhotoURLs {
		identities[photoBasename(u)] = true
	}
	if len(identities) != 11 {
		t.Errorf("photo_urls carry %d distinct photograph identities, want 11: %#v", len(identities), detail.PhotoURLs)
	}
	// Recommendation cards advertise other adverts; none of their photographs
	// may reach this advert's list.
	for _, u := range detail.PhotoURLs {
		for _, foreign := range []string{"1r169052714326239", "1r173307466150659", "1r176902207606034"} {
			if strings.Contains(u, foreign) {
				t.Errorf("recommendation photograph %s leaked into photo_urls: %q", foreign, u)
			}
		}
	}
}

// Structured data contributes photographs only when its own url/sku identity is
// this advert's. A matching Offer's image list is appended after the gallery,
// even when its product label contradicts the page; a block for another advert
// contributes nothing.
func TestDetailJSONLDImagesRequireMatchingIdentity(t *testing.T) {
	const url = "https://www.imot.bg/obiava-1c176570442942924-prodava-tristaen-apartament-grad-sofiya-banishora"
	detail, err := ParseDetailPage(readFixture(t, "detail-jsonld-identity.html"), url)
	if err != nil {
		t.Fatalf("advert rejected: %v", err)
	}
	if len(detail.PhotoURLs) != 4 {
		t.Fatalf("photo_urls = %#v, want the two gallery photographs with the page's social fallback plus the identity-matched offer image", detail.PhotoURLs)
	}
	want := []string{
		"https://imotstatic3.focus.bg/imot/photosimotbg/1/924/big1/1c176570442942924_pg.jpg",
		"https://imotstatic3.focus.bg/imot/photosimotbg/1/924/big/1c176570442942924_pg.jpg",
		"https://imotstatic3.focus.bg/imot/photosimotbg/1/924/big1/1c176570442942924_h6.jpg",
		"https://imotstatic3.focus.bg/imot/photosimotbg/1/924/big1/1c176570442942924_a3.jpg",
	}
	for i, wantURL := range want {
		if detail.PhotoURLs[i] != wantURL {
			t.Errorf("photo_urls[%d] = %q, want %q", i, detail.PhotoURLs[i], wantURL)
		}
	}
	for _, foreign := range []string{"1o170965300976509_bk.jpg", "1o170965300976509_se.jpg"} {
		for _, u := range detail.PhotoURLs {
			if strings.Contains(u, foreign) {
				t.Errorf("foreign JSON-LD image %s leaked into photo_urls: %q", foreign, u)
			}
		}
	}
}

// The photograph collector never falls back to a page-wide scan: a page whose
// recommendations carry focus.bg photographs yields only the primary gallery's
// own images, with the larger advertised variant preferred.
func TestUniquePhotoURLsScopesToPrimaryGallery(t *testing.T) {
	html := `<meta property="og:image" content="//cdn3.focus.bg/imot/photosimotbg/2/768//big/2c178773245606768_e1.jpg">` +
		`<div id="rezon-gallery"><div id="owlcarousel">` +
		`<div class="item"><img data-src="//cdn3.focus.bg/imot/photosimotbg/2/768//big1/2c178773245606768_e1.jpg"></div>` +
		`<div class="item"><img data-src-gallery="//cdn3.focus.bg/imot/photosimotbg/2/768//big1/2c178773245606768_mu.png"></div>` +
		`</div></div>` +
		`<div class="pic"><img src="//cdn3.focus.bg/imot/photosimotbg/1/039/1r999999999999039_zz.jpg"></div>`
	photos := uniquePhotoURLs(html)
	want := []string{
		"https://cdn3.focus.bg/imot/photosimotbg/2/768/big1/2c178773245606768_e1.jpg",
		"https://cdn3.focus.bg/imot/photosimotbg/2/768/big/2c178773245606768_e1.jpg",
		"https://cdn3.focus.bg/imot/photosimotbg/2/768/big1/2c178773245606768_mu.png",
	}
	if len(photos) != len(want) {
		t.Fatalf("photos = %#v, want only the gallery photographs and their advertised variants", photos)
	}
	for i, wantURL := range want {
		if photos[i] != wantURL {
			t.Errorf("photos[%d] = %q, want %q", i, photos[i], wantURL)
		}
	}
}

// A truncated advert response is not the source's complete layout: neither its
// missing features section nor its primary placeholder may be read as absence.
func TestDetailTruncatedAdvertKeepsOptionalSectionsUnknown(t *testing.T) {
	const url = "https://www.imot.bg/obiava-1c176600053014964-tristaen-apartament-grad-sofiya-banishora"
	html := `<meta property="og:url" content="` + url + `">` +
		`<div class="ad2023"><div class="left">` +
		`<img src="https://www.imot.bg/images/picturess/nophoto_660x495.svg">` +
		`<div class="adParams"><div class="params">Площ: 93 кв.м, Агенция</div></div>` +
		`<div class="text">Тристаен апартамент в Банишора с източно изложение.</div>` +
		`<div class="phone">тел.: 0888 123 456</div>` // no closing tags: a truncated response
	detail, err := ParseDetailPage(html, url)
	if err != nil {
		t.Fatalf("advert-shaped truncated page rejected: %v", err)
	}
	requireDetailEvidence(t, detail, DetailKeyPhotoURLs, DetailPresenceUnknown, DetailReasonNoSelectorHit)
	requireDetailEvidence(t, detail, DetailKeyFeatures, DetailPresenceUnknown, DetailReasonNoSelectorHit)
}

// A recognized layout whose features container rendered markup this parser does
// not understand proves nothing about the advert's features: it stays unknown,
// unlike the genuine omission of the whole section.
func TestDetailUnrecognizedFeaturesOnRecognizedLayoutStayUnknown(t *testing.T) {
	const url = "https://www.imot.bg/obiava-1c176600053014964-tristaen-apartament-grad-sofiya-banishora"
	html := `<meta property="og:url" content="` + url + `">` +
		`<div class="ad2023"><div class="left">` +
		`<div class="borderBox"><div class="carExtri"><span class="Title">Особености</span><br>` +
		`<div class="items"><span>Асансьор</span></div></div></div>` +
		`<div class="adParams"><div class="params">Площ: 93 кв.м, Агенция</div></div>` +
		`<div class="text">Тристаен апартамент в Банишора с източно изложение.</div>` +
		`<div class="phone">тел.: 0888 123 456</div>` +
		`</div></div></html>`
	detail, err := ParseDetailPage(html, url)
	if err != nil {
		t.Fatalf("advert-shaped page rejected: %v", err)
	}
	if len(detail.Features) != 0 {
		t.Errorf("features = %#v, want none: the inner markup is not recognized", detail.Features)
	}
	requireDetailEvidence(t, detail, DetailKeyFeatures, DetailPresenceUnknown, DetailReasonFeaturesUnrecognized)
}

// Notification popups render their own smaller no-photo placeholder outside the
// primary advert subtree. It must not be read as this advert's photo absence.
func TestDetailNotificationPlaceholderIsNotPhotoAbsence(t *testing.T) {
	const url = "https://www.imot.bg/obiava-1c176600053014964-tristaen-apartament-grad-sofiya-banishora"
	html := `<meta property="og:url" content="` + url + `">` +
		`<div class="ad2023"><div class="left">` +
		`<div class="adParams"><div class="params">Площ: 93 кв.м, Агенция</div></div>` +
		`<div class="text">Тристаен апартамент в Банишора с източно изложение.</div>` +
		`<div class="phone">тел.: 0888 123 456</div>` +
		`</div></div>` +
		`<div id="notification-popup"><a href=""><img src="../images/picturess/nophoto_490x341.svg"></a></div>` +
		`</html>`
	detail, err := ParseDetailPage(html, url)
	if err != nil {
		t.Fatalf("advert-shaped page rejected: %v", err)
	}
	requireDetailEvidence(t, detail, DetailKeyPhotoURLs, DetailPresenceUnknown, DetailReasonNoSelectorHit)
}
