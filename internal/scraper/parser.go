package scraper

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	stdhtml "html"
	neturl "net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	reTotalCount = regexp.MustCompile(`от общо\s+([0-9 ]+)(\+?)\s+обяви`)
	// reNoResults is imot.bg's explicit zero-match marker, verified live on a
	// genuinely empty search: <div class="SearchInfoLine"> Няма намерени обяви - Продава</div>.
	reNoResults  = regexp.MustCompile(`Няма намерени обяви`)
	reMaxPages   = 50 // safety cap when total count is unknown
	rePriceEUR   = regexp.MustCompile(`([0-9 ]+) €`)
	rePriceBGN   = regexp.MustCompile(`([0-9 ]+\.[0-9]+) лв`)
	reSqM        = regexp.MustCompile(`^(\d+)\s*кв\.м`)
	reFloor      = regexp.MustCompile(`^(\d+)-(?:ви|ри|ти|ми) ет\.\s*от\s*(\d+)`)
	reFloorShort = regexp.MustCompile(`^(\d+)-(?:ви|ри|ти|ми) ет\.`)
	reParter     = regexp.MustCompile(`^Партер\s+от\s*(\d+)`)
	reLevel      = regexp.MustCompile(`^ниво (-?\d+)\s*от\s*(\d+)`)
	reYearExact  = regexp.MustCompile(`Въведен в експлоатация\s*(\d{4})\s*г\.`)
	reYearRange  = regexp.MustCompile(`Въведен в експлоатация\s*(\d{4})\s*-\s*(\d{4})\s*г\.`)
	reYearNot    = regexp.MustCompile(`Не е въведен в експлоатация`)
	rePhone      = regexp.MustCompile(`тел\.:?\s*([0-9\s]+)`)
	reListingID  = regexp.MustCompile(`/obiava-([a-z0-9]+)-`)
	reLocation   = regexp.MustCompile(`<location>(.*?)</location>`)
	reTitle      = regexp.MustCompile(`<a[^>]*class="title[^"]*"[^>]*>(.*?)</a>`)
	reURL        = regexp.MustCompile(`href="(//www\.imot\.bg/obiava-[^"]*)"`)
	reAgency     = regexp.MustCompile(`class="name">\s*<a[^>]*>(.*?)</a>`)

	// Detail page patterns
	reDetailText        = regexp.MustCompile(`(?s)class="text"[^>]*>(.*?)</div>`)
	reDetailParams      = regexp.MustCompile(`class="params"[^>]*>(.*?)</div>`)
	reDetailPhone       = regexp.MustCompile(`(?s)class="phone[^"]*"[^>]*>(.*?)</div>`)
	reDetailAgencyURL   = regexp.MustCompile(`(?s)class="url"[^>]*>(.*?)</div>`)
	reDetailOGURL       = regexp.MustCompile(`property="og:url" content="([^"]+)"`)
	reDetailViewCount   = regexp.MustCompile(`Обявата е посетена\s*<span>\s*([0-9 ]+)\s*</span>\s*пъти`)
	reDetailCorrectedAt = regexp.MustCompile(`Коригирана в\s*([0-9]{1,2}):([0-9]{2})\s*на\s*([0-9]{1,2})\s+([^,]+),\s*([0-9]{4})\s*год\.`)
	reDetailPublishedAt = regexp.MustCompile(`Публикувана в\s*([0-9]{1,2}):([0-9]{2})\s*на\s*([0-9]{1,2})\s+([^,<]+),\s*([0-9]{4})\s*год`)
	reFeaturesBlock     = regexp.MustCompile(`(?s)Особености</span>.*?<div class="items">(.*?</div>)\s*</div>`)
	reFeatureItem       = regexp.MustCompile(`<div>\s*([^<]+?)\s*</div>`)
	reBrokerName        = regexp.MustCompile(`Брокер:</div>\s*<div class="name">\s*([^<]+?)\s*</div>`)
	reBrokerPhone       = regexp.MustCompile(`(?s)Брокер:</div>.*?<div class="phone">\s*(?:<small>)?\s*([^<\s][^<]*?)\s*(?:</small>)?\s*</div>`)
	reAgencyOffice      = regexp.MustCompile(`Офис:\s*([^<\n]+?)\s*(?:</div>|\n)`)
	reVatNote           = regexp.MustCompile(`Не се начислява ДДС`)
	reParterDetail      = regexp.MustCompile(`(?i)партер`)

	// Primary advert layout, gallery and structured data. The gallery is the
	// only place an advert's own photographs are rendered; recommendation cards
	// elsewhere on the page advertise other adverts. The tag scanner uses
	// reTagToken/reAttr to walk real markup instead of a page-wide URL scan.
	reFeaturesHeading = regexp.MustCompile(`(?is)<span[^>]*\bclass\s*=\s*["'][^"']*\bTitle\b[^"']*["'][^>]*>\s*Особености`)
	reJSONLDScript    = regexp.MustCompile(`(?is)<script[^>]*\btype\s*=\s*["']application/ld\+json["'][^>]*>(.*?)</script>`)
	reMetaTag         = regexp.MustCompile(`(?is)<meta\b((?:"[^"]*"|'[^']*'|[^>"'])*)>`)
	reTagToken        = regexp.MustCompile(`(?is)<(/?)([a-zA-Z][a-zA-Z0-9]*)((?:"[^"]*"|'[^']*'|[^>"'])*)>`)
	reAttr            = regexp.MustCompile(`(?is)(?:^|\s)([a-zA-Z_:][-a-zA-Z0-9_:.]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)

	// Detail page identity and page-kind evidence. adParams is a separate
	// container class from params, so it needs its own pattern; the search-page
	// regexes cannot be reused because their anchors are different.
	reAdvertNumber  = regexp.MustCompile(`(\d{15})`)
	reCanonicalTag  = regexp.MustCompile(`(?is)<link[^>]*\brel=["']?canonical["']?[^>]*>`)
	reHrefAttr      = regexp.MustCompile(`(?is)\bhref=["']([^"']+)["']`)
	reAdParams      = regexp.MustCompile(`class="adParams"`)
	reChallengePage = regexp.MustCompile(`(?i)(cf-chl|__cf_chl|challenge-platform|cf_chl_opt|cf_chl_tk|just a moment|checking your browser|enable javascript and cookies|attention required|g-recaptcha|hcaptcha|recaptcha/api|достъпът е ограничен)`)
	reRemovedNotice = regexp.MustCompile(`(?i)(обявата не е намерена|обявата е изтрита|обявата е премахната|обявата е свалена|не съществува такава обява)`)

	// Sales search-form taxonomy. The search page names each property type as a
	// labelled checkbox; the results page's per-type links are mixed with
	// neighbourhood links of the same URL shape and cannot separate them. The
	// form's own `vigroupsjs` marker proves the type filter is present, and its
	// `f38` location select proves which city's page this is. A missing or
	// renamed block is a loud failure, never an empty taxonomy.
	reTaxonomyGroupMarker  = regexp.MustCompile(`(?is)\bid\s*=\s*["']vigroupsjs["']`)
	reTaxonomyCitySelect   = regexp.MustCompile(`(?is)<select[^>]*\bname\s*=\s*["']f38["'][^>]*>(.*?)</select>`)
	reTaxonomyOptionTag    = regexp.MustCompile(`(?is)<option\b([^>]*)>`)
	reTaxonomySelectedAttr = regexp.MustCompile(`(?is)\bselected\b`)
	reTaxonomyValueAttr    = regexp.MustCompile(`(?is)\bvalue\s*=\s*["']([^"']*)["']`)
	reTaxonomyTypeLabel    = regexp.MustCompile(`(?is)<label[^>]*>\s*<input[^>]*\bid\s*=\s*["']vi[0-9]+["'][^>]*>(.*?)</label>`)
	reTaxonomyTypeID       = regexp.MustCompile(`(?is)\bid\s*=\s*["']vi[0-9]+["']`)
)

// maxUnknownTypeSamples bounds the unrecognized-type sample list so an unknown
// card type is actionable evidence without echoing page content.
const maxUnknownTypeSamples = 5

// ScanListings reads the listing-card blocks from one result page and reports
// both the accepted rows and the card-level integrity counts a completeness
// decision needs. A card dropped for missing type/price/size, or carrying a
// property type this CLI does not recognize, is evidence that coverage is not
// exhaustive; ParseListings alone would discard that evidence with the card.
//
// A block counts as an advert card only when it proves advert identity. Result
// pages render news teasers inside the same class="zaglavie" marker the cards
// use; without the identity check every teaser became a phantom card of an
// invented type. A block with no advert link and no advert number is skipped
// and counted separately, so it is neither a dropped card nor an unknown type.
func ScanListings(html string) CardScan {
	var scan CardScan

	blocks := strings.Split(html, `class="zaglavie"`)
	seenTypes := make(map[string]bool)
	for i := 1; i < len(blocks); i++ {
		block := blocks[i]
		// Limit block to reasonable size (avoid parsing into next listing)
		if idx := strings.Index(block, `class="zaglavie"`); idx > 0 {
			block = block[:idx]
		}

		if !hasAdvertIdentity(block) {
			scan.SkippedNonCardBlocks++
			continue
		}
		scan.CardBlocks++

		listing, typeRecognized := parseListingBlock(block)
		if !typeRecognized && listing.Type != "" {
			scan.UnknownTypeCards++
			if len(scan.UnknownTypeSamples) < maxUnknownTypeSamples && !seenTypes[listing.Type] {
				seenTypes[listing.Type] = true
				scan.UnknownTypeSamples = append(scan.UnknownTypeSamples, listing.Type)
			}
		}
		if listing.Type != "" && (listing.PriceEUR > 0 || listing.SizeSqM > 0) {
			// Extract photo URL from full HTML using listing ID
			if listing.ID != "" {
				photoPat := regexp.MustCompile(`src="(//[^"]*focus\.bg/imot/photosimotbg/[^"]*?/` + regexp.QuoteMeta(listing.ID) + `_[^"]+\.jpg)"`)
				if m := photoPat.FindStringSubmatch(html); len(m) > 1 {
					listing.PhotoURL = "https:" + m[1]
				}
			}
			scan.Listings = append(scan.Listings, listing)
			continue
		}
		scan.DroppedCards++
	}

	return scan
}

// hasAdvertIdentity reports whether a "zaglavie"-marked block carries the
// advert link or advert number an imot.bg advert card always has. The class
// marker alone is shared with non-advert modules and proves nothing.
func hasAdvertIdentity(block string) bool {
	return reURL.MatchString(block) || reListingID.MatchString(block)
}

// ParseListings extracts listings from HTML. It keeps its original signature for
// callers that only want the accepted rows; callers that need the card-level
// integrity counts use ScanListings.
func ParseListings(html string) []Listing {
	return ScanListings(html).Listings
}

// absorbCardScan folds one page's card-level integrity counts into the search
// envelope, keeping the unknown-type sample list bounded and unique.
func (r *SearchResult) absorbCardScan(scan CardScan) {
	r.CardBlocks += scan.CardBlocks
	r.SkippedNonCardBlocks += scan.SkippedNonCardBlocks
	r.DroppedCards += scan.DroppedCards
	r.UnknownTypeCards += scan.UnknownTypeCards
	for _, sample := range scan.UnknownTypeSamples {
		if len(r.UnknownTypeSamples) >= maxUnknownTypeSamples {
			return
		}
		dup := false
		for _, existing := range r.UnknownTypeSamples {
			if existing == sample {
				dup = true
				break
			}
		}
		if !dup {
			r.UnknownTypeSamples = append(r.UnknownTypeSamples, sample)
		}
	}
}

func parseListingBlock(block string) (Listing, bool) {
	l := Listing{
		ScrapedAt: FormatTimestamp(time.Now()),
	}

	// Extract URL and ID
	if m := reURL.FindStringSubmatch(block); len(m) > 1 {
		l.URL = "https:" + m[1]
		if idm := reListingID.FindStringSubmatch(m[1]); len(idm) > 1 {
			l.ID = idm[1]
		}
	}

	// Extract title (type + location together)
	titleText := ""
	if m := reTitle.FindStringSubmatch(block); len(m) > 1 {
		titleText = stripTags(m[1])
	}

	// Extract location
	if m := reLocation.FindStringSubmatch(block); len(m) > 1 {
		locParts := strings.SplitN(m[1], ",", 2)
		if len(locParts) >= 1 {
			city := strings.TrimSpace(locParts[0])
			city = strings.TrimPrefix(city, "град ")
			city = strings.TrimPrefix(city, "с. ")
			city = strings.TrimPrefix(city, "кв. ")
			l.City = city
		}
		if len(locParts) >= 2 {
			l.Neighborhood = strings.TrimSpace(locParts[1])
		}
	}

	// Extract type from title. typeRecognized is false when no known
	// property-type keyword matched, which is the evidence a coverage decision
	// needs: the raw fallback text is kept on the row, but it may belong to any
	// source partition.
	var typeRecognized bool
	l.Type, typeRecognized = extractTypeEvidence(titleText)

	// Extract prices
	if m := rePriceEUR.FindStringSubmatch(block); len(m) > 1 {
		l.PriceEUR = parsePrice(m[1])
	}
	if m := rePriceBGN.FindStringSubmatch(block); len(m) > 1 {
		l.PriceBGN = parsePrice(m[1])
	}

	// Extract info section
	info := extractInfo(block)
	if info != "" {
		parseInfo(info, &l)
	}

	// Extract agency
	if m := reAgency.FindStringSubmatch(block); len(m) > 1 {
		// UnescapeString covers the full entity set (&#39;, &amp;, &quot;,
		// &nbsp;, &bdquo;, &rsquo;, ...), not just the three handled before.
		l.Agency = strings.TrimSpace(stdhtml.UnescapeString(m[1]))
	}

	// Generate hash for dedup
	if l.ID == "" {
		l.ID = generateHash(l)
	}

	return l, typeRecognized
}

// extractType resolves a card title to its property type, returning the raw
// type text when no known keyword matched.
func extractType(title string) string {
	propertyType, _ := extractTypeEvidence(title)
	return propertyType
}

// extractTypeEvidence is extractType plus whether a known property-type keyword
// produced the answer. An unrecognized title still yields its raw type text so
// the row is not silently lost, but the false flag is the evidence a coverage
// decision needs: an unknown card type may belong to any source partition.
func extractTypeEvidence(title string) (string, bool) {
	// Title looks like "Продава 1-СТАЕН", "Продава КЪЩА" or "Дава под Наем 2-СТАЕН"
	title = strings.TrimPrefix(title, "Продава ")
	title = strings.TrimPrefix(title, "Се отдава ")
	title = strings.TrimPrefix(title, "Дава под Наем ")
	title = strings.TrimPrefix(title, "Дава под наем ")
	title = strings.TrimSpace(title)

	// The type runs from the start of the title until the location begins.
	// Titles glue the location to the type ("МНОГОСТАЕНград София") and write
	// the type in title case as often as in upper case ("Продава Двустаен"),
	// so the boundary is the location introducer and matching is
	// case-insensitive. Cutting at the first lowercase letter turned "Продава
	// Двустаен" into the one-letter type "Д", which made the canonical type
	// depend on the advertiser's capitalisation.
	head := typeHead(title)
	if canonical, ok := matchTypeKeyword(strings.ToUpper(head)); ok {
		return canonical, true
	}

	// No keyword matched at all. The raw type text is the sample a coverage
	// decision acts on; it is never a one-letter fragment.
	return head, false
}

// typeKeyword is one spelling of a property type card titles use, mapped to the
// canonical type string emitted on rows. taxonomyLabelSlugs is the source's
// advertised vocabulary; every label there must resolve through this table,
// while the spellings of one type share a canonical so a row's type cannot
// change with the advertiser's capitalisation, abbreviation or synonym choice.
type typeKeyword struct {
	// match is the uppercase form looked up in the title's type part.
	match string
	// canonical is the type string emitted on the row.
	canonical string
	// weak marks a label that names a whole source partition rather than one
	// card type (the "БИЗНЕС ИМОТ" grouping). A weak keyword only answers when
	// no specific keyword matched, so "БИЗНЕС ИМОТ, АПТЕКА" stays АПТЕКА.
	weak bool
}

// typeKeywords covers the labels taxonomyLabelSlugs advertises plus the word
// forms imot.bg prints in card titles. An alias exists only where the source
// itself uses that form.
var typeKeywords = []typeKeyword{
	// Apartment sizes: the search form advertises "2-СТАЕН" while titles also
	// spell the size out.
	{match: "1-СТАЕН", canonical: "1-СТАЕН"},
	{match: "ЕДНОСТАЕН", canonical: "1-СТАЕН"},
	{match: "2-СТАЕН", canonical: "2-СТАЕН"},
	{match: "ДВУСТАЕН", canonical: "2-СТАЕН"},
	{match: "3-СТАЕН", canonical: "3-СТАЕН"},
	{match: "ТРИСТАЕН", canonical: "3-СТАЕН"},
	{match: "4-СТАЕН", canonical: "4-СТАЕН"},
	{match: "ЧЕТИРИСТАЕН", canonical: "4-СТАЕН"},
	{match: "МНОГОСТАЕН", canonical: "МНОГОСТАЕН"},
	{match: "МЕЗОНЕТ", canonical: "МЕЗОНЕТ"},
	// The source labels this type "АТЕЛИЕ, ТАВАН"; rows keep the shorter name
	// this CLI already emits.
	{match: "АТЕЛИЕ, ТАВАН", canonical: "АТЕЛИЕ"},
	{match: "АТЕЛИЕ", canonical: "АТЕЛИЕ"},
	{match: "ОФИС", canonical: "ОФИС"},
	{match: "МАГАЗИН", canonical: "МАГАЗИН"},
	{match: "ЗАВЕДЕНИЕ", canonical: "ЗАВЕДЕНИЕ"},
	{match: "СКЛАД", canonical: "СКЛАД"},
	{match: "ХОТЕЛ", canonical: "ХОТЕЛ"},
	// The source labels this type "ПРОМ. ПОМЕЩЕНИЕ"; titles also carry the
	// spelled-out words and the short legacy name, all emitting one canonical.
	{match: "ПРОМ. ПОМЕЩЕНИЕ", canonical: "ПРОМИШЛЕНО"},
	{match: "ПРОМИШЛЕНО ПОМЕЩЕНИЕ", canonical: "ПРОМИШЛЕНО"},
	{match: "ПРОМИШЛЕНО", canonical: "ПРОМИШЛЕНО"},
	// "БИЗНЕС ИМОТ" is the source's grouping label; its cards normally name a
	// subtype, so the grouping only answers when no subtype matched.
	{match: "БИЗНЕС ИМОТ", canonical: "БИЗНЕС ИМОТ", weak: true},
	// The source labels this type "ЕТАЖ ОТ КЪЩА"; rows keep the canonical
	// "ЕТАЖ" the long form already resolved to.
	{match: "ЕТАЖ ОТ КЪЩА", canonical: "ЕТАЖ"},
	{match: "ЕТАЖ", canonical: "ЕТАЖ"},
	{match: "КЪЩА", canonical: "КЪЩА"},
	{match: "ВИЛА", canonical: "ВИЛА"},
	{match: "ПАРЦЕЛ", canonical: "ПАРЦЕЛ"},
	// The source labels this pair "ГАРАЖ, ПАРКОМЯСТО"; rows keep the canonical
	// the longer word already produced.
	{match: "ГАРАЖ, ПАРКОМЯСТО", canonical: "ПАРКОМЯСТО"},
	{match: "ГАРАЖ", canonical: "ГАРАЖ"},
	{match: "ПАРКОМЯСТО", canonical: "ПАРКОМЯСТО"},
	// The source labels land "ЗЕМЕДЕЛСКА ЗЕМЯ"; rows keep the canonical "ЗЕМЯ".
	{match: "ЗЕМЕДЕЛСКА ЗЕМЯ", canonical: "ЗЕМЯ"},
	{match: "ЗЕМЯ", canonical: "ЗЕМЯ"},
	// Business property subtypes (appear under the "БИЗНЕС ИМОТ" filter).
	{match: "АВТОМИВКА", canonical: "АВТОМИВКА"},
	{match: "АВТОСЕРВИЗ", canonical: "АВТОСЕРВИЗ"},
	{match: "АПТЕКА", canonical: "АПТЕКА"},
	{match: "БАНКОВ ОФИС", canonical: "БАНКОВ ОФИС"},
	{match: "БЕНЗИНОСТАНЦИЯ", canonical: "БЕНЗИНОСТАНЦИЯ"},
	{match: "КЛИНИКА", canonical: "КЛИНИКА"},
	{match: "ЛЕКАРСКИ КАБИНЕТ", canonical: "ЛЕКАРСКИ КАБИНЕТ"},
	{match: "ФЕРМА", canonical: "ФЕРМА"},
	{match: "СПА", canonical: "СПА"},
	{match: "СОЛЯРНО СТУДИО", canonical: "СОЛЯРНО СТУДИО"},
	{match: "СТОМАТОЛОГИЧЕН КАБИНЕТ", canonical: "СТОМАТОЛОГИЧЕН КАБИНЕТ"},
	{match: "ТЪРГОВСКИ КОМПЛЕКС", canonical: "ТЪРГОВСКИ КОМПЛЕКС"},
	{match: "ФАБРИКА", canonical: "ФАБРИКА"},
	{match: "ЗАВОД", canonical: "ЗАВОД"},
	{match: "ФИТНЕС ЗАЛА", canonical: "ФИТНЕС ЗАЛА"},
	{match: "ФРИЗЬОРСКИ", canonical: "ФРИЗЬОРСКИ"},
	{match: "КОЗМЕТИЧЕН САЛОН", canonical: "КОЗМЕТИЧЕН САЛОН"},
	{match: "ПАРКИНГ", canonical: "ПАРКИНГ"},
	{match: "ФОТОГРАФСКО СТУДИО", canonical: "ФОТОГРАФСКО СТУДИО"},
	{match: "ДЕТСКИ ЦЕНТЪР", canonical: "ДЕТСКИ ЦЕНТЪР"},
	{match: "АКВАПАРК", canonical: "АКВАПАРК"},
	{match: "ВИЛНО СЕЛИЩЕ", canonical: "ВИЛНО СЕЛИЩЕ"},
	{match: "СОЛЯРЕН ПАРК", canonical: "СОЛЯРЕН ПАРК"},
	{match: "ДОМ ЗА ВЪЗРАСТНИ ХОРА", canonical: "ДОМ ЗА ВЪЗРАСТНИ ХОРА"},
	{match: "САМОСТОЯТЕЛНА СГРАДА", canonical: "САМОСТОЯТЕЛНА СГРАДА"},
	{match: "ХЛАДИЛЕН СКЛАД", canonical: "ХЛАДИЛЕН СКЛАД"},
}

// locationIntroducers begin the part of a card title that names the location
// rather than the property type. Titles glue them to the type ("2-СТАЕНград
// София"), so they are searched without assuming a preceding space.
var locationIntroducers = []string{"град", "гр.", "с.", "кв.", "ж.к.", "област", "обл.", " в ", " на "}

// typeHead returns the leading part of a card title that can hold the property
// type: everything before the location introducer, or the whole title when the
// title names no location.
//
// A marker is only an introducer when it ENDS a word. "град" inside "СГРАДА"
// would otherwise cut "САМОСТОЯТЕЛНА СГРАДА" down to "САМОСТОЯТЕЛНА С", which no
// keyword can match: the card is then reported as an unrecognized property type,
// every sweep carrying such an advertisement is withheld from absence detection,
// and a real business-property type reads as a parser gap. The glued form the
// markers exist for ("2-СТАЕНград София") still matches, because there the
// marker is followed by a space.
func typeHead(title string) string {
	cut := len(title)
	for i := range title {
		for _, marker := range locationIntroducers {
			end := i + len(marker)
			if end > len(title) || !strings.EqualFold(title[i:end], marker) {
				continue
			}
			if after, _ := utf8.DecodeRuneInString(title[end:]); unicode.IsLetter(after) {
				continue
			}
			if i < cut {
				cut = i
			}
			break
		}
	}
	return strings.TrimSpace(title[:cut])
}

// matchTypeKeyword resolves an uppercased type part to a canonical type. An
// exact spelling wins outright; otherwise the longest keyword wins, with the
// earliest position as tie-break. Weak keywords (source partition names) are
// only consulted when no specific keyword matched.
func matchTypeKeyword(part string) (string, bool) {
	for _, kw := range typeKeywords {
		if part == kw.match {
			return kw.canonical, true
		}
	}
	if canonical, ok := longestTypeMatch(part, false); ok {
		return canonical, true
	}
	return longestTypeMatch(part, true)
}

// longestTypeMatch returns the canonical type of the longest keyword found in
// part, optionally including weak partition names.
func longestTypeMatch(part string, includeWeak bool) (string, bool) {
	bestCanonical, bestMatch := "", ""
	bestPos := -1
	for _, kw := range typeKeywords {
		if kw.weak && !includeWeak {
			continue
		}
		pos := strings.Index(part, kw.match)
		if pos < 0 {
			continue
		}
		longer := len([]rune(kw.match)) > len([]rune(bestMatch))
		earlier := len([]rune(kw.match)) == len([]rune(bestMatch)) && (bestPos < 0 || pos < bestPos)
		if longer || earlier {
			bestCanonical, bestMatch, bestPos = kw.canonical, kw.match, pos
		}
	}
	if bestMatch == "" {
		return "", false
	}
	return bestCanonical, true
}

func extractInfo(block string) string {
	// Find the info div content
	marker := `class="info">`
	idx := strings.Index(block, marker)
	if idx < 0 {
		return ""
	}
	after := block[idx+len(marker):]
	endIdx := strings.Index(after, "</div>")
	if endIdx < 0 {
		return after
	}
	return strings.TrimSpace(after[:endIdx])
}

func parseInfo(info string, l *Listing) {
	// The info field is comma-separated with structure:
	// [sqm, floor?, year?, description..., phone]
	// But description can also contain commas, so we parse from left.
	// Strip any markup first and turn block-level boundaries into separators,
	// otherwise adjacent blocks glue together ("Без комисионнаПродава се").
	info = stripTags(info)

	// Extract phone from the end first
	phoneIdx := strings.LastIndex(info, "тел.:")
	if phoneIdx < 0 {
		phoneIdx = strings.LastIndex(info, "тел.")
	}

	if phoneIdx >= 0 {
		phonePart := info[phoneIdx:]
		if m := rePhone.FindStringSubmatch(phonePart); len(m) > 1 {
			l.Phone = strings.ReplaceAll(m[1], " ", "")
		}
		info = info[:phoneIdx]
	}

	// Remove trailing comma and spaces
	info = strings.TrimRight(strings.TrimSpace(info), ",")

	// Parse the structured parts by scanning ALL parts (not positional)
	// because street names can appear between size and floor:
	// "74 кв.м, бул. Джеймс Баучър, 3-ти ет. от 5, Въведен в експлоатация 2007 г., ..."
	parts := strings.Split(info, ", ")

	floorFound := false
	yearFound := false
	var descParts []string

	for _, part := range parts {
		part = strings.TrimSpace(part)

		// Size
		if m := reSqM.FindStringSubmatch(part); len(m) > 1 && l.SizeSqM == 0 {
			l.SizeSqM, _ = strconv.Atoi(m[1])
			continue
		}

		// Floor
		if !floorFound {
			if m := reFloor.FindStringSubmatch(part); len(m) > 2 {
				l.Floor = fmt.Sprintf("%s от %s", m[1], m[2])
				floorFound = true
				continue
			} else if m := reFloorShort.FindStringSubmatch(part); len(m) > 1 {
				l.Floor = m[1]
				floorFound = true
				continue
			} else if m := reParter.FindStringSubmatch(part); len(m) > 1 {
				l.Floor = fmt.Sprintf("0 от %s", m[1])
				floorFound = true
				continue
			} else if m := reLevel.FindStringSubmatch(part); len(m) > 2 {
				l.Floor = fmt.Sprintf("%s от %s", m[1], m[2])
				floorFound = true
				continue
			}
		}

		// Year
		if !yearFound {
			if m := reYearRange.FindStringSubmatch(part); len(m) > 2 {
				l.YearBuilt = m[1] + "-" + m[2]
				yearFound = true
				continue
			} else if m := reYearExact.FindStringSubmatch(part); len(m) > 1 {
				l.YearBuilt = m[1]
				yearFound = true
				continue
			} else if reYearNot.MatchString(part) {
				l.YearBuilt = "under construction"
				yearFound = true
				continue
			} else if strings.Contains(part, "Ще бъде въведен") {
				if m := reYearExact.FindStringSubmatch(part); len(m) > 1 {
					l.YearBuilt = "under construction - " + m[1]
				} else {
					l.YearBuilt = "under construction"
				}
				yearFound = true
				continue
			}
		}

		// Everything else is description
		descParts = append(descParts, part)
	}

	// Set description from remaining parts
	if len(descParts) > 0 {
		desc := strings.Join(descParts, ", ")
		desc = strings.TrimSpace(stdhtml.UnescapeString(desc))
		// &nbsp; (and friends) decode to U+00A0; store a plain space instead.
		desc = strings.ReplaceAll(desc, "\u00a0", " ")
		// Cap by rune count. Byte slicing (desc[:497]) split 2-byte Cyrillic
		// characters and made the JSON encoder emit U+FFFD in stored data.
		desc = TruncateRunes(desc, 500, "...")
		l.Description = desc
	}

	// Fallback: extract floor from description if still empty
	if l.Floor == "" && l.Description != "" {
		if m := reFloor.FindStringSubmatch(l.Description); len(m) > 2 {
			l.Floor = fmt.Sprintf("%s от %s", m[1], m[2])
		} else if m := reFloorShort.FindStringSubmatch(l.Description); len(m) > 1 {
			l.Floor = m[1]
		} else if m := reParter.FindStringSubmatch(l.Description); len(m) > 1 {
			l.Floor = fmt.Sprintf("0 от %s", m[1])
		} else if m := reLevel.FindStringSubmatch(l.Description); len(m) > 2 {
			l.Floor = fmt.Sprintf("%s от %s", m[1], m[2])
		}
	}
}

func parsePrice(s string) int {
	// Remove spaces and parse
	s = strings.ReplaceAll(s, " ", "")
	n, err := strconv.Atoi(s)
	if err != nil {
		// Try parsing as float (for BGN prices like "82144.86")
		f, err2 := strconv.ParseFloat(s, 64)
		if err2 != nil {
			return 0
		}
		return int(f)
	}
	return n
}

// blockLevelTags are the HTML elements whose boundaries become a separator when
// tags are stripped. Without that separator the text of adjacent blocks runs
// together, which real pages triggered: the detail description uses <br> between
// every line, so "...ДЖЕЙМС БАУЧЪР!<br><br>Отлична локация..." was stored as
// "...ДЖЕЙМС БАУЧЪР!Отлична локация...".
var blockLevelTags = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true,
	"br": true, "caption": true, "dd": true, "div": true, "dl": true,
	"dt": true, "fieldset": true, "figcaption": true, "figure": true,
	"footer": true, "form": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true, "header": true, "hr": true,
	"legend": true, "li": true, "main": true, "nav": true, "ol": true,
	"p": true, "pre": true, "section": true, "table": true, "tbody": true,
	"td": true, "tfoot": true, "th": true, "thead": true, "tr": true,
	"ul": true,
}

