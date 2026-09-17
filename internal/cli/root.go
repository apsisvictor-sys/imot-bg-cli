package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/apsisvictor/imot-cli/internal/radarclient"
	"github.com/apsisvictor/imot-cli/internal/scraper"
	"github.com/apsisvictor/imot-cli/internal/store"
	"github.com/apsisvictor/imot-cli/internal/translit"
)

var (
	flagCity         string
	flagType         string
	flagMinPrice     int
	flagMaxPrice     int
	flagMinSqM       int
	flagMaxSqM       int
	flagFloorFrom    int
	flagFloorTo      int
	flagNeighborhood string
	flagPages        int
	flagJSON         bool
	flagWithMeta     bool
	flagAgent        bool
	flagQuiet        bool
	flagFull         bool
	flagRent         bool
	flagInterval     string
)

func addSearchFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&flagCity, "city", "", "City name (Bulgarian or transliterated)")
	cmd.Flags().StringVar(&flagType, "type", "", "Property type (1-стаен, 2-стаен, къща, etc.)")
	cmd.Flags().IntVar(&flagMinPrice, "min-price", 0, "Minimum price in EUR")
	cmd.Flags().IntVar(&flagMaxPrice, "max-price", 0, "Maximum price in EUR")
	cmd.Flags().IntVar(&flagMinSqM, "min-sqm", 0, "Minimum size in sq.m")
	cmd.Flags().IntVar(&flagMaxSqM, "max-sqm", 0, "Maximum size in sq.m")
	cmd.Flags().IntVar(&flagFloorFrom, "floor-from", -1, "Source-side minimum floor (-1=unset; 0=ground floor)")
	cmd.Flags().IntVar(&flagFloorTo, "floor-to", -1, "Source-side maximum floor (-1=unset; 0=ground floor)")
	cmd.Flags().StringVar(&flagNeighborhood, "neighborhood", "", "Neighborhood (partial match)")
	cmd.Flags().IntVar(&flagPages, "pages", 0, "Number of pages to fetch (0=all pages, auto-detect from total count)")
	cmd.Flags().BoolVar(&flagJSON, "json", false, "JSON output on stdout")
	cmd.Flags().BoolVar(&flagWithMeta, "with-meta", false, "When used with --json, output search metadata envelope instead of bare listing array")
	cmd.Flags().BoolVar(&flagAgent, "agent", false, "Terse LLM-optimized output")
	cmd.Flags().BoolVar(&flagQuiet, "quiet", false, "Slim projection + stats (with --json: {stats, listings} envelope; without: one-line summary)")
	cmd.Flags().BoolVar(&flagRent, "rent", false, "Search rentals instead of sales")
}

// addFullFlag registers the --full detail-enrichment flag (search only).
// Concurrency and pacing are built into the scraper; no tuning flags exist.
func addFullFlag(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&flagFull, "full", false, "Enrich every listing with detail-page data (features, published date, broker, photos); automatic polite concurrency")
}

// NewRootCommand creates the root cobra command
func NewRootCommand() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "imot",
		Short: "CLI for scraping and querying imot.bg real estate listings",
		Long:  "imot - scrape, store, and query Bulgarian real estate listings from imot.bg",
	}

	rootCmd.AddCommand(newSearchCmd())
	rootCmd.AddCommand(newSyncCmd())
	rootCmd.AddCommand(newLocalCmd())
	rootCmd.AddCommand(newStatsCmd())
	rootCmd.AddCommand(newSQLCmd())
	rootCmd.AddCommand(newWatchCmd())
	rootCmd.AddCommand(newCitiesCmd())
	rootCmd.AddCommand(newDetailCmd())
	rootCmd.AddCommand(newTaxonomyCmd())
	rootCmd.AddCommand(newMapPinsCmd())
	rootCmd.AddCommand(newRadarCmd())

	return rootCmd
}

func newSearchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "search",
		Short: "Search listings from imot.bg (live)",
		Long:  "Fetches listings from imot.bg and displays them. Use --json for machine-readable output, --quiet for the slim projection, --full for detail enrichment.",
		RunE:  runSearch,
	}
	addSearchFlags(cmd)
	addFullFlag(cmd)
	cmd.Flags().String("file", "", "Parse a saved search-results HTML file instead of fetching live (use - for stdin)")
	return cmd
}

func newSyncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Scrape and store listings in local SQLite",
		Long:  "Downloads listings from imot.bg and stores them in ~/.imot/imot.db with deduplication.",
		RunE:  runSync,
	}
	addSearchFlags(cmd)
	return cmd
}

func newLocalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "local",
		Short: "Query local SQLite database",
		Long:  "Query previously synced listings from ~/.imot/imot.db.",
		RunE:  runLocal,
	}
	addSearchFlags(cmd)
	return cmd
}

func newStatsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Price analytics for local data",
		Long:  "Shows count, average, median, min, max, price/sqm, and neighborhood breakdown.",
		RunE:  runStats,
	}
	addSearchFlags(cmd)
	// quiet flag already added via addSearchFlags
	return cmd
}

func newSQLCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sql [query]",
		Short: "Execute direct SQL queries against the local database",
		Long:  "Run arbitrary SQL queries against ~/.imot/imot.db. Use for custom analytics.",
		Args:  cobra.MinimumNArgs(1),
		RunE:  runSQL,
	}
	return cmd
}

func newWatchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Monitor for new listings",
		Long:  "Periodically scrapes imot.bg and reports new listings not in local DB.",
		RunE:  runWatch,
	}
	addSearchFlags(cmd)
	cmd.Flags().StringVar(&flagInterval, "interval", "30m", "Check interval (e.g., 5m, 30m, 1h)")
	return cmd
}

func newCitiesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cities",
		Short: "List available cities and their URL slugs",
		RunE:  runCities,
	}
	return cmd
}

func newDetailCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "detail [url]",
		Short: "Fetch or parse one listing's detail page",
		Long: "Fetches a listing's detail page from imot.bg and extracts enriched data: full description, floor, year, heating, construction type, all phones, agency URL, photos, features and broker contact.\n\n" +
			"A successful payload keeps its flat fields and adds contract_version=imot-detail-v2, advert_id " +
			"(the page's own advert number) and field_evidence, which resolves every named detail field to " +
			"present, verified_absent or unknown so a missing selector is never read as absence.\n\n" +
			"With --file it parses a saved page instead and makes no network request. The saved page is " +
			"validated exactly like a live one: it must be a genuine advert page and must carry its own " +
			"advert identity. A challenge page, a removal notice, an unreadable page or a page with " +
			"absent/wrong advert identity exits non-zero and prints typed error metadata as JSON.",
		Args: cobra.MaximumNArgs(1),
		RunE: runDetail,
	}
	cmd.Flags().BoolVar(&flagJSON, "json", true, "JSON output (default true)")
	cmd.Flags().String("file", "", "Parse a saved detail-page HTML file instead of fetching live (use - for stdin)")
	cmd.Flags().String("expect-url", "", "Listing URL the saved page is expected to contain; verifies advert identity (only with --file)")
	return cmd
}

