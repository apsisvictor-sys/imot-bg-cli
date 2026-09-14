package scraper

import "testing"

func TestParseDetailExtractsOrderedPhotoURLs(t *testing.T) {
	html := `
<meta property="og:image" content="//cdn3.focus.bg/imot/photosimotbg/1/675//big/1c176754466608675_3d.jpg">
<div id="rezon-gallery">
  <div class="item"><img data-src-gallery="//cdn3.focus.bg/imot/photosimotbg/1/675//big1/1c176754466608675_3d.jpg"></div>
  <div class="item"><img data-src="//cdn3.focus.bg/imot/photosimotbg/1/675//big1/1c176754466608675_4d.jpg"></div>
  <div class="item"><img src="//cdn3.focus.bg/imot/photosimotbg/1/675//big1/1c176754466608675_4d.jpg"></div>
</div>
<div class="recommendation"><img src="//cdn3.focus.bg/imot/photosimotbg/1/675//big1/1c176754466608675_5d.jpg"></div>
`
	detail := ParseDetail(html)
	if detail.PhotoURL != "https://cdn3.focus.bg/imot/photosimotbg/1/675/big1/1c176754466608675_3d.jpg" {
		t.Fatalf("unexpected main photo: %q", detail.PhotoURL)
	}
	if len(detail.PhotoURLs) != 3 {
		t.Fatalf("expected 2 unique photos plus one advertised fallback, got %d: %#v", len(detail.PhotoURLs), detail.PhotoURLs)
	}
	if detail.PhotoURLs[1] != "https://cdn3.focus.bg/imot/photosimotbg/1/675/big/1c176754466608675_3d.jpg" {
		t.Fatalf("unexpected fallback photo: %q", detail.PhotoURLs[1])
	}
	if detail.PhotoURLs[2] != "https://cdn3.focus.bg/imot/photosimotbg/1/675/big1/1c176754466608675_4d.jpg" {
		t.Fatalf("unexpected second photo: %q", detail.PhotoURLs[2])
	}
}
