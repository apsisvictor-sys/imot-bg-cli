package scraper

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The nineteen slugs the live Sofia city page advertised on 2026-09-11,
// sorted. zemedelska-zemya is deliberately absent: the source does not link it.
var wantSofiaTypeSlugs = []string{
	"atelie-tavan",
	"biznes-imot",
	"chetiristaen",
	"dvustaen",
	"ednostaen",
	"etazh-ot-kashta",
	"garazh-parkomyasto",
	"hotel",
	"kashta",
	"magazin",
	"mezonet",
	"mnogostaen",
	"ofis",
	"partsel",
	"promishleno-pomeshtenie",
	"sklad",
	"tristaen",
	"vila",
	"zavedenie",
}

// Hash of wantSofiaTypeSlugs joined by LF and SHA-256 hashed. Recorded so a
// future change to the normalization or the fixture is visible, not silent.
const wantSofiaTaxonomyHash = "34ab9bb4dfff98fefdcb554709e5cf7c1292a1a52cf4adaddf8cc495b5074e11"

func TestParseTaxonomyFromCityPageFixture(t *testing.T) {
	tax, err := ParseTaxonomy(readFixture(t, "city-page.html"), TaxonomyParams{
		City:      "София",
		CitySlug:  "grad-sofiya",
		SourceURL: "city-page.html",
	})
	if err != nil {
		t.Fatalf("ParseTaxonomy returned error: %v", err)
	}

	if tax.ContractVersion != TaxonomyContractVersion {
		t.Errorf("contract_version = %q, want %q", tax.ContractVersion, TaxonomyContractVersion)
	}
	if tax.City != "София" {
		t.Errorf("city = %q, want София", tax.City)
	}
	if tax.SourceURL != "city-page.html" {
		t.Errorf("source_url = %q, want city-page.html", tax.SourceURL)
	}
	if _, err := time.Parse(time.RFC3339, tax.ObservedAt); err != nil {
		t.Errorf("observed_at = %q is not RFC3339: %v", tax.ObservedAt, err)
	}

	if len(tax.TypeSlugs) != len(wantSofiaTypeSlugs) {
		t.Fatalf("type_slugs = %d slugs (%v), want %d", len(tax.TypeSlugs), tax.TypeSlugs, len(wantSofiaTypeSlugs))
	}
	for i, want := range wantSofiaTypeSlugs {
		if tax.TypeSlugs[i] != want {
			t.Fatalf("type_slugs[%d] = %q, want %q (full: %v)", i, tax.TypeSlugs[i], want, tax.TypeSlugs)
		}
	}
	if tax.TaxonomyHash != wantSofiaTaxonomyHash {
		t.Errorf("taxonomy_hash = %q, want %q", tax.TaxonomyHash, wantSofiaTaxonomyHash)
	}
	if tax.TaxonomyHash != TaxonomyHash(tax.TypeSlugs) {
		t.Errorf("taxonomy_hash does not match its own type_slugs")
	}
	for _, forbidden := range []string{"zemedelska-zemya", "place-za-stroezh", "banishora", "lozenets"} {
		for _, slug := range tax.TypeSlugs {
			if slug == forbidden {
				t.Errorf("type_slugs must not contain %q: %v", forbidden, tax.TypeSlugs)
			}
		}
	}
}

// The payload must round-trip through JSON with the exact contract keys, so a
// consumer can rely on the field names rather than on Go struct tags drifting.
func TestTaxonomyJSONFieldNames(t *testing.T) {
	tax, err := ParseTaxonomy(readFixture(t, "city-page.html"), TaxonomyParams{
		City:      "София",
		CitySlug:  "grad-sofiya",
		SourceURL: "city-page.html",
	})
	if err != nil {
		t.Fatalf("ParseTaxonomy returned error: %v", err)
	}
	encoded, err := json.Marshal(tax)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{
		`"contract_version":"` + TaxonomyContractVersion + `"`,
		`"city":"София"`,
		`"source_url":"city-page.html"`,
		`"observed_at":"`,
		`"type_slugs":[`,
		`"taxonomy_hash":"` + wantSofiaTaxonomyHash + `"`,
	} {
		if !strings.Contains(string(encoded), key) {
			t.Errorf("JSON missing %s: %s", key, encoded)
		}
	}
}

func TestTaxonomyHashKnownValue(t *testing.T) {
	// sha256("a\nb"), sorted and deduplicated from {"b","a","a"}.
	const want = "7e18f737311b2dc3b2f269dd78396b0351f14fb66efa879f768cb23181883c78"
	if got := TaxonomyHash([]string{"b", "a", "a"}); got != want {
		t.Fatalf("TaxonomyHash = %q, want %q", got, want)
	}
}

