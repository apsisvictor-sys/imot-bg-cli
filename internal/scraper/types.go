package scraper

import (
	"errors"
	"fmt"
	"time"
)

// Search page failure kinds. They are carried in SearchError.Kind so callers can
// branch on the failure without parsing error strings.
const (
	// SearchErrorFetchFailed: the page request failed (non-200 or network error).
	SearchErrorFetchFailed = "fetch_failed"
	// SearchErrorUnreadablePage: the page returned HTTP 200 but showed no
	// listing cards, no total count, and no explicit no-results marker, so it
	// must not be read as an empty result set.
	SearchErrorUnreadablePage = "unreadable_page"
	// SearchErrorTotalCountUnknown: page 1 had no parseable total count, so the
	// remaining pages were bounded by the safety page cap.
	SearchErrorTotalCountUnknown = "total_count_unknown"
	// SearchErrorDetailEnrichment: one or more detail-page enrichments failed.
	SearchErrorDetailEnrichment = "detail_enrichment_failed"
)

// ErrUnreadablePage is the canonical error for a search page that returned HTTP
// 200 but carries no evidence of being a search result page. Its text is what
// SearchError.Error holds when SearchError.Kind is SearchErrorUnreadablePage.
var ErrUnreadablePage = errors.New("search page unreadable: no listing cards, no total count, and no explicit no-results marker")

// Detail page failure kinds carried in DetailError.Kind. A caller branches on
// the kind instead of the error text, the same way search failures are read.
const (
	// DetailErrorFetchFailed: the detail request failed (non-200 or network
	// error). A 404 is a fetch failure with HTTPStatus 404.
	DetailErrorFetchFailed = "fetch_failed"
	// DetailErrorChallengePage: the page is a bot/captcha interstitial, not an
	// advert. It must never be read as an advert with empty fields.
	DetailErrorChallengePage = "challenge_page"
	// DetailErrorRemovedAdvert: HTTP 200 carried an explicit removed/not-found
	// notice instead of the advert.
	DetailErrorRemovedAdvert = "removed_advert"
	// DetailErrorUnreadablePage: HTTP 200 carried no advert structure and no
	// recognizable challenge or removal notice (changed layout, block page).
	DetailErrorUnreadablePage = "unreadable_page"
	// DetailErrorMissingIdentity: the page is advert-shaped but exposes no
	// canonical advert identity of its own. The requested URL is NOT substituted:
	// unknown identity stays unknown.
	DetailErrorMissingIdentity = "missing_identity"
	// DetailErrorWrongIdentity: the page exposes a canonical advert identity that
	// names a different advertisement than the one requested.
	DetailErrorWrongIdentity = "wrong_identity"
)

// DetailError is the typed failure returned when a fetched or locally parsed
// detail page is not an acceptable advert page for the requested advert. Every
// field is additive metadata: the success payload keeps its original shape.
//
// HTTPStatus is populated for DetailErrorFetchFailed when the source answered a
// non-200 status, so a caller can treat a verified 404 differently from a 403 or
// a timeout without parsing error strings.
//
// EffectiveURL is the URL the source finally served after redirects, when the
// request produced a response. It is omitted when no response arrived (timeout,
// DNS failure), because then the effective URL is unknown rather than equal to
// the requested one.
//
// RetryAfterSeconds is the source's own Retry-After delay, parsed from the HTTP
// response header. It is a pointer so a proven zero ("Retry-After: 0") emits 0
// and an absent header omits the field entirely. It is only set when the header
// was present and parseable as either delta-seconds or an HTTP date.
type DetailError struct {
	Kind              string `json:"kind"`
	RequestedURL      string `json:"requested_url,omitempty"`
	RequestedAdvertID string `json:"requested_advert_id,omitempty"`
	ObservedURL       string `json:"observed_url,omitempty"`
	ObservedAdvertID  string `json:"observed_advert_id,omitempty"`
	HTTPStatus        int    `json:"http_status,omitempty"`
	EffectiveURL      string `json:"effective_url,omitempty"`
	RetryAfterSeconds *int   `json:"retry_after_seconds,omitempty"`
	Message           string `json:"error"`
}

// Error keeps the kind in the text so logs and wrapped messages stay legible.
func (e *DetailError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", e.Kind, e.Message)
}

