package scraper

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// R1 producer-correctness regressions. They cover the two false-absence cases
// from the review (a short or later-block description, and unrecognized feature
// markup), the malformed-requested-identity hole, and the source evidence a
// coverage decision needs (reported/clamped total, resolution, card integrity).

// A short but real description is present, not verified_absent. The old parser
// required more than 20 bytes, so a short description fell through to absence and
// a consumer would have cleared the stored one.
func TestParseDetailPageKeepsShortNonemptyDescription(t *testing.T) {
	detail, err := ParseDetailPage(readFixture(t, "detail-short-description.html"), legitDetailURL)
	if err != nil {
		t.Fatalf("advert with a short description rejected: %v", err)
	}
	if detail.FullDescription != "Продава се" {
		t.Fatalf("full_description = %q, want the short description the source advertised", detail.FullDescription)
	}
	requireDetailEvidence(t, detail, DetailKeyFullDescription, DetailPresencePresent, DetailReasonTextBlock)
}

// A description that only appears after the first two class="text" blocks is
// still the source's description. Scanning two matches made it look absent.
func TestParseDetailPageKeepsLaterBlockDescription(t *testing.T) {
	const url = "https://www.imot.bg/obiava-1a177425523801314-dvustaen-apartament"
	html := `<meta property="og:url" content="` + url + `">` +
		`<div class="text"></div>` +
		`<div class="text">В imot.bg от 2015 г.</div>` +
		`<div class="text">Просторен двустаен апартамент с източно изложение и паркомясто в Лозенец.</div>` +
		`<div class="phone">тел.: 0888 123 456</div>`
	detail, err := ParseDetailPage(html, url)
	if err != nil {
		t.Fatalf("advert-shaped page rejected: %v", err)
	}
	if !strings.Contains(detail.FullDescription, "Просторен двустаен") {
		t.Fatalf("full_description = %q; a description after the second text block must still be found", detail.FullDescription)
	}
	requireDetailEvidence(t, detail, DetailKeyFullDescription, DetailPresencePresent, DetailReasonTextBlock)
}

// A recognized text container that rendered only its provenance line is the
// source's own structure for "no description", so genuine absence still clears.
func TestParseDetailPageProvenanceOnlyDescriptionIsVerifiedAbsent(t *testing.T) {
	detail, err := ParseDetailPage(readFixture(t, "detail-presence-absent.html"), "https://www.imot.bg/obiava-1d177425523801314-dvustaen-apartament")
	if err != nil {
		t.Fatalf("presence fixture rejected: %v", err)
	}
	requireDetailEvidence(t, detail, DetailKeyFullDescription, DetailPresenceVerifiedAbsent, DetailReasonTextBlockEmpty)
}

// A features container whose inner markup produced no recognized tag proves
// nothing about the advert's features. Treating it as absence cleared stored
// features, so it must stay unknown.
func TestDetailChangedFeatureMarkupStaysUnknown(t *testing.T) {
	detail, err := ParseDetailPage(readFixture(t, "detail-features-unrecognized.html"), legitDetailURL)
	if err != nil {
		t.Fatalf("advert rejected: %v", err)
	}
	if len(detail.Features) != 0 {
		t.Fatalf("features = %#v, want none: the inner markup is not recognized", detail.Features)
	}
	requireDetailEvidence(t, detail, DetailKeyFeatures, DetailPresenceUnknown, DetailReasonFeaturesUnrecognized)
}

// An empty text container is a placeholder, not proof that the advert has no
// description. Only a rendered provenance line is recognized absence.
func TestDetailEmptyTextContainerStaysUnknown(t *testing.T) {
	const url = "https://www.imot.bg/obiava-1e177425523801314-dvustaen-apartament"
	html := `<meta property="og:url" content="` + url + `">` +
		`<div class="text"></div>` +
		`<div class="phone">тел.: 0888 123 456</div>`
	detail, err := ParseDetailPage(html, url)
	if err != nil {
		t.Fatalf("advert-shaped page rejected: %v", err)
	}
	requireDetailEvidence(t, detail, DetailKeyFullDescription, DetailPresenceUnknown, DetailReasonTextBlockPlaceholder)
}

