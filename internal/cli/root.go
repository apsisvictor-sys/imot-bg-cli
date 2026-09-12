package cli

import (
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
	if !strings.HasPrefix(url, "https://www.imot.bg/obiava-") {
		return fmt.Errorf("URL must be an imot.bg listing URL (e.g. https://www.imot.bg/obiava-...)")
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
		Short: "Report the property-type taxonomy advertised by a city page",
		Long: "Reads one imot.bg city page and prints the property-type slugs its own navigation advertises, as " +
			"{contract_version, city, source_url, observed_at, type_slugs, taxonomy_hash}. The hash is the SHA-256 " +
			"of the sorted unique slugs joined by LF, so two readings of the same page agree.\n\n" +
			"With --file it parses a saved, correctly decoded city page and makes no network request. A page that " +
			"is not a recognized city page for --city (challenge, block page, other city, moved navigation) exits " +
			"non-zero instead of printing an empty taxonomy.",
		Args: cobra.NoArgs,
		RunE: runTaxonomy,
	}
	cmd.Flags().String("city", "", "City name (Bulgarian), e.g. София")
	cmd.Flags().Bool("json", true, "JSON output (default true; JSON is the only supported shape)")
	cmd.Flags().String("file", "", "Parse a saved city-page HTML file instead of fetching live (use - for stdin)")
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
// Zero keeps its existing meaning: no bound for price/size, auto-detect for
// --pages.
func validateBounds(minPrice, maxPrice, minSqM, maxSqM, pages int) error {
	if minPrice < 0 || maxPrice < 0 || minSqM < 0 || maxSqM < 0 {
		return fmt.Errorf("price and size bounds must not be negative")
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
	return nil
}

func runSearch(cmd *cobra.Command, args []string) error {
	if err := validateBounds(flagMinPrice, flagMaxPrice, flagMinSqM, flagMaxSqM, flagPages); err != nil {
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
	if err := validateBounds(flagMinPrice, flagMaxPrice, flagMinSqM, flagMaxSqM, flagPages); err != nil {
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
	if err := validateBounds(flagMinPrice, flagMaxPrice, flagMinSqM, flagMaxSqM, flagPages); err != nil {
		return err
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
	if err := validateBounds(flagMinPrice, flagMaxPrice, flagMinSqM, flagMaxSqM, flagPages); err != nil {
		return err
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
	if err := validateBounds(flagMinPrice, flagMaxPrice, flagMinSqM, flagMaxSqM, flagPages); err != nil {
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

func formatListingAgent(l scraper.Listing) {
	// Terse one-line format for LLM consumption. Truncate by rune count: byte
	// slicing a 2-byte Cyrillic character emits U+FFFD.
	desc := scraper.TruncateRunes(l.Description, 100, "...")
	fmt.Printf("€%d | %d sqm | %s | %s, %s | floor:%s | year:%s | tel:%s | %s\n",
		l.PriceEUR, l.SizeSqM, l.Type, l.City, l.Neighborhood,
		l.Floor, l.YearBuilt, l.Phone, desc)
}
