package scraper

import (
	"crypto/sha256"
	"fmt"
	stdhtml "html"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
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
	reDetailPhoto       = regexp.MustCompile(`property="og:image"\s+content="([^"]+)"`)
	reDetailViewCount   = regexp.MustCompile(`Обявата е посетена\s*<span>\s*([0-9 ]+)\s*</span>\s*пъти`)
	reDetailCorrectedAt = regexp.MustCompile(`Коригирана в\s*([0-9]{1,2}):([0-9]{2})\s*на\s*([0-9]{1,2})\s+([^,]+),\s*([0-9]{4})\s*год\.`)
	reDetailPublishedAt = regexp.MustCompile(`Публикувана в\s*([0-9]{1,2}):([0-9]{2})\s*на\s*([0-9]{1,2})\s+([^,<]+),\s*([0-9]{4})\s*год`)
	reFeaturesBlock     = regexp.MustCompile(`(?s)Особености</span>.*?<div class="items">(.*?</div>)\s*</div>`)
	reFeatureItem       = regexp.MustCompile(`<div>\s*([^<]+?)\s*</div>`)
	reBrokerName        = regexp.MustCompile(`Брокер:</div>\s*<div class="name">\s*([^<]+?)\s*</div>`)
	reBrokerPhone       = regexp.MustCompile(`(?s)Брокер:</div>.*?<div class="phone">\s*(?:<small>)?\s*([^<\s][^<]*?)\s*(?:</small>)?\s*</div>`)
	reAgencyOffice      = regexp.MustCompile(`Офис:\s*([^<\n]+?)\s*(?:</div>|\n)`)
	reVatNote           = regexp.MustCompile(`Не се начислява ДДС`)
	reImotPhotoURL      = regexp.MustCompile(`(?i)(?:(?:https?:)?//)?[^"'\s<>]*focus\.bg/imot/photosimotbg/[^"'\s<>]+\.jpg`)
	reParterDetail      = regexp.MustCompile(`(?i)партер`)

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
func ScanListings(html string) CardScan {
	var scan CardScan

	blocks := strings.Split(html, `class="zaglavie"`)
	scan.CardBlocks = len(blocks) - 1
	seenTypes := make(map[string]bool)
	for i := 1; i < len(blocks); i++ {
		block := blocks[i]
		// Limit block to reasonable size (avoid parsing into next listing)
		if idx := strings.Index(block, `class="zaglavie"`); idx > 0 {
			block = block[:idx]
		}

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

	// Title may have location glued without space: "МНОГОСТАЕНград София, Лозенец"
	// Split at first lowercase character to isolate the type keyword
	cutIdx := len(title)
	for i, r := range title {
		if i > 0 && unicode.IsLower(r) {
			cutIdx = i
			break
		}
	}
	typePart := strings.TrimSpace(title[:cutIdx])

	// Property-type keywords. Overlapping keys are common ("БАНКОВ ОФИС"
	// contains "ОФИС", "ЕТАЖ ОТ КЪЩА" contains both "ЕТАЖ" and "КЪЩА"), so the
	// ranking below decides the answer instead of the order of this list.
	types := []string{
		"1-СТАЕН", "2-СТАЕН", "3-СТАЕН", "4-СТАЕН",
		"МНОГОСТАЕН", "МЕЗОНЕТ", "КЪЩА", "ВИЛА",
		"ОФИС", "МАГАЗИН", "ЗАВЕДЕНИЕ", "СКЛАД",
		"ГАРАЖ", "ПАРКОМЯСТО", "ЗЕМЯ", "ПАРЦЕЛ", "АТЕЛИЕ",
		"ЕТАЖ", "ПРОМИШЛЕНО",
		// Business property subtypes (appear under "БИЗНЕС ИМОТ" filter)
		"АВТОМИВКА", "АВТОСЕРВИЗ", "АПТЕКА", "БАНКОВ ОФИС",
		"БЕНЗИНОСТАНЦИЯ", "КЛИНИКА", "ЛЕКАРСКИ КАБИНЕТ", "ФЕРМА",
		"СПА", "СОЛЯРНО СТУДИО", "СТОМАТОЛОГИЧЕН КАБИНЕТ",
		"ТЪРГОВСКИ КОМПЛЕКС", "ФАБРИКА", "ЗАВОД",
		"ФИТНЕС ЗАЛА", "ФРИЗЬОРСКИ", "КОЗМЕТИЧЕН САЛОН",
		"ПАРКИНГ", "ФОТОГРАФСКО СТУДИО", "ДЕТСКИ ЦЕНТЪР",
		"АКВАПАРК", "ВИЛНО СЕЛИЩЕ", "СОЛЯРЕН ПАРК",
		"ДОМ ЗА ВЪЗРАСТНИ ХОРА", "САМОСТОЯТЕЛНА СГРАДА",
		"ХЛАДИЛЕН СКЛАД",
	}
	// 1. An exact match wins outright.
	for _, t := range types {
		if strings.EqualFold(typePart, t) {
			return t, true
		}
	}

	// 2. Otherwise the longest matching key, because the longer key is the more
	// specific type ("БАНКОВ ОФИС" over "ОФИС"). Equal-length ties go to the key
	// that starts earliest in the title, so "ЕТАЖ ОТ КЪЩА" is ЕТАЖ (offset 0)
	// rather than КЪЩА (offset 8); list order is the final tie-break.
	best := ""
	bestPos := -1
	for _, t := range types {
		pos := strings.Index(typePart, t)
		if pos < 0 {
			continue
		}
		longer := len([]rune(t)) > len([]rune(best))
		earlier := len([]rune(t)) == len([]rune(best)) && (bestPos < 0 || pos < bestPos)
		if longer || earlier {
			best = t
			bestPos = pos
		}
	}
	if best != "" {
		return best, true
	}

	// 3. No keyword matched at all.
	return typePart, false
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
	if m := reDetailParams.FindStringSubmatch(html); len(m) > 1 {
		paramsBlockSeen = true
		params := stripTags(m[1])
		paramsParts := strings.Split(params, ",")
		for _, p := range paramsParts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
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

	// 6. Photo URLs from og:image and gallery/CDN references.
	photos := uniquePhotoURLs(html)
	if len(photos) > 0 {
		d.PhotoURL = photos[0]
		d.PhotoURLs = photos
		ev.present(DetailKeyPhotoURLs, photos, DetailReasonPhotoSelector)
	} else {
		// The parser has no marker that proves a gallery is empty, so an advert
		// with no photo selector stays unknown, never verified absence.
		ev.unknown(DetailKeyPhotoURLs, DetailReasonNoSelectorHit)
	}

	// 7. Feature tags from the "Особености" block. A matched block is not by
	// itself proof that the advert has no features: changed inner markup renders
	// tags this parser does not recognize, and treating that as verified absence
	// cleared stored features. Only a provably empty container is absence.
	featuresBlockSeen := false
	featuresContainerEmpty := false
	if m := reFeaturesBlock.FindStringSubmatch(html); len(m) > 1 {
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
	switch {
	case len(d.Features) > 0:
		ev.present(DetailKeyFeatures, d.Features, DetailReasonFeaturesBlock)
	case featuresContainerEmpty:
		// The source rendered its features container with nothing in it. Raw
		// stays null: a non-present entry may not carry a value, and the state
		// plus reason carry the absence proof.
		ev.verifiedAbsent(DetailKeyFeatures, nil, DetailReasonFeaturesBlockEmpty)
	case featuresBlockSeen:
		// The container exists but its inner markup produced no recognized tag,
		// so it proves nothing about the advert's features.
		ev.unknown(DetailKeyFeatures, DetailReasonFeaturesUnrecognized)
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

func normalizePhotoURL(url string) string {
	url = strings.TrimSpace(url)
	url = strings.Trim(url, `"'`)
	if strings.HasPrefix(url, "//") {
		return "https:" + url
	}
	if strings.HasPrefix(url, "http://") {
		return "https://" + strings.TrimPrefix(url, "http://")
	}
	if strings.HasPrefix(url, "https://") {
		return url
	}
	if strings.Contains(url, "focus.bg/imot/photosimotbg/") {
		return "https://" + strings.TrimPrefix(url, "/")
	}
	return url
}

func uniquePhotoURLs(html string) []string {
	// Deduplicate by basename: the gallery renders the same image under several
	// size variants (/big/, /big1/, ...). Keep the largest variant seen for each
	// image, and never downgrade when a later duplicate is smaller.
	byBase := make(map[string]string)
	var order []string
	add := func(raw string) {
		url := normalizePhotoURL(raw)
		if url == "" {
			return
		}
		base := photoBasename(url)
		if existing, ok := byBase[base]; ok {
			if photoVariantRank(url) <= photoVariantRank(existing) {
				return
			}
			byBase[base] = url
			for i, o := range order {
				if photoBasename(o) == base {
					order[i] = url
					break
				}
			}
			return
		}
		byBase[base] = url
		order = append(order, url)
	}

	if m := reDetailPhoto.FindStringSubmatch(html); len(m) > 1 {
		add(m[1])
	}
	for _, raw := range reImotPhotoURL.FindAllString(html, -1) {
		add(raw)
	}
	return order
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

// photoVariantRank scores the size variant of a photosimotbg image URL: a
// larger number is a larger image. /big/ is the og:image/social source and
// outranks the /big1/ gallery render; an unmarked URL is treated as a thumbnail.
// Ranking (rather than a one-off /big1/ swap) keeps the choice monotonic: a
// later duplicate only replaces the stored URL when it is strictly larger.
func photoVariantRank(url string) int {
	switch {
	case strings.Contains(url, "/big/"):
		return 3
	case strings.Contains(url, "/big1/"):
		return 2
	default:
		return 0
	}
}

// photoBasename extracts a stable per-image key from a photosimotbg URL,
// e.g. ".../2/768//big/2c178773245606768_e1.jpg" -> "2c178773245606768_e1.jpg".
func photoBasename(url string) string {
	if i := strings.LastIndex(url, "/"); i >= 0 {
		return url[i+1:]
	}
	return url
}