func runDetail(cmd *cobra.Command, args []string) error {
	filePath, err := cmd.Flags().GetString("file")
	if err != nil {
		return err
	}
	expectURL, err := cmd.Flags().GetString("expect-url")
	if err != nil {
		return err
	}

	switch {
	case filePath != "" && len(args) > 0:
		return fmt.Errorf("pass either a listing URL or --file, not both")
	case filePath == "" && len(args) == 0:
		return fmt.Errorf("provide a listing URL, or --file <path> to parse a saved page")
	case filePath == "" && expectURL != "":
		return fmt.Errorf("--expect-url is only meaningful with --file; a live URL is already its own expectation")
	}

	if filePath != "" {
		return runDetailFromFile(filePath, expectURL)
	}

	url := args[0]
	if !strings.HasPrefix(url, "https://www.imot.bg/obiava-") || scraper.AdvertIDFromURL(url) == "" {
		return fmt.Errorf("URL must be an imot.bg listing URL carrying a 15-digit advert number (e.g. https://www.imot.bg/obiava-...)")
	}

	client := scraper.NewClient()
	detail, err := client.FetchDetail(url)
	if err != nil {
		return emitDetailError(err)
	}
	return encodeDetail(detail)
}

// runDetailFromFile parses a saved page through exactly the same validation as
// the live path, so a fixture proves the real contract. It makes no network
// request. expectedURL may be empty, in which case the page is still required to
// carry its own advert identity but no identity comparison is made.
func runDetailFromFile(filePath, expectedURL string) error {
	if expectedURL != "" && (!strings.HasPrefix(expectedURL, "https://www.imot.bg/obiava-") || scraper.AdvertIDFromURL(expectedURL) == "") {
		return fmt.Errorf("--expect-url must be an imot.bg listing URL carrying a 15-digit advert number (e.g. https://www.imot.bg/obiava-...)")
	}

	raw, err := readLocalPage(filePath)
	if err != nil {
		return err
	}

	detail, err := scraper.ParseDetailPage(scraper.DecodeHTMLBytes(raw), expectedURL)
	if err != nil {
		return emitDetailError(err)
	}
	return encodeDetail(detail)
}

// encodeDetail writes the unchanged detail payload on the JSON stream.
func encodeDetail(detail scraper.DetailListing) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(detail)
}

// emitDetailError prints a typed *DetailError's metadata as JSON on stdout —
// the same stream success uses — and returns it so the process exits non-zero.
// A consumer therefore can never read a challenge, removed or wrong-identity
// page as a successful advert with empty fields.
func emitDetailError(err error) error {
	var detailErr *scraper.DetailError
	if errors.As(err, &detailErr) {
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if encErr := enc.Encode(detailErr); encErr != nil {
			return fmt.Errorf("%w (and printing error metadata failed: %v)", detailErr, encErr)
		}
		return detailErr
	}
	return err
}

func newTaxonomyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "taxonomy",
		Short: "Report the property-type taxonomy advertised by the sales search form",
		Long: "Reads one imot.bg sales search page and prints the property-type slugs its type filter advertises, as " +
			"{contract_version, city, source_url, observed_at, type_slugs, taxonomy_hash}. The hash is the SHA-256 " +
			"of the sorted unique slugs joined by LF, so two readings of the same page agree.\n\n" +
			"With --file it parses a saved, correctly decoded search page and makes no network request. A page that " +
			"is not the recognized search form for --city (challenge, block page, another city selected, renamed " +
			"form, unmapped or missing type label) exits non-zero instead of printing an incomplete taxonomy.",
		Args: cobra.NoArgs,
		RunE: runTaxonomy,
	}
	cmd.Flags().String("city", "", "City name (Bulgarian), e.g. София")
	cmd.Flags().Bool("json", true, "JSON output (default true; JSON is the only supported shape)")
	cmd.Flags().String("file", "", "Parse a saved sales-search HTML file instead of fetching live (use - for stdin)")
	return cmd
}

func runTaxonomy(cmd *cobra.Command, args []string) error {
	cityFlag, err := cmd.Flags().GetString("city")
	if err != nil {
		return err
	}
	filePath, err := cmd.Flags().GetString("file")
	if err != nil {
		return err
	}

	if strings.TrimSpace(cityFlag) == "" {
		return fmt.Errorf("--city is required; a taxonomy names one city's source page")
	}
	city := resolveCity(cityFlag)
	citySlug := scraper.CitySlug(city)
	if citySlug == "" {
		return fmt.Errorf("unknown city %q: no imot.bg city slug is known for it", cityFlag)
	}

	if filePath != "" {
		raw, err := readLocalPage(filePath)
		if err != nil {
			return err
		}
		taxonomy, err := scraper.ParseTaxonomy(scraper.DecodeHTMLBytes(raw), scraper.TaxonomyParams{
			City:      city,
			CitySlug:  citySlug,
			SourceURL: filePath,
		})
		if err != nil {
			return err
		}
		return encodeTaxonomy(taxonomy)
	}

	taxonomy, err := scraper.NewClient().FetchTaxonomy(city)
	if err != nil {
		return err
	}
	return encodeTaxonomy(taxonomy)
}

// encodeTaxonomy writes the taxonomy payload compactly on stdout, matching the
// shape the contract publishes.
func encodeTaxonomy(taxonomy scraper.Taxonomy) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(taxonomy)
}

func resolveCity(city string) string {
	if _, ok := scraper.CityMap[city]; ok {
		return city
	}
	if _, ok := scraper.OblastMap[city]; ok {
		return city
	}
	// Try transliteration reverse
	return translit.NormalizeCity(city)
}

// validateBounds rejects impossible numeric bounds before any source request or
// database access. Cobra's integer flags already refuse fractions, trailing
// characters and unsafe integers at parse time; negatives and reversed ranges
// were still accepted and could silently produce empty or surprising results.
// Zero keeps its existing meaning: no bound for price/size; floor uses -1 for
// unset so ground floor (0) remains representable.
func validateBounds(minPrice, maxPrice, minSqM, maxSqM, floorFrom, floorTo, pages int) error {
	if minPrice < 0 || maxPrice < 0 || minSqM < 0 || maxSqM < 0 {
		return fmt.Errorf("price and size bounds must not be negative")
	}
	if floorFrom < -1 || floorTo < -1 {
		return fmt.Errorf("floor bounds must be -1 (unset) or non-negative")
	}
	if pages < 0 {
		return fmt.Errorf("--pages must not be negative; 0 means all pages")
	}
	if maxPrice > 0 && minPrice > maxPrice {
		return fmt.Errorf("--min-price %d exceeds --max-price %d", minPrice, maxPrice)
	}
	if maxSqM > 0 && minSqM > maxSqM {
		return fmt.Errorf("--min-sqm %d exceeds --max-sqm %d", minSqM, maxSqM)
	}
	if floorFrom >= 0 && floorTo >= 0 && floorFrom > floorTo {
		return fmt.Errorf("--floor-from %d exceeds --floor-to %d", floorFrom, floorTo)
	}
	return nil
}

func optionalFloor(value int) *int {
	if value < 0 {
		return nil
	}
	copy := value
	return &copy
}