// The parser has no marker that proves a gallery is empty, so an advert with no
// photo stays unknown and can never be read as a verified empty gallery.
func TestDetailNoPhotoEvidenceStaysUnknown(t *testing.T) {
	detail, err := ParseDetailPage(readFixture(t, "detail-legit-sparse.html"), "")
	if err != nil {
		t.Fatalf("sparse advert rejected: %v", err)
	}
	requireDetailEvidence(t, detail, DetailKeyPhotoURLs, DetailPresenceUnknown, DetailReasonNoSelectorHit)
}

// A non-empty requested URL that carries no 15-digit advert number is a caller
// error. Skipping the ownership comparison there accepted whatever page the
// source served, including a different advert.
func TestParseDetailPageRejectsMalformedRequestedAdvertID(t *testing.T) {
	_, err := ParseDetailPage(readFixture(t, "detail-legit.html"), "https://www.imot.bg/obiava-dvustaen-apartament")
	de := requireDetailError(t, err)
	if de.Kind != DetailErrorMissingIdentity {
		t.Fatalf("kind = %q, want %q", de.Kind, DetailErrorMissingIdentity)
	}
	if de.RequestedAdvertID != "" {
		t.Errorf("requested_advert_id = %q, want empty", de.RequestedAdvertID)
	}

	// The caller error dominates every page classification: a challenge page
	// cannot be accepted either.
	_, err = ParseDetailPage(readFixture(t, "detail-challenge.html"), "https://www.imot.bg/obiava-no-number")
	de = requireDetailError(t, err)
	if de.Kind != DetailErrorMissingIdentity {
		t.Fatalf("challenge-page kind = %q, want %q", de.Kind, DetailErrorMissingIdentity)
	}
}

// An offline parse without an expected URL keeps working: no comparison is
// requested, so no malformed expectation exists to reject.
func TestParseDetailPageWithoutExpectedURLStillAcceptsValidPage(t *testing.T) {
	if _, err := ParseDetailPage(readFixture(t, "detail-legit.html"), ""); err != nil {
		t.Fatalf("legitimate advert rejected without an expected URL: %v", err)
	}
}

// The source's own total count may be absent or clamped ("1000+"). A coverage
// decision must not read "no count printed" as zero, nor a clamped count as the
// real total.
func TestParseTotalCountEvidence(t *testing.T) {
	cases := []struct {
		html     string
		count    int
		reported bool
		capped   bool
	}{
		{"показани 1-40 от общо 1 354 обяви", 1354, true, false},
		{"показани 1-40 от общо 1000+ обяви", 1000, true, true},
		{`<div class="SearchInfoLine">Няма намерени обяви - Продава</div>`, 0, false, false},
	}
	for _, tc := range cases {
		count, reported, capped := ParseTotalCountEvidence(tc.html)
		if count != tc.count || reported != tc.reported || capped != tc.capped {
			t.Errorf("ParseTotalCountEvidence(%q) = (%d,%v,%v), want (%d,%v,%v)",
				tc.html, count, reported, capped, tc.count, tc.reported, tc.capped)
		}
	}
	// The legacy entry point keeps its meaning: a clamped count parses to the
	// lower bound it prints.
	if got := ParseTotalCount("от общо 1000+ обяви"); got != 1000 {
		t.Errorf("ParseTotalCount clamped = %d, want 1000", got)
	}
}