// DetailListing holds the enriched data extracted from a listing's detail page.
// Search-page fields (ID, Type, City, Neighborhood, PriceEUR, PriceBGN, SizeSqM)
// are NOT duplicated here — they come from the search card.
type DetailListing struct {
	URL              string   `json:"url"`
	FullDescription  string   `json:"full_description"`
	Floor            string   `json:"floor"`                   // e.g. "4-ти от 4", "Партер от 5"
	YearBuilt        string   `json:"year_built"`              // e.g. "2007", "1960-1969"
	ConstructionType string   `json:"construction_type"`       // e.g. "Тухла", "Панел", "ЕПК"
	HeatingTEC       string   `json:"heating_tec"`             // "ДА" or "НЕ"
	HeatingGas       string   `json:"heating_gas"`             // "ДА" or "НЕ"
	SellerType       string   `json:"seller_type"`             // "Агенция" or "Частно лице"
	Phones           string   `json:"phones"`                  // semicolon-separated, most complete from detail page
	AgencyURL        string   `json:"agency_url"`              // e.g. "mchome.imot.bg"
	ViewCount        int      `json:"view_count,omitempty"`    // detail page visit counter when visible
	CorrectedAt      string   `json:"corrected_at,omitempty"`  // e.g. "2026-05-18T09:06:00+03:00" when visible
	PhotoURL         string   `json:"photo_url"`               // main photo from og:image or first gallery image
	PhotoURLs        []string `json:"photo_urls,omitempty"`    // ordered gallery photos from the detail page
	Features         []string `json:"features,omitempty"`      // "Особености" tag list, e.g. Обзаведен, Асансьор, СОТ
	PublishedAt      string   `json:"published_at,omitempty"`  // RFC3339, from "Публикувана в ..."
	BrokerName       string   `json:"broker_name,omitempty"`   // broker display name from the dealer block
	BrokerPhone      string   `json:"broker_phone,omitempty"`  // broker direct phone
	AgencyOffice     string   `json:"agency_office,omitempty"` // office address, e.g. "ул. Отец Паисий 15, ет. 3, офис 9"
	VatNote          string   `json:"vat_note,omitempty"`      // e.g. "Не се начислява ДДС"
}

// Listing represents a single real estate listing from imot.bg
type Listing struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	City         string         `json:"city"`
	Neighborhood string         `json:"neighborhood"`
	PriceEUR     int            `json:"price_eur"`
	PriceBGN     int            `json:"price_bgn"`
	SizeSqM      int            `json:"size_sqm"`
	Floor        string         `json:"floor"`
	YearBuilt    string         `json:"year_built"`
	Description  string         `json:"description"`
	Phone        string         `json:"phone"`
	Agency       string         `json:"agency"`
	URL          string         `json:"url"`
	ScrapedAt    string         `json:"scraped_at"`
	PhotoURL     string         `json:"photo_url"`            // main photo from CDN, e.g. "//cdn3.focus.bg/imot/photosimotbg/1/675/1c176754466608675_3d.jpg"
	PhotoURLs    []string       `json:"photo_urls,omitempty"` // optional ordered gallery photos when available
	Detail       *DetailListing `json:"detail,omitempty"`     // enriched detail-page data; populated by search --full
}

// SlimListing is the compact projection used by `search --json --quiet`.
// Card-only data: one page-set fetch, zero detail requests, token-light.
type SlimListing struct {
	ID           string  `json:"id"`
	Type         string  `json:"type"`
	Neighborhood string  `json:"neighborhood"`
	PriceEUR     int     `json:"price_eur"`
	SizeSqM      int     `json:"size_sqm"`
	PricePerSqM  float64 `json:"price_per_sqm"`
	Floor        string  `json:"floor,omitempty"`
	Phone        string  `json:"phone,omitempty"`
	Seller       string  `json:"seller"`            // "agency" or "private"
	Snippet      string  `json:"snippet,omitempty"` // first 120 chars of the card description
	URL          string  `json:"url"`
}

// SearchStats summarizes a priced result set.
type SearchStats struct {
	Count           int     `json:"count"`
	PricedCount     int     `json:"priced_count"`
	MeanEUR         float64 `json:"mean_eur"`
	MedianEUR       float64 `json:"median_eur"`
	P25EUR          float64 `json:"p25_eur"`
	P75EUR          float64 `json:"p75_eur"`
	MinEUR          int     `json:"min_eur"`
	MaxEUR          int     `json:"max_eur"`
	MedianEURPerSqM float64 `json:"median_eur_per_sqm"`
	AgencyCount     int     `json:"agency_count"`
	PrivateCount    int     `json:"private_count"`
}

