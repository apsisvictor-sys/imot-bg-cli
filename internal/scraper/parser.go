package scraper

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var (
	reTotalCount     = regexp.MustCompile(`от общо\s+(\d+)\+?\s+обяви`)
	reMaxPages       = 50 // safety cap when total count is unknown
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
	reDetailText      = regexp.MustCompile(`(?s)class="text"[^>]*>(.*?)</div>`)
	reDetailParams    = regexp.MustCompile(`class="params"[^>]*>(.*?)</div>`)
	reDetailPhone     = regexp.MustCompile(`(?s)class="phone[^>]*"[^>]*>(.*?)</div>`)
	reDetailAgencyURL = regexp.MustCompile(`(?s)class="url"[^>]*>(.*?)</div>`)
	reDetailOGURL     = regexp.MustCompile(`property="og:url" content="([^"]+)"`)
	reParterDetail   = regexp.MustCompile(`(?i)партер`)
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
		l.Agency = strings.TrimSpace(m[1])
		// Unescape HTML entities
		l.Agency = strings.ReplaceAll(l.Agency, "&#39;", "'")
		l.Agency = strings.ReplaceAll(l.Agency, "&amp;", "&")
		l.Agency = strings.ReplaceAll(l.Agency, "&quot;", "\"")
	}

	// Generate hash for dedup
	if l.ID == "" {
		l.ID = generateHash(l)
	}

	return l
}

func extractType(title string) string {
	// Title looks like "Продава 1-СТАЕН" or "Продава КЪЩА"
	title = strings.TrimPrefix(title, "Продава ")
	title = strings.TrimPrefix(title, "Се отдава ")
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

	// Check for known types
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
	for _, t := range types {
		if strings.Contains(typePart, t) {
			return t
		}
	}
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
	// But description can also contain commas, so we parse from left

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
		desc = strings.TrimSpace(desc)
		desc = strings.ReplaceAll(desc, "&#39;", "'")
		desc = strings.ReplaceAll(desc, "&amp;", "&")
		desc = strings.ReplaceAll(desc, "&quot;", "\"")
		if len(desc) > 500 {
			desc = desc[:497] + "..."
		}
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

func stripTags(s string) string {
	var result strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			continue
		}
		if !inTag {
			result.WriteRune(r)
		}
	}
	return strings.TrimSpace(result.String())
}

func generateHash(l Listing) string {
	data := fmt.Sprintf("%s|%s|%s|%d|%d|%s", l.Type, l.City, l.Neighborhood, l.PriceEUR, l.SizeSqM, l.Phone)
	h := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%x", h)[:16]
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

// ParseDetail extracts enriched data from a listing's detail page HTML.
// Returns a DetailListing with fields only available on the detail page.
func ParseDetail(html string) DetailListing {
	d := DetailListing{}

	// 1. Full description from class="text" div (first match is the listing text,
	// second is usually "В imot.bg от YYYY г. agency.imot.bg")
	textMatches := reDetailText.FindAllStringSubmatch(html, 2)
	for _, m := range textMatches {
		clean := stripTags(m[1])
		clean = strings.TrimSpace(clean)
		clean = strings.ReplaceAll(clean, "&#39;", "'")
		clean = strings.ReplaceAll(clean, "&amp;", "&")
		clean = strings.ReplaceAll(clean, "&quot;", "\"")
		clean = strings.ReplaceAll(clean, "&#128204;", "")
		clean = strings.ReplaceAll(clean, "&#10071;", "")
		clean = strings.ReplaceAll(clean, "&#128311;", "")
		clean = strings.ReplaceAll(clean, "&#10024;", "")
		clean = strings.ReplaceAll(clean, "&#128205;", "")
		clean = strings.ReplaceAll(clean, "&#128188;", "")
		clean = strings.ReplaceAll(clean, "&#9889;", "")
		clean = strings.ReplaceAll(clean, "&#128222;", "")
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
			if p == "Агенция" || p == "Частно лицо" {
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

	// 5. URL from the page itself (canonical)
	// Extract from og:url or canonical link if available
	if m := reDetailOGURL.FindStringSubmatch(html); len(m) > 1 {
		d.URL = m[1]
	}

	return d
}