// stripTags removes HTML tags and inserts a single space wherever a block-level
// element boundary occurred. Inline tags (<b>, <a>, <span>) add no separator, so
// formatting inside a word or phrase stays intact.
func stripTags(s string) string {
	var result strings.Builder
	inTag := false
	var tag strings.Builder
	lastRune := rune(0)
	writeSep := func() {
		if result.Len() == 0 {
			return
		}
		if !unicode.IsSpace(lastRune) {
			result.WriteByte(' ')
			lastRune = ' '
		}
	}
	for _, r := range s {
		if inTag {
			if r == '>' {
				if isBlockLevelTag(tag.String()) {
					writeSep()
				}
				inTag = false
				tag.Reset()
				continue
			}
			tag.WriteRune(r)
			continue
		}
		if r == '<' {
			inTag = true
			tag.Reset()
			continue
		}
		result.WriteRune(r)
		lastRune = r
	}
	return strings.TrimSpace(result.String())
}

// isBlockLevelTag reports whether raw tag text ("div", "/div", "br/",
// "div class=\"info\"") names a block-level element.
func isBlockLevelTag(raw string) bool {
	raw = strings.TrimLeft(strings.TrimSpace(raw), "/!?")
	i := 0
	for i < len(raw) {
		c := raw[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			i++
			continue
		}
		break
	}
	if i == 0 {
		return false
	}
	return blockLevelTags[strings.ToLower(raw[:i])]
}

