package radarclient

import (
	"fmt"
	"strings"
)

// Result groups. They are the wire vocabulary; the default of the CLI and of
// the MCP tools is supported plus possible, because an excluded row is a
// deliberate mismatch rather than a candidate.
const (
	GroupSupported = "supported"
	GroupPossible  = "possible"
	GroupExcluded  = "excluded"
)

// Missing-field policies: how an unresolved property field (an unparseable
// floor) is treated. possible keeps the advert discoverable in the possible
// group, exclude drops it.
const (
	PolicyPossible = "possible"
	PolicyExclude  = "exclude"
)

// MaxIdentifierLength mirrors the wire's ceiling for a neighbourhood slug, a
// property type or a listing id.
const MaxIdentifierLength = 128

// propertyTypeAliases maps every lowercase spelling the property-type filter
// accepts to the canonical values the Radar listing projection stores in
// `propertyType`. The keys mirror the source's own type labels (the labels of
// PROPERTY_TYPE_SLUGS in the Radar collector); the values are the canonical
// types the imot.bg parser emits on a row, which is what the Radar database
// stores and what the API's `propertyType = ANY(...)` filter therefore needs.
//
// Most labels are their own uppercase value. The exceptions are the labels
// whose card titles the source writes differently, and each follows the
// parser's canonical table so a filter value can never name a type no row
// carries:
//
//	ателие                 -> АТЕЛИЕ   (source label "АТЕЛИЕ, ТАВАН")
//	гараж                  -> ГАРАЖ and ПАРКОМЯСТО ("ГАРАЖ, ПАРКОМЯСТО")
//	промишлено помещение   -> ПРОМИШЛЕНО ("ПРОМ. ПОМЕЩЕНИЕ")
//	етаж от къща           -> ЕТАЖ
//	земеделска земя        -> ЗЕМЯ
//
// "бизнес имот" is the source's grouping label: rows under it normally carry a
// subtype (АПТЕКА, КЛИНИКА, ...), so filtering by БИЗНЕС ИМОТ matches only the
// rows the parser could not sub-classify. The table states that honestly
// rather than inventing a subtype list here.
var propertyTypeAliases = map[string][]string{
	"1-стаен":           {"1-СТАЕН"},
	"едностаен":         {"1-СТАЕН"},
	"2-стаен":           {"2-СТАЕН"},
	"двустаен":          {"2-СТАЕН"},
	"3-стаен":           {"3-СТАЕН"},
	"тристаен":          {"3-СТАЕН"},
	"4-стаен":           {"4-СТАЕН"},
	"четиристаен":       {"4-СТАЕН"},
	"многостаен":        {"МНОГОСТАЕН"},
	"мезонет":           {"МЕЗОНЕТ"},
	"къща":              {"КЪЩА"},
	"вила":              {"ВИЛА"},
	"офис":              {"ОФИС"},
	"магазин":           {"МАГАЗИН"},
	"заведение":         {"ЗАВЕДЕНИЕ"},
	"склад":             {"СКЛАД"},
	"гараж":             {"ГАРАЖ", "ПАРКОМЯСТО"},
	"гараж, паркомясто": {"ПАРКОМЯСТО", "ГАРАЖ"},
	"паркомясто":        {"ПАРКОМЯСТО"},
	"ателие":            {"АТЕЛИЕ"},
	"ателие, таван":     {"АТЕЛИЕ"},
	"таван":             {"АТЕЛИЕ"},
	"парцел":            {"ПАРЦЕЛ"},
	"промишлено помещение": {"ПРОМИШЛЕНО"},
	"пром. помещение":      {"ПРОМИШЛЕНО"},
	"промишлено":           {"ПРОМИШЛЕНО"},
	"хотел":                {"ХОТЕЛ"},
	"бизнес имот":          {"БИЗНЕС ИМОТ"},
	"етаж от къща":         {"ЕТАЖ"},
	"етаж":                 {"ЕТАЖ"},
	"земеделска земя":      {"ЗЕМЯ"},
	"земя":                 {"ЗЕМЯ"},
}

