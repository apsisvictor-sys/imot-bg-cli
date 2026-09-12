package scraper

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The twenty type labels the live Sofia sales search form advertised on
// 2026-09-12, sorted. The results-page navigation links only nineteen of them,
// so the taxonomy reads the search form's labelled type checkboxes instead;
// zemedelska-zemya is advertised there and is part of the source vocabulary.
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
	"zemedelska-zemya",
}

// Hash of wantSofiaTypeSlugs joined by LF and SHA-256 hashed. Recorded so a
// future change to the normalization or the fixture is visible, not silent.
const wantSofiaTaxonomyHash = "a24399854a769cd57744d4deb36575c53f7534fd6aa517e23a71c38131b3fe41"

// wantSofiaTypeLabels are the form's own checkbox labels, in the order the live
// page renders them. Positive tests render a page from this list; a test that
// changes one entry is the page-changed case.
var wantSofiaTypeLabels = []string{
	"1-СТАЕН",
	"2-СТАЕН",
	"3-СТАЕН",
	"4-СТАЕН",
	"МНОГОСТАЕН",
	"МЕЗОНЕТ",
	"АТЕЛИЕ, ТАВАН",
	"ОФИС",
	"МАГАЗИН",
	"ЗАВЕДЕНИЕ",
	"СКЛАД",
	"ХОТЕЛ",
	"ПРОМ. ПОМЕЩЕНИЕ",
	"БИЗНЕС ИМОТ",
	"ЕТАЖ ОТ КЪЩА",
	"КЪЩА",
	"ВИЛА",
	"ПАРЦЕЛ",
	"ГАРАЖ, ПАРКОМЯСТО",
	"ЗЕМЕДЕЛСКА ЗЕМЯ",
}