// TruncateRunes caps s at max runes including suffix, so callers can bound text
// without splitting a multi-byte UTF-8 character. Byte slicing (s[:n]) corrupts
// Cyrillic text and makes Go's JSON encoder emit U+FFFD.
func TruncateRunes(s string, max int, suffix string) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	keep := max - len([]rune(suffix))
	if keep < 0 {
		keep = 0
	}
	return string(runes[:keep]) + suffix
}

func generateHash(l Listing) string {
	data := fmt.Sprintf("%s|%s|%s|%d|%d|%s", l.Type, l.City, l.Neighborhood, l.PriceEUR, l.SizeSqM, l.Phone)
	h := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%x", h)[:16]
}

func parseBulgarianMonth(month string) time.Month {
	switch strings.ToLower(strings.TrimSpace(month)) {
	case "януари":
		return time.January
	case "февруари":
		return time.February
	case "март":
		return time.March
	case "април":
		return time.April
	case "май":
		return time.May
	case "юни":
		return time.June
	case "юли":
		return time.July
	case "август":
		return time.August
	case "септември":
		return time.September
	case "октомври":
		return time.October
	case "ноември":
		return time.November
	case "декември":
		return time.December
	default:
		return 0
	}
}

// ParseTotalCount extracts the total number of listings from the page HTML.
// Looks for pattern: "от общо NNN обяви". A missing count returns 0, so a caller
// that must distinguish "the source reported zero" from "the source printed no
// count" uses ParseTotalCountEvidence instead.
func ParseTotalCount(html string) int {
	n, _, _ := ParseTotalCountEvidence(html)
	return n
}

