package scraper

import (
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