func runSearch(cmd *cobra.Command, args []string) error {
	if err := validateBounds(flagMinPrice, flagMaxPrice, flagMinSqM, flagMaxSqM, flagFloorFrom, flagFloorTo, flagPages); err != nil {
		return err
	}
	if flagFull && flagQuiet {
		return fmt.Errorf("choose either --full or --quiet, not both")
	}

	filePath, err := cmd.Flags().GetString("file")
	if err != nil {
		return err
	}
	if filePath != "" {
		return runSearchFromFile(filePath)
	}

	if flagCity == "" {
		return fmt.Errorf("--city is required")
	}

	flagCity = resolveCity(flagCity)
	params := scraper.SearchParams{
		City:         flagCity,
		Type:         flagType,
		MinPrice:     flagMinPrice,
		MaxPrice:     flagMaxPrice,
		MinSqM:       flagMinSqM,
		MaxSqM:       flagMaxSqM,
		FloorFrom:    optionalFloor(flagFloorFrom),
		FloorTo:      optionalFloor(flagFloorTo),
		Neighborhood: flagNeighborhood,
		Pages:        flagPages,
		Rent:         flagRent,
	}

	client := scraper.NewClient()
	result, err := client.SearchWithMeta(params)
	if err != nil {
		return fmt.Errorf("search failed: %w", err)
	}

	// Client-side filtering
	result.Listings = dedupListings(result.Listings)
	result.Listings = filterListings(result.Listings, params, flagNeighborhood != "")

	if flagFull {
		// Polite concurrent detail enrichment. Individual failures are
		// reported and marked partial; they never abort the run.
		fetchErrs := client.FetchDetailsConcurrent(result.Listings)
		var failed int
		for i, ferr := range fetchErrs {
			if ferr != nil {
				failed++
				fmt.Fprintf(os.Stderr, "detail failed for %s: %v\n", result.Listings[i].ID, ferr)
			}
		}
		if failed > 0 {
			result.Partial = true
			result.Errors = append(result.Errors, scraper.SearchError{
				Page:  0,
				URL:   "detail-enrichment",
				Kind:  scraper.SearchErrorDetailEnrichment,
				Error: fmt.Sprintf("%d/%d detail pages failed", failed, len(fetchErrs)),
			})
		}
	}

	return emitSearchResult(result)
}

// emitSearchResult renders a search result through the existing output paths. It
// is shared by the live scrape and the offline --file parse, so the two cannot
// drift in JSON shape.
func emitSearchResult(result scraper.SearchResult) error {
	// A partial result must keep listings as an array, never null: consumers read
	// a null/absent array with total_count 0 as "verified empty", which is the
	// exact misreading an unreadable page must not allow. The envelope types
	// listings as an array even when the scrape or the client-side filter emptied
	// the set; empty_verified and partial carry the meaning.
	if result.Listings == nil {
		result.Listings = []scraper.Listing{}
	}

	stats := scraper.ComputeStats(result.Listings)

	if flagJSON && flagQuiet {
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		return enc.Encode(scraper.QuietResult{
			RequestedCity:         result.RequestedCity,
			RequestedNeighborhood: result.RequestedNeighborhood,
			RequestedType:         result.RequestedType,
			Rent:                  flagRent,
			TotalCount:            result.TotalCount,
			PagesFetched:          result.PagesFetched,
			Partial:               result.Partial,
			Stats:                 stats,
			Listings:              scraper.ToSlimListings(result.Listings),
		})
	}

	if flagFull {
		result.Stats = &stats
		if flagJSON && flagWithMeta {
			enc := json.NewEncoder(os.Stdout)
			enc.SetEscapeHTML(false)
			return enc.Encode(result)
		}
		fmt.Fprintf(os.Stderr, "Stats: %d listings | mean €%.0f | median €%.0f | p25 €%.0f | p75 €%.0f | median %.1f €/m² | agency %d | private %d\n",
			stats.Count, stats.MeanEUR, stats.MedianEUR, stats.P25EUR, stats.P75EUR, stats.MedianEURPerSqM, stats.AgencyCount, stats.PrivateCount)
		return outputListings(result.Listings)
	}

	if flagJSON && flagWithMeta {
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		return enc.Encode(result)
	}
	return outputListings(result.Listings)
}

// runSearchFromFile parses a saved search-results page with no network access.
// Node fixture tests use it to prove the parser offline against saved, correctly
// decoded HTML, including one search page per type slug. It accepts --with-meta
// and --quiet, but not --full or --pages: those describe a live fetch.
func runSearchFromFile(filePath string) error {
	if flagFull {
		return fmt.Errorf("--full enriches listings from live detail pages; it cannot be used with --file")
	}
	if flagPages != 0 {
		return fmt.Errorf("--pages has no meaning with --file; a saved page is parsed as-is")
	}
	if flagFloorFrom >= 0 || flagFloorTo >= 0 {
		return fmt.Errorf("floor bounds require a live source search; they cannot be applied to --file")
	}

	raw, err := readLocalPage(filePath)
	if err != nil {
		return err
	}

	city := ""
	if flagCity != "" {
		city = resolveCity(flagCity)
	}
	params := scraper.SearchParams{
		City:         city,
		Type:         flagType,
		MinPrice:     flagMinPrice,
		MaxPrice:     flagMaxPrice,
		MinSqM:       flagMinSqM,
		MaxSqM:       flagMaxSqM,
		Neighborhood: flagNeighborhood,
		Rent:         flagRent,
	}

	result := scraper.ParseSearchPage(scraper.DecodeHTMLBytes(raw), filePath)
	result.RequestedCity = city
	result.RequestedNeighborhood = flagNeighborhood
	result.RequestedType = flagType

	result.Listings = dedupListings(result.Listings)
	// No neighborhood slug is resolved offline, so the neighborhood filter stays
	// client-side here even though a live search can narrow it at the source.
	result.Listings = filterListings(result.Listings, params, false)
	result.ClientFilters = offlineClientFilters(params)

	return emitSearchResult(result)
}

// offlineClientFilters names the filters an offline parse applied to the rows it
// read. ServerFilters stays empty: no source query was made.
func offlineClientFilters(params scraper.SearchParams) []string {
	filters := []string{}
	if params.MinPrice > 0 {
		filters = append(filters, "min_price")
	}
	if params.MaxPrice > 0 {
		filters = append(filters, "max_price")
	}
	if params.MinSqM > 0 {
		filters = append(filters, "min_sqm")
	}
	if params.MaxSqM > 0 {
		filters = append(filters, "max_sqm")
	}
	if params.Neighborhood != "" {
		filters = append(filters, "neighborhood")
	}
	return filters
}

// readLocalPage reads a saved HTML page from a path, or from stdin when the path
// is "-", and rejects an empty input so a missing fixture fails loudly instead
// of parsing as an unreadable page.
func readLocalPage(filePath string) ([]byte, error) {
	var raw []byte
	var err error
	if filePath == "-" {
		raw, err = io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("reading stdin: %w", err)
		}
	} else {
		raw, err = os.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", filePath, err)
		}
	}
	if strings.TrimSpace(string(raw)) == "" {
		return nil, fmt.Errorf("%s is empty; an offline parse needs saved page HTML", filePath)
	}
	return raw, nil
}