// QuietResult is the `search --json --quiet` envelope: slim listings + stats.
type QuietResult struct {
	RequestedCity         string        `json:"requested_city"`
	RequestedNeighborhood string        `json:"requested_neighborhood,omitempty"`
	RequestedType         string        `json:"requested_type,omitempty"`
	Rent                  bool          `json:"rent"`
	TotalCount            int           `json:"total_count"`
	PagesFetched          int           `json:"pages_fetched"`
	Partial               bool          `json:"partial"`
	Stats                 SearchStats   `json:"stats"`
	Listings              []SlimListing `json:"listings"`
}

// SearchError describes a recoverable page-level scrape failure.
type SearchError struct {
	Page int    `json:"page"`
	URL  string `json:"url"`
	// Kind is one of the SearchError* constants; omitted on older-shaped rows.
	Kind  string `json:"kind,omitempty"`
	Error string `json:"error"`
}

// SearchResult is the robust search envelope used by automated consumers.
type SearchResult struct {
	Listings                 []Listing `json:"listings"`
	RequestedCity            string    `json:"requested_city"`
	RequestedNeighborhood    string    `json:"requested_neighborhood,omitempty"`
	ResolvedNeighborhoodSlug string    `json:"resolved_neighborhood_slug,omitempty"`
	RequestedType            string    `json:"requested_type,omitempty"`
	TotalCount               int       `json:"total_count"`
	PagesPlanned             int       `json:"pages_planned"`
	PagesFetched             int       `json:"pages_fetched"`
	Partial                  bool      `json:"partial"`
	// ServerFilters names the filters imot.bg applied in the request URL itself.
	// A name here means the source query was narrowed, so total_count already
	// excludes everything outside that filter.
	ServerFilters []string `json:"server_filters"`
	// ClientFilters names the filters the CLI applied to rows it had already
	// downloaded. A name here did NOT narrow the source query: total_count still
	// counts listings outside the filter, and only the returned listings are
	// narrowed. min_price, max_price, min_sqm and max_sqm are always client-side
	// because the imot.bg request URL carries no price or size parameter.
	ClientFilters []string `json:"client_filters"`
	// EmptyVerified is true only when imot.bg explicitly reported zero matches
	// (its "Няма намерени обяви" marker). It stays false when a page merely
	// yielded no cards, which is signalled through Partial and Errors instead.
	EmptyVerified bool `json:"empty_verified"`
	// ServerFilterSupport names every filter this CLI is able to apply at the
	// SOURCE, independent of what one particular query used.
	//
	// ServerFilters answers "what did this request narrow?" and therefore omits
	// type on an unfiltered query. A caller deciding which dimensions it may
	// decompose along needs the other question answered: what CAN be narrowed?
	// Conflating the two is not harmless — a client that read ServerFilters from an
	// unfiltered query concluded no type split was possible, stopped decomposing,
	// and silently collected the first 1000 results of a neighbourhood holding
	// more than two thousand.
	ServerFilterSupport []string      `json:"server_filter_support"`
	Stats               *SearchStats  `json:"stats,omitempty"` // set by search --full
	Errors              []SearchError `json:"errors,omitempty"`
}

// CityMap maps Bulgarian city names to URL slugs
var CityMap = map[string]string{
	"София":          "grad-sofiya",
	"Варна":          "grad-varna",
	"Бургас":         "grad-burgas",
	"Пловдив":        "grad-plovdiv",
	"Велико Търново": "grad-veliko-tarnovo",
	"Стара Загора":   "grad-stara-zagora",
	"Русе":           "grad-ruse",
	"Плевен":         "grad-pleven",
	"Шумен":          "grad-shumen",
	"Благоевград":    "grad-blagoevgrad",
	"Перник":         "grad-pernik",
	"Враца":          "grad-vratsa",
	"Габрово":        "grad-gabrovo",
	"Пазарджик":      "grad-pazardzhik",
	"Кърджали":       "grad-kardzhali",
	"Хасково":        "grad-haskovo",
	"Сливен":         "grad-sliven",
	"Добрич":         "grad-dobrich",
	"Ловеч":          "grad-lovech",
	"Монтана":        "grad-montana",
	"Видин":          "grad-vidin",
	"Разград":        "grad-razgrad",
	"Търговище":      "grad-targovishte",
	"Силистра":       "grad-silistra",
	"Кюстендил":      "grad-kyustendil",
	"Ямбол":          "grad-yambol",
	"Смолян":         "grad-smolyan",
}

