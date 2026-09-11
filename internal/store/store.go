package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/apsisvictor/imot-cli/internal/scraper"
)

const schema = `
CREATE TABLE IF NOT EXISTS listings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    listing_hash TEXT UNIQUE NOT NULL,
    type TEXT NOT NULL,
    city TEXT NOT NULL,
    neighborhood TEXT NOT NULL,
    price_eur INTEGER,
    price_bgn INTEGER,
    price_per_sqm REAL GENERATED ALWAYS AS (CASE WHEN size_sqm > 0 THEN price_eur * 1.0 / size_sqm ELSE 0 END) STORED,
    size_sqm INTEGER,
    floor TEXT,
    year_built TEXT,
    description TEXT,
    phone TEXT,
    agency TEXT,
    url TEXT,
    first_seen_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_seen_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    price_history TEXT
);

CREATE INDEX IF NOT EXISTS idx_listings_city ON listings(city);
CREATE INDEX IF NOT EXISTS idx_listings_neighborhood ON listings(neighborhood);
CREATE INDEX IF NOT EXISTS idx_listings_type ON listings(type);
CREATE INDEX IF NOT EXISTS idx_listings_price ON listings(price_eur);

CREATE TABLE IF NOT EXISTS sync_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    city TEXT NOT NULL,
    property_type TEXT,
    pages_scraped INTEGER,
    listings_found INTEGER,
    new_listings INTEGER,
    scraped_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
`

// Store handles SQLite operations
type Store struct {
	db *sql.DB
}

// New creates a new Store, initializing the database
func New(dbPath string) (*Store, error) {
	if dbPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("getting home dir: %w", err)
		}
		dbPath = filepath.Join(home, ".imot", "imot.db")
	}

	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating db directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Enable WAL mode for better concurrent reads
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, fmt.Errorf("setting WAL mode: %w", err)
	}

	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("creating schema: %w", err)
	}

	return &Store{db: db}, nil
}

// Close closes the database
func (s *Store) Close() error {
	return s.db.Close()
}

// DB returns the underlying database connection
func (s *Store) DB() *sql.DB {
	return s.db
}

type dbExecutor interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
	QueryRow(query string, args ...interface{}) *sql.Row
}

type priceEntry struct {
	Date  string `json:"date"`
	Price int    `json:"price"`
}

// generateListingHash creates a stable SHA-256 hash for deduplication.
func generateListingHash(l scraper.Listing) string {
	identity := l.ID
	if identity == "" {
		identity = l.URL
	}
	if identity != "" {
		sum := sha256.Sum256([]byte(identity))
		return fmt.Sprintf("%x", sum)
	}
	data := fmt.Sprintf("%s|%s|%s|%d|%d|%s|%s",
		l.Type, l.City, l.Neighborhood, l.PriceEUR, l.SizeSqM, l.Floor, l.Phone)
	sum := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%x", sum)
}

func appendPriceHistory(raw sql.NullString, scrapedAt string, price int) (string, error) {
	entries := []priceEntry{}
	if raw.Valid && strings.TrimSpace(raw.String) != "" {
		if err := json.Unmarshal([]byte(raw.String), &entries); err != nil {
			return "", fmt.Errorf("parsing price history: %w", err)
		}
	}
	entries = append(entries, priceEntry{Date: scrapedAt, Price: price})
	buf, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("encoding price history: %w", err)
	}
	return string(buf), nil
}

