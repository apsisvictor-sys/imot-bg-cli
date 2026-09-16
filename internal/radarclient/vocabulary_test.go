package radarclient

import (
	"strings"
	"testing"
)

func TestCanonicalPropertyTypes(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  []string
	}{
		{"source label", "2-стаен", []string{"2-СТАЕН"}},
		{"already canonical", "2-СТАЕН", []string{"2-СТАЕН"}},
		{"mixed case with spacing", "  Двустаен ", []string{"2-СТАЕН"}},
		{"source label with a comma", "АТЕЛИЕ, ТАВАН", []string{"АТЕЛИЕ"}},
		{"attic short form", "таван", []string{"АТЕЛИЕ"}},
		{"spelled out industrial", "промишлено помещение", []string{"ПРОМИШЛЕНО"}},
		{"abbreviated industrial", "пром. помещение", []string{"ПРОМИШЛЕНО"}},
		{"house floor", "етаж от къща", []string{"ЕТАЖ"}},
		{"land", "земеделска земя", []string{"ЗЕМЯ"}},
		{"garage spans two canonical values", "гараж", []string{"ГАРАЖ", "ПАРКОМЯСТО"}},
		{"parking space", "паркомясто", []string{"ПАРКОМЯСТО"}},
		{"business grouping", "бизнес имот", []string{"БИЗНЕС ИМОТ"}},
		{"plot", "парцел", []string{"ПАРЦЕЛ"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := CanonicalPropertyTypes(tc.value)
			if !ok {
				t.Fatalf("CanonicalPropertyTypes(%q) did not resolve", tc.value)
			}
			if !stringSlicesEqual(got, tc.want) {
				t.Fatalf("CanonicalPropertyTypes(%q) = %#v, want %#v", tc.value, got, tc.want)
			}
		})
	}

	if _, ok := CanonicalPropertyTypes("фитнес зала"); ok {
		t.Error("a label outside the catalogue must not canonicalize")
	}
	if _, ok := CanonicalPropertyTypes(""); ok {
		t.Error("an empty label must not canonicalize")
	}
}

func TestCanonicalPropertyTypesReturnsACopy(t *testing.T) {
	first, _ := CanonicalPropertyTypes("гараж")
	first[0] = "tampered"
	second, _ := CanonicalPropertyTypes("гараж")
	if second[0] != "ГАРАЖ" {
		t.Fatalf("the table was mutated through a returned slice: %#v", second)
	}
}

func TestCanonicalPropertyTypeList(t *testing.T) {
	got, err := CanonicalPropertyTypeList([]string{"2-стаен", "2-СТАЕН", "офис"})
	if err != nil {
		t.Fatalf("CanonicalPropertyTypeList: %v", err)
	}
	if !stringSlicesEqual(got, []string{"2-СТАЕН", "ОФИС"}) {
		t.Fatalf("list = %#v", got)
	}

	if _, err := CanonicalPropertyTypeList([]string{"няма такъв"}); err == nil {
		t.Fatal("expected an error for an unknown label")
	} else if !strings.Contains(err.Error(), "2-стаен") {
		t.Errorf("the error should name a supported label: %v", err)
	}
	if _, err := CanonicalPropertyTypeList([]string{"   "}); err == nil {
		t.Fatal("expected an error for an empty label")
	}
	if _, err := CanonicalPropertyTypeList(nil); err != nil {
		t.Fatalf("an empty list is valid: %v", err)
	}
}

func TestPropertyTypeLabelsAreUniqueAndComplete(t *testing.T) {
	labels := PropertyTypeLabels()
	if len(labels) != 20 {
		t.Fatalf("labels = %d, want the source's twenty", len(labels))
	}
	seen := make(map[string]bool, len(labels))
	for _, label := range labels {
		if seen[label] {
			t.Errorf("duplicate label %q", label)
		}
		seen[label] = true
		if _, ok := CanonicalPropertyTypes(label); !ok {
			t.Errorf("advertised label %q does not canonicalize", label)
		}
	}
}

func TestNormalizeNeighborhoods(t *testing.T) {
	got, err := NormalizeNeighborhoods([]string{" yavorov ", "yavorov", "lozenets", ""})
	if err == nil {
		t.Fatalf("an empty slug must be rejected, got %#v", got)
	}
	got, err = NormalizeNeighborhoods([]string{" yavorov ", "yavorov", "lozenets"})
	if err != nil {
		t.Fatalf("NormalizeNeighborhoods: %v", err)
	}
	if !stringSlicesEqual(got, []string{"yavorov", "lozenets"}) {
		t.Fatalf("slugs = %#v", got)
	}
	if _, err := NormalizeNeighborhoods([]string{strings.Repeat("a", MaxIdentifierLength+1)}); err == nil {
		t.Fatal("expected an error for an over-long slug")
	}
	if empty, err := NormalizeNeighborhoods(nil); err != nil || len(empty) != 0 {
		t.Fatalf("nil input = %#v, %v", empty, err)
	}
}

func TestNormalizeGroups(t *testing.T) {
	got, err := NormalizeGroups([]string{"supported", "Possible", "supported", " excluded "})
	if err != nil {
		t.Fatalf("NormalizeGroups: %v", err)
	}
	if !stringSlicesEqual(got, []string{"supported", "possible", "excluded"}) {
		t.Fatalf("groups = %#v", got)
	}
	if empty, err := NormalizeGroups(nil); err != nil || len(empty) != 0 {
		t.Fatalf("an empty group list should stay empty for the API default: %#v, %v", empty, err)
	}
	if _, err := NormalizeGroups([]string{"everything"}); err == nil {
		t.Fatal("expected an error for an unknown group")
	}
}

func TestNormalizeMissingFieldPolicy(t *testing.T) {
	cases := map[string]string{
		"":         "",
		"possible": PolicyPossible,
		" EXCLUDE": PolicyExclude,
	}
	for input, want := range cases {
		got, err := NormalizeMissingFieldPolicy(input)
		if err != nil {
			t.Fatalf("NormalizeMissingFieldPolicy(%q): %v", input, err)
		}
		if got != want {
			t.Errorf("NormalizeMissingFieldPolicy(%q) = %q, want %q", input, got, want)
		}
	}
	if _, err := NormalizeMissingFieldPolicy("maybe"); err == nil {
		t.Fatal("expected an error for an unknown policy")
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