// OblastMap maps oblast (region) names to URL slugs
var OblastMap = map[string]string{
	"област София":          "oblast-sofia",
	"област Бургас":         "oblast-burgas",
	"област Варна":          "oblast-varna",
	"област Пловдив":        "oblast-plovdiv",
	"област Велико Търново": "oblast-veliko-tarnovo",
	"област Стара Загора":   "oblast-stara-zagora",
	"област Русе":           "oblast-ruse",
	"област Плевен":         "oblast-pleven",
	"област Шумен":          "oblast-shumen",
	"област Благоевград":    "oblast-blagoevgrad",
	"област Перник":         "oblast-pernik",
	"област Враца":          "oblast-vratsa",
	"област Габрово":        "oblast-gabrovo",
	"област Пазарджик":      "oblast-pazardzhik",
	"област Кърджали":       "oblast-kardzhali",
	"област Хасково":        "oblast-haskovo",
	"област Сливен":         "oblast-sliven",
	"област Добрич":         "oblast-dobrich",
	"област Ловеч":          "oblast-lovech",
	"област Монтана":        "oblast-montana",
	"област Видин":          "oblast-vidin",
	"област Разград":        "oblast-razgrad",
	"област Търговище":      "oblast-targovishte",
	"област Силистра":       "oblast-silistra",
	"област Кюстендил":      "oblast-kyustendil",
	"област Ямбол":          "oblast-yambol",
	"област Смолян":         "oblast-smolyan",
}

// TypeMap maps Bulgarian property type names to URL slugs
var TypeMap = map[string]string{
	"1-стаен":    "ednostaen",
	"2-стаен":    "dvustaen",
	"3-стаен":    "tristaen",
	"4-стаен":    "chetiristaen",
	"многостаен": "mnogostaen",
	"мезонет":    "mezonet",
	"къща":       "kashta",
	"вила":       "vila",
	"офис":       "ofis",
	"магазин":    "magazin",
	"заведение":  "zavedenie",
	"склад":      "sklad",
	"гараж":      "garazh-parkomyasto",
	"ателие":     "atelie-tavan",
	"парцел":     "partsel",
	"промишлено помещение": "promishleno-pomeshtenie",
	"хотел":        "hotel",
	"бизнес имот":  "biznes-imot",
	"етаж от къща": "etazh-ot-kashta",
	// Retained for compatibility, but the city page does not advertise this
	// slug and it must not be used as a completeness partition.
	"земя": "zemedelska-zemya",
}

// TaxonomyContractVersion is the version tag on the city-page taxonomy payload.
// It changes only when the meaning of the payload changes, so a consumer can
// refuse a shape it does not understand instead of guessing.
const TaxonomyContractVersion = "imot-taxonomy-v1"

// Taxonomy is the property-type taxonomy advertised by one imot.bg city page.
// The slugs come from the page's own navigation, not from this CLI's TypeMap:
// the configured map is what the payload is compared against, so it cannot also
// be its source. TypeSlugs is always sorted, unique and ASCII; TaxonomyHash is
// the SHA-256 of those slugs joined by LF, so two runs over the same page agree
// without sharing a clock or a network request.
type Taxonomy struct {
	ContractVersion string   `json:"contract_version"`
	City            string   `json:"city"`
	SourceURL       string   `json:"source_url"`
	ObservedAt      string   `json:"observed_at"`
	TypeSlugs       []string `json:"type_slugs"`
	TaxonomyHash    string   `json:"taxonomy_hash"`
}

// TaxonomyParams tells ParseTaxonomy which city page it is reading. CitySlug is
// the URL slug the navigation links must sit under ("grad-sofiya"); SourceURL is
// the fetched page URL, or the local file path for the offline parse, and is
// recorded verbatim as provenance.
type TaxonomyParams struct {
	City      string
	CitySlug  string
	SourceURL string
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