func runSync(cmd *cobra.Command, args []string) error {
	if err := validateBounds(flagMinPrice, flagMaxPrice, flagMinSqM, flagMaxSqM, flagFloorFrom, flagFloorTo, flagPages); err != nil {
		return err
	}
	if flagCity == "" {
		return fmt.Errorf("--city is required")
	}

	flagCity = resolveCity(flagCity)
	params := scraper.SearchParams{
		City:         flagCity,
		Type:         flagType,
		MinPrice:     flagMinPrice,
		MaxPrice:     flagMaxPrice,
		MinSqM:       flagMinSqM,
		MaxSqM:       flagMaxSqM,
		FloorFrom:    optionalFloor(flagFloorFrom),
		FloorTo:      optionalFloor(flagFloorTo),
		Neighborhood: flagNeighborhood,
		Pages:        flagPages,
		Rent:         flagRent,
	}

	client := scraper.NewClient()
	result, err := client.SearchWithMeta(params)
	if err != nil {
		return fmt.Errorf("search failed: %w", err)
	}
	if result.Partial {
		return fmt.Errorf("search returned partial results; refusing to sync incomplete scrape")
	}

	// Client-side filtering
	result.Listings = dedupListings(result.Listings)
	result.Listings = filterListings(result.Listings, params, flagNeighborhood != "")

	st, err := store.New("")
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer st.Close()

	newCount, err := st.SyncListings(flagCity, flagType, result.PagesFetched, result.Listings)
	if err != nil {
		return fmt.Errorf("syncing listings: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Synced %d listings (%d new) from %s\n", len(result.Listings), newCount, flagCity)
	return nil
}

func runLocal(cmd *cobra.Command, args []string) error {
	if err := validateBounds(flagMinPrice, flagMaxPrice, flagMinSqM, flagMaxSqM, flagFloorFrom, flagFloorTo, flagPages); err != nil {
		return err
	}
	if flagFloorFrom >= 0 || flagFloorTo >= 0 {
		return fmt.Errorf("floor bounds are only supported for live searches")
	}
	if flagCity == "" {
		flagCity = ""
	} else {
		flagCity = resolveCity(flagCity)
	}

	st, err := store.New("")
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer st.Close()

	listings, err := st.QueryListings(flagCity, flagType, flagNeighborhood, flagMinPrice, flagMaxPrice, flagMinSqM, flagMaxSqM)
	if err != nil {
		return fmt.Errorf("querying: %w", err)
	}

	return outputListings(listings)
}

func runStats(cmd *cobra.Command, args []string) error {
	if err := validateBounds(flagMinPrice, flagMaxPrice, flagMinSqM, flagMaxSqM, flagFloorFrom, flagFloorTo, flagPages); err != nil {
		return err
	}
	if flagFloorFrom >= 0 || flagFloorTo >= 0 {
		return fmt.Errorf("floor bounds are only supported for live searches")
	}
	if flagCity == "" {
		flagCity = ""
	} else {
		flagCity = resolveCity(flagCity)
	}

	st, err := store.New("")
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer st.Close()

	stats, err := st.GetStats(flagCity, flagType, flagNeighborhood)
	if err != nil {
		return fmt.Errorf("computing stats: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Statistics for %s %s\n",
		func() string {
			if flagCity != "" {
				return flagCity
			}
			return "all cities"
		}(),
		func() string {
			if flagType != "" {
				return flagType
			}
			return "all types"
		}())

	fmt.Fprintf(os.Stderr, "─────────────────────────────────\n")
	fmt.Fprintf(os.Stderr, "  Listings:      %d\n", stats.Count)
	fmt.Fprintf(os.Stderr, "  Avg price:     €%.0f\n", stats.AvgPrice)
	fmt.Fprintf(os.Stderr, "  Median price:  €%.0f\n", stats.MedianPrice)
	fmt.Fprintf(os.Stderr, "  Min price:     €%d\n", stats.MinPrice)
	fmt.Fprintf(os.Stderr, "  Max price:     €%d\n", stats.MaxPrice)
	fmt.Fprintf(os.Stderr, "  Avg €/sqm:     €%.0f\n", stats.AvgPricePerSqm)

	if !flagQuiet && len(stats.ByNeighborhood) > 0 {
		fmt.Fprintf(os.Stderr, "\nBy Neighborhood:\n")
		fmt.Fprintf(os.Stderr, "%-30s %8s %12s %10s\n", "Neighborhood", "Count", "Avg Price", "Avg €/sqm")
		fmt.Fprintf(os.Stderr, "%-30s %8s %12s %10s\n", "──────────────────────────────", "────────", "────────────", "──────────")
		for nb, s := range stats.ByNeighborhood {
			name := scraper.TruncateRunes(nb, 28, "")
			fmt.Fprintf(os.Stderr, "%-30s %8d €%10.0f €%8.0f\n", name, s.Count, s.AvgPrice, s.AvgPPS)
		}
	}

	return nil
}

func runSQL(cmd *cobra.Command, args []string) error {
	query := strings.Join(args, " ")

	st, err := store.New("")
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer st.Close()

	rows, err := st.DB().Query(query)
	if err != nil {
		return fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("getting columns: %w", err)
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(cols, "\t"))

	for rows.Next() {
		values := make([]interface{}, len(cols))
		valuePtrs := make([]interface{}, len(cols))
		for i := range values {
			valuePtrs[i] = &values[i]
		}
		if err := rows.Scan(valuePtrs...); err != nil {
			return fmt.Errorf("scanning row: %w", err)
		}

		var strs []string
		for _, v := range values {
			switch val := v.(type) {
			case []byte:
				strs = append(strs, string(val))
			case string:
				strs = append(strs, val)
			case int64:
				strs = append(strs, strconv.FormatInt(val, 10))
			case float64:
				strs = append(strs, fmt.Sprintf("%.2f", val))
			case nil:
				strs = append(strs, "NULL")
			default:
				strs = append(strs, fmt.Sprintf("%v", val))
			}
		}
		fmt.Fprintln(tw, strings.Join(strs, "\t"))
	}
	tw.Flush()

	return rows.Err()
}

func runWatch(cmd *cobra.Command, args []string) error {
	if err := validateBounds(flagMinPrice, flagMaxPrice, flagMinSqM, flagMaxSqM, flagFloorFrom, flagFloorTo, flagPages); err != nil {
		return err
	}
	if flagCity == "" {
		return fmt.Errorf("--city is required")
	}

	interval, err := time.ParseDuration(flagInterval)
	if err != nil {
		return fmt.Errorf("invalid interval %q: %w", flagInterval, err)
	}

	flagCity = resolveCity(flagCity)
	params := scraper.SearchParams{
		City:         flagCity,
		Type:         flagType,
		MinPrice:     flagMinPrice,
		MaxPrice:     flagMaxPrice,
		MinSqM:       flagMinSqM,
		MaxSqM:       flagMaxSqM,
		FloorFrom:    optionalFloor(flagFloorFrom),
		FloorTo:      optionalFloor(flagFloorTo),
		Neighborhood: flagNeighborhood,
		Pages:        flagPages,
		Rent:         flagRent,
	}

	st, err := store.New("")
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer st.Close()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// First check immediately
	check := func() {
		fmt.Fprintf(os.Stderr, "[%s] Checking for new listings...\n", time.Now().Format("15:04:05"))

		client := scraper.NewClient()
		listings, err := client.Search(params)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}

		listings = dedupListings(listings)
		listings = filterListings(listings, params, flagNeighborhood != "")

		newCount := 0
		for _, l := range listings {
			isNew, err := st.UpsertListing(l)
			if err != nil {
				continue
			}
			if isNew {
				newCount++
				formatListingAgent(l)
			}
		}

		fmt.Fprintf(os.Stderr, "[%s] Found %d listings, %d new\n", time.Now().Format("15:04:05"), len(listings), newCount)
	}

	check()

	for {
		select {
		case <-ticker.C:
			check()
		case <-sigChan:
			fmt.Fprintf(os.Stderr, "\nStopping watch.\n")
			return nil
		}
	}
}

func runCities(cmd *cobra.Command, args []string) error {
	w := tabwriter.NewWriter(os.Stderr, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "CITY\tSLUG")
	fmt.Fprintln(w, "────\t────")
	for city, slug := range scraper.CityMap {
		fmt.Fprintf(w, "%s\t%s\n", city, slug)
	}
	w.Flush()
	return nil
}

// filterListings applies client-side filters. When serverSideNeighborhood is true,
// the server already filtered by neighborhood via URL slug — skip the neighborhood
// check here to avoid dropping valid results that have an empty Neighborhood field.
func filterListings(listings []scraper.Listing, params scraper.SearchParams, serverSideNeighborhood bool) []scraper.Listing {
	var filtered []scraper.Listing
	for _, l := range listings {
		if params.MinPrice > 0 && l.PriceEUR < params.MinPrice {
			continue
		}
		if params.MaxPrice > 0 && l.PriceEUR > params.MaxPrice {
			continue
		}
		if params.MinSqM > 0 && l.SizeSqM < params.MinSqM {
			continue
		}
		if params.MaxSqM > 0 && l.SizeSqM > params.MaxSqM {
			continue
		}
		if params.Neighborhood != "" && !serverSideNeighborhood {
			if !strings.Contains(strings.ToLower(l.Neighborhood), strings.ToLower(params.Neighborhood)) {
				continue
			}
		}
		filtered = append(filtered, l)
	}
	return filtered
}

// dedupListings removes duplicates by listing ID.
func dedupListings(listings []scraper.Listing) []scraper.Listing {
	seen := make(map[string]bool)
	var result []scraper.Listing
	for _, l := range listings {
		if !seen[l.ID] {
			seen[l.ID] = true
			result = append(result, l)
		}
	}
	return result
}

func outputListings(listings []scraper.Listing) error {
	if len(listings) == 0 {
		fmt.Fprintf(os.Stderr, "No listings found.\n")
		if flagJSON {
			fmt.Println("[]")
		}
		return nil
	}

	switch {
	case flagJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		return enc.Encode(listings)
	case flagAgent:
		for _, l := range listings {
			formatListingAgent(l)
		}
	case flagQuiet:
		s := scraper.ComputeStats(listings)
		if s.PricedCount > 0 {
			fmt.Fprintf(os.Stderr, "Listings: %d | mean €%.0f | median €%.0f | median %.1f €/m²\n", s.Count, s.MeanEUR, s.MedianEUR, s.MedianEURPerSqM)
		} else {
			fmt.Fprintf(os.Stderr, "Listings: %d (no priced listings)\n", s.Count)
		}
	default:
		w := tabwriter.NewWriter(os.Stderr, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "PRICE\tSIZE\tTYPE\tLOCATION\tFLOOR\tYEAR\n")
		fmt.Fprintf(w, "─────\t────\t────\t────────\t─────\t────\n")
		for _, l := range listings {
			location := l.City
			if l.Neighborhood != "" {
				location += ", " + l.Neighborhood
			}
			fmt.Fprintf(w, "€%d\t%d sqm\t%s\t%s\t%s\t%s\n",
				l.PriceEUR, l.SizeSqM, l.Type, location, l.Floor, l.YearBuilt)
		}
		w.Flush()
		fmt.Fprintf(os.Stderr, "\n(%d listings)\n", len(listings))
	}
	return nil
}

// newMapPinsCmd registers the scope-level map-pin producer: one map scope
// (category x city x neighbourhood x property type) in, validated native source
// pins plus honest coverage metadata out. The source's own map form is fetched
// and submitted per run; no field value is fabricated or replayed.
func newMapPinsCmd() *cobra.Command {
	var (
		city         string
		neighborhood string
		propType     string
		rent         bool
		jsonOut      bool
	)

	cmd := &cobra.Command{
		Use:   "map-pins",
		Short: "Fetch one imot.bg map scope's native source pins",
		Long: "Fetches the source's map page for one scope and submits the page's own mapgfixparams form to " +
			"read the map body's positional pin arrays (latitudes, longitudes, advert ids). The arrays must be " +
			"equal-length and every advert id must be canonical; a single bad record is dropped with a bounded " +
			"warning code while the rest survive, and unequal arrays fail the payload rather than shifting one " +
			"advert's coordinate onto another.\n\n" +
			"--json prints the imot-map-pins-v1 envelope; without it a one-line summary reports parsed pins, " +
			"distinct points, completeness and warnings. Completeness is 'complete' only when the source's own " +
			"mapped-advert heading count equals the validated pins; a missing or mismatching heading stays " +
			"'unknown' rather than being read as a complete batch. A challenge, block or unreadable map page " +
			"exits non-zero after printing typed error metadata as JSON.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(city) == "" {
				return fmt.Errorf("--city is required; a map scope names one city")
			}

			params := scraper.SearchParams{
				City:         resolveCity(city),
				Neighborhood: neighborhood,
				Type:         propType,
				Rent:         rent,
			}

			result, err := scraper.NewClient().FetchMapPins(params)
			if err != nil {
				return emitMapPinsError(err)
			}
			if jsonOut {
				return encodeMapPins(result)
			}
			return printMapPinsSummary(result)
		},
	}

	cmd.Flags().StringVar(&city, "city", "", "City name (Bulgarian), e.g. София")
	cmd.Flags().StringVar(&neighborhood, "neighborhood", "", "Neighbourhood name (Bulgarian or transliterated)")
	cmd.Flags().StringVar(&propType, "type", "", "Property type label or source slug (e.g. тристаен, tristaen)")
	cmd.Flags().BoolVar(&rent, "rent", false, "Fetch the rentals map instead of the sales map")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print the imot-map-pins-v1 envelope instead of a one-line summary")
	return cmd
}

