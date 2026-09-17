package scraper

import (
	"reflect"
	"testing"
)

func floorIntPtr(value int) *int { return &value }

func TestBuildURLWithSlugUsesSourceFloorBounds(t *testing.T) {
	params := SearchParams{
		City:         "София",
		Type:         "2-стаен",
		Neighborhood: "Малинова долина",
		FloorFrom:    floorIntPtr(3),
		FloorTo:      floorIntPtr(49),
	}

	got := buildURLWithSlug(params, "malinova-dolina", 2)
	want := "https://www.imot.bg/obiavi/prodazhbi/grad-sofiya/malinova-dolina/dvustaen/p-2?floor_from=3&floor_to=49"
	if got != want {
		t.Fatalf("buildURLWithSlug() = %q, want %q", got, want)
	}
}

func TestSearchFiltersReportsFloorAsSourceSide(t *testing.T) {
	params := SearchParams{City: "София", Type: "2-стаен", FloorTo: floorIntPtr(2)}
	server, client := SearchFilters(params, "malinova-dolina")
	if !reflect.DeepEqual(server, []string{"city", "neighborhood", "type", "floor"}) {
		t.Fatalf("server filters = %#v", server)
	}
	if len(client) != 0 {
		t.Fatalf("client filters = %#v, want none", client)
	}

	support := ServerFilterSupport()
	if !reflect.DeepEqual(support, []string{"city", "neighborhood", "type", "floor"}) {
		t.Fatalf("server filter support = %#v", support)
	}
}

func TestBuildURLWithSlugOmitsUnsetFloorBounds(t *testing.T) {
	params := SearchParams{City: "София", Type: "2-стаен"}
	got := buildURLWithSlug(params, "malinova-dolina", 1)
	want := "https://www.imot.bg/obiavi/prodazhbi/grad-sofiya/malinova-dolina/dvustaen/"
	if got != want {
		t.Fatalf("buildURLWithSlug() = %q, want %q", got, want)
	}
}
