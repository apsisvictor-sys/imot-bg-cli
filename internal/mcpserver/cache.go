package mcpserver

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const cacheSchema = `
CREATE TABLE IF NOT EXISTS search_cache (
    key           TEXT PRIMARY KEY,
    payload       TEXT NOT NULL,
    listing_count INTEGER NOT NULL,
    created_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS detail_cache (
    listing_id TEXT PRIMARY KEY,
    payload    TEXT NOT NULL,
    fetched_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS usage_log (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    identity   TEXT NOT NULL,
    tool       TEXT NOT NULL,
    cache_hit  INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_usage_identity_time ON usage_log(identity, created_at);
CREATE INDEX IF NOT EXISTS idx_usage_time ON usage_log(created_at);
`

// Cache is the MCP server's own store. It is deliberately separate from the
// CLI's ~/.imot/imot.db so the two processes never contend for the same file or
// share a schema that one of them owns.
type Cache struct {
	db *sql.DB
}

// OpenCache opens (creating if needed) the MCP cache database.
func OpenCache(dbPath string) (*Cache, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("cache path is empty")
	}
	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("creating cache directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening cache: %w", err)
	}
	// A single writer keeps SQLite happy under concurrent tool calls and costs
	// nothing at this request rate.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enabling WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("setting busy timeout: %w", err)
	}
	if _, err := db.Exec(cacheSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating cache schema: %w", err)
	}
	return &Cache{db: db}, nil
}

// Close closes the cache database.
func (c *Cache) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

// GetSearch returns a cached search payload when it exists and is still within
// ttl. An expired entry reports a miss; pruning is handled separately.
func (c *Cache) GetSearch(key string, ttl time.Duration) ([]byte, time.Time, bool, error) {
	var payload string
	var createdAt int64
	err := c.db.QueryRow(
		`SELECT payload, created_at FROM search_cache WHERE key = ?`, key,
	).Scan(&payload, &createdAt)
	if err == sql.ErrNoRows {
		return nil, time.Time{}, false, nil
	}
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("reading search cache: %w", err)
	}

	created := time.Unix(createdAt, 0)
	if ttl > 0 && time.Since(created) > ttl {
		return nil, created, false, nil
	}
	return []byte(payload), created, true, nil
}

// PutSearch stores a search payload.
func (c *Cache) PutSearch(key string, payload []byte, listingCount int) error {
	_, err := c.db.Exec(
		`INSERT INTO search_cache (key, payload, listing_count, created_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET payload = excluded.payload,
		                               listing_count = excluded.listing_count,
		                               created_at = excluded.created_at`,
		key, string(payload), listingCount, time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("writing search cache: %w", err)
	}
	return nil
}

// GetDetail returns a cached listing detail within ttl.
func (c *Cache) GetDetail(listingID string, ttl time.Duration) ([]byte, time.Time, bool, error) {
	var payload string
	var fetchedAt int64
	err := c.db.QueryRow(
		`SELECT payload, fetched_at FROM detail_cache WHERE listing_id = ?`, listingID,
	).Scan(&payload, &fetchedAt)
	if err == sql.ErrNoRows {
		return nil, time.Time{}, false, nil
	}
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("reading detail cache: %w", err)
	}

	fetched := time.Unix(fetchedAt, 0)
	if ttl > 0 && time.Since(fetched) > ttl {
		return nil, fetched, false, nil
	}
	return []byte(payload), fetched, true, nil
}

// PutDetail stores a listing detail payload.
func (c *Cache) PutDetail(listingID string, payload []byte) error {
	_, err := c.db.Exec(
		`INSERT INTO detail_cache (listing_id, payload, fetched_at) VALUES (?, ?, ?)
		 ON CONFLICT(listing_id) DO UPDATE SET payload = excluded.payload,
		                                       fetched_at = excluded.fetched_at`,
		listingID, string(payload), time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("writing detail cache: %w", err)
	}
	return nil
}

// RecordUsage logs one tool call for quota accounting and pilot analysis. The
// cacheHit flag means "served without a live source fetch": it is true for MCP
// cache hits and for Radar reads, and false only for an imot.bg request. The
// column keeps its original name for compatibility.
func (c *Cache) RecordUsage(identity, tool string, cacheHit bool) error {
	hit := 0
	if cacheHit {
		hit = 1
	}
	_, err := c.db.Exec(
		`INSERT INTO usage_log (identity, tool, cache_hit, created_at) VALUES (?, ?, ?, ?)`,
		identity, tool, hit, time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("recording usage: %w", err)
	}
	return nil
}

// LiveCount returns how many live (cache-missing) operations an identity has
// performed since a point in time.
func (c *Cache) LiveCount(identity string, since time.Time) (int, error) {
	var count int
	err := c.db.QueryRow(
		`SELECT COUNT(*) FROM usage_log WHERE identity = ? AND cache_hit = 0 AND created_at >= ?`,
		identity, since.Unix(),
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting identity usage: %w", err)
	}
	return count, nil
}

// LiveCountAll returns how many live operations every identity combined has
// performed since a point in time.
func (c *Cache) LiveCountAll(since time.Time) (int, error) {
	var count int
	err := c.db.QueryRow(
		`SELECT COUNT(*) FROM usage_log WHERE cache_hit = 0 AND created_at >= ?`,
		since.Unix(),
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting total usage: %w", err)
	}
	return count, nil
}

// Prune drops cache entries and usage rows that are no longer useful.
func (c *Cache) Prune(usageBefore, searchBefore, detailBefore time.Time) error {
	if _, err := c.db.Exec(`DELETE FROM usage_log WHERE created_at < ?`, usageBefore.Unix()); err != nil {
		return fmt.Errorf("pruning usage log: %w", err)
	}
	if _, err := c.db.Exec(`DELETE FROM search_cache WHERE created_at < ?`, searchBefore.Unix()); err != nil {
		return fmt.Errorf("pruning search cache: %w", err)
	}
	if _, err := c.db.Exec(`DELETE FROM detail_cache WHERE fetched_at < ?`, detailBefore.Unix()); err != nil {
		return fmt.Errorf("pruning detail cache: %w", err)
	}
	return nil
}