// encodeMapPins writes the unchanged map-pin payload on the JSON stream.
func encodeMapPins(result scraper.MapPinsResult) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

// printMapPinsSummary prints the coverage facts a person reads: how many pins
// were parsed, how many distinct points they cover, whether the source proved
// the batch complete, the source's own count when it reported one, and every
// warning code that says what was refused.
func printMapPinsSummary(result scraper.MapPinsResult) error {
	reported := "none"
	if result.ReportedMappedCount != nil {
		reported = strconv.Itoa(*result.ReportedMappedCount)
	}
	warnings := "none"
	if len(result.Warnings) > 0 {
		warnings = strings.Join(result.Warnings, ",")
	}
	fmt.Printf("pins: %d | distinct points: %d | completeness: %s | reported mapped count: %s | warnings: %s\n",
		len(result.Pins), result.DistinctPoints, result.Completeness, reported, warnings)
	return nil
}

// emitMapPinsError prints a typed *MapPinsError's metadata as JSON on stdout —
// the same stream success uses — and returns it so the process exits non-zero.
// A consumer therefore can never read a challenge or a misaligned page as a
// scope with zero pins.
func emitMapPinsError(err error) error {
	var mapErr *scraper.MapPinsError
	if errors.As(err, &mapErr) {
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if encErr := enc.Encode(mapErr); encErr != nil {
			return fmt.Errorf("%w (and printing error metadata failed: %v)", mapErr, encErr)
		}
		return mapErr
	}
	return err
}

func formatListingAgent(l scraper.Listing) {
	// Terse one-line format for LLM consumption. Truncate by rune count: byte
	// slicing a 2-byte Cyrillic character emits U+FFFD.
	desc := scraper.TruncateRunes(l.Description, 100, "...")
	fmt.Printf("€%d | %d sqm | %s | %s, %s | floor:%s | year:%s | tel:%s | %s\n",
		l.PriceEUR, l.SizeSqM, l.Type, l.City, l.Neighborhood,
		l.Floor, l.YearBuilt, l.Phone, desc)
}

// ── imot radar: read the published Radar geographic API ─────────────────────
//
// This group speaks the versioned HTTP contract (radar-geo-1) implemented in
// broker-essentials/apps/market-radar. It reads already-published Radar data
// and never scrapes imot.bg: a Radar outage, a refusal or a missing listing is
// an error with a typed code, never an empty result and never a live-source
// fallback. Every flag is local to this group, so nothing here can change the
// meaning of `imot search`.
//
// Configuration is read from the environment: IMOT_RADAR_API_BASE_URL names
// the API origin and IMOT_RADAR_API_READ_TOKEN carries the read bearer token.
// The token is never printed, echoed or placed in a URL.

