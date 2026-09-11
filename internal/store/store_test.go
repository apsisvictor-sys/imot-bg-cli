package store

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/apsisvictor/imot-cli/internal/scraper"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := New(filepath.Join(t.TempDir(), "imot.db"))
	if err != nil {
		t.Fatalf("New store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestSyncListingsStoresSameSpecsDifferentFloors(t *testing.T) {
	st := newTestStore(t)
	listings := []scraper.Listing{
		{ID: "adv-1", Type: "2-СТАЕН", City: "София", Neighborhood: "Лозенец", PriceEUR: 100000, SizeSqM: 80, Floor: "2", URL: "https://www.imot.bg/obiava-adv-1-test", ScrapedAt: "2026-05-12T00:00:00Z"},
		{ID: "adv-2", Type: "2-СТАЕН", City: "София", Neighborhood: "Лозенец", PriceEUR: 100000, SizeSqM: 80, Floor: "5", URL: "https://www.imot.bg/obiava-adv-2-test", ScrapedAt: "2026-05-12T00:00:00Z"},
	}

	newCount, err := st.SyncListings("София", "2-стаен", 1, listings)
	if err != nil {
		t.Fatalf("SyncListings: %v", err)
	}
	if newCount != 2 {
		t.Fatalf("expected 2 new listings, got %d", newCount)
	}

	var count int
	if err := st.DB().QueryRow("SELECT COUNT(*) FROM listings").Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 stored rows, got %d", count)
	}
}

func TestUpsertListingPriceHistoryUsesValidJSON(t *testing.T) {
	st := newTestStore(t)
	listing := scraper.Listing{ID: "adv-1", Type: "2-СТАЕН", City: "София", Neighborhood: "Лозенец", PriceEUR: 100000, PriceBGN: 195583, SizeSqM: 80, URL: "https://www.imot.bg/obiava-adv-1-test", ScrapedAt: "2026-05-12T00:00:00Z"}
	if _, err := st.UpsertListing(listing); err != nil {
		t.Fatalf("initial upsert: %v", err)
	}
	listing.PriceEUR = 95000
	listing.PriceBGN = 185812
	listing.ScrapedAt = "2026-05-13T00:00:00Z"
	if _, err := st.UpsertListing(listing); err != nil {
		t.Fatalf("price update: %v", err)
	}

	var raw string
	if err := st.DB().QueryRow("SELECT price_history FROM listings WHERE url = ?", listing.URL).Scan(&raw); err != nil {
		t.Fatalf("select price history: %v", err)
	}
	var entries []priceEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		t.Fatalf("price history is not valid JSON: %v; raw=%s", err, raw)
	}
	if len(entries) != 1 || entries[0].Price != 95000 {
		t.Fatalf("unexpected price history entries: %#v", entries)
	}
}
