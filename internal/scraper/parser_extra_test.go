package scraper

import (
	"net/http"
	"path"
	"strings"
	"testing"
)

func TestExtractTypeHandlesRentalTitles(t *testing.T) {
	cases := map[string]string{
		"Дава под Наем 2-СТАЕНград София, Яворов": "2-СТАЕН",
		"Дава под наем МЕЗОНЕТград София":         "МЕЗОНЕТ",
		"Продава 1-СТАЕНград София, Лозенец":      "1-СТАЕН",
		"Се отдава ОФИСград Варна":                "ОФИС",
	}
	for title, want := range cases {
		if got := extractType(title); got != want {
			t.Errorf("extractType(%q) = %q, want %q", title, got, want)
		}
	}
}

const detailFixture = `
<div class="text">iHOME REAL ESTATE отдава под наем двустаен апартамент в кв. 'Яворов', обзаведен, скоро освежен и ремонтиран.</div>
<div class="adParams">
  <div>Площ:<br/><strong>47 m<sup>2</sup></strong></div>
  <div>Етаж:<br/><strong>4-ти от 7</strong></div>
</div>
<div>Не се начислява ДДС</div>
<div class="info">
  <div>Публикувана в 11:20 на 26 август, 2026 год.</div>
  Обявата е посетена <span>65</span> пъти.
</div>
<div class="borderBox MB20">
  <div class="carExtri">
    <span class="Title">Особености</span><br/>
    <div class="items">
      <div>Обзаведен</div>
      <div>Асансьор</div>
      <div>СОТ</div>
      <div>Климатик</div>
      <div>Окабеляване</div>
    </div>
  </div>
</div>
<div class="dealer2023 borderBox MB20">
  <div>
    <div class="type">Брокер:</div>
    <div class="name">Daniela Dobreva</div>
    <div class="phone"><small>+359898214671</small></div>
  </div>
  <div class="region">
    Офис: ул. Отец Паисий  15, ет. 3, офис 9
  </div>
</div>
`

func TestParseDetailExtractsFeaturesBrokerPublishedVat(t *testing.T) {
	d := ParseDetail(detailFixture)

	wantFeatures := []string{"Обзаведен", "Асансьор", "СОТ", "Климатик", "Окабеляване"}
	if len(d.Features) != len(wantFeatures) {
		t.Fatalf("features = %#v, want %v", d.Features, wantFeatures)
	}
	for i, f := range wantFeatures {
		if d.Features[i] != f {
			t.Fatalf("features = %#v, want %v", d.Features, wantFeatures)
		}
	}
	if d.PublishedAt != "2026-08-26T11:20:00+03:00" {
		t.Errorf("published_at = %q", d.PublishedAt)
	}
	if d.BrokerName != "Daniela Dobreva" {
		t.Errorf("broker_name = %q", d.BrokerName)
	}
	if d.BrokerPhone != "+359898214671" {
		t.Errorf("broker_phone = %q", d.BrokerPhone)
	}
	if !strings.Contains(d.AgencyOffice, "Отец Паисий") {
		t.Errorf("agency_office = %q", d.AgencyOffice)
	}
	if d.VatNote != "Не се начислява ДДС" {
		t.Errorf("vat_note = %q", d.VatNote)
	}
}

func TestUniquePhotoURLsDedupsSizeVariants(t *testing.T) {
	html := `
<meta property="og:image" content="//cdn3.focus.bg/imot/photosimotbg/2/768//big/2c178773245606768_e1.jpg">
<img src="//cdn3.focus.bg/imot/photosimotbg/2/768//big1/2c178773245606768_e1.jpg">
<img src="//cdn3.focus.bg/imot/photosimotbg/2/768//big1/2c178773245606768_mu.jpg">
`
	photos := uniquePhotoURLs(html)
	if len(photos) != 2 {
		t.Fatalf("expected 2 unique photos, got %d: %#v", len(photos), photos)
	}
	if !strings.Contains(photos[0], "/big/2c178773245606768_e1.jpg") {
		t.Fatalf("expected /big/ variant preferred, got %q", photos[0])
	}
}