// ParseTotalCountEvidence extracts the source's own total count together with
// whether the page printed one at all and whether it printed the clamped "N+"
// form. A coverage decision must not read "no count printed" as zero, and a
// clamped count is a lower bound rather than the real total.
func ParseTotalCountEvidence(html string) (count int, reported bool, capped bool) {
	m := reTotalCount.FindStringSubmatch(html)
	if len(m) < 2 {
		return 0, false, false
	}
	n, err := strconv.Atoi(strings.ReplaceAll(m[1], " ", ""))
	if err != nil {
		return 0, false, false
	}
	capped = len(m) > 2 && m[2] == "+"
	return n, true, capped
}

// HasNoResultsMarker reports whether the page explicitly states that the query
// matched nothing. Only imot.bg's own marker proves an empty result set; the
// mere absence of listing cards does not.
func HasNoResultsMarker(html string) bool {
	return reNoResults.MatchString(html)
}

// ParseSearchPage builds a SearchResult from one saved search-results page with
// no network access. It is the offline twin of the page-1 path in SearchWithMeta
// and applies the same readability rule: no cards, no total count and no
// explicit no-results marker means Partial with an unreadable_page error, never
// a verified empty market.
//
// source is recorded on the error entry so a fixture test can name the file it
// parsed. ServerFilters stays empty because no source query was made; the caller
// fills Requested* and the client-side filter names it actually applied.
func ParseSearchPage(html, source string) SearchResult {
	result := SearchResult{
		Listings:            []Listing{},
		PagesFetched:        1,
		PagesPlanned:        1,
		ServerFilters:       []string{},
		ClientFilters:       []string{},
		ServerFilterSupport: ServerFilterSupport(),
	}

	scan := ScanListings(html)
	result.Listings = append(result.Listings, scan.Listings...)
	result.absorbCardScan(scan)
	result.TotalCount, result.TotalCountReported, result.TotalCountCapped = ParseTotalCountEvidence(html)
	noResultsMarker := HasNoResultsMarker(html)

	if len(result.Listings) == 0 && result.TotalCount == 0 && !noResultsMarker {
		result.Partial = true
		result.PagesFetched = 0
		result.Errors = append(result.Errors, SearchError{
			Page:  1,
			URL:   source,
			Kind:  SearchErrorUnreadablePage,
			Error: ErrUnreadablePage.Error(),
		})
		return result
	}

	if noResultsMarker && len(result.Listings) == 0 && result.TotalCount == 0 {
		result.EmptyVerified = true
	}
	return result
}

// Taxonomy extraction -------------------------------------------------------

// taxonomyLabelSlugs maps each visible label of the imot.bg sales search
// form's property-type checkboxes to the ASCII slug this CLI addresses that
// type by. The label is the only name the source gives a type, so this map is
// the taxonomy vocabulary: a label the source adds, renames or drops must be
// reconciled here before a payload is published, because an unmapped label
// means the advertised vocabulary is no longer fully represented.
//
// Keys are in the normalized form taxonomyLabelKey produces: HTML entities
// decoded, single spaces, one space after a comma, uppercase.
var taxonomyLabelSlugs = map[string]string{
	"1-СТАЕН":           "ednostaen",
	"2-СТАЕН":           "dvustaen",
	"3-СТАЕН":           "tristaen",
	"4-СТАЕН":           "chetiristaen",
	"МНОГОСТАЕН":        "mnogostaen",
	"МЕЗОНЕТ":           "mezonet",
	"АТЕЛИЕ, ТАВАН":     "atelie-tavan",
	"ОФИС":              "ofis",
	"МАГАЗИН":           "magazin",
	"ЗАВЕДЕНИЕ":         "zavedenie",
	"СКЛАД":             "sklad",
	"ХОТЕЛ":             "hotel",
	"ПРОМ. ПОМЕЩЕНИЕ":   "promishleno-pomeshtenie",
	"БИЗНЕС ИМОТ":       "biznes-imot",
	"ЕТАЖ ОТ КЪЩА":      "etazh-ot-kashta",
	"КЪЩА":              "kashta",
	"ВИЛА":              "vila",
	"ПАРЦЕЛ":            "partsel",
	"ГАРАЖ, ПАРКОМЯСТО": "garazh-parkomyasto",
	"ЗЕМЕДЕЛСКА ЗЕМЯ":   "zemedelska-zemya",
}

// ParseTaxonomy reads one sales search page's advertised property-type
// taxonomy. It is offline: html is the already-decoded page and no request is
// made.
//
// The page is imot.bg's "Търсене в imot.bg - Продава" search form for one city.
// Each property type is a checkbox labelled with its Bulgarian name, and the
// city is the single selected option of the form's f38 location select.
// Neighbourhood links and business-type checkboxes sit in the same page and are
// deliberately not read: only the labelled type checkboxes are the taxonomy.
//
// The page must prove it is the search form for params.City and must advertise
// the complete known vocabulary. A challenge page, another city's page, a
// renamed form, an unmapped label or a partial vocabulary therefore fails with
// an error. An empty or reduced type list is never returned: an unknown page
// kind cannot be read as "no types", and a partially recognized page cannot be
// read as the source's taxonomy.
func ParseTaxonomy(html string, params TaxonomyParams) (Taxonomy, error) {
	if reChallengePage.MatchString(html) {
		return Taxonomy{}, fmt.Errorf("taxonomy page is a bot challenge, not a city search page")
	}
	city := strings.TrimSpace(params.City)
	citySlug := strings.TrimSpace(params.CitySlug)
	if citySlug == "" {
		return Taxonomy{}, fmt.Errorf("taxonomy needs a city slug to recognize its source page")
	}
	if got := resolveCitySlug(city); got != citySlug {
		return Taxonomy{}, fmt.Errorf("taxonomy city %q does not match city slug %q", params.City, params.CitySlug)
	}
	expectedCity, ok := taxonomyCitySelector(city)
	if !ok {
		return Taxonomy{}, fmt.Errorf("taxonomy needs a city this CLI knows by name to verify the selected city, got %q", params.City)
	}
	if !reTaxonomyGroupMarker.MatchString(html) {
		return Taxonomy{}, fmt.Errorf("page %q is not the imot.bg type-filter search form", params.SourceURL)
	}
	selected, ok := taxonomySelectedCity(html)
	if !ok {
		return Taxonomy{}, fmt.Errorf("page %q has no single selected city in its location select", params.SourceURL)
	}
	if selected != expectedCity {
		return Taxonomy{}, fmt.Errorf("page %q selects city %q, expected %q for %q", params.SourceURL, selected, expectedCity, city)
	}

	var candidates, unmapped []string
	matches := reTaxonomyTypeLabel.FindAllStringSubmatch(html, -1)
	// Every advertised type checkbox must be one of the labelled ones. A type
	// the source renders outside a label would otherwise be dropped silently.
	if ids := reTaxonomyTypeID.FindAllString(html, -1); len(ids) != len(matches) {
		return Taxonomy{}, fmt.Errorf("page %q carries %d type checkbox id(s) but only %d are labelled", params.SourceURL, len(ids), len(matches))
	}
	for _, m := range matches {
		label := taxonomyLabelKey(m[1])
		slug, ok := taxonomyLabelSlugs[label]
		if !ok {
			unmapped = append(unmapped, label)
			continue
		}
		candidates = append(candidates, slug)
	}
	if len(unmapped) > 0 {
		return Taxonomy{}, fmt.Errorf("page %q advertises type label(s) %s that no taxonomy mapping covers", params.SourceURL, strings.Join(boundedLabels(unmapped), ", "))
	}

	slugs := SortedUniqueTaxonomySlugs(candidates)
	if len(slugs) < len(taxonomyLabelSlugs) {
		return Taxonomy{}, fmt.Errorf("page %q advertises %d of the %d known property types; the source vocabulary changed and must be reconciled", params.SourceURL, len(slugs), len(taxonomyLabelSlugs))
	}

	return Taxonomy{
		ContractVersion: TaxonomyContractVersion,
		City:            city,
		SourceURL:       params.SourceURL,
		ObservedAt:      FormatTimestamp(time.Now().UTC()),
		TypeSlugs:       slugs,
		TaxonomyHash:    TaxonomyHash(slugs),
	}, nil
}

// taxonomyCitySelector returns the value the search form's f38 location select
// must have selected for this city: "град <name>" for a city this CLI knows by
// name, and the already-prefixed name for an oblast.
func taxonomyCitySelector(city string) (string, bool) {
	city = strings.TrimSpace(city)
	if _, ok := CityMap[city]; ok {
		return "град " + city, true
	}
	if _, ok := OblastMap[city]; ok {
		return city, true
	}
	return "", false
}

// taxonomySelectedCity reads the single selected option of the f38 location
// select. Zero or several selected options prove nothing about which city the
// page is for, so both fail.
func taxonomySelectedCity(html string) (string, bool) {
	block := reTaxonomyCitySelect.FindStringSubmatch(html)
	if len(block) < 2 {
		return "", false
	}
	selected := ""
	for _, m := range reTaxonomyOptionTag.FindAllStringSubmatch(block[1], -1) {
		attrs := m[1]
		if !reTaxonomySelectedAttr.MatchString(attrs) {
			continue
		}
		value := reTaxonomyValueAttr.FindStringSubmatch(attrs)
		if len(value) < 2 || selected != "" {
			return "", false
		}
		selected = stdhtml.UnescapeString(strings.TrimSpace(value[1]))
	}
	return selected, selected != ""
}

// taxonomyLabelKey normalizes a raw checkbox label to the map key form: HTML
// entities decoded, one space after a comma, single spaces, uppercase.
func taxonomyLabelKey(raw string) string {
	label := stdhtml.UnescapeString(raw)
	label = strings.ReplaceAll(label, ",", ", ")
	return strings.ToUpper(strings.Join(strings.Fields(label), " "))
}

// boundedLabels keeps a failure message actionable without echoing an
// arbitrarily large page fragment: at most three labels, each at most forty
// runes.
func boundedLabels(labels []string) []string {
	const maxLabels = 3
	const maxRunes = 40
	out := make([]string, 0, maxLabels+1)
	for _, label := range labels {
		if len(out) == maxLabels {
			out = append(out, "…")
			break
		}
		runes := []rune(label)
		if len(runes) > maxRunes {
			label = string(runes[:maxRunes]) + "…"
		}
		out = append(out, label)
	}
	return out
}

// SortedUniqueTaxonomySlugs normalizes a raw slug list for the taxonomy payload:
// trimmed, lowercased, ASCII-only, deduplicated and sorted. Exported so a
// consumer can recompute the same list the hash commits to.
func SortedUniqueTaxonomySlugs(raw []string) []string {
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		slug := strings.ToLower(strings.TrimSpace(s))
		if slug == "" || seen[slug] {
			continue
		}
		seen[slug] = true
		out = append(out, slug)
	}
	sort.Strings(out)
	return out
}

