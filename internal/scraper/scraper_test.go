package scraper

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func htmlResponse(status int, body string) *http.Response {
	var buf bytes.Buffer
	writer := transform.NewWriter(&buf, charmap.Windows1251.NewEncoder())
	_, _ = writer.Write([]byte(body))
	_ = writer.Close()
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader(buf.Bytes())),
		Header:     make(http.Header),
	}
}

func TestSearchWithMetaMarksPartialOnLaterPageFailure(t *testing.T) {
	client := &Client{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "/p-2") {
			return htmlResponse(http.StatusInternalServerError, "boom"), nil
		}
		return htmlResponse(http.StatusOK, "от общо 80 обяви"), nil
	})}}

	result, err := client.SearchWithMeta(SearchParams{City: "София", Pages: 0})
	if err != nil {
		t.Fatalf("SearchWithMeta returned error: %v", err)
	}
	if !result.Partial {
		t.Fatal("expected partial result after page 2 failure")
	}
	if result.TotalCount != 80 {
		t.Fatalf("expected total count 80, got %d", result.TotalCount)
	}
	if result.PagesPlanned != 2 || result.PagesFetched != 1 {
		t.Fatalf("unexpected page metadata: planned=%d fetched=%d", result.PagesPlanned, result.PagesFetched)
	}
	if len(result.Errors) != 1 || result.Errors[0].Page != 2 {
		t.Fatalf("expected one page-2 error, got %#v", result.Errors)
	}
}

func TestSearchWithMetaIncludesResolvedNeighborhoodSlug(t *testing.T) {
	client := &Client{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return htmlResponse(http.StatusOK, "от общо 0 обяви"), nil
	})}}

	result, err := client.SearchWithMeta(SearchParams{City: "София", Neighborhood: "Лозенец", Pages: 1})
	if err != nil {
		t.Fatalf("SearchWithMeta returned error: %v", err)
	}
	if result.ResolvedNeighborhoodSlug != "lozenets" {
		t.Fatalf("expected lozenets slug, got %q", result.ResolvedNeighborhoodSlug)
	}
	if result.RequestedNeighborhood != "Лозенец" {
		t.Fatalf("expected requested neighborhood to be preserved, got %q", result.RequestedNeighborhood)
	}
}

func TestTypeMapIncludesSourceBackedCityTypes(t *testing.T) {
	expected := []struct{ label, slug string }{
		{"ателие", "atelie-tavan"},
		{"парцел", "partsel"},
		{"промишлено помещение", "promishleno-pomeshtenie"},
		{"хотел", "hotel"},
		{"бизнес имот", "biznes-imot"},
		{"етаж от къща", "etazh-ot-kashta"},
		{"земеделска земя", "zemedelska-zemya"},
	}
	for _, want := range expected {
		if got := TypeMap[want.label]; got != want.slug {
			t.Errorf("TypeMap[%q] = %q, want %q", want.label, got, want.slug)
		}
	}
	if _, ok := TypeMap["земя"]; !ok {
		t.Fatal("the short земя alias should remain available to callers")
	}
}

func TestParseTotalCount(t *testing.T) {
	if got := ParseTotalCount("показани 1-40 от общо 1 354 обяви"); got != 1354 {
		t.Fatalf("expected 1354, got %d", got)
	}
}

func clientReturning(body string, status int) *Client {
	return &Client{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return htmlResponse(status, body), nil
	})}}
}

func TestFetchDetailNowRejectsChallengePage(t *testing.T) {
	client := clientReturning(`<title>Just a moment...</title><div id="cf-chl-widget">Checking your browser before accessing www.imot.bg.</div>`, http.StatusOK)

	_, err := client.fetchDetailNow(legitDetailURL)
	var de *DetailError
	if !errors.As(err, &de) {
		t.Fatalf("expected *DetailError, got %T: %v", err, err)
	}
	if de.Kind != DetailErrorChallengePage {
		t.Fatalf("kind = %q, want %q", de.Kind, DetailErrorChallengePage)
	}
	if de.RequestedAdvertID != "177425523801314" {
		t.Errorf("requested_advert_id = %q", de.RequestedAdvertID)
	}
}

// A page that carries its own advert structure but no canonical advert identity
// must fail with missing_identity. The old behaviour substituted the requested
// URL, which turned an unknown identity into a confident-looking detail.
func TestFetchDetailNowRejectsMissingIdentityWithoutSubstitutingRequestedURL(t *testing.T) {
	client := clientReturning(`<div class="text">Обява без каноничен адрес в страницата.</div><div class="phone">тел.: 0888 123 456</div>`, http.StatusOK)

	detail, err := client.fetchDetailNow(legitDetailURL)
	var de *DetailError
	if !errors.As(err, &de) {
		t.Fatalf("expected *DetailError, got %T: %v", err, err)
	}
	if de.Kind != DetailErrorMissingIdentity {
		t.Fatalf("kind = %q, want %q", de.Kind, DetailErrorMissingIdentity)
	}
	if de.ObservedAdvertID != "" {
		t.Errorf("observed_advert_id = %q, want empty", de.ObservedAdvertID)
	}
	if detail.URL == legitDetailURL {
		t.Errorf("detail.URL must stay empty, got the substituted requested URL %q", detail.URL)
	}
}

func TestFetchDetailNowRejectsWrongIdentity(t *testing.T) {
	body := `<meta property="og:url" content="` + otherDetailURL + `"><div class="text">Тристаен апартамент в Младост 1, с южно изложение.</div><div class="adParams">Площ: 95 кв.м, Етаж: 6-ти от 8</div>`
	client := clientReturning(body, http.StatusOK)

	_, err := client.fetchDetailNow(legitDetailURL)
	var de *DetailError
	if !errors.As(err, &de) {
		t.Fatalf("expected *DetailError, got %T: %v", err, err)
	}
	if de.Kind != DetailErrorWrongIdentity {
		t.Fatalf("kind = %q, want %q", de.Kind, DetailErrorWrongIdentity)
	}
}

func TestFetchDetailNowReportsHTTPStatusForRemovedAdvert(t *testing.T) {
	client := clientReturning("not found", http.StatusNotFound)

	_, err := client.fetchDetailNow(legitDetailURL)
	var de *DetailError
	if !errors.As(err, &de) {
		t.Fatalf("expected *DetailError, got %T: %v", err, err)
	}
	if de.Kind != DetailErrorFetchFailed {
		t.Fatalf("kind = %q, want %q", de.Kind, DetailErrorFetchFailed)
	}
	if de.HTTPStatus != http.StatusNotFound {
		t.Errorf("http_status = %d, want 404", de.HTTPStatus)
	}
}

func TestFetchDetailNowAcceptsLegitimateAdvert(t *testing.T) {
	body := `<meta property="og:url" content="` + legitDetailURL + `"><div class="text">Просторен двустаен апартамент в кв. Лозенец.</div><div class="adParams">Площ: 72 кв.м, Етаж: 4-ти от 7</div>`
	client := clientReturning(body, http.StatusOK)

	detail, err := client.fetchDetailNow(legitDetailURL)
	if err != nil {
		t.Fatalf("legitimate advert rejected: %v", err)
	}
	if detail.URL != legitDetailURL {
		t.Errorf("url = %q, want %q", detail.URL, legitDetailURL)
	}
	if !strings.Contains(detail.FullDescription, "Лозенец") {
		t.Errorf("full_description = %q", detail.FullDescription)
	}
}