func TestSortedUniqueTaxonomySlugs(t *testing.T) {
	got := SortedUniqueTaxonomySlugs([]string{" Dvustaen ", "ednostaen", "dvustaen", "", "dvustaen"})
	want := []string{"dvustaen", "ednostaen"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestParseTaxonomyRejectsUnreadablePage(t *testing.T) {
	tax, err := ParseTaxonomy(readFixture(t, "city-page-unreadable.html"), TaxonomyParams{
		City:     "София",
		CitySlug: "grad-sofiya",
	})
	if err == nil {
		t.Fatalf("expected an error, got taxonomy %#v", tax)
	}
	if len(tax.TypeSlugs) != 0 {
		t.Errorf("a rejected page must not yield slugs: %v", tax.TypeSlugs)
	}
}

func TestParseTaxonomyRejectsChallengePage(t *testing.T) {
	tax, err := ParseTaxonomy(readFixture(t, "detail-challenge.html"), TaxonomyParams{
		City:     "София",
		CitySlug: "grad-sofiya",
	})
	if err == nil {
		t.Fatalf("expected an error, got taxonomy %#v", tax)
	}
	if len(tax.TypeSlugs) != 0 {
		t.Errorf("a challenge page must not yield slugs: %v", tax.TypeSlugs)
	}
}

// An unmapped source slug is exactly what the collector's map correction needs
// to see, so the parser must surface it instead of filtering to TypeMap.
func TestParseTaxonomySurfacesUnknownSourceSlug(t *testing.T) {
	html := `<div class="typeList">
	<a href="//www.imot.bg/obiavi/prodazhbi/grad-sofiya/dvustaen">2-стаен</a>
	<a href="//www.imot.bg/obiavi/prodazhbi/grad-sofiya/letovishten-kompleks">ваканционен комплекс</a>
</div>`
	tax, err := ParseTaxonomy(html, TaxonomyParams{City: "София", CitySlug: "grad-sofiya"})
	if err != nil {
		t.Fatalf("ParseTaxonomy returned error: %v", err)
	}
	found := false
	for _, slug := range tax.TypeSlugs {
		if slug == "letovishten-kompleks" {
			found = true
		}
	}
	if !found {
		t.Fatalf("unknown source slug was filtered out: %v", tax.TypeSlugs)
	}
}

func TestParseTaxonomyRejectsNavigationForAnotherCity(t *testing.T) {
	html := `<div class="typeList">
	<a href="//www.imot.bg/obiavi/prodazhbi/grad-varna/dvustaen">2-стаен</a>
	<a href="//www.imot.bg/obiavi/prodazhbi/grad-varna/tristaen">3-стаен</a>
</div>`
	tax, err := ParseTaxonomy(html, TaxonomyParams{City: "София", CitySlug: "grad-sofiya"})
	if err == nil {
		t.Fatalf("expected an error, got taxonomy %#v", tax)
	}
	if len(tax.TypeSlugs) != 0 {
		t.Errorf("another city's links must not yield slugs: %v", tax.TypeSlugs)
	}
}

func TestParseTaxonomyReadsTypeSelect(t *testing.T) {
	html := `<select name="type">
	<option value="">Всички</option>
	<option value="dvustaen">2-стаен</option>
	<option value="/obiavi/prodazhbi/grad-sofiya/tristaen">3-стаен</option>
</select>`
	tax, err := ParseTaxonomy(html, TaxonomyParams{City: "София", CitySlug: "grad-sofiya"})
	if err != nil {
		t.Fatalf("ParseTaxonomy returned error: %v", err)
	}
	if len(tax.TypeSlugs) != 2 || tax.TypeSlugs[0] != "dvustaen" || tax.TypeSlugs[1] != "tristaen" {
		t.Fatalf("type_slugs = %v, want [dvustaen tristaen]", tax.TypeSlugs)
	}
}

func TestParseTaxonomyIgnoresNonCityAndPaginationLinks(t *testing.T) {
	html := `<div class="typeList">
	<a href="//www.imot.bg/obiavi/prodazhbi/grad-sofiya/dvustaen">2-стаен</a>
	<a href="//www.imot.bg/obiavi/prodazhbi/grad-varna/tristaen">Варна</a>
	<a href="//www.imot.bg/obiavi/prodazhbi/grad-sofiya/p-2">2</a>
	<a href="//www.imot.bg/obiavi/naemi/grad-sofiya/mezonet">мезонет под наем</a>
</div>`
	tax, err := ParseTaxonomy(html, TaxonomyParams{City: "София", CitySlug: "grad-sofiya"})
	if err != nil {
		t.Fatalf("ParseTaxonomy returned error: %v", err)
	}
	if len(tax.TypeSlugs) != 2 || tax.TypeSlugs[0] != "dvustaen" || tax.TypeSlugs[1] != "mezonet" {
		t.Fatalf("type_slugs = %v, want [dvustaen mezonet]", tax.TypeSlugs)
	}
}

func TestCityPageURLAndSlug(t *testing.T) {
	if got := CitySlug("София"); got != "grad-sofiya" {
		t.Errorf("CitySlug(София) = %q, want grad-sofiya", got)
	}
	if got := CityPageURL("София"); got != "https://www.imot.bg/obiavi/prodazhbi/grad-sofiya" {
		t.Errorf("CityPageURL(София) = %q", got)
	}
	if got := CityPageURL("Няма-такъв-град"); got != "" {
		t.Errorf("CityPageURL(unknown) = %q, want empty", got)
	}
}