// TaxonomyHash returns the full lowercase SHA-256 hex digest of the sorted
// unique ASCII slugs joined with LF. It normalizes first, so callers cannot
// hash a different order than they publish.
func TaxonomyHash(raw []string) string {
	h := sha256.Sum256([]byte(strings.Join(SortedUniqueTaxonomySlugs(raw), "\n")))
	return fmt.Sprintf("%x", h)
}

// DetailPageKind classifies what a fetched or saved page actually is, judged
// from its own content rather than from the URL that was requested.
type DetailPageKind string

const (
	// DetailPageKindListing: the page carries imot.bg advert structure.
	DetailPageKindListing DetailPageKind = "listing"
	// DetailPageKindChallenge: a bot/captcha interstitial.
	DetailPageKindChallenge DetailPageKind = "challenge"
	// DetailPageKindRemoved: an explicit advert-removed/not-found notice.
	DetailPageKindRemoved DetailPageKind = "removed"
	// DetailPageKindUnreadable: none of the above (changed layout, block page).
	DetailPageKindUnreadable DetailPageKind = "unreadable"
)

// AdvertIDFromURL returns the 15-digit advertisement number inside a URL or
// advert id, or "" when the value carries none. The two-character kind prefix
// imot.bg puts in `obiava-<id>-...` is a type variant of the same advertisement,
// so the number — not the full slug — decides which property a page belongs to.
// This matches the collector's own adNumberFrom.
func AdvertIDFromURL(value string) string {
	return reAdvertNumber.FindString(value)
}

// advertIdentity returns the canonical advert URL a page claims for itself and
// the 15-digit advert number inside it. og:url wins over the canonical link, and
// both are read from the page, so identity never depends on the requested URL.
func advertIdentity(html string) (canonicalURL, advertID string) {
	var candidates []string
	if m := reDetailOGURL.FindStringSubmatch(html); len(m) > 1 {
		candidates = append(candidates, m[1])
	}
	for _, tag := range reCanonicalTag.FindAllString(html, -1) {
		if m := reHrefAttr.FindStringSubmatch(tag); len(m) > 1 {
			candidates = append(candidates, m[1])
		}
	}
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		id := AdvertIDFromURL(c)
		lower := strings.ToLower(c)
		// Identity must be an imot.bg advert URL carrying a 15-digit advert
		// number. Search URLs use "/obiavi/" (plural) and carry no such number,
		// so they never supply identity. A relative advert URL is accepted too.
		if id != "" && (strings.Contains(lower, "imot.bg") || strings.Contains(lower, "obiava")) {
			return c, id
		}
	}
	// No advert identity. Report the first canonical URL when one exists, so the
	// error metadata can show what the page claimed to be.
	if len(candidates) > 0 {
		return strings.TrimSpace(candidates[0]), ""
	}
	return "", ""
}

// advertStructureHits counts independent pieces of advert-page structure.
// class="text" is the site's generic text container, so one hit is weak
// evidence; two hits, or one hit plus the page's own advert identity, are what
// separate an advert from a challenge, a removal notice or a block page.
// Photo and feature blocks are deliberately not required: an advert with no
// photo and no feature tag is still a genuine advert.
func advertStructureHits(html string) int {
	hits := 0
	if reDetailText.MatchString(html) {
		hits++
	}
	if reDetailParams.MatchString(html) || reAdParams.MatchString(html) {
		hits++
	}
	if reDetailPhone.MatchString(html) {
		hits++
	}
	if reDetailViewCount.MatchString(html) {
		hits++
	}
	if reDetailPublishedAt.MatchString(html) || reDetailCorrectedAt.MatchString(html) {
		hits++
	}
	if reFeaturesBlock.MatchString(html) {
		hits++
	}
	if reBrokerName.MatchString(html) {
		hits++
	}
	if reDetailAgencyURL.MatchString(html) {
		hits++
	}
	return hits
}

// ClassifyDetailPage reports what the HTML is, judged on its own content. The
// advert check runs first, so a genuine advert that embeds a captcha widget for
// its contact form is not mistaken for a challenge page.
func ClassifyDetailPage(html string) DetailPageKind {
	hits := advertStructureHits(html)
	_, advertID := advertIdentity(html)
	if hits >= 2 || (advertID != "" && hits >= 1) {
		return DetailPageKindListing
	}
	if reChallengePage.MatchString(html) {
		return DetailPageKindChallenge
	}
	if reRemovedNotice.MatchString(html) {
		return DetailPageKindRemoved
	}
	return DetailPageKindUnreadable
}

// ParseDetailPage validates that html is a genuine imot.bg advert page and that
// it belongs to the requested advert, then extracts the detail fields.
//
// requestedURL may be empty when the caller only wants the page judged on its
// own (the offline fixture path): the page must still expose a canonical advert
// identity of its own, but no comparison is made. A non-empty requested URL must
// carry its own 15-digit advert number; an unparseable requested identity is an
// error, never permission to skip the ownership check. The requested URL is
// never used as a fallback for missing identity — unknown identity stays
// unknown — and no failure is ever reported as a successful detail with empty
// fields.
func ParseDetailPage(html, requestedURL string) (DetailListing, error) {
	return ParseDetailPageWithMeta(html, requestedURL, "")
}

// ParseDetailPageWithMeta is ParseDetailPage plus effectiveURL, the URL the
// source finally served after redirects. It is recorded on every rejection as
// error metadata so a consumer can see where a redirect actually landed. The
// success payload keeps its original flat fields and its canonical "url", with
// the presence contract (contract_version, advert_id, field_evidence) added on
// top. An empty effectiveURL (offline parse, or no response) omits the field
// rather than pretending the requested URL was served.
func ParseDetailPageWithMeta(html, requestedURL, effectiveURL string) (DetailListing, error) {
	canonical, observedID := advertIdentity(html)
	requestedID := AdvertIDFromURL(requestedURL)

	reject := func(kind, message string) (DetailListing, error) {
		return DetailListing{}, &DetailError{
			Kind:              kind,
			RequestedURL:      strings.TrimSpace(requestedURL),
			RequestedAdvertID: requestedID,
			ObservedURL:       canonical,
			ObservedAdvertID:  observedID,
			EffectiveURL:      strings.TrimSpace(effectiveURL),
			Message:           message,
		}
	}

	// A non-empty requested URL must name an advert. Verifying ownership is not
	// optional when the requested identity is malformed: skipping the comparison
	// would accept whatever page the source served, including a different advert.
	if strings.TrimSpace(requestedURL) != "" && requestedID == "" {
		return reject(DetailErrorMissingIdentity, "requested URL carries no 15-digit advert number, so the page's identity cannot be verified")
	}

	switch ClassifyDetailPage(html) {
	case DetailPageKindChallenge:
		return reject(DetailErrorChallengePage, "detail page is a bot challenge, not a listing advert")
	case DetailPageKindRemoved:
		return reject(DetailErrorRemovedAdvert, "detail page reports that the advert is no longer available")
	case DetailPageKindUnreadable:
		return reject(DetailErrorUnreadablePage, "detail page is unreadable: no advert structure found")
	}

	if observedID == "" {
		return reject(DetailErrorMissingIdentity, "detail page exposes no advert identity of its own")
	}
	if requestedID != "" && requestedID != observedID {
		return reject(DetailErrorWrongIdentity, fmt.Sprintf("detail page belongs to advert %s, not requested %s", observedID, requestedID))
	}

	detail := ParseDetail(html)
	detail.URL = canonical
	// advert_id comes from the page's own independently parsed identity; the
	// requested URL is never substituted for it.
	detail.AdvertID = observedID
	return detail, nil
}

// detailEvidence accumulates the per-field presence record for one parsed
// detail page. Every named DetailKey starts at unknown with a bounded
// no_selector_hit reason, so a field the parser never inspected can never be
// published as verified absence.
type detailEvidence struct {
	fields map[string]DetailFieldEvidence
}

func newDetailEvidence() *detailEvidence {
	fields := make(map[string]DetailFieldEvidence, len(DetailKeys))
	for _, key := range DetailKeys {
		fields[key] = DetailFieldEvidence{State: DetailPresenceUnknown, Raw: nil, Reason: DetailReasonNoSelectorHit}
	}
	return &detailEvidence{fields: fields}
}

func (e *detailEvidence) set(key, state string, raw any, reason string) {
	e.fields[key] = DetailFieldEvidence{State: state, Raw: raw, Reason: reason}
}

func (e *detailEvidence) present(key string, raw any, reason string) {
	e.set(key, DetailPresencePresent, raw, reason)
}

func (e *detailEvidence) verifiedAbsent(key string, raw any, reason string) {
	e.set(key, DetailPresenceVerifiedAbsent, raw, reason)
}

func (e *detailEvidence) unknown(key, reason string) {
	e.set(key, DetailPresenceUnknown, nil, reason)
}