const (
	// radarCommandTimeout bounds a whole radar command. Each call carries its
	// own 5s client timeout; this is the ceiling for --all pagination.
	radarCommandTimeout = 5 * time.Minute
	// radarMaxPages bounds --all. It is a safety cap, not a query limit: when
	// it is reached while more pages remain, the output says so and the
	// command exits non-zero.
	radarMaxPages = 50
	// radarHumanRowsPerGroup bounds the human table. The JSON envelope always
	// carries every row of the page.
	radarHumanRowsPerGroup = 10
)

// newRadarCmd registers the published-data command group.
func newRadarCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "radar",
		Short: "Query the published Market Radar geographic API",
		Long: "Queries the published Market Radar read API (contract radar-geo-1) over HTTPS. " +
			"These commands read already-collected Radar data; they never scrape imot.bg, so a " +
			"Radar outage returns a typed error instead of an empty result.\n\n" +
			"Configuration comes from the environment: IMOT_RADAR_API_BASE_URL names the API " +
			"origin and IMOT_RADAR_API_READ_TOKEN carries the read bearer token. The token is " +
			"never printed, echoed or placed in a URL.",
	}
	cmd.AddCommand(newRadarSearchCmd())
	cmd.AddCommand(newRadarLocationCmd())
	return cmd
}

// radarSearchFlags are local to the radar search command: no package-level
// flag variable is shared with imot search, so the two meanings cannot drift.
type radarSearchFlags struct {
	neighborhoods      []string
	neighborhoodSlugs  []string
	types              []string
	minSqm             float64
	maxSqm             float64
	minPrice           float64
	maxPrice           float64
	floorMax           int
	lat                float64
	lng                float64
	radiusMeters       int
	missingFieldPolicy string
	groups             string
	all                bool
	jsonOut            bool

	// anchorSet reports whether each anchor flag was given at all, so a real
	// zero coordinate is distinguishable from an unset flag.
	latSet    bool
	lngSet    bool
	radiusSet bool
}

func newRadarSearchCmd() *cobra.Command {
	flags := &radarSearchFlags{}

	cmd := &cobra.Command{
		Use:   "search",
		Short: "Search published Radar listings by property and geographic filters",
		Long: "Builds one geographic search and prints its result groups.\n\n" +
			"Every filter is a predicate on the matching population; the groups report how the " +
			"geographic evidence classifies a match: supported (a location the evidence backs), " +
			"possible (the location is not proven, not disproven) and excluded (an explicit " +
			"mismatch). supported and possible are requested by default.\n\n" +
			"An anchor is optional, but an anchor, a longitude and a radius travel together: " +
			"--lat, --lng and --radius must all be given, and --radius alone is refused rather " +
			"than applied to an implicit centre.\n\n" +
			"--json prints the raw radar-geo-1 envelope. With --all it prints one wrapper holding " +
			"the raw envelope of every page plus explicit truncation fields, because a safety cap " +
			"or an interruption must never look like a complete result. Human output prints the " +
			"group totals, coverage and the page's top rows.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			flags.latSet = cmd.Flags().Changed("lat")
			flags.lngSet = cmd.Flags().Changed("lng")
			flags.radiusSet = cmd.Flags().Changed("radius")

			input, err := flags.input()
			if err != nil {
				return err
			}
			client, err := radarclient.FromEnv()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), radarCommandTimeout)
			defer cancel()

			if flags.all {
				return runRadarSearchAll(ctx, client, input, flags.jsonOut, os.Stdout)
			}
			response, err := client.GeoSearch(ctx, input)
			if err != nil {
				return err
			}
			if flags.jsonOut {
				return writeRadarSearchJSON(os.Stdout, response)
			}
			return printRadarSearchHuman(os.Stdout, response)
		},
	}

	f := cmd.Flags()
	f.StringSliceVar(&flags.neighborhoods, "neighborhood", nil, "Bulgarian neighbourhood label (repeatable); transliterated to the Radar catalogue slug and validated by the API")
	f.StringSliceVar(&flags.neighborhoodSlugs, "neighborhood-slug", nil, "Radar catalogue neighbourhood slug (repeatable); used verbatim")
	f.StringSliceVar(&flags.types, "type", nil, "Property type label or canonical value, case-insensitive (repeatable; e.g. 2-стаен, 2-СТАЕН, ателие)")
	f.Float64Var(&flags.minSqm, "min-sqm", 0, "Minimum size in square metres")
	f.Float64Var(&flags.maxSqm, "max-sqm", 0, "Maximum size in square metres")
	f.Float64Var(&flags.minPrice, "min-price", 0, "Minimum asking price in EUR")
	f.Float64Var(&flags.maxPrice, "max-price", 0, "Maximum asking price in EUR")
	f.IntVar(&flags.floorMax, "floor-max", -1, "Maximum floor; 0 is the ground floor, -1 leaves the floor unbounded")
	f.Float64Var(&flags.lat, "lat", 0, "Anchor latitude WGS84 (requires --lng and --radius)")
	f.Float64Var(&flags.lng, "lng", 0, "Anchor longitude WGS84 (requires --lat and --radius)")
	f.IntVar(&flags.radiusMeters, "radius", 0, "Anchor radius in metres, 1..50000 (requires --lat and --lng)")
	f.StringVar(&flags.missingFieldPolicy, "missing-field-policy", radarclient.PolicyPossible, "How an unresolved floor is treated: possible or exclude")
	f.StringVar(&flags.groups, "groups", radarclient.GroupSupported+","+radarclient.GroupPossible, "Result groups to return, comma-separated: supported, possible, excluded")
	f.BoolVar(&flags.all, "all", false, "Follow every page while any requested group hasMore is true (at most 50 pages) and report truncation")
	f.BoolVar(&flags.jsonOut, "json", false, "Print the raw radar-geo-1 envelope instead of the human summary")
	return cmd
}

// input turns the flags into a validated search request. It is the single
// normalization boundary for the command, so a bad filter is refused before
// any network call.
func (f *radarSearchFlags) input() (radarclient.GeoSearchInput, error) {
	input := radarclient.GeoSearchInput{PageSize: radarclient.DefaultPageSize}

	types, err := radarclient.CanonicalPropertyTypeList(f.types)
	if err != nil {
		return input, fmt.Errorf("--type: %w", err)
	}
	input.Population.PropertyTypes = types

	neighborhoods, err := radarNeighborhoods(f.neighborhoods, f.neighborhoodSlugs)
	if err != nil {
		return input, err
	}
	input.Population.Neighborhoods = neighborhoods

	if f.minSqm < 0 || f.maxSqm < 0 {
		return input, fmt.Errorf("--min-sqm and --max-sqm must not be negative")
	}
	if f.minPrice < 0 || f.maxPrice < 0 {
		return input, fmt.Errorf("--min-price and --max-price must not be negative")
	}
	if f.maxSqm > 0 && f.minSqm > f.maxSqm {
		return input, fmt.Errorf("--min-sqm %.1f exceeds --max-sqm %.1f", f.minSqm, f.maxSqm)
	}
	if f.maxPrice > 0 && f.minPrice > f.maxPrice {
		return input, fmt.Errorf("--min-price %.0f exceeds --max-price %.0f", f.minPrice, f.maxPrice)
	}
	if f.minSqm > 0 {
		input.Population.AreaMin = &f.minSqm
	}
	if f.maxSqm > 0 {
		input.Population.AreaMax = &f.maxSqm
	}
	if f.minPrice > 0 {
		input.Population.PriceMin = &f.minPrice
	}
	if f.maxPrice > 0 {
		input.Population.PriceMax = &f.maxPrice
	}
	if f.floorMax < -1 {
		return input, fmt.Errorf("--floor-max must be -1 (unbounded), 0 (ground floor) or a positive floor number")
	}
	if f.floorMax >= 0 {
		floor := f.floorMax
		input.Population.FloorMax = &floor
	}

	if f.latSet || f.lngSet || f.radiusSet {
		if !f.latSet || !f.lngSet || !f.radiusSet {
			return input, fmt.Errorf("--lat, --lng and --radius must be given together; an anchor always carries a radius")
		}
		if f.lat < -90 || f.lat > 90 {
			return input, fmt.Errorf("--lat must be between -90 and 90")
		}
		if f.lng < -180 || f.lng > 180 {
			return input, fmt.Errorf("--lng must be between -180 and 180")
		}
		if f.radiusMeters < radarclient.MinRadiusMeters || f.radiusMeters > radarclient.MaxRadiusMeters {
			return input, fmt.Errorf("--radius must be between %d and %d metres", radarclient.MinRadiusMeters, radarclient.MaxRadiusMeters)
		}
		input.Geo.Anchor = &radarclient.GeoPoint{Lat: f.lat, Lng: f.lng}
		input.Geo.RadiusMeters = &f.radiusMeters
	}

	policy, err := radarclient.NormalizeMissingFieldPolicy(f.missingFieldPolicy)
	if err != nil {
		return input, fmt.Errorf("--missing-field-policy: %w", err)
	}
	input.MissingFieldPolicy = policy

	groups, err := radarParseGroups(f.groups)
	if err != nil {
		return input, err
	}
	input.Groups = groups
	return input, nil
}