func upsertListing(exec dbExecutor, l scraper.Listing) (bool, error) {
	hash := generateListingHash(l)

	// Check if exists by canonical hash, with URL fallback for old local DB rows.
	var existingHash string
	var existingPrice int
	var priceHistory sql.NullString
	err := exec.QueryRow(
		"SELECT listing_hash, price_eur, price_history FROM listings WHERE listing_hash = ?",
		hash,
	).Scan(&existingHash, &existingPrice, &priceHistory)
	if err == sql.ErrNoRows && l.URL != "" {
		err = exec.QueryRow(
			"SELECT listing_hash, price_eur, price_history FROM listings WHERE url = ?",
			l.URL,
		).Scan(&existingHash, &existingPrice, &priceHistory)
	}

	if err == sql.ErrNoRows {
		// Insert new
		_, err := exec.Exec(`
			INSERT INTO listings (listing_hash, type, city, neighborhood, price_eur, price_bgn,
				size_sqm, floor, year_built, description, phone, agency, url)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			hash, l.Type, l.City, l.Neighborhood, l.PriceEUR, l.PriceBGN,
			l.SizeSqM, l.Floor, l.YearBuilt, l.Description, l.Phone, l.Agency, l.URL)
		if err != nil {
			return false, fmt.Errorf("inserting listing: %w", err)
		}
		return true, nil
	}

	if err != nil {
		return false, fmt.Errorf("checking existing: %w", err)
	}

	// Update last_seen_at and migrate old local hashes to the canonical hash.
	_, err = exec.Exec("UPDATE listings SET listing_hash = ?, last_seen_at = CURRENT_TIMESTAMP WHERE listing_hash = ?", hash, existingHash)
	if err != nil {
		return false, fmt.Errorf("updating last_seen: %w", err)
	}

	// If price changed, update price history.
	if existingPrice != l.PriceEUR {
		newHistory, err := appendPriceHistory(priceHistory, l.ScrapedAt, l.PriceEUR)
		if err != nil {
			return false, err
		}
		_, err = exec.Exec(`
			UPDATE listings SET price_eur = ?, price_bgn = ?, price_history = ?,
				last_seen_at = CURRENT_TIMESTAMP WHERE listing_hash = ?`,
			l.PriceEUR, l.PriceBGN, newHistory, hash)
		if err != nil {
			return false, fmt.Errorf("updating price: %w", err)
		}
	}

	return false, nil
}

// UpsertListing inserts or updates a listing.
func (s *Store) UpsertListing(l scraper.Listing) (bool, error) {
	return upsertListing(s.db, l)
}

// SyncListings atomically upserts listings and records sync metadata.
func (s *Store) SyncListings(city, propType string, pagesScraped int, listings []scraper.Listing) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("beginning sync transaction: %w", err)
	}
	defer tx.Rollback()

	newCount := 0
	for _, l := range listings {
		isNew, err := upsertListing(tx, l)
		if err != nil {
			return 0, err
		}
		if isNew {
			newCount++
		}
	}

	if err := insertSyncLog(tx, city, propType, pagesScraped, len(listings), newCount); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("committing sync transaction: %w", err)
	}
	return newCount, nil
}

func insertSyncLog(exec dbExecutor, city, propType string, pagesScraped, listingsFound, newListings int) error {
	_, err := exec.Exec(`
		INSERT INTO sync_log (city, property_type, pages_scraped, listings_found, new_listings)
		VALUES (?, ?, ?, ?, ?)`,
		city, propType, pagesScraped, listingsFound, newListings)
	return err
}

// InsertSyncLog records a sync operation.
func (s *Store) InsertSyncLog(city, propType string, pagesScraped, listingsFound, newListings int) error {
	return insertSyncLog(s.db, city, propType, pagesScraped, listingsFound, newListings)
}

// QueryListings queries listings with filters
func (s *Store) QueryListings(city, propType, neighborhood string, minPrice, maxPrice, minSqM, maxSqM int) ([]scraper.Listing, error) {
	query := "SELECT type, city, neighborhood, price_eur, price_bgn, size_sqm, floor, year_built, description, phone, agency, url FROM listings WHERE 1=1"
	var args []interface{}

	if city != "" {
		query += " AND city LIKE ?"
		args = append(args, "%"+city+"%")
	}
	if propType != "" {
		query += " AND type LIKE ?"
		args = append(args, "%"+strings.ToUpper(propType)+"%")
	}
	if neighborhood != "" {
		query += " AND neighborhood LIKE ?"
		args = append(args, "%"+neighborhood+"%")
	}
	if minPrice > 0 {
		query += " AND price_eur >= ?"
		args = append(args, minPrice)
	}
	if maxPrice > 0 {
		query += " AND price_eur <= ?"
		args = append(args, maxPrice)
	}
	if minSqM > 0 {
		query += " AND size_sqm >= ?"
		args = append(args, minSqM)
	}
	if maxSqM > 0 {
		query += " AND size_sqm <= ?"
		args = append(args, maxSqM)
	}

	query += " ORDER BY price_eur ASC"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying listings: %w", err)
	}
	defer rows.Close()

	var listings []scraper.Listing
	for rows.Next() {
		var l scraper.Listing
		err := rows.Scan(&l.Type, &l.City, &l.Neighborhood, &l.PriceEUR, &l.PriceBGN,
			&l.SizeSqM, &l.Floor, &l.YearBuilt, &l.Description, &l.Phone, &l.Agency, &l.URL)
		if err != nil {
			return nil, fmt.Errorf("scanning listing: %w", err)
		}
		listings = append(listings, l)
	}

	return listings, rows.Err()
}

// Stats holds price statistics
type Stats struct {
	Count          int
	AvgPrice       float64
	MedianPrice    float64
	AvgPricePerSqm float64
	MinPrice       int
	MaxPrice       int
	ByNeighborhood map[string]NeighborhoodStats
}

// NeighborhoodStats holds stats for a neighborhood
type NeighborhoodStats struct {
	Count    int
	AvgPrice float64
	AvgPPS   float64
}

// GetStats computes statistics for matching listings
func (s *Store) GetStats(city, propType, neighborhood string) (*Stats, error) {
	where := "WHERE size_sqm > 0 AND price_eur > 0"
	var args []interface{}

	if city != "" {
		where += " AND city LIKE ?"
		args = append(args, "%"+city+"%")
	}
	if propType != "" {
		where += " AND type LIKE ?"
		args = append(args, "%"+strings.ToUpper(propType)+"%")
	}
	if neighborhood != "" {
		where += " AND neighborhood LIKE ?"
		args = append(args, "%"+neighborhood+"%")
	}

	// Get basic stats
	var count int
	var avgPrice, avgPPS float64
	var minPrice, maxPrice int

	err := s.db.QueryRow(
		"SELECT COUNT(*), AVG(price_eur), AVG(price_per_sqm), MIN(price_eur), MAX(price_eur) FROM listings "+where,
		args...,
	).Scan(&count, &avgPrice, &avgPPS, &minPrice, &maxPrice)

	if err != nil {
		return nil, fmt.Errorf("computing stats: %w", err)
	}

	stats := &Stats{
		Count:          count,
		AvgPrice:       avgPrice,
		AvgPricePerSqm: avgPPS,
		MinPrice:       minPrice,
		MaxPrice:       maxPrice,
	}

	// Get all prices for median
	rows, err := s.db.Query("SELECT price_eur FROM listings "+where+" ORDER BY price_eur", args...)
	if err != nil {
		return nil, fmt.Errorf("getting prices for median: %w", err)
	}
	var prices []int
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return nil, err
		}
		prices = append(prices, p)
	}
	rows.Close()

	if len(prices) > 0 {
		sort.Ints(prices)
		mid := len(prices) / 2
		if len(prices)%2 == 0 {
			stats.MedianPrice = float64(prices[mid-1]+prices[mid]) / 2
		} else {
			stats.MedianPrice = float64(prices[mid])
		}
	}

	// By neighborhood
	nbWhere := strings.Replace(where, "size_sqm > 0 AND price_eur > 0", "1=1", 1)
	nbQuery := "SELECT neighborhood, COUNT(*), AVG(price_eur), AVG(price_per_sqm) FROM listings " +
		nbWhere + " AND size_sqm > 0 AND price_eur > 0 GROUP BY neighborhood ORDER BY AVG(price_eur) DESC"

	nbRows, err := s.db.Query(nbQuery, args...)
	if err != nil {
		return stats, nil // Return partial stats
	}
	defer nbRows.Close()

	stats.ByNeighborhood = make(map[string]NeighborhoodStats)
	for nbRows.Next() {
		var nb string
		var cnt int
		var avgP, avgS float64
		if err := nbRows.Scan(&nb, &cnt, &avgP, &avgS); err != nil {
			continue
		}
		stats.ByNeighborhood[nb] = NeighborhoodStats{
			Count:    cnt,
			AvgPrice: avgP,
			AvgPPS:   avgS,
		}
	}

	return stats, nil
}