// ParseDetail extracts enriched data from a listing's detail page HTML.
//
// Every DetailKeys entry receives an evidence entry: present when a value of
// the expected type was extracted, verified_absent when a recognized source
// structure proves the source did not advertise the field, and unknown when
// nothing proved either way. Unknown is never published as verified absence.
// The contract version tags the payload; advert_id is filled by
// ParseDetailPageWithMeta, which owns identity validation.
// Returns a DetailListing with fields only available on the detail page.
func ParseDetail(html string) DetailListing {
	d := DetailListing{ContractVersion: DetailContractVersion}
	ev := newDetailEvidence()

	// 1. Full description from class="text" divs. The page can carry several: the
	// listing text, the "В imot.bg от YYYY г." provenance line, and occasionally
	// further text blocks. Every block is inspected. A description that is short,
	// or that only appears after the first two blocks, is still the source's
	// description; missing it made the field look verified_absent and let the
	// consumer clear a stored description it never disproved.
	textMatches := reDetailText.FindAllStringSubmatch(html, -1)
	sawTextContainer := len(textMatches) > 0
	sawProvenance := false
	for _, m := range textMatches {
		clean := stripTags(m[1])
		clean = decorativeEntities.Replace(clean)
		clean = strings.TrimSpace(stdhtml.UnescapeString(clean))
		clean = strings.ReplaceAll(clean, "\u00a0", " ")
		// Skip the "В imot.bg от" line
		if strings.HasPrefix(clean, "В imot.bg от") {
			sawProvenance = true
			continue
		}
		if clean != "" {
			d.FullDescription = clean
			break
		}
	}
	switch {
	case d.FullDescription != "":
		ev.present(DetailKeyFullDescription, d.FullDescription, DetailReasonTextBlock)
	case sawProvenance:
		// The advert's own text container rendered only its provenance line. That
		// is the source's recognized structure for "this advert has no
		// description text", so verified absence is justified.
		ev.verifiedAbsent(DetailKeyFullDescription, nil, DetailReasonTextBlockEmpty)
	case sawTextContainer:
		// An empty text container is a placeholder, not proof of absence.
		ev.unknown(DetailKeyFullDescription, DetailReasonTextBlockPlaceholder)
	default:
		ev.unknown(DetailKeyFullDescription, DetailReasonNoSelectorHit)
	}

	// 2. Structured params line: class="params"
	// e.g. "Площ: 24 кв.м, Агенция, Етаж: 4-ти от 4, Газ: НЕ, ТEЦ: ДА, Тухла, Въведен в експлоатация 1930 - 1939 г.,"
	//
	// The recognized params block is an enumerable source structure: a labelled
	// key the block omits is verified absence, not an unknown. The bare tokens
	// (seller type, construction) are not labelled, so their absence stays
	// unknown — an unrecognized token could be a new source value.
	paramsBlockSeen := false
	// paramsRecognized records that the block rendered at least one known key.
	// An empty or unrecognized block proves nothing about the keys it omits.
	paramsRecognized := false
	sawFloorKey, sawGasKey, sawTECKey, sawYearKey := false, false, false, false
	additionalParams := make(map[string]string)
	if m := reDetailParams.FindStringSubmatch(html); len(m) > 1 {
		paramsBlockSeen = true
		params := stripTags(m[1])
		paramsParts := strings.Split(params, ",")
		for _, p := range paramsParts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if colon := strings.Index(p, ":"); colon > 0 {
				label := strings.TrimSpace(p[:colon])
				value := strings.TrimSpace(p[colon+1:])
				if label != "" && value != "" {
					additionalParams[label] = value
				}
			}
			// Seller type
			if p == "Агенция" || p == "Частно лице" || p == "Частно лицо" {
				d.SellerType = p
				paramsRecognized = true
				continue
			}
			// Floor
			if strings.HasPrefix(p, "Етаж:") {
				sawFloorKey = true
				paramsRecognized = true
				floorVal := strings.TrimSpace(strings.TrimPrefix(p, "Етаж:"))
				// Normalize Партер → 0
				if reParterDetail.MatchString(floorVal) {
					floorVal = reParterDetail.ReplaceAllString(floorVal, "0")
				}
				d.Floor = floorVal
				continue
			}
			// Gas
			if strings.HasPrefix(p, "Газ:") {
				sawGasKey = true
				paramsRecognized = true
				d.HeatingGas = strings.TrimSpace(strings.TrimPrefix(p, "Газ:"))
				continue
			}
			// TEC (imot.bg uses mixed Cyrillic/Latin: ТEЦ where E can be either)
			if strings.HasPrefix(p, "ТEЦ:") || strings.HasPrefix(p, "ТЕЦ:") {
				sawTECKey = true
				paramsRecognized = true
				val := p
				val = strings.TrimPrefix(val, "ТEЦ:")
				val = strings.TrimPrefix(val, "ТЕЦ:")
				d.HeatingTEC = strings.TrimSpace(val)
				continue
			}
			// Construction type (Тухла, Панел, ЕПК, etc.)
			if p == "Тухла" || p == "Панел" || p == "ЕПК" || p == "Гредоред" || p == "Метална конструкция" {
				d.ConstructionType = p
				paramsRecognized = true
				continue
			}
			// Year
			if strings.HasPrefix(p, "Въведен в експлоатация") {
				sawYearKey = true
				paramsRecognized = true
				p = strings.TrimSuffix(p, ",")
				p = strings.TrimSpace(p)
				if m2 := reYearRange.FindStringSubmatch(p); len(m2) > 2 {
					d.YearBuilt = m2[1] + "-" + m2[2]
				} else if m2 := reYearExact.FindStringSubmatch(p); len(m2) > 1 {
					d.YearBuilt = m2[1]
				} else if strings.Contains(p, "Ще бъде въведен") {
					// Extract expected year: "Ще бъде въведен в експлоатация 2026 г."
					if m2 := reYearExact.FindStringSubmatch(p); len(m2) > 1 {
						d.YearBuilt = "under construction - " + m2[1]
					} else {
						d.YearBuilt = "under construction"
					}
				} else if strings.HasSuffix(p, "Въведен в експлоатация") || strings.HasSuffix(p, "Въведен в експлоатация ,") {
					d.YearBuilt = ""
				}
				continue
			}
		}
	}
	if len(additionalParams) > 0 {
		d.SourceParams = additionalParams
	}

	setParamsEvidence := func(key, value string, sawKey bool) {
		switch {
		case value != "":
			ev.present(key, value, DetailReasonParamsBlock)
		case sawKey:
			ev.unknown(key, DetailReasonParamsValueUnparsed)
		case paramsRecognized:
			ev.verifiedAbsent(key, nil, DetailReasonParamsKeyAbsent)
		case paramsBlockSeen:
			// The block matched but rendered no recognizable key, so it proves
			// nothing about the keys it omits.
			ev.unknown(key, DetailReasonParamsUnrecognized)
		default:
			ev.unknown(key, DetailReasonNoSelectorHit)
		}
	}
	setParamsEvidence(DetailKeyFloor, d.Floor, sawFloorKey)
	setParamsEvidence(DetailKeyHeatingGas, d.HeatingGas, sawGasKey)
	setParamsEvidence(DetailKeyHeatingTEC, d.HeatingTEC, sawTECKey)
	setParamsEvidence(DetailKeyYearBuilt, d.YearBuilt, sawYearKey)
	if d.SellerType != "" {
		ev.present(DetailKeySellerType, d.SellerType, DetailReasonParamsBlock)
	} else if paramsBlockSeen {
		ev.unknown(DetailKeySellerType, DetailReasonParamsUnrecognized)
	} else {
		ev.unknown(DetailKeySellerType, DetailReasonNoSelectorHit)
	}
	if d.ConstructionType != "" {
		ev.present(DetailKeyConstructionType, d.ConstructionType, DetailReasonParamsBlock)
	} else if paramsBlockSeen {
		ev.unknown(DetailKeyConstructionType, DetailReasonParamsUnrecognized)
	} else {
		ev.unknown(DetailKeyConstructionType, DetailReasonNoSelectorHit)
	}

	// 3. Phones from detail page - collect all unique phone numbers
	var phoneList []string
	seen := make(map[string]bool)
	phoneBlocks := reDetailPhone.FindAllStringSubmatch(html, -1)
	// Regex to match Bulgarian phone-like sequences
	rePhoneDigits := regexp.MustCompile(`(?:\+359|0)[\d\s/\-]*\d`)
	for _, m := range phoneBlocks {
		// Strip all HTML tags first
		ph := stripTags(m[1])
		// Split on double-space or semicolon (common separators between two phones)
		for _, segment := range strings.Split(ph, "  ") {
			segment = strings.TrimSpace(segment)
			if segment == "" {
				continue
			}
			for _, part := range strings.Split(segment, ";") {
				part = strings.TrimSpace(part)
				for _, match := range rePhoneDigits.FindAllString(part, -1) {
					p := strings.ReplaceAll(match, " ", "")
					p = strings.ReplaceAll(p, "/", "")
					p = strings.ReplaceAll(p, "-", "")
					if len(p) < 5 {
						continue
					}
					if !seen[p] {
						seen[p] = true
						phoneList = append(phoneList, p)
					}
				}
			}
		}
	}
	if len(phoneList) > 0 {
		d.Phones = strings.Join(phoneList, ";")
	}
	if d.Phones != "" {
		ev.present(DetailKeyPhones, d.Phones, DetailReasonPhoneBlock)
	} else if len(phoneBlocks) > 0 {
		// The phone container exists but resolved no number; it may hide the
		// number behind a click, so this is unknown rather than absence.
		ev.unknown(DetailKeyPhones, DetailReasonPhoneBlockUnresolved)
	} else {
		ev.unknown(DetailKeyPhones, DetailReasonNoSelectorHit)
	}

	// 4. Agency URL from class="url" div
	agencyURLBlockSeen := false
	if m := reDetailAgencyURL.FindStringSubmatch(html); len(m) > 1 {
		agencyURLBlockSeen = true
		d.AgencyURL = stripTags(m[1])
	}
	if d.AgencyURL != "" {
		ev.present(DetailKeyAgencyURL, d.AgencyURL, DetailReasonAgencyURLBlock)
	} else if agencyURLBlockSeen {
		ev.unknown(DetailKeyAgencyURL, DetailReasonAgencyURLBlockEmpty)
	} else {
		ev.unknown(DetailKeyAgencyURL, DetailReasonNoSelectorHit)
	}

	if m := reDetailViewCount.FindStringSubmatch(html); len(m) > 1 {
		d.ViewCount = parsePrice(m[1])
		// A proven zero is a present value, not a missing one.
		ev.present(DetailKeyViewCount, d.ViewCount, DetailReasonViewCountMarker)
	} else {
		ev.unknown(DetailKeyViewCount, DetailReasonNoSelectorHit)
	}
	correctedMatch := reDetailCorrectedAt.FindStringSubmatch(html)
	if len(correctedMatch) > 5 {
		hour, _ := strconv.Atoi(correctedMatch[1])
		minute, _ := strconv.Atoi(correctedMatch[2])
		day, _ := strconv.Atoi(correctedMatch[3])
		month := parseBulgarianMonth(correctedMatch[4])
		year, _ := strconv.Atoi(correctedMatch[5])
		if month != 0 {
			loc, err := time.LoadLocation("Europe/Sofia")
			if err != nil {
				loc = time.FixedZone("Europe/Sofia", 3*60*60)
			}
			d.CorrectedAt = time.Date(year, month, day, hour, minute, 0, 0, loc).Format(time.RFC3339)
		}
	}
	switch {
	case d.CorrectedAt != "":
		ev.present(DetailKeyCorrectedAt, d.CorrectedAt, DetailReasonCorrectedAtMarker)
	case len(correctedMatch) > 5:
		ev.unknown(DetailKeyCorrectedAt, DetailReasonCorrectedAtUnparsed)
	default:
		ev.unknown(DetailKeyCorrectedAt, DetailReasonNoSelectorHit)
	}

	// 5. URL from the page itself (canonical). og:url wins; the canonical link is
	// the fallback for pages that omit it.
	if canonical, _ := advertIdentity(html); canonical != "" {
		d.URL = canonical
	}

	// 6. Photo URLs. The advert's own photographs are read from its primary
	// gallery (#rezon-gallery), the identity-matched structured-data offer and
	// og:image; recommendation cards elsewhere on the page advertise other
	// adverts. The explicit no-photo placeholder in the primary image position
	// proves an empty gallery, while a page with no marker at all stays unknown
	// rather than clearing stored photographs.
	photos, noPhotoPlaceholder := advertPhotoURLs(html)
	switch {
	case len(photos) > 0:
		d.PhotoURL = photos[0]
		d.PhotoURLs = photos
		ev.present(DetailKeyPhotoURLs, photos, DetailReasonPhotoSelector)
	case noPhotoPlaceholder && recognizedAdvertLayout(html):
		ev.verifiedAbsent(DetailKeyPhotoURLs, nil, DetailReasonNoPhotoPlaceholder)
	default:
		ev.unknown(DetailKeyPhotoURLs, DetailReasonNoSelectorHit)
	}

	// 7. Feature tags from the "Особености" block. A matched block is not by
	// itself proof that the advert has no features: changed inner markup renders
	// tags this parser does not recognize, and treating that as verified absence
	// cleared stored features. A provably empty container, or a complete
	// recognized advert that rendered no features section at all, is absence.
	// The features section lives inside the primary advert subtree. When the
	// page carries that subtree, scope the search to it so a recommendation
	// card's feature list cannot stand in for this advert's section.
	featuresScope := html
	if region, ok := elementInnerHTML(html, func(_, attrs string) bool {
		return hasClass(attrs, "ad2023")
	}); ok {
		featuresScope = region
	}
	featuresBlockSeen := false
	featuresContainerEmpty := false
	if m := reFeaturesBlock.FindStringSubmatch(featuresScope); len(m) > 1 {
		featuresBlockSeen = true
		seen := make(map[string]bool)
		for _, item := range reFeatureItem.FindAllStringSubmatch(m[1], -1) {
			f := strings.TrimSpace(item[1])
			if f == "" || seen[f] {
				continue
			}
			seen[f] = true
			d.Features = append(d.Features, f)
		}
		// The capture stops at the first closing </div>, so drop that one
		// delimiter before judging whether the container is empty.
		items := m[1]
		if idx := strings.LastIndex(items, "</div>"); idx >= 0 {
			items = items[:idx]
		}
		featuresContainerEmpty = strings.TrimSpace(stripTags(items)) == "" && !strings.Contains(items, "<")
	}
	featuresHeadingSeen := reFeaturesHeading.MatchString(featuresScope)
	switch {
	case len(d.Features) > 0:
		ev.present(DetailKeyFeatures, d.Features, DetailReasonFeaturesBlock)
	case featuresContainerEmpty:
		// The source rendered its features container with nothing in it. Raw
		// stays null: a non-present entry may not carry a value, and the state
		// plus reason carry the absence proof.
		ev.verifiedAbsent(DetailKeyFeatures, nil, DetailReasonFeaturesBlockEmpty)
	case featuresBlockSeen || featuresHeadingSeen:
		// The container exists (or its heading does) but its inner markup
		// produced no recognized tag, so it proves nothing about the advert's
		// features.
		ev.unknown(DetailKeyFeatures, DetailReasonFeaturesUnrecognized)
	case recognizedAdvertLayout(html):
		// A complete, identity-verified advert that rendered no features section
		// advertised no features: the land and parking adverts do this.
		ev.verifiedAbsent(DetailKeyFeatures, nil, DetailReasonFeaturesAbsent)
	default:
		ev.unknown(DetailKeyFeatures, DetailReasonNoSelectorHit)
	}

	// 8. Published timestamp.
	publishedMatch := reDetailPublishedAt.FindStringSubmatch(html)
	if len(publishedMatch) > 5 {
		hour, _ := strconv.Atoi(publishedMatch[1])
		minute, _ := strconv.Atoi(publishedMatch[2])
		day, _ := strconv.Atoi(publishedMatch[3])
		month := parseBulgarianMonth(publishedMatch[4])
		year, _ := strconv.Atoi(publishedMatch[5])
		if month != 0 {
			loc, err := time.LoadLocation("Europe/Sofia")
			if err != nil {
				loc = time.FixedZone("Europe/Sofia", 3*60*60)
			}
			d.PublishedAt = time.Date(year, month, day, hour, minute, 0, 0, loc).Format(time.RFC3339)
		}
	}
	switch {
	case d.PublishedAt != "":
		ev.present(DetailKeyPublishedAt, d.PublishedAt, DetailReasonPublishedAtMarker)
	case len(publishedMatch) > 5:
		ev.unknown(DetailKeyPublishedAt, DetailReasonPublishedAtUnparsed)
	default:
		ev.unknown(DetailKeyPublishedAt, DetailReasonNoSelectorHit)
	}

	// 9. Broker block and agency office.
	if m := reBrokerName.FindStringSubmatch(html); len(m) > 1 {
		d.BrokerName = strings.TrimSpace(m[1])
	}
	if m := reBrokerPhone.FindStringSubmatch(html); len(m) > 1 {
		phone := strings.TrimSpace(stripTags(m[1]))
		phone = strings.NewReplacer(" ", "", "/", "", "-", "").Replace(phone)
		d.BrokerPhone = phone
	}
	if m := reAgencyOffice.FindStringSubmatch(html); len(m) > 1 {
		d.AgencyOffice = strings.TrimSpace(stripTags(m[1]))
	}
	setStringEvidence := func(key, value, presentReason string) {
		if value != "" {
			ev.present(key, value, presentReason)
			return
		}
		ev.unknown(key, DetailReasonNoSelectorHit)
	}
	setStringEvidence(DetailKeyBrokerName, d.BrokerName, DetailReasonBrokerNameBlock)
	setStringEvidence(DetailKeyBrokerPhone, d.BrokerPhone, DetailReasonBrokerPhoneBlock)
	setStringEvidence(DetailKeyAgencyOffice, d.AgencyOffice, DetailReasonAgencyOfficeBlock)

	// 10. VAT note.
	if reVatNote.MatchString(html) {
		d.VatNote = "Не се начислява ДДС"
		ev.present(DetailKeyVatNote, d.VatNote, DetailReasonVatNoteMarker)
	} else {
		ev.unknown(DetailKeyVatNote, DetailReasonNoSelectorHit)
	}

	d.FieldEvidence = ev.fields
	return d
}