// radarNeighborhoods normalizes labels to catalogue-slug candidates and keeps
// explicit slugs verbatim, preserving order and dropping duplicates. The API
// owns catalogue membership: an unknown slug comes back as a typed
// invalid_query, never as an empty result.
func radarNeighborhoods(labels, slugs []string) ([]string, error) {
	combined := make([]string, 0, len(labels)+len(slugs))
	for _, label := range labels {
		if strings.TrimSpace(label) == "" {
			return nil, fmt.Errorf("--neighborhood must not be empty")
		}
		combined = append(combined, translit.ToSlug(label))
	}
	for _, slug := range slugs {
		if strings.TrimSpace(slug) == "" {
			return nil, fmt.Errorf("--neighborhood-slug must not be empty")
		}
		combined = append(combined, slug)
	}
	normalized, err := radarclient.NormalizeNeighborhoods(combined)
	if err != nil {
		return nil, fmt.Errorf("--neighborhood: %w", err)
	}
	return normalized, nil
}

// radarParseGroups parses the comma-separated group list.
func radarParseGroups(raw string) ([]string, error) {
	groups, err := radarclient.NormalizeGroups(strings.Split(raw, ","))
	if err != nil {
		return nil, fmt.Errorf("--groups: %w", err)
	}
	if len(groups) == 0 {
		return nil, fmt.Errorf("--groups needs at least one of supported, possible, excluded")
	}
	return groups, nil
}

// radarAllOutput is the --all payload: the raw envelope of every page, in
// order, plus an explicit statement of whether every page was reached. A cap
// or an interruption is never silent.
type radarAllOutput struct {
	RequestedGroups  []string                        `json:"requested_groups"`
	PagesFetched     int                             `json:"pages_fetched"`
	Truncated        bool                            `json:"truncated"`
	TruncationReason string                          `json:"truncation_reason,omitempty"`
	Envelopes        []radarclient.GeoSearchResponse `json:"envelopes"`
}

// runRadarSearchAll follows next pages while any requested group reports
// hasMore. It writes what it read before returning an error, so an
// interruption leaves an honest, machine-readable partial answer and a
// non-zero exit status.
func runRadarSearchAll(ctx context.Context, client *radarclient.Client, input radarclient.GeoSearchInput, jsonOut bool, w io.Writer) error {
	output := radarAllOutput{
		RequestedGroups: input.Groups,
		Envelopes:       []radarclient.GeoSearchResponse{},
	}
	var runErr error

	for page := 1; ; page++ {
		input.Page = page
		response, err := client.GeoSearch(ctx, input)
		if err != nil {
			output.Truncated = true
			output.TruncationReason = radarTruncationReason(err)
			runErr = err
			break
		}
		output.Envelopes = append(output.Envelopes, response)
		output.PagesFetched = page
		if !radarHasMore(response.HasMore, input.Groups) {
			break
		}
		if page >= radarMaxPages {
			output.Truncated = true
			output.TruncationReason = "page_cap"
			runErr = fmt.Errorf("--all stopped at the %d-page safety cap while hasMore was still true; re-run the same filters to read further pages", radarMaxPages)
			break
		}
	}

	if jsonOut {
		if err := writeRadarAllJSON(w, output); err != nil {
			return err
		}
	} else if err := printRadarAllHuman(w, output); err != nil {
		return err
	}
	return runErr
}

// radarHasMore reports whether any requested group still has a later page. A
// group that was not requested cannot extend pagination.
func radarHasMore(hasMore radarclient.GeoGroupHasMore, groups []string) bool {
	for _, group := range groups {
		switch group {
		case radarclient.GroupSupported:
			if hasMore.Supported {
				return true
			}
		case radarclient.GroupPossible:
			if hasMore.Possible {
				return true
			}
		case radarclient.GroupExcluded:
			if hasMore.Excluded {
				return true
			}
		}
	}
	return false
}

// radarTruncationReason names why an --all run stopped early: the typed API
// code when the failure carried one, otherwise a generic marker.
func radarTruncationReason(err error) string {
	var apiErr *radarclient.APIError
	if errors.As(err, &apiErr) && apiErr.Code != "" {
		return apiErr.Code
	}
	return "request_failed"
}

// writeRadarSearchJSON writes the raw envelope unchanged.
func writeRadarSearchJSON(w io.Writer, response radarclient.GeoSearchResponse) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(response)
}

// writeRadarAllJSON writes the --all wrapper holding each page's raw envelope.
func writeRadarAllJSON(w io.Writer, output radarAllOutput) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

// writeRadarLocationJSON writes one listing's full location evidence.
func writeRadarLocationJSON(w io.Writer, detail radarclient.GeoLocationDetail) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(detail)
}

// printRadarSearchHuman prints the group totals, coverage and the page's top
// rows: price, area, floor, method/precision, warning count and URL.
func printRadarSearchHuman(w io.Writer, response radarclient.GeoSearchResponse) error {
	fmt.Fprintf(w, "totals: supported %d | possible %d | excluded %d | page %d (page size %d)\n",
		response.Totals.Supported, response.Totals.Possible, response.Totals.Excluded, response.Page, response.PageSize)

	inventory := "unknown"
	if response.Coverage.InventoryComplete != nil {
		inventory = strconv.FormatBool(*response.Coverage.InventoryComplete)
	}
	states := response.Coverage.LocationStates
	fmt.Fprintf(w, "observed: %s | inventory complete: %s | unresolved floors: %d\n",
		radarText(response.ObservedAt), inventory, response.Coverage.UnresolvedFloorCount)
	fmt.Fprintf(w, "location states: pending %d | in_progress %d | retry_due %d | complete %d | blocked %d | unlocated %d\n",
		states.Pending, states.InProgress, states.RetryDue, states.Complete, states.Blocked, states.Unlocated)

	groups := []struct {
		name    string
		rows    []radarclient.GeoListingRow
		hasMore bool
	}{
		{radarclient.GroupSupported, response.Groups.Supported, response.HasMore.Supported},
		{radarclient.GroupPossible, response.Groups.Possible, response.HasMore.Possible},
		{radarclient.GroupExcluded, response.Groups.Excluded, response.HasMore.Excluded},
	}
	for _, group := range groups {
		if len(group.rows) == 0 {
			fmt.Fprintf(w, "%s: no rows on this page (hasMore=%t)\n", group.name, group.hasMore)
			continue
		}
		fmt.Fprintf(w, "%s:\n", group.name)
		table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(table, "PRICE\tAREA\tFLOOR\tMETHOD/PRECISION\tWARNINGS\tURL")
		limit := len(group.rows)
		if limit > radarHumanRowsPerGroup {
			limit = radarHumanRowsPerGroup
		}
		for _, row := range group.rows[:limit] {
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%d\t%s\n",
				radarPrice(row.PriceEur), radarArea(row.AreaSqm), radarTextPointer(row.Floor),
				radarLocationTag(row.Location), len(row.Location.Warnings), row.URL)
		}
		if err := table.Flush(); err != nil {
			return err
		}
		if omitted := len(group.rows) - limit; omitted > 0 {
			fmt.Fprintf(w, "  (%d more %s rows on this page; hasMore=%t)\n", omitted, group.name, group.hasMore)
		} else if group.hasMore {
			fmt.Fprintf(w, "  (hasMore=true: more %s rows exist on later pages)\n", group.name)
		}
	}
	return nil
}