func TestComputeStatsDistributionAndSplit(t *testing.T) {
	listings := []Listing{
		{PriceEUR: 500, SizeSqM: 50},
		{PriceEUR: 660, SizeSqM: 60, Agency: "ГОЛД ЕСТЕЙТ ЕООД"},
		{PriceEUR: 720, SizeSqM: 60},
		{PriceEUR: 780, SizeSqM: 60, Agency: "АДРЕС"},
	}
	s := ComputeStats(listings)
	if s.Count != 4 || s.PricedCount != 4 {
		t.Fatalf("counts = %d/%d, want 4/4", s.Count, s.PricedCount)
	}
	if s.MeanEUR != 665 || s.MedianEUR != 690 {
		t.Errorf("mean/median = %.1f/%.1f, want 665/690", s.MeanEUR, s.MedianEUR)
	}
	if s.P25EUR != 620 || s.P75EUR != 735 {
		t.Errorf("p25/p75 = %.1f/%.1f, want 620/735", s.P25EUR, s.P75EUR)
	}
	if s.MinEUR != 500 || s.MaxEUR != 780 {
		t.Errorf("min/max = %d/%d, want 500/780", s.MinEUR, s.MaxEUR)
	}
	if s.MedianEURPerSqM != 11.5 {
		t.Errorf("median €/m² = %.1f, want 11.5", s.MedianEURPerSqM)
	}
	if s.AgencyCount != 2 || s.PrivateCount != 2 {
		t.Errorf("agency/private = %d/%d, want 2/2", s.AgencyCount, s.PrivateCount)
	}
}

func TestToSlimListingsProjection(t *testing.T) {
	long := strings.Repeat("а", 300)
	listings := []Listing{
		{ID: "x1", Type: "2-СТАЕН", Neighborhood: "Яворов", PriceEUR: 600, SizeSqM: 50,
			Description: long, Agency: "АЙ ХОУМ", URL: "https://www.imot.bg/obiava-x1"},
		{ID: "x2", Type: "2-СТАЕН", Neighborhood: "Яворов", PriceEUR: 500, SizeSqM: 0,
			Phone: "0888111222", URL: "https://www.imot.bg/obiava-x2"},
	}
	slim := ToSlimListings(listings)
	if len(slim) != 2 {
		t.Fatalf("len = %d, want 2", len(slim))
	}
	first := slim[0]
	if first.Seller != "agency" || first.PricePerSqM != 12 {
		t.Errorf("first = %+v", first)
	}
	if got := len([]rune(first.Snippet)); got != 120 {
		t.Errorf("snippet len = %d, want 120", got)
	}
	second := slim[1]
	if second.Seller != "private" || second.PricePerSqM != 0 {
		t.Errorf("second = %+v", second)
	}
}

func TestFetchDetailsConcurrentEnrichesAllListings(t *testing.T) {
	detailHTML := func(id string) string {
		return `<meta property="og:url" content="https://www.imot.bg/obiava-` + id + `"><div class="text">Описание ` + id + `</div>`
	}
	listings := []Listing{
		{ID: "a1", URL: "https://www.imot.bg/obiava-a1"},
		{ID: "b2", URL: "https://www.imot.bg/obiava-b2"},
		{ID: "c3", URL: "https://www.imot.bg/obiava-c3"},
	}
	client := &Client{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		id := path.Base(req.URL.Path)
		return htmlResponse(http.StatusOK, detailHTML(id)), nil
	})}}

	// The pool must not inherit the 8s single-detail sleep: this test would
	// take ~24s serially and only ~2s with the 2s concurrent spacing.
	errs := client.FetchDetailsConcurrent(listings)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("listing %d failed: %v", i, err)
		}
	}
	for i, l := range listings {
		if l.Detail == nil || !strings.Contains(l.Detail.FullDescription, l.ID) {
			t.Errorf("listing %d not enriched: %+v", i, l.Detail)
		}
	}
}
