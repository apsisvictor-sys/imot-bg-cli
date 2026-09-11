package scraper

import "testing"

func TestParseDetailExtractsOrderedPhotoURLs(t *testing.T) {
	html := `
<meta property="og:image" content="//cdn3.focus.bg/imot/photosimotbg/1/675/1c176754466608675_3d.jpg">
<img src="//cdn3.focus.bg/imot/photosimotbg/1/675/1c176754466608675_4d.jpg">
<a href="https://cdn3.focus.bg/imot/photosimotbg/1/675/1c176754466608675_5d.jpg">photo</a>
<img src="//cdn3.focus.bg/imot/photosimotbg/1/675/1c176754466608675_4d.jpg">
`
	detail := ParseDetail(html)
	if detail.PhotoURL != "https://cdn3.focus.bg/imot/photosimotbg/1/675/1c176754466608675_3d.jpg" {
		t.Fatalf("unexpected main photo: %q", detail.PhotoURL)
	}
	if len(detail.PhotoURLs) != 3 {
		t.Fatalf("expected 3 unique photos, got %d: %#v", len(detail.PhotoURLs), detail.PhotoURLs)
	}
	if detail.PhotoURLs[1] != "https://cdn3.focus.bg/imot/photosimotbg/1/675/1c176754466608675_4d.jpg" {
		t.Fatalf("unexpected second photo: %q", detail.PhotoURLs[1])
	}
}