func normalizePhotoURL(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.Trim(raw, `"'`)
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	} else if strings.HasPrefix(raw, "http://") {
		raw = "https://" + strings.TrimPrefix(raw, "http://")
	} else if !strings.HasPrefix(raw, "https://") && strings.Contains(raw, "focus.bg/imot/photosimotbg/") {
		raw = "https://" + strings.TrimPrefix(raw, "/")
	}
	parsed, err := neturl.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return raw
	}
	// imot emits both `/509/big1/...` and `/509//big1/...` for the same
	// photograph. Collapse only repeated slashes in the path so those source
	// spellings share one identity; query strings remain untouched.
	for strings.Contains(parsed.Path, "//") {
		parsed.Path = strings.ReplaceAll(parsed.Path, "//", "/")
	}
	return parsed.String()
}

// Primary advert layout and photo extraction --------------------------------
//
// An imot.bg detail page renders the advert's own content under .ad2023 and
// several recommendation cards for other adverts elsewhere on the page. Only
// the primary gallery (#rezon-gallery), the identity-matched structured-data
// offer and og:image describe this advert, so every photograph is read from
// those scopes instead of a page-wide URL scan.

// noPhotoPlaceholderURL is imot.bg's explicit "this advert has no photograph"
// image in the primary image position. Recommendation cards and notification
// popups render a smaller 490x341 placeholder outside the primary advert
// subtree; that is not evidence about this advert.
const noPhotoPlaceholderURL = "https://www.imot.bg/images/picturess/nophoto_660x495.svg"

// Detail evidence reasons for source structures this parser recognizes but the
// shared reason vocabulary did not name before.
const (
	DetailReasonNoPhotoPlaceholder = "no_photo_placeholder"
	DetailReasonFeaturesAbsent     = "features_absent"
)

// rawTextElements are elements whose content HTML does not parse as markup. Tag
// scanning skips them so a script string that looks like a tag cannot be read
// as page structure.
var rawTextElements = map[string]bool{
	"script": true, "style": true, "textarea": true, "title": true,
}

// voidElements never have children, so a tag scanner must not wait for their
// closing tag.
var voidElements = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true,
	"hr": true, "img": true, "input": true, "link": true, "meta": true,
	"param": true, "source": true, "track": true, "wbr": true,
}

// tagToken is one tag found while scanning HTML.
type tagToken struct {
	closing   bool
	selfClose bool
	name      string
	attrs     string
	start     int
	end       int
}

// scanTags walks the tags of one HTML fragment in order, skipping comments,
// declarations and the raw text of script/style/textarea/title. It calls fn for
// every remaining tag and stops when fn returns false. The result is false when
// the fragment contains an unterminated comment, declaration or raw-text
// element, which is how a truncated page stays recognizable.
func scanTags(html string, fn func(tagToken) bool) bool {
	i := 0
	for i < len(html) {
		lt := strings.IndexByte(html[i:], '<')
		if lt < 0 {
			return true
		}
		i += lt
		rest := html[i:]
		switch {
		case strings.HasPrefix(rest, "<!--"):
			end := strings.Index(rest[4:], "-->")
			if end < 0 {
				return false
			}
			i += 4 + end + 3
			continue
		case strings.HasPrefix(rest, "<!"), strings.HasPrefix(rest, "<?"):
			end := strings.IndexByte(rest, '>')
			if end < 0 {
				return false
			}
			i += end + 1
			continue
		}
		m := reTagToken.FindStringSubmatchIndex(rest)
		if m == nil || m[0] != 0 {
			i++
			continue
		}
		closing := rest[m[2]:m[3]] == "/"
		name := strings.ToLower(rest[m[4]:m[5]])
		attrs := rest[m[6]:m[7]]
		tagEnd := i + m[1]
		selfClose := strings.HasSuffix(strings.TrimSpace(rest[:m[1]-1]), "/")
		if !closing && !selfClose && rawTextElements[name] {
			end := indexClosingTag(html, tagEnd, name)
			if end < 0 {
				return false
			}
			i = end
			continue
		}
		if !fn(tagToken{closing: closing, selfClose: selfClose, name: name, attrs: attrs, start: i, end: tagEnd}) {
			return true
		}
		i = tagEnd
	}
	return true
}

// indexClosingTag returns the offset of the first closing tag for name at or
// after from, or -1 when the raw-text element never closes.
func indexClosingTag(html string, from int, name string) int {
	lower := strings.ToLower(html[from:])
	needle := "</" + name
	for at := 0; ; {
		rel := strings.Index(lower[at:], needle)
		if rel < 0 {
			return -1
		}
		pos := at + rel
		after := pos + len(needle)
		if after >= len(lower) {
			return from + pos
		}
		switch lower[after] {
		case '>', ' ', '\t', '\n', '\r', '/':
			return from + pos
		}
		at = pos + 1
	}
}

// attrValue returns one tag attribute's value. Attribute names are matched
// case-insensitively, as HTML requires; the value keeps its original case.
func attrValue(attrs, name string) (string, bool) {
	for _, m := range reAttr.FindAllStringSubmatch(attrs, -1) {
		if !strings.EqualFold(m[1], name) {
			continue
		}
		switch {
		case m[2] != "":
			return m[2], true
		case m[3] != "":
			return m[3], true
		default:
			return m[4], true
		}
	}
	return "", false
}

// hasClass reports whether a tag's class attribute carries want as a whole
// class token, so "ad2023" never matches a longer unrelated class name.
func hasClass(attrs, want string) bool {
	value, ok := attrValue(attrs, "class")
	if !ok {
		return false
	}
	for _, token := range strings.Fields(value) {
		if token == want {
			return true
		}
	}
	return false
}

// idEquals reports whether a tag's id attribute is exactly want.
func idEquals(attrs, want string) bool {
	value, ok := attrValue(attrs, "id")
	return ok && value == want
}

// elementInnerHTML returns the inner HTML of the first element whose start tag
// matches. It reports false when the page has no such element or the element
// never closes, so a truncated page cannot yield a partial region.
func elementInnerHTML(html string, match func(name, attrs string) bool) (string, bool) {
	depth := 0
	root := ""
	innerStart := -1
	inner := ""
	closed := false
	scanTags(html, func(t tagToken) bool {
		if depth == 0 {
			if !t.closing && match(t.name, t.attrs) {
				if t.selfClose || voidElements[t.name] {
					closed = true
					return false
				}
				root = t.name
				innerStart = t.end
				depth = 1
			}
			return true
		}
		if t.closing {
			if t.name == root {
				depth--
				if depth == 0 {
					inner = html[innerStart:t.start]
					closed = true
					return false
				}
			}
			return true
		}
		if t.name == root && !t.selfClose && !voidElements[t.name] {
			depth++
		}
		return true
	})
	return inner, closed
}

// forEachImg calls fn with every img tag's attributes, skipping script and
// style content.
func forEachImg(html string, fn func(attrs string) bool) {
	scanTags(html, func(t tagToken) bool {
		if t.closing || t.name != "img" {
			return true
		}
		return fn(t.attrs)
	})
}

