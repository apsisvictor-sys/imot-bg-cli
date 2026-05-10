# imot.bg CLI

A command-line tool for scraping, storing, and querying Bulgarian real estate listings from [imot.bg](https://www.imot.bg).

## Quick Reference

```bash
# Build
go build -o imot ./cmd/imot/

# Search (live from imot.bg, no storage)
./imot search --city София --neighborhood Лозенец --type "2-стаен" --pages 1 --json

# Sync (scrape + store in local SQLite at ~/.imot/imot.db)
./imot sync --city София --neighborhood Лозенец --type "2-стаен" --pages 0

# Query local database
./imot local --city София --type "2-стаен" --neighborhood Лозенец --json

# Statistics
./imot stats --city София --neighborhood Лозенец

# Direct SQL
./imot sql "SELECT type, count(*) FROM listings GROUP BY type"

# Watch for new listings (polls every 30 min)
./imot watch --city София --neighborhood Лозенец --type "2-стаен" --interval 30m
```

## Commands

### `search`
Scrapes imot.bg live and outputs results to stdout. Does NOT store in database.

| Flag | Type | Description |
|------|------|-------------|
| `--city` | string | **Required.** Bulgarian city name (e.g. `София`, `Варна`, `Пловдив`) |
| `--neighborhood` | string | Neighborhood name. Accepts Bulgarian (`Лозенец`) or transliterated (`lozenets`). Partial match. |
| `--type` | string | Property type in Bulgarian (see Type Reference below) |
| `--pages` | int | Number of pages to fetch. `0` = all pages (auto-detect). `1` = first page only. |
| `--min-price` | int | Minimum price in EUR |
| `--max-price` | int | Maximum price in EUR |
| `--min-sqm` | int | Minimum size in sq.m |
| `--max-sqm` | int | Maximum size in sq.m |
| `--rent` | bool | Search rentals instead of sales |
| `--json` | bool | JSON output (for piping/programmatic use) |
| `--agent` | bool | Terse one-line format optimized for LLM consumption |
| `--quiet` | bool | Only show count + average price |

### `sync`
Same flags as `search`. Scrapes imot.bg and **stores** results in local SQLite (`~/.imot/imot.db`). Deduplicates by hash — running twice adds 0 new listings.

### `local`
Queries the local SQLite database with same filter flags. Does NOT hit imot.bg.

### `stats`
Computes price statistics from local data: count, average, median, min, max, price/sqm, neighborhood breakdown.

### `sql`
Execute arbitrary SQL against `~/.imot/imot.db`. Useful for ad-hoc analytics.

### `watch`
Polls imot.bg at regular intervals and reports new listings not previously seen.

| Flag | Type | Description |
|------|------|-------------|
| `--interval` | duration | Check interval (e.g. `5m`, `30m`, `1h`). Default: `30m`. |

### `cities`
Lists all supported cities and their URL slugs.

## Type Reference

### Residential
| Bulgarian | CLI flag | URL slug |
|-----------|----------|----------|
| 1-стаен | `"1-стаен"` | `ednostaen` |
| 2-стаен | `"2-стаен"` | `dvustaen` |
| 3-стаен | `"3-стаен"` | `tristaen` |
| 4-стаен | `"4-стаен"` | `chetiristaen` |
| многостаен | `"многостаен"` | `mnogostaen` |
| мезонет | `"мезонет"` | `mezonet` |
| къща | `"къща"` | `kashta` |

### Commercial
| Bulgarian | CLI flag | URL slug |
|-----------|----------|----------|
| офис | `"офис"` | `ofis` |
| магазин | `"магазин"` | `magazin` |
| заведение | `"заведение"` | `zavedenie` |
| склад | `"склад"` | `sklad` |

### Parking
| Bulgarian | CLI flag | URL slug |
|-----------|----------|----------|
| гараж / паркомясто | `"гараж"` | `garazh-parkomyasto` |

Note: `гараж` and `паркомясто` are separate subtypes on imot.bg but share the same search filter. The CLI extracts both as separate types (`ГАРАЖ` and `ПАРКОМЯСТО`).

### Land
| Bulgarian | CLI flag | URL slug |
|-----------|----------|----------|
| парцел | `"парцел"` | `place-za-stroezh` |
| земя | `"земя"` | `zemedelska-zemya` |

## Data Schema

Each listing has these fields:

| Field | JSON key | Type | Description |
|-------|----------|------|-------------|
| ID | `id` | string | Extracted from imot.bg URL (e.g. `1b177425523801314`) |
| Type | `type` | string | Uppercase Bulgarian (e.g. `2-СТАЕН`, `МАГАЗИН`) |
| City | `city` | string | Bulgarian city name (e.g. `София`) |
| Neighborhood | `neighborhood` | string | Bulgarian neighborhood name (e.g. `Лозенец`) |
| Price EUR | `price_eur` | int | Price in euros. `0` if not listed. |
| Price BGN | `price_bgn` | int | Price in leva. |
| Size | `size_sqm` | int | Area in square meters |
| Floor | `floor` | string | Floor info (e.g. `3 от 5`, `Партер`). Empty if not parseable. |
| Year Built | `year_built` | string | Year or range (e.g. `2007`, `1960-1969`, `under construction`) |
| Description | `description` | string | Truncated to 500 chars from search page card. Full description available on detail page (not yet extracted). |
| Phone | `phone` | string | Agent/owner phone. May be truncated (e.g. `0899`) if imot.bg shows partial number. |
| Agency | `agency` | string | Agency name. Empty string = owner listing. |
| URL | `url` | string | Full imot.bg listing URL |
| Scraped At | `scraped_at` | string | ISO 8601 timestamp |

## SQLite Database

Location: `~/.imot/imot.db`

### Tables

**`listings`** — deduplicated by hash (type + city + neighborhood + price + size + floor + phone)

| Column | Type | Notes |
|--------|------|-------|
| `id` | INTEGER | Auto-increment PK |
| `listing_hash` | TEXT | FNV-1a hash for dedup |
| `type` | TEXT | Uppercase Bulgarian |
| `city` | TEXT | |
| `neighborhood` | TEXT | |
| `price_eur` | INTEGER | |
| `price_bgn` | INTEGER | |
| `price_per_sqm` | REAL | Generated column |
| `size_sqm` | INTEGER | |
| `floor` | TEXT | |
| `year_built` | TEXT | |
| `description` | TEXT | |
| `phone` | TEXT | |
| `agency` | TEXT | |
| `url` | TEXT | |
| `first_seen_at` | DATETIME | |
| `last_seen_at` | DATETIME | Updated on re-sync |
| `price_history` | TEXT | JSON array of price changes |

**`sync_log`** — records each sync operation

| Column | Type |
|--------|------|
| `city` | TEXT |
| `property_type` | TEXT |
| `pages_scraped` | INTEGER |
| `listings_found` | INTEGER |
| `new_listings` | INTEGER |
| `scraped_at` | DATETIME |

### Useful Queries

```sql
-- Count by type
SELECT type, count(*) FROM listings GROUP BY type ORDER BY count(*) DESC;

-- Price stats per neighborhood
SELECT neighborhood, count(*), avg(price_eur), min(price_eur), max(price_eur)
FROM listings WHERE price_eur > 0 GROUP BY neighborhood;

-- Owner listings (no agency)
SELECT * FROM listings WHERE agency = '' OR agency IS NULL;

-- Recently added
SELECT * FROM listings ORDER BY first_seen_at DESC LIMIT 20;

-- Phone coverage
SELECT count(*), SUM(CASE WHEN phone != '' THEN 1 ELSE 0 END) FROM listings;
```

## Architecture

```
cmd/imot/main.go        — entry point
internal/
  cli/root.go           — cobra commands, flags, output formatting
  scraper/
    scraper.go          — HTTP client, URL building, neighborhood resolution
    parser.go           — HTML parsing (regex-based), type/floor/year extraction
    types.go            — data structures, city/type/oblast maps
  store/store.go        — SQLite operations, dedup, upsert, price history
  translit/
    translit.go         — Bulgarian ↔ Latin transliteration
    translit_test.go    — transliteration tests
```

## How Parsing Works

1. **Fetch HTML** from imot.bg search results page (windows-1251 → UTF-8)
2. **Split into blocks** on `class="zaglavie"` markers (one per listing card)
3. **Extract from each block**:
   - URL and ID from `<a href="//www.imot.bg/obiava-...">`
   - Location from `<location>град София, Лозенец</location>`
   - Type from title text (e.g. "Продава 2-СТАЕН") — split at first lowercase char to handle glued text like `МНОГОСТАЕНград София`
   - Prices via regex: `([0-9 ]+) €` and `([0-9 ]+\.[0-9]+) лв`
   - Agency from `class="name">` link
   - Info section from `class="info">` div — comma-separated structured data

4. **Info section parsing** (the `parseInfo` function):
   - Phone extracted from end (`тел.:` marker)
   - Remaining parts scanned for: size (`\d+ кв.м`), floor (`\d+-[тм]и ет. от \d+`), year (`Въведен в експлоатация \d{4}`)
   - **Non-positional scan**: street names can appear between fields, so the parser checks every part against every pattern rather than assuming fixed positions
   - Everything else becomes the description

5. **Deduplication**: FNV-1a hash of `type|city|neighborhood|price|size|floor|phone`

## Known Limitations

- **Description is truncated** at 500 chars from the search card. Full description requires fetching each listing's detail page individually (not yet implemented).
- **Phone may be truncated**: if imot.bg shows a partial number (e.g. `0899`), the CLI captures only what's visible.
- **Floor regex gaps**: patterns like "Партер", "1-ви ет." (instead of "1-ти") may not match. The regex currently handles `[digit]-[тм]и ет.` format.
- **Neighborhood resolution**: transliteration first, then POST form fallback. Some neighborhoods may 404 if neither method resolves the correct URL slug.
- **Count accuracy**: occasionally off by 1 vs imot.bg total (likely dedup hash collision or one listing failing to parse). Observed for 3-стаен (209 vs 210) and магазин (57 vs 58).

## Build

Requires Go 1.22+.

```bash
go build -o imot ./cmd/imot/
```

## Development

```bash
# Run tests
go test ./...

# Build and test a single neighborhood
go build -o imot ./cmd/imot/ && ./imot sync --city София --neighborhood Лозенец --type "2-стаен" --pages 1

# Wipe database and start fresh
rm ~/.imot/imot.db
```