// taxonomyFormHTML renders the smallest page the parser accepts: the form's
// vigroupsjs marker, one labelled viN checkbox per label, and an f38 location
// select carrying selected. A page built from every wantSofiaTypeLabels entry
// is the page shape the live source serves.
func taxonomyFormHTML(selected string, labels []string) string {
	var b strings.Builder
	b.WriteString(`<form name="search" action="//www.imot.bg/pcgi/imot.cgi">`)
	b.WriteString(`<input type='hidden' id='vigroupsjs' value='{"1":1,"2":1}'>`)
	b.WriteString(`<select class="sw510" name="f38" onchange="ChangeImotDropDown(this)">`)
	b.WriteString(`<option value="">`)
	b.WriteString(`<option value="град Варна">град Варна`)
	if selected == "" {
		b.WriteString(`<option value="град София">град София`)
	} else {
		b.WriteString(fmt.Sprintf(`<option selected value="%s">%s`, selected, selected))
	}
	b.WriteString(`</select>`)
	for i, label := range labels {
		b.WriteString(fmt.Sprintf(`<div id="gr1"><div><label><input type="checkbox" id="vi%d" onclick="javascript:srcvichange(1)">%s</label></div></div>`, i+1, label))
	}
	b.WriteString(`</form>`)
	return b.String()
}

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
	found := false
	for _, slug := range tax.TypeSlugs {
		if slug == "zemedelska-zemya" {
			found = true
		}
	}
	if !found {
		t.Errorf("type_slugs must carry the advertised land type: %v", tax.TypeSlugs)
	}
	for _, forbidden := range []string{"place-za-stroezh", "letovishten-kompleks", "banishora", "lozenets", "p-2"} {
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

// The selected location is the page's own proof of which city it is for. A
// form that selects another city is not this city's taxonomy even when its
// type vocabulary is identical.
func TestParseTaxonomyRejectsAnotherCityForm(t *testing.T) {
	tax, err := ParseTaxonomy(taxonomyFormHTML("град Варна", wantSofiaTypeLabels), TaxonomyParams{
		City:     "София",
		CitySlug: "grad-sofiya",
	})
	if err == nil {
		t.Fatalf("expected an error, got taxonomy %#v", tax)
	}
	if len(tax.TypeSlugs) != 0 {
		t.Errorf("another city's form must not yield slugs: %v", tax.TypeSlugs)
	}
	if !strings.Contains(err.Error(), "град Варна") {
		t.Errorf("error should name the selected city, got %v", err)
	}
}

// A city/slug pair that disagrees is a caller mistake, and the page cannot
// repair it: the payload would name a city its source URL does not.
func TestParseTaxonomyRejectsMismatchedCitySlug(t *testing.T) {
	_, err := ParseTaxonomy(taxonomyFormHTML("град София", wantSofiaTypeLabels), TaxonomyParams{
		City:     "София",
		CitySlug: "grad-varna",
	})
	if err == nil {
		t.Fatal("expected an error for a city and slug that disagree")
	}
}

// An unmapped label means the source advertises a type this repository's
// mapping cannot represent. Publishing the rest would silently hide it, so the
// whole read fails instead.
func TestParseTaxonomyRejectsUnmappedSourceLabel(t *testing.T) {
	labels := append([]string{}, wantSofiaTypeLabels...)
	labels[0] = "ЛЕТОВИЩЕН КОМПЛЕКС"
	tax, err := ParseTaxonomy(taxonomyFormHTML("град София", labels), TaxonomyParams{
		City:     "София",
		CitySlug: "grad-sofiya",
	})
	if err == nil {
		t.Fatalf("expected an error, got taxonomy %#v", tax)
	}
	if len(tax.TypeSlugs) != 0 {
		t.Errorf("an unmapped label must not yield a partial taxonomy: %v", tax.TypeSlugs)
	}
	if !strings.Contains(err.Error(), "ЛЕТОВИЩЕН КОМПЛЕКС") {
		t.Errorf("error should name the unmapped label, got %v", err)
	}
}

// A page that carries the filter marker but fewer labels than the known
// vocabulary is a partial read of a changed page, never a smaller taxonomy.
func TestParseTaxonomyRejectsPartialVocabulary(t *testing.T) {
	tax, err := ParseTaxonomy(taxonomyFormHTML("град София", wantSofiaTypeLabels[:2]), TaxonomyParams{
		City:     "София",
		CitySlug: "grad-sofiya",
	})
	if err == nil {
		t.Fatalf("expected an error, got taxonomy %#v", tax)
	}
	if len(tax.TypeSlugs) != 0 {
		t.Errorf("a partial vocabulary must not yield slugs: %v", tax.TypeSlugs)
	}
}

// A type checkbox the source renders outside a label cannot be named, so its
// type would disappear from the taxonomy without an unmapped label to report.
func TestParseTaxonomyRejectsUnlabelledTypeCheckbox(t *testing.T) {
	html := strings.Replace(
		taxonomyFormHTML("град София", wantSofiaTypeLabels),
		`<label><input type="checkbox" id="vi1" onclick="javascript:srcvichange(1)">`,
		`<span><input type="checkbox" id="vi1" onclick="javascript:srcvichange(1)">`,
		1,
	)
	tax, err := ParseTaxonomy(html, TaxonomyParams{City: "София", CitySlug: "grad-sofiya"})
	if err == nil {
		t.Fatalf("expected an error, got taxonomy %#v", tax)
	}
	if len(tax.TypeSlugs) != 0 {
		t.Errorf("an unlabelled type checkbox must not yield slugs: %v", tax.TypeSlugs)
	}
}

// A page without the type filter is not the search form at all; the parser
// must not fall back to any other navigation on the page.
func TestParseTaxonomyRejectsPageWithoutTypeFilter(t *testing.T) {
	html := strings.Replace(taxonomyFormHTML("град София", wantSofiaTypeLabels), "vigroupsjs", "notthefilter", 1)
	tax, err := ParseTaxonomy(html, TaxonomyParams{City: "София", CitySlug: "grad-sofiya"})
	if err == nil {
		t.Fatalf("expected an error, got taxonomy %#v", tax)
	}
	if len(tax.TypeSlugs) != 0 {
		t.Errorf("a page without the type filter must not yield slugs: %v", tax.TypeSlugs)
	}
}

// The page carries business-type checkboxes and location links whose labels
// are not property types. They share the page, not the taxonomy.
func TestParseTaxonomyIgnoresNonTypeMarkup(t *testing.T) {
	html := taxonomyFormHTML("град София", wantSofiaTypeLabels) +
		`<label><input type="checkbox" class="bstypes" value="19" id="bs19">Вилно селище</label>` +
		`<select name="f31" class="sw260"><option value="0">Етаж<option value="100">Последен</select>` +
		`<div class="locations"><a href="//www.imot.bg/obiavi/prodazhbi/grad-sofiya/banishora">Банишора</a>` +
		`<a href="//www.imot.bg/obiavi/prodazhbi/grad-sofiya/letovishten-kompleks">ваканционен комплекс</a></div>`
	tax, err := ParseTaxonomy(html, TaxonomyParams{City: "София", CitySlug: "grad-sofiya"})
	if err != nil {
		t.Fatalf("ParseTaxonomy returned error: %v", err)
	}
	for _, forbidden := range []string{"vilno-selishte", "banishora", "letovishten-kompleks"} {
		for _, slug := range tax.TypeSlugs {
			if slug == forbidden {
				t.Errorf("non-type markup leaked into type_slugs: %q in %v", forbidden, tax.TypeSlugs)
			}
		}
	}
	if len(tax.TypeSlugs) != len(wantSofiaTypeSlugs) {
		t.Fatalf("type_slugs = %v, want the twenty form types", tax.TypeSlugs)
	}
}

// The explicit taxonomy mapping must cover every recognized source label and
// stay addressable by the CLI's own label table, so a slug the taxonomy
// publishes can always be requested again.
func TestTaxonomyLabelSlugsAreAddressableByTypeMap(t *testing.T) {
	if len(taxonomyLabelSlugs) != len(wantSofiaTypeLabels) {
		t.Fatalf("taxonomyLabelSlugs has %d entries, want %d", len(taxonomyLabelSlugs), len(wantSofiaTypeLabels))
	}
	for _, label := range wantSofiaTypeLabels {
		if _, ok := taxonomyLabelSlugs[label]; !ok {
			t.Errorf("source label %q has no taxonomy mapping", label)
		}
	}
	addressable := make(map[string]bool, len(TypeMap))
	for _, slug := range TypeMap {
		addressable[slug] = true
	}
	for label, slug := range taxonomyLabelSlugs {
		if !addressable[slug] {
			t.Errorf("taxonomy label %q maps to slug %q, which TypeMap cannot address", label, slug)
		}
	}
}

func TestCityPageURLAndSlug(t *testing.T) {
	if got := CitySlug("София"); got != "grad-sofiya" {
		t.Errorf("CitySlug(София) = %q, want grad-sofiya", got)
	}
	if got := CityPageURL("София"); got != "https://www.imot.bg/search/prodazhbi/grad-sofiya" {
		t.Errorf("CityPageURL(София) = %q", got)
	}
	if got := CityPageURL("Няма-такъв-град"); got != "" {
		t.Errorf("CityPageURL(unknown) = %q, want empty", got)
	}
}