// ogImageURL returns the page's og:image value, preferring the property
// attribute and accepting name= for pages that use the older spelling.
func ogImageURL(html string) string {
	for _, m := range reMetaTag.FindAllStringSubmatch(html, -1) {
		property, ok := attrValue(m[1], "property")
		if !ok {
			property, _ = attrValue(m[1], "name")
		}
		if !strings.EqualFold(strings.TrimSpace(property), "og:image") {
			continue
		}
		if content, ok := attrValue(m[1], "content"); ok {
			return content
		}
	}
	return ""
}

// recognizedAdvertLayout reports whether the page is a complete,
// identity-verified imot.bg advert layout: the page ends normally, carries its
// own advert identity and renders the .ad2023 > .left primary subtree. Only such
// a page justifies reading an omitted optional section as the source's absence;
// an unrecognized or truncated page proves nothing and leaves the field unknown.
func recognizedAdvertLayout(html string) bool {
	if _, advertID := advertIdentity(html); advertID == "" {
		return false
	}
	if !strings.Contains(strings.ToLower(html), "</html>") {
		return false
	}
	region, ok := elementInnerHTML(html, func(_, attrs string) bool {
		return hasClass(attrs, "ad2023")
	})
	if !ok {
		return false
	}
	_, ok = elementInnerHTML(region, func(_, attrs string) bool {
		return hasClass(attrs, "left")
	})
	return ok
}

// primaryNoPhotoPlaceholder reports whether the advert's own primary image
// position rendered imot.bg's explicit no-photo placeholder.
func primaryNoPhotoPlaceholder(html string) bool {
	if og := ogImageURL(html); og != "" && isNoPhotoPlaceholder(og) {
		return true
	}
	region, ok := elementInnerHTML(html, func(_, attrs string) bool {
		return hasClass(attrs, "ad2023")
	})
	if !ok {
		return false
	}
	found := false
	forEachImg(region, func(attrs string) bool {
		for _, raw := range imgCandidateURLs(attrs) {
			if isNoPhotoPlaceholder(raw) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// isNoPhotoPlaceholder reports whether one raw URL is the explicit primary
// placeholder.
func isNoPhotoPlaceholder(raw string) bool {
	return normalizePhotoURL(raw) == noPhotoPlaceholderURL
}

// imgCandidateURLs returns the advertised photograph URLs of one img tag in
// discovery order: the fullscreen gallery attribute, the lazy source, then the
// eager source. Ranking still decides which variant survives deduplication, so
// this order only fixes what is discovered.
func imgCandidateURLs(attrs string) []string {
	var urls []string
	for _, name := range []string{"data-src-gallery", "data-src", "src"} {
		if value, ok := attrValue(attrs, name); ok && strings.TrimSpace(value) != "" {
			urls = append(urls, value)
		}
	}
	return urls
}

// galleryPhotoRefs reads the advert's own photograph references from its
// primary gallery. #rezon-gallery wraps the fullscreen carousel and the small
// picture list, so both the fullscreen and the advertised small variants are
// read; recommendation cards and notification popups live outside it. Script
// content is skipped by the scanner and promo labels are not photographs.
func galleryPhotoRefs(html string) []string {
	gallery, ok := elementInnerHTML(html, func(_, attrs string) bool {
		return idEquals(attrs, "rezon-gallery")
	})
	if !ok {
		return nil
	}
	var refs []string
	forEachImg(gallery, func(attrs string) bool {
		if hasClass(attrs, "promo") {
			return true
		}
		refs = append(refs, imgCandidateURLs(attrs)...)
		return true
	})
	return refs
}

// jsonLDPhotoRefs returns the photographs advertised by the page's structured
// data, but only when the block's own identity proves it describes this advert.
// A JSON-LD block for another advert, or one declaring conflicting advert
// numbers, contributes nothing.
func jsonLDPhotoRefs(html, advertID string) []string {
	if advertID == "" {
		return nil
	}
	var refs []string
	for _, m := range reJSONLDScript.FindAllStringSubmatch(html, -1) {
		var doc any
		if err := json.Unmarshal([]byte(strings.TrimSpace(m[1])), &doc); err != nil {
			continue
		}
		if !jsonLDIdentityMatches(doc, advertID) {
			continue
		}
		walkJSONLD(doc, func(key string, value any) {
			if key == "image" {
				addJSONLDImage(value, &refs)
			}
		})
	}
	return refs
}

// jsonLDIdentityMatches reports whether every advert number a structured-data
// document declares under url/sku is the page's own advert number, and that it
// declares at least one. The product name is deliberately not consulted: a sale
// advert whose JSON-LD label says "Дава под Наем" still owns its photographs.
func jsonLDIdentityMatches(doc any, advertID string) bool {
	seen := 0
	matched := true
	walkJSONLD(doc, func(key string, value any) {
		if key != "url" && key != "sku" {
			return
		}
		text, ok := value.(string)
		if !ok {
			return
		}
		id := AdvertIDFromURL(text)
		if id == "" {
			return
		}
		seen++
		if id != advertID {
			matched = false
		}
	})
	return seen > 0 && matched
}

// addJSONLDImage appends the URL forms of one schema.org image value: a URL
// string, a list of them, or an ImageObject with url/contentUrl.
func addJSONLDImage(value any, refs *[]string) {
	switch v := value.(type) {
	case string:
		*refs = append(*refs, v)
	case []any:
		for _, item := range v {
			addJSONLDImage(item, refs)
		}
	case map[string]any:
		for _, key := range []string{"url", "contentUrl"} {
			if text, ok := v[key].(string); ok {
				*refs = append(*refs, text)
				return
			}
		}
	}
}

// walkJSONLD visits every key/value pair of a decoded JSON document.
func walkJSONLD(value any, visit func(key string, value any)) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			visit(key, child)
			walkJSONLD(child, visit)
		}
	case []any:
		for _, child := range v {
			walkJSONLD(child, visit)
		}
	}
}

// advertPhotoURLs collects the advert's own photographs together with every
// advertised size variant. Photographs are grouped by identity (the URL
// basename) and each group is ordered fullscreen-first, so the largest
// advertised variant leads and the source's own smaller variants remain as
// fallbacks. Identities keep the order the page first presented them: primary
// gallery slides, then the identity-matched advertised offer list, then the
// og:image social fallback. The second result reports the explicit primary
// no-photo placeholder.
func advertPhotoURLs(html string) ([]string, bool) {
	_, advertID := advertIdentity(html)

	type photoVariants struct {
		urls []string
		seen map[string]bool
	}
	var identityOrder []string
	groups := make(map[string]*photoVariants)
	add := func(raw string) {
		url := normalizePhotoURL(raw)
		if !isSupportedPhotoURL(url) {
			return
		}
		identity := photoBasename(url)
		if identity == "" {
			return
		}
		group, ok := groups[identity]
		if !ok {
			group = &photoVariants{seen: make(map[string]bool)}
			groups[identity] = group
			identityOrder = append(identityOrder, identity)
		}
		if group.seen[url] {
			return
		}
		group.seen[url] = true
		// Insert the variant so the largest advertised size leads. Equal ranks
		// keep their discovery order.
		rank := photoVariantRank(url)
		at := len(group.urls)
		for i, existing := range group.urls {
			if rank > photoVariantRank(existing) {
				at = i
				break
			}
		}
		group.urls = append(group.urls, "")
		copy(group.urls[at+1:], group.urls[at:])
		group.urls[at] = url
	}

	for _, raw := range galleryPhotoRefs(html) {
		add(raw)
	}
	for _, raw := range jsonLDPhotoRefs(html, advertID) {
		add(raw)
	}
	if og := ogImageURL(html); og != "" {
		add(og)
	}

	if len(identityOrder) == 0 {
		return nil, primaryNoPhotoPlaceholder(html)
	}
	photos := make([]string, 0, len(identityOrder))
	for _, identity := range identityOrder {
		photos = append(photos, groups[identity].urls...)
	}
	return photos, false
}

// uniquePhotoURLs keeps its original entry point for callers that only want the
// photograph list. It now returns the scoped, identity-safe collection, with
// each photograph's advertised variants grouped fullscreen-first; the explicit
// no-photo placeholder is reported by advertPhotoURLs.
func uniquePhotoURLs(html string) []string {
	photos, _ := advertPhotoURLs(html)
	return photos
}

// supportedPhotoExtensions are the advertised gallery formats this parser
// accepts. The .pic suffix is imot.bg's legacy photograph endpoint and is
// returned by the fullscreen gallery alongside the regular image suffixes.
// SVG is deliberately absent: imot.bg uses it for the no-photo placeholder
// and for interface icons, never for an advert photograph.
var supportedPhotoExtensions = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".pic": true,
}

// isSupportedPhotoURL reports whether one advertised URL is an absolute
// http(s) reference to a supported photograph format. imot.bg's own interface
// artwork (placeholders, promo labels, notification icons) lives under
// /images/picturess/ and is never an advertised photograph.
func isSupportedPhotoURL(url string) bool {
	lower := strings.ToLower(url)
	if !strings.HasPrefix(lower, "https://") && !strings.HasPrefix(lower, "http://") {
		return false
	}
	if strings.Contains(lower, "/images/picturess/") {
		return false
	}
	path := lower
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	dot := strings.LastIndexByte(path, '.')
	if dot < 0 {
		return false
	}
	return supportedPhotoExtensions[path[dot:]]
}

// decorativeEntities are numeric entities imot.bg bakes into listing text as
// bullets/emoji. They are stripped while still encoded so UnescapeString does
// not turn them into pictographs.
var decorativeEntities = strings.NewReplacer(
	"&#128204;", "", // pushpin
	"&#10071;", "", // exclamation mark
	"&#128311;", "", // blue diamond
	"&#10024;", "", // sparkles
	"&#128205;", "", // round pushpin
	"&#128188;", "", // briefcase
	"&#9889;", "", // high voltage
	"&#128222;", "", // telephone
)

// photoVariantRank scores the size variant of a photograph URL: a larger
// number is a larger image. Live pages measured the same photograph as 800x354
// at /big/ and 1600x708 at /big1/, so the fullscreen /big1/ gallery render
// outranks the /big/ social image; an unmarked URL is a thumbnail. Ranking
// (rather than a one-off /big1/ swap) keeps the choice monotonic: a later
// duplicate only replaces the stored URL when it is strictly larger.
func photoVariantRank(url string) int {
	lower := strings.ToLower(url)
	switch {
	case strings.Contains(lower, "/big1/"):
		return 3
	case strings.Contains(lower, "/big/") || strings.Contains(lower, "/big2/"):
		return 2
	default:
		return 1
	}
}

// photoBasename extracts the stable photograph identity from a photograph URL,
// e.g. ".../2/768//big1/2c178773245606768_e1.jpg" -> "2c178773245606768_e1.jpg".
// Every size variant of one photograph shares this basename, so it is the dedup
// key; the CDN host and the variant directory are deliberately not part of it,
// because the same image is advertised from several CDN hosts. The name is kept
// case-sensitive: CDN paths are case-sensitive, and two photographs can differ
// by case alone.
func photoBasename(url string) string {
	if i := strings.IndexAny(url, "?#"); i >= 0 {
		url = url[:i]
	}
	if i := strings.LastIndexByte(url, '/'); i >= 0 {
		return url[i+1:]
	}
	return url
}
