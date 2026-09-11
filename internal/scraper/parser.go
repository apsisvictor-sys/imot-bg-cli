package scraper

import (
	"crypto/sha256"
	"fmt"
	stdhtml "html"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var (
	reTotalCount = regexp.MustCompile(`от общо\s+([0-9 ]+)\+?\s+обяви`)
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
	reDetailPhone       = regexp.MustCompile(`(?s)class="phone[^>]*"[^>]*>(.*?)</div>`)
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
)

// ParseListings extracts listings from HTML
func ParseListings(html string) []Listing {
	var listings []Listing

	blocks := strings.Split(html, `class="zaglavie"`)
	for i := 1; i < len(blocks); i++ {
		block := blocks[i]
		// Limit block to reasonable size (avoid parsing into next listing)
		if idx := strings.Index(block, `class="zaglavie"`); idx > 0 {
			block = block[:idx]
		}

		listing := parseListingBlock(block)
		if listing.Type != "" && (listing.PriceEUR > 0 || listing.SizeSqM > 0) {
			// Extract photo URL from full HTML using listing ID
			if listing.ID != "" {
				photoPat := regexp.MustCompile(`src="(//[^"]*focus\.bg/imot/photosimotbg/[^"]*?/` + regexp.QuoteMeta(listing.ID) + `_[^"]+\.jpg)"`)
				if m := photoPat.FindStringSubmatch(html); len(m) > 1 {
					listing.PhotoURL = "https:" + m[1]
				}
			}
			listings = append(listings, listing)
		}
	}

	return listings
}

func parseListingBlock(block string) Listing {
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

	// Extract type from title
	l.Type = extractType(titleText)

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

	return l
}

func extractType(title string) string {
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
			return t
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
		return best
	}

	// 3. No keyword matched at all.
	return typePart
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
// Looks for pattern: "от общо NNN обяви"
func ParseTotalCount(html string) int {
	m := reTotalCount.FindStringSubmatch(html)
	if len(m) > 1 {
		n, err := strconv.Atoi(strings.ReplaceAll(m[1], " ", ""))
		if err == nil {
			return n
		}
	}
	return 0
}

// HasNoResultsMarker reports whether the page explicitly states that the query
// matched nothing. Only imot.bg's own marker proves an empty result set; the
// mere absence of listing cards does not.
func HasNoResultsMarker(html string) bool {
	return reNoResults.MatchString(html)
}

// ParseDetail extracts enriched data from a listing's detail page HTML.
// Returns a DetailListing with fields only available on the detail page.
func ParseDetail(html string) DetailListing {
	d := DetailListing{}

	// 1. Full description from class="text" div (first match is the listing text,
	// second is usually "В imot.bg от YYYY г. agency.imot.bg")
	textMatches := reDetailText.FindAllStringSubmatch(html, 2)
	for _, m := range textMatches {
		clean := stripTags(m[1])
		clean = decorativeEntities.Replace(clean)
		clean = strings.TrimSpace(stdhtml.UnescapeString(clean))
		clean = strings.ReplaceAll(clean, "\u00a0", " ")
		// Skip the "В imot.bg от" line
		if strings.HasPrefix(clean, "В imot.bg от") {
			continue
		}
		if len(clean) > 20 {
			d.FullDescription = clean
			break
		}
	}

	// 2. Structured params line: class="params"
	// e.g. "Площ: 24 кв.м, Агенция, Етаж: 4-ти от 4, Газ: НЕ, ТEЦ: ДА, Тухла, Въведен в експлоатация 1930 - 1939 г.,"
	if m := reDetailParams.FindStringSubmatch(html); len(m) > 1 {
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
				continue
			}
			// Floor
			if strings.HasPrefix(p, "Етаж:") {
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
				d.HeatingGas = strings.TrimSpace(strings.TrimPrefix(p, "Газ:"))
				continue
			}
			// TEC (imot.bg uses mixed Cyrillic/Latin: ТEЦ where E can be either)
			if strings.HasPrefix(p, "ТEЦ:") || strings.HasPrefix(p, "ТЕЦ:") {
				val := p
				val = strings.TrimPrefix(val, "ТEЦ:")
				val = strings.TrimPrefix(val, "ТЕЦ:")
				d.HeatingTEC = strings.TrimSpace(val)
				continue
			}
			// Construction type (Тухла, Панел, ЕПК, etc.)
			if p == "Тухла" || p == "Панел" || p == "ЕПК" || p == "Гредоред" || p == "Метална конструкция" {
				d.ConstructionType = p
				continue
			}
			// Year
			if strings.HasPrefix(p, "Въведен в експлоатация") {
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

	// 3. Phones from detail page - collect all unique phone numbers
	var phoneList []string
	seen := make(map[string]bool)
	phoneBlocks := reDetailPhone.FindAllStringSubmatch(html, -1)
	// Regex to match Bulgarian phone-like sequences
	rePhoneDigits := regexp.MustCompile(`(?:\+359|0)\d[\d/\-]*\d`)
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

	// 4. Agency URL from class="url" div
	if m := reDetailAgencyURL.FindStringSubmatch(html); len(m) > 1 {
		d.AgencyURL = stripTags(m[1])
	}

	if m := reDetailViewCount.FindStringSubmatch(html); len(m) > 1 {
		d.ViewCount = parsePrice(m[1])
	}
	if m := reDetailCorrectedAt.FindStringSubmatch(html); len(m) > 5 {
		hour, _ := strconv.Atoi(m[1])
		minute, _ := strconv.Atoi(m[2])
		day, _ := strconv.Atoi(m[3])
		month := parseBulgarianMonth(m[4])
		year, _ := strconv.Atoi(m[5])
		if month != 0 {
			loc, err := time.LoadLocation("Europe/Sofia")
			if err != nil {
				loc = time.FixedZone("Europe/Sofia", 3*60*60)
			}
			d.CorrectedAt = time.Date(year, month, day, hour, minute, 0, 0, loc).Format(time.RFC3339)
		}
	}

	// 5. URL from the page itself (canonical)
	// Extract from og:url or canonical link if available
	if m := reDetailOGURL.FindStringSubmatch(html); len(m) > 1 {
		d.URL = m[1]
	}

	// 6. Photo URLs from og:image and gallery/CDN references.
	photos := uniquePhotoURLs(html)
	if len(photos) > 0 {
		d.PhotoURL = photos[0]
		d.PhotoURLs = photos
	}

	// 7. Feature tags from the "Особености" block.
	if m := reFeaturesBlock.FindStringSubmatch(html); len(m) > 1 {
		seen := make(map[string]bool)
		for _, item := range reFeatureItem.FindAllStringSubmatch(m[1], -1) {
			f := strings.TrimSpace(item[1])
			if f == "" || seen[f] {
				continue
			}
			seen[f] = true
			d.Features = append(d.Features, f)
		}
	}

	// 8. Published timestamp.
	if m := reDetailPublishedAt.FindStringSubmatch(html); len(m) > 5 {
		hour, _ := strconv.Atoi(m[1])
		minute, _ := strconv.Atoi(m[2])
		day, _ := strconv.Atoi(m[3])
		month := parseBulgarianMonth(m[4])
		year, _ := strconv.Atoi(m[5])
		if month != 0 {
			loc, err := time.LoadLocation("Europe/Sofia")
			if err != nil {
				loc = time.FixedZone("Europe/Sofia", 3*60*60)
			}
			d.PublishedAt = time.Date(year, month, day, hour, minute, 0, 0, loc).Format(time.RFC3339)
		}
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

	// 10. VAT note.
	if reVatNote.MatchString(html) {
		d.VatNote = "Не се начислява ДДС"
	}

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