// A card whose type is unrecognized, and a card dropped for missing price/size,
// are integrity evidence. Silently discarding them is how a sweep looks complete
// while losing rows.
func TestScanListingsReportsCardIntegrity(t *testing.T) {
	html := `<div class="SearchInfoLine">показани 1-2 от общо 2 обяви</div>` +
		`<div class="listItem"><div class="zaglavie">` +
		`<a href="//www.imot.bg/obiava-1b177425523801314-barbekyu" class="title">Продава БАРБЕКЮград София, Лозенец</a>` +
		`<location>град София, Лозенец</location>` +
		`<div class="info">65 кв.м, 3-ти ет. от 5, тел.: 0888 123 456</div>` +
		`</div><div class="price">120 000 €</div></div>` +
		`<div class="listItem"><div class="zaglavie">` +
		`<a href="//www.imot.bg/obiava-1c176754466608675-dvustaen" class="title">Продава 2-СТАЕНград София, Лозенец</a>` +
		`<location>град София, Лозенец</location>` +
		`</div></div>`

	scan := ScanListings(html)
	if scan.CardBlocks != 2 {
		t.Fatalf("card_blocks = %d, want 2", scan.CardBlocks)
	}
	if len(scan.Listings) != 1 {
		t.Fatalf("listings = %d, want 1", len(scan.Listings))
	}
	if scan.UnknownTypeCards != 1 || len(scan.UnknownTypeSamples) != 1 || scan.UnknownTypeSamples[0] != "БАРБЕКЮ" {
		t.Errorf("unknown type evidence = %d %#v, want 1 [БАРБЕКЮ]", scan.UnknownTypeCards, scan.UnknownTypeSamples)
	}
	if scan.DroppedCards != 1 {
		t.Errorf("dropped_cards = %d, want 1", scan.DroppedCards)
	}
	// ParseListings keeps its original signature and returns the same rows.
	if got := ParseListings(html); len(got) != 1 || got[0].Type != "БАРБЕКЮ" {
		t.Errorf("ParseListings = %#v, want the one accepted row", got)
	}
}

// The recognized-card fixture stays a clean scan: one block, one accepted row,
// no unknown type and no dropped card.
func TestParseSearchPageCarriesCoverageEvidence(t *testing.T) {
	result := ParseSearchPage(readFixture(t, "search-page.html"), "search-page.html")
	if !result.TotalCountReported || result.TotalCountCapped {
		t.Errorf("total count evidence = reported %v capped %v, want true/false", result.TotalCountReported, result.TotalCountCapped)
	}
	if result.CardBlocks != 1 || result.DroppedCards != 0 || result.UnknownTypeCards != 0 {
		t.Errorf("card integrity = blocks %d dropped %d unknown %d, want 1/0/0",
			result.CardBlocks, result.DroppedCards, result.UnknownTypeCards)
	}
	if result.NeighborhoodResolution != "" {
		t.Errorf("neighborhood_resolution = %q, want empty for an offline parse", result.NeighborhoodResolution)
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{
		`"total_count_reported":true`,
		`"total_count_capped":false`,
		`"card_blocks":1`,
		`"dropped_cards":0`,
		`"unknown_type_cards":0`,
	} {
		if !strings.Contains(string(encoded), key) {
			t.Errorf("JSON missing %s: %s", key, encoded)
		}
	}
}

// The slug resolution records how the slug was obtained. Only a probe or
// redirect confirmed it against the source; an unverified transliteration is a
// guess that cannot support a completeness claim.
func TestResolveNeighborhoodSlugEvidence(t *testing.T) {
	probeClient := &Client{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return htmlResponse(http.StatusOK, "от общо 0 обяви"), nil
	})}}
	slug, resolution := probeClient.resolveNeighborhoodSlugEvidence(SearchParams{City: "София", Neighborhood: "Лозенец"})
	if slug != "lozenets" || resolution != NeighborhoodResolutionProbe {
		t.Fatalf("probe resolution = %q/%q, want lozenets/%s", slug, resolution, NeighborhoodResolutionProbe)
	}

	redirectClient := &Client{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPost {
			resp := htmlResponse(http.StatusMovedPermanently, "")
			resp.Header.Set("Location", "https://www.imot.bg/obiavi/prodazhbi/grad-sofiya/lozenets")
			return resp, nil
		}
		return htmlResponse(http.StatusNotFound, "not found"), nil
	})}}
	slug, resolution = redirectClient.resolveNeighborhoodSlugEvidence(SearchParams{City: "София", Neighborhood: "Лозенец"})
	if slug != "lozenets" || resolution != NeighborhoodResolutionRedirect {
		t.Fatalf("redirect resolution = %q/%q, want lozenets/%s", slug, resolution, NeighborhoodResolutionRedirect)
	}

	unverifiedClient := &Client{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPost {
			return htmlResponse(http.StatusOK, "no redirect"), nil
		}
		return htmlResponse(http.StatusNotFound, "not found"), nil
	})}}
	slug, resolution = unverifiedClient.resolveNeighborhoodSlugEvidence(SearchParams{City: "София", Neighborhood: "Лозенец"})
	if slug != "lozenets" || resolution != NeighborhoodResolutionUnverified {
		t.Fatalf("fallback resolution = %q/%q, want lozenets/%s", slug, resolution, NeighborhoodResolutionUnverified)
	}
}

