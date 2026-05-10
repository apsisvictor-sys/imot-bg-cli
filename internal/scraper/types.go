package scraper

import "time"

// DetailListing holds the enriched data extracted from a listing's detail page.
// Search-page fields (ID, Type, City, Neighborhood, PriceEUR, PriceBGN, SizeSqM)
// are NOT duplicated here — they come from the search card.
type DetailListing struct {
	URL              string `json:"url"`
	FullDescription  string `json:"full_description"`
	Floor            string `json:"floor"`              // e.g. "4-ти от 4", "Партер от 5"
	YearBuilt        string `json:"year_built"`         // e.g. "2007", "1960-1969"
	ConstructionType string `json:"construction_type"`  // e.g. "Тухла", "Панел", "ЕПК"
	HeatingTEC       string `json:"heating_tec"`        // "ДА" or "НЕ"
	HeatingGas       string `json:"heating_gas"`        // "ДА" or "НЕ"
	SellerType       string `json:"seller_type"`        // "Агенция" or "Частно лице"
	Phones           string `json:"phones"`             // semicolon-separated, most complete from detail page
	AgencyURL        string `json:"agency_url"`         // e.g. "mchome.imot.bg"
}

// Listing represents a single real estate listing from imot.bg
type Listing struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	City         string `json:"city"`
	Neighborhood string `json:"neighborhood"`
	PriceEUR     int    `json:"price_eur"`
	PriceBGN     int    `json:"price_bgn"`
	SizeSqM      int    `json:"size_sqm"`
	Floor        string `json:"floor"`
	YearBuilt    string `json:"year_built"`
	Description  string `json:"description"`
	Phone        string `json:"phone"`
	Agency       string `json:"agency"`
	URL          string `json:"url"`
	ScrapedAt    string `json:"scraped_at"`
}

// CityMap maps Bulgarian city names to URL slugs
var CityMap = map[string]string{
	"София":            "grad-sofiya",
	"Варна":            "grad-varna",
	"Бургас":           "grad-burgas",
	"Пловдив":          "grad-plovdiv",
	"Велико Търново":   "grad-veliko-tarnovo",
	"Стара Загора":     "grad-stara-zagora",
	"Русе":             "grad-ruse",
	"Плевен":           "grad-pleven",
	"Шумен":            "grad-shumen",
	"Благоевград":      "grad-blagoevgrad",
	"Перник":           "grad-pernik",
	"Враца":            "grad-vratsa",
	"Габрово":          "grad-gabrovo",
	"Пазарджик":        "grad-pazardzhik",
	"Кърджали":         "grad-kardzhali",
	"Хасково":          "grad-haskovo",
	"Сливен":           "grad-sliven",
	"Добрич":           "grad-dobrich",
	"Ловеч":            "grad-lovech",
	"Монтана":          "grad-montana",
	"Видин":            "grad-vidin",
	"Разград":          "grad-razgrad",
	"Търговище":        "grad-targovishte",
	"Силистра":         "grad-silistra",
	"Кюстендил":        "grad-kyustendil",
	"Ямбол":            "grad-yambol",
	"Смолян":           "grad-smolyan",
}

// OblastMap maps oblast (region) names to URL slugs
var OblastMap = map[string]string{
	"област София":        "oblast-sofia",
	"област Бургас":       "oblast-burgas",
	"област Варна":        "oblast-varna",
	"област Пловдив":      "oblast-plovdiv",
	"област Велико Търново": "oblast-veliko-tarnovo",
	"област Стара Загора": "oblast-stara-zagora",
	"област Русе":         "oblast-ruse",
	"област Плевен":       "oblast-pleven",
	"област Шумен":        "oblast-shumen",
	"област Благоевград":  "oblast-blagoevgrad",
	"област Перник":       "oblast-pernik",
	"област Враца":        "oblast-vratsa",
	"област Габрово":      "oblast-gabrovo",
	"област Пазарджик":    "oblast-pazardzhik",
	"област Кърджали":     "oblast-kardzhali",
	"област Хасково":      "oblast-haskovo",
	"област Сливен":       "oblast-sliven",
	"област Добрич":       "oblast-dobrich",
	"област Ловеч":        "oblast-lovech",
	"област Монтана":      "oblast-montana",
	"област Видин":        "oblast-vidin",
	"област Разград":      "oblast-razgrad",
	"област Търговище":    "oblast-targovishte",
	"област Силистра":     "oblast-silistra",
	"област Кюстендил":    "oblast-kyustendil",
	"област Ямбол":        "oblast-yambol",
	"област Смолян":       "oblast-smolyan",
}

// TypeMap maps Bulgarian property type names to URL slugs
var TypeMap = map[string]string{
	"1-стаен":      "ednostaen",
	"2-стаен":      "dvustaen",
	"3-стаен":      "tristaen",
	"4-стаен":      "chetiristaen",
	"многостаен":   "mnogostaen",
	"мезонет":      "mezonet",
	"къща":         "kashta",
	"вила":         "vila",
	"офис":         "ofis",
	"магазин":      "magazin",
	"заведение":    "zavedenie",
	"склад":        "sklad",
	"гараж":        "garazh-parkomyasto",
	"земя":         "zemedelska-zemya",
	"парцел":       "place-za-stroezh",
}

// SearchParams holds the parameters for a search query
type SearchParams struct {
	City         string
	Type         string
	MinPrice     int
	MaxPrice     int
	MinSqM       int
	MaxSqM       int
	Neighborhood string
	Pages        int
	Rent         bool
}

// FormatTimestamp returns a standard timestamp string
func FormatTimestamp(t time.Time) string {
	return t.Format("2006-01-02T15:04:05Z07:00")
}
