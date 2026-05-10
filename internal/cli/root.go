package cli

import (
	"encoding/json"
	"fmt"
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
	flagAgent        bool
	flagQuiet        bool
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
	cmd.Flags().BoolVar(&flagAgent, "agent", false, "Terse LLM-optimized output")
	cmd.Flags().BoolVar(&flagQuiet, "quiet", false, "Only count + average price")
	cmd.Flags().BoolVar(&flagRent, "rent", false, "Search rentals instead of sales")
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

	return rootCmd
}

func newSearchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "search",
		Short: "Search listings from imot.bg (live)",
		Long:  "Fetches listings from imot.bg and displays them. Use --json for machine-readable output.",
		RunE:  runSearch,
	}
	addSearchFlags(cmd)
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

func runSearch(cmd *cobra.Command, args []string) error {
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
	listings, err := client.Search(params)
	if err != nil {
		return fmt.Errorf("search failed: %w", err)
	}

	// Client-side filtering
	listings = dedupListings(listings)
	listings = filterListings(listings, params, flagNeighborhood != "")

	return outputListings(listings)
}

func runSync(cmd *cobra.Command, args []string) error {
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
	listings, err := client.Search(params)
	if err != nil {
		return fmt.Errorf("search failed: %w", err)
	}

	// Client-side filtering
	listings = dedupListings(listings)
	listings = filterListings(listings, params, flagNeighborhood != "")

	st, err := store.New("")
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer st.Close()

	newCount := 0
	for _, l := range listings {
		isNew, err := st.UpsertListing(l)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: upsert failed: %v\n", err)
			continue
		}
		if isNew {
			newCount++
		}
	}

	err = st.InsertSyncLog(flagCity, flagType, flagPages, len(listings), newCount)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: logging sync: %v\n", err)
	}

	fmt.Fprintf(os.Stderr, "Synced %d listings (%d new) from %s\n", len(listings), newCount, flagCity)
	return nil
}

func runLocal(cmd *cobra.Command, args []string) error {
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
			name := nb
			if len(name) > 28 {
				name = name[:28]
			}
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
		var total int
		for _, l := range listings {
			total += l.PriceEUR
		}
		avg := float64(total) / float64(len(listings))
		fmt.Fprintf(os.Stderr, "Listings: %d | Avg price: €%.0f\n", len(listings), avg)
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
	// Terse one-line format for LLM consumption
	desc := l.Description
	if len(desc) > 100 {
		desc = desc[:97] + "..."
	}
	fmt.Printf("€%d | %d sqm | %s | %s, %s | floor:%s | year:%s | tel:%s | %s\n",
		l.PriceEUR, l.SizeSqM, l.Type, l.City, l.Neighborhood,
		l.Floor, l.YearBuilt, l.Phone, desc)
}