// PropertyTypeLabels returns the source labels the property-type filter
// accepts, in the order the source's own search form advertises them. It is
// the list an error message shows a caller.
func PropertyTypeLabels() []string {
	return []string{
		"1-стаен", "2-стаен", "3-стаен", "4-стаен", "многостаен", "мезонет",
		"къща", "вила", "офис", "магазин", "заведение", "склад", "гараж",
		"ателие", "парцел", "промишлено помещение", "хотел", "бизнес имот",
		"етаж от къща", "земеделска земя",
	}
}

// CanonicalPropertyTypes resolves one caller-supplied property-type label,
// case-insensitively, to the canonical value(s) the API filter needs. A label
// whose meaning the source splits across two stored values (гараж) resolves to
// both. The second result is false for a label the catalogue does not know.
func CanonicalPropertyTypes(label string) ([]string, bool) {
	values, ok := propertyTypeAliases[propertyTypeKey(label)]
	if !ok {
		return nil, false
	}
	return append([]string(nil), values...), true
}

// CanonicalPropertyTypeList canonicalizes a whole caller list, dropping
// duplicates and rejecting a label the catalogue does not know. It is the one
// place the CLI and the MCP tools share, so their vocabularies cannot drift.
func CanonicalPropertyTypeList(values []string) ([]string, error) {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			return nil, fmt.Errorf("a radar property type must not be empty")
		}
		canonical, ok := CanonicalPropertyTypes(value)
		if !ok {
			return nil, fmt.Errorf("unknown property type %q; supported labels: %s",
				oneLine(value), strings.Join(PropertyTypeLabels(), ", "))
		}
		for _, item := range canonical {
			if seen[item] {
				continue
			}
			seen[item] = true
			out = append(out, item)
		}
	}
	return out, nil
}

// NormalizeNeighborhoods trims and de-duplicates catalogue slugs, rejecting an
// empty or over-long value. Membership is the API's to decide: the client
// cannot know the catalogue, and an unknown slug comes back as a typed
// invalid_query rather than as an empty result.
func NormalizeNeighborhoods(slugs []string) ([]string, error) {
	out := make([]string, 0, len(slugs))
	seen := make(map[string]bool, len(slugs))
	for _, raw := range slugs {
		slug := strings.TrimSpace(raw)
		if slug == "" {
			return nil, fmt.Errorf("a radar neighbourhood must not be empty")
		}
		if len(slug) > MaxIdentifierLength {
			return nil, fmt.Errorf("a radar neighbourhood must be at most %d characters", MaxIdentifierLength)
		}
		if seen[slug] {
			continue
		}
		seen[slug] = true
		out = append(out, slug)
	}
	return out, nil
}

// NormalizeGroups validates and de-duplicates result groups, preserving the
// caller's order. An empty input stays empty, which leaves the API's own
// default (all three groups) in force; consumers that want a narrower default
// set it before calling.
func NormalizeGroups(groups []string) ([]string, error) {
	out := make([]string, 0, len(groups))
	seen := make(map[string]bool, len(groups))
	for _, raw := range groups {
		group := strings.ToLower(strings.TrimSpace(raw))
		if group == "" {
			continue
		}
		switch group {
		case GroupSupported, GroupPossible, GroupExcluded:
		default:
			return nil, fmt.Errorf("unsupported radar result group %q; use supported, possible or excluded", oneLine(raw))
		}
		if seen[group] {
			continue
		}
		seen[group] = true
		out = append(out, group)
	}
	return out, nil
}

// NormalizeMissingFieldPolicy validates the policy value. An empty input stays
// empty, which leaves the API's own default (possible) in force.
func NormalizeMissingFieldPolicy(policy string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(policy))
	switch normalized {
	case "", PolicyPossible, PolicyExclude:
		return normalized, nil
	default:
		return "", fmt.Errorf("unsupported missing-field policy %q; use possible or exclude", oneLine(policy))
	}
}

func propertyTypeKey(label string) string {
	return strings.ToLower(strings.Join(strings.Fields(label), " "))
}

// oneLine bounds a caller-supplied value for an error message: no newlines,
// no unbounded echo.
func oneLine(value string) string {
	collapsed := strings.Join(strings.Fields(value), " ")
	runes := []rune(collapsed)
	if len(runes) > 120 {
		return string(runes[:120]) + "..."
	}
	return collapsed
}
