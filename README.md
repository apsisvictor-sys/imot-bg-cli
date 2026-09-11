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
| `--json` | bool | JSON output (for piping/programmatic use). Default shape is a bare listing array for backwards compatibility. |
| `--with-meta` | bool | With `--json`, output a metadata envelope: listings, requested/resolved neighborhood, total count, pages fetched/planned, partial flag, and page errors. |
| `--agent` | bool | Terse one-line format optimized for LLM consumption |
| `--quiet` | bool | Only show count + average price |
| `--file` | string | Parse a saved search-results HTML file instead of fetching live (use `-` for stdin). Makes no network request. |

With `--file`, `--city` is optional and no neighborhood slug is resolved: the
neighborhood filter runs client-side. `--pages` and `--full` are rejected because
they describe a live fetch. `--with-meta`, `--quiet` and `--json` all work, and the
envelope keeps the live shape: `server_filters` is empty, `client_filters` names the
filters actually applied, and a page with no cards, no total count and no no-results
marker becomes `partial` with an `unreadable_page` error rather than a verified empty
market.

```bash
./imot search --file internal/scraper/testdata/search-page.html --json
./imot search --file internal/scraper/testdata/search-unreadable.html --json --with-meta
```

Offline fixtures live in `internal/scraper/testdata/`. Each is a shared contract
used by both the Go tests and the Node fixture tests that run the built binary.

### `sync`
Same filter flags as `search` (no `--file`, no `--full`). Scrapes imot.bg and **stores** results in local SQLite (`~/.imot/imot.db`). Deduplicates by hash — running twice adds 0 new listings.

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

### `detail`
Fetches one listing's detail page from imot.bg and prints enriched data as JSON:
full description, floor, year, heating, construction type, all phones, agency URL,
photos, feature tags, broker contact and VAT note.

| Flag | Type | Description |
|------|------|-------------|
| `--file` | string | Parse a saved detail-page HTML file instead of fetching live. Use `-` for stdin. Makes no network request. |
| `--expect-url` | string | Listing URL the saved page is expected to contain. Only valid with `--file`; verifies advert identity. |
| `--json` | bool | JSON output (default true). |

```bash
./imot detail https://www.imot.bg/obiava-1b177425523801314-...
./imot detail --file internal/scraper/testdata/detail-legit.html \
  --expect-url https://www.imot.bg/obiava-1b177425523801314-dvustaen-apartament
```

The live and offline paths run exactly the same validation. A page must be a genuine
advert page and must carry its own canonical advert identity (from `og:url` or the
canonical link). The requested URL is never substituted for a missing one.

On rejection the command exits non-zero and prints typed error metadata as JSON on
stdout, so a challenge page, a removed advert, an unreadable page or a page whose
identity is absent or different can never be read as a successful advert with empty
fields:

| `kind` | Meaning |
|--------|---------|
| `fetch_failed` | Non-200 or network failure; `http_status` carries the status when there was one. |
| `challenge_page` | Bot/captcha interstitial. |
| `removed_advert` | HTTP 200 carried an explicit removed/not-found notice. |
| `unreadable_page` | HTTP 200 with no advert structure and no recognizable notice. |
| `missing_identity` | Advert-shaped page that exposes no canonical advert identity of its own. |
| `wrong_identity` | Page exposes a canonical advert identity for a different advertisement. |

Error metadata fields: `kind`, `requested_url`, `requested_advert_id`, `observed_url`,
`observed_advert_id`, `http_status`, `error`. The success payload keeps its original
shape; these fields are additive.

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
| парцел | `"парцел"` | `partsel` |
| земя | `"земя"` | `zemedelska-zemya` (legacy, not advertised on city pages) |

## Data Schema

Each listing has these fields:

| Field | JSON key | Type | Description |
|-------|----------|------|-------------|
| ID | `id` | string | Extracted from imot.bg URL (e.g. `1b177425523801314`) |
| Type | `type` | string | Uppercase Bulgarian (e.g. `2-СТАЕН`, `МАГАЗИН`) |
| City | `city` | string | Bulgarian city name (e.g. `София`) |
| Neighborhood | `neighborhood` | string | Bulgarian neighborhood/location text from the listing card (e.g. `Лозенец`) |
| Price EUR | `price_eur` | int | Price in euros. `0` if not listed. |
| Price BGN | `price_bgn` | int | Price in leva. |
| Size | `size_sqm` | int | Area in square meters |
| Floor | `floor` | string | Floor info (e.g. `3 от 5`, `Партер`). Empty if not parseable. |
| Year Built | `year_built` | string | Year or range (e.g. `2007`, `1960-1969`, `under construction`) |
| Description | `description` | string | Truncated to 500 chars from search page card. Use `imot detail` for the full description. |
| Phone | `phone` | string | Agent/owner phone. May be truncated (e.g. `0899`) if imot.bg shows partial number. |
| Agency | `agency` | string | Agency name. Empty string = owner listing. |
| URL | `url` | string | Full imot.bg listing URL |
| Scraped At | `scraped_at` | string | ISO 8601 timestamp |

### Search Metadata Envelope

`search --json` remains a bare array of listings. Add `--with-meta` for robust automation:

```bash
./imot search --city София --neighborhood Лозенец --pages 1 --json --with-meta
```

Envelope fields:

| Field | Type | Description |
|-------|------|-------------|
| `listings` | array | Parsed listing rows after CLI-side filtering/deduplication |
| `requested_city` | string | Normalized city requested by the caller |
| `requested_neighborhood` | string | Neighborhood text requested by the caller |
| `resolved_neighborhood_slug` | string | URL slug resolved and used for server-side filtering |
| `requested_type` | string | Property type requested by the caller |
| `total_count` | int | Count parsed from imot.bg first page, when available |
| `pages_planned` | int | Pages the CLI intended to fetch |
| `pages_fetched` | int | Pages successfully fetched and parsed |
| `partial` | bool | `true` when the scrape may be incomplete |
| `server_filters` | array | Names of the filters imot.bg applied in the request URL (`city`, `neighborhood`, `type`). A name here narrowed the source query, so `total_count` already excludes anything outside it. |
| `client_filters` | array | Names of the filters the CLI applied to already-downloaded rows (`min_price`, `max_price`, `min_sqm`, `max_sqm`). A name here did **not** narrow the source query: `total_count` still counts listings outside the filter. |
| `empty_verified` | bool | `true` only when imot.bg explicitly reported zero matches (its `Няма намерени обяви` marker). A page that merely yielded no cards is reported through `partial`/`errors` instead, never as `empty_verified`. |
| `errors` | array | Page-level recoverable failures. Each entry carries `page`, `url`, `error` and a machine-readable `kind`: `fetch_failed`, `unreadable_page`, `total_count_unknown`, or `detail_enrichment_failed`. |

`--min-price`, `--max-price`, `--min-sqm` and `--max-sqm` are client-side only: the
imot.bg request URL has no price or size parameter, so `client_filters` lists them
for the query while `total_count` still counts listings outside the band. Callers
that need the source query itself narrowed must decompose the query.

An unreadable page (block, captcha, changed layout) returns `partial=true` with an
`unreadable_page` error and an empty `listings` array, so `listings: []` plus
`total_count: 0` never silently means "verified empty" unless `empty_verified` is
true.

Scheduled workers should treat `partial=true` as unsafe for disappearance detection.

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

- **Description is truncated** at 500 chars from the search card. The full description is available through `imot detail` or `search --full`.
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