// SearchWithMeta must wire the evidence through, not only ScanListings.
func TestSearchWithMetaReportsCoverageEvidence(t *testing.T) {
	unknownTypeBody := `<div class="SearchInfoLine">показани 1-1 от общо 1 обяви</div>` +
		`<div class="listItem"><div class="zaglavie">` +
		`<a href="//www.imot.bg/obiava-1b177425523801314-barbekyu" class="title">Продава БАРБЕКЮград София, Лозенец</a>` +
		`<location>град София, Лозенец</location>` +
		`<div class="info">65 кв.м, 3-ти ет. от 5, тел.: 0888 123 456</div>` +
		`</div><div class="price">120 000 €</div></div>`

	client := &Client{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return htmlResponse(http.StatusOK, unknownTypeBody), nil
	})}}
	result, err := client.SearchWithMeta(SearchParams{City: "София", Pages: 1})
	if err != nil {
		t.Fatalf("SearchWithMeta returned error: %v", err)
	}
	if !result.TotalCountReported || result.TotalCountCapped {
		t.Errorf("total count evidence = reported %v capped %v, want true/false", result.TotalCountReported, result.TotalCountCapped)
	}
	if result.CardBlocks != 1 || result.UnknownTypeCards != 1 || result.DroppedCards != 0 {
		t.Errorf("card integrity = blocks %d unknown %d dropped %d, want 1/1/0",
			result.CardBlocks, result.UnknownTypeCards, result.DroppedCards)
	}
	if len(result.UnknownTypeSamples) != 1 || result.UnknownTypeSamples[0] != "БАРБЕКЮ" {
		t.Errorf("unknown_type_samples = %#v, want [БАРБЕКЮ]", result.UnknownTypeSamples)
	}
}

// A source that clamps its counter at the result cap says so with "1000+"; the
// envelope must carry that as evidence rather than as a plain 1000.
func TestSearchWithMetaReportsCappedTotalCount(t *testing.T) {
	client := &Client{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return htmlResponse(http.StatusOK, "показани 1-40 от общо 1000+ обяви"), nil
	})}}
	result, err := client.SearchWithMeta(SearchParams{City: "София", Pages: 1})
	if err != nil {
		t.Fatalf("SearchWithMeta returned error: %v", err)
	}
	if result.TotalCount != 1000 || !result.TotalCountReported || !result.TotalCountCapped {
		t.Fatalf("total count evidence = %d reported=%v capped=%v, want 1000/true/true",
			result.TotalCount, result.TotalCountReported, result.TotalCountCapped)
	}
}

// A probed neighbourhood slug is confirmed against the source and reported as
// such.
func TestSearchWithMetaReportsNeighborhoodResolution(t *testing.T) {
	client := &Client{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return htmlResponse(http.StatusOK, "от общо 0 обяви"), nil
	})}}
	result, err := client.SearchWithMeta(SearchParams{City: "София", Neighborhood: "Лозенец", Pages: 1})
	if err != nil {
		t.Fatalf("SearchWithMeta returned error: %v", err)
	}
	if result.NeighborhoodResolution != NeighborhoodResolutionProbe {
		t.Fatalf("neighborhood_resolution = %q, want %q", result.NeighborhoodResolution, NeighborhoodResolutionProbe)
	}
	if result.ResolvedNeighborhoodSlug != "lozenets" {
		t.Fatalf("resolved_neighborhood_slug = %q, want lozenets", result.ResolvedNeighborhoodSlug)
	}
}
