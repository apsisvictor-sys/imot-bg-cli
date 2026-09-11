package scraper

import (
	"bytes"
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

func TestTypeMapIncludesAtelie(t *testing.T) {
	if TypeMap["ателие"] == "" {
		t.Fatal("expected ателие type mapping")
	}
}

func TestParseTotalCount(t *testing.T) {
	if got := ParseTotalCount("показани 1-40 от общо 1 354 обяви"); got != 1354 {
		t.Fatalf("expected 1354, got %d", got)
	}
}