// printRadarAllHuman prints the combined --all rows once and states the page
// count and any truncation.
func printRadarAllHuman(w io.Writer, output radarAllOutput) error {
	if output.PagesFetched == 0 {
		fmt.Fprintln(w, "radar: no page completed")
		return nil
	}
	combined := output.Envelopes[len(output.Envelopes)-1]
	combined.Groups = radarclient.GeoGroups{}
	for _, envelope := range output.Envelopes {
		combined.Groups.Supported = append(combined.Groups.Supported, envelope.Groups.Supported...)
		combined.Groups.Possible = append(combined.Groups.Possible, envelope.Groups.Possible...)
		combined.Groups.Excluded = append(combined.Groups.Excluded, envelope.Groups.Excluded...)
	}
	if err := printRadarSearchHuman(w, combined); err != nil {
		return err
	}
	fmt.Fprintf(w, "pages fetched: %d", output.PagesFetched)
	if output.Truncated {
		fmt.Fprintf(w, " | TRUNCATED (%s): every requested page was not read", output.TruncationReason)
	}
	fmt.Fprintln(w)
	return nil
}

// newRadarLocationCmd registers the single-listing evidence lookup.
func newRadarLocationCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "location <id>",
		Short: "Print one listing's full Radar location evidence",
		Long: "Fetches one listing's full location evidence from the Radar locations/batch operation: " +
			"the method that produced the display point, its precision, whether it is source-asserted " +
			"or derived, the source pin, the support geometry kind, every quoted property clue, the " +
			"recorded alternatives and the warning codes.\n\n" +
			"A listing the Radar store does not know exits non-zero with a typed not_found error: an " +
			"unknown advert is not an unlocated one. --json prints the evidence document unchanged.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(args[0])
			if id == "" {
				return fmt.Errorf("a listing id is required")
			}
			client, err := radarclient.FromEnv()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), radarCommandTimeout)
			defer cancel()

			detail, err := client.GeoLocation(ctx, id)
			if err != nil {
				return err
			}
			if jsonOut {
				return writeRadarLocationJSON(os.Stdout, detail)
			}
			return printRadarLocationHuman(os.Stdout, detail)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print the full location evidence as JSON")
	return cmd
}

// printRadarLocationHuman prints the evidence a person reads: identity,
// method/precision/verification, the display and source points, the support
// kind, the quoted clues, alternatives and warnings.
func printRadarLocationHuman(w io.Writer, detail radarclient.GeoLocationDetail) error {
	fmt.Fprintf(w, "listing: %s | adv: %s\n", radarText(detail.ListingID), radarText(detail.AdvID))
	fmt.Fprintf(w, "method: %s | precision: %s | verification: %s | outcome: %s | state: %s\n",
		radarTextPointer(detail.Method), radarTextPointer(detail.Precision), radarTextPointer(detail.Verification),
		radarTextPointer(detail.Outcome), radarTextPointer(detail.State))
	fmt.Fprintf(w, "display point: %s\n", radarPoint(detail.DisplayPoint))
	fmt.Fprintf(w, "source pin: %s\n", radarSourcePin(detail))
	fmt.Fprintf(w, "support: %s\n", radarSupport(detail.Support, detail.SupportKind))

	if len(detail.Evidence) == 0 {
		fmt.Fprintln(w, "evidence: none")
	} else {
		fmt.Fprintln(w, "evidence:")
		for _, evidence := range detail.Evidence {
			ambiguous := ""
			if evidence.Ambiguous != nil && *evidence.Ambiguous {
				ambiguous = " (ambiguous)"
			}
			fmt.Fprintf(w, "  - [%s] %q relation=%s%s feature_ids=%d\n",
				radarText(evidence.SourceField), evidence.Quote, radarText(evidence.Relation), ambiguous, len(evidence.FeatureIDs))
		}
	}

	if len(detail.Alternatives) == 0 {
		fmt.Fprintln(w, "alternatives: none")
	} else {
		fmt.Fprintf(w, "alternatives (%d):\n", len(detail.Alternatives))
		for _, alternative := range detail.Alternatives {
			fmt.Fprintf(w, "  - precision=%s support=%s point=%s evidence=%d\n",
				radarText(alternative.Precision), radarText(alternative.SupportKind),
				radarPoint(alternative.DisplayPoint), len(alternative.Evidence))
		}
	}

	fmt.Fprintf(w, "warnings: %s\n", radarWarnings(detail.Warnings))
	fmt.Fprintf(w, "processed at: %s\n", radarTextPointer(detail.ProcessedAt))
	return nil
}

func radarPoint(point *radarclient.GeoPoint) string {
	if point == nil {
		return "none"
	}
	return fmt.Sprintf("%.6f, %.6f", point.Lat, point.Lng)
}

func radarSourcePin(detail radarclient.GeoLocationDetail) string {
	if detail.SourcePin == nil {
		return "none"
	}
	pin := radarPoint(detail.SourcePin)
	if detail.SourcePinObservedAt != nil && *detail.SourcePinObservedAt != "" {
		pin += " (observed " + *detail.SourcePinObservedAt + ")"
	}
	return pin
}

func radarSupport(geometry *radarclient.GeoJSONGeometry, kind *string) string {
	supportKind := "unknown"
	if kind != nil && strings.TrimSpace(*kind) != "" {
		supportKind = *kind
	}
	if geometry == nil {
		return supportKind + " (no geometry)"
	}
	return supportKind + " (" + radarText(geometry.Type) + ")"
}

func radarWarnings(warnings []string) string {
	if len(warnings) == 0 {
		return "none"
	}
	return strings.Join(warnings, ", ")
}

func radarText(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}

func radarTextPointer(value *string) string {
	if value == nil {
		return "—"
	}
	return radarText(*value)
}

func radarPrice(value *float64) string {
	if value == nil {
		return "—"
	}
	return fmt.Sprintf("€%.0f", *value)
}

func radarArea(value *float64) string {
	if value == nil {
		return "—"
	}
	return fmt.Sprintf("%.0f m²", *value)
}

func radarLocationTag(location radarclient.GeoListingLocation) string {
	method, precision := "—", "—"
	if location.Method != nil {
		method = *location.Method
	}
	if location.Precision != nil {
		precision = *location.Precision
	}
	return method + "/" + precision
}
