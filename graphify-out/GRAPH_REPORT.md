# Graph Report - imot-cli  (2026-05-30)

## Corpus Check
- 13 files · ~11,443 words
- Verdict: corpus is large enough that graph structure adds value.

## Summary
- 134 nodes · 211 edges · 11 communities (10 shown, 1 thin omitted)
- Extraction: 91% EXTRACTED · 9% INFERRED · 0% AMBIGUOUS · INFERRED: 20 edges (avg confidence: 0.8)
- Token cost: 0 input · 0 output

## Community Hubs (Navigation)
- [[_COMMUNITY_Community 0|Community 0]]
- [[_COMMUNITY_Community 1|Community 1]]
- [[_COMMUNITY_Community 2|Community 2]]
- [[_COMMUNITY_Community 3|Community 3]]
- [[_COMMUNITY_Community 4|Community 4]]
- [[_COMMUNITY_Community 5|Community 5]]
- [[_COMMUNITY_Community 6|Community 6]]
- [[_COMMUNITY_Community 7|Community 7]]
- [[_COMMUNITY_Community 8|Community 8]]
- [[_COMMUNITY_Community 9|Community 9]]
- [[_COMMUNITY_Community 10|Community 10]]

## God Nodes (most connected - your core abstractions)
1. `imot.bg CLI` - 11 edges
2. `NewRootCommand()` - 10 edges
3. `parseListingBlock()` - 9 edges
4. `Store` - 8 edges
5. `Commands` - 8 edges
6. `Client` - 7 edges
7. `ParseDetail()` - 7 edges
8. `resolveCity()` - 7 edges
9. `runWatch()` - 7 edges
10. `New()` - 7 edges

## Surprising Connections (you probably didn't know these)
- `main()` --calls--> `NewRootCommand()`  [INFERRED]
  cmd/imot/main.go → internal/cli/root.go
- `parseListingBlock()` --calls--> `FormatTimestamp()`  [INFERRED]
  internal/scraper/parser.go → internal/scraper/types.go
- `resolveCity()` --calls--> `NormalizeCity()`  [INFERRED]
  internal/cli/root.go → internal/translit/translit.go
- `runDetail()` --calls--> `NewClient()`  [INFERRED]
  internal/cli/root.go → internal/scraper/scraper.go
- `TestParseDetailExtractsOrderedPhotoURLs()` --calls--> `ParseDetail()`  [INFERRED]
  internal/scraper/parser_photo_test.go → internal/scraper/parser.go

## Communities (11 total, 1 thin omitted)

### Community 0 - "Community 0"
Cohesion: 0.16
Nodes (25): addSearchFlags(), dedupListings(), filterListings(), formatListingAgent(), newCitiesCmd(), newDetailCmd(), newLocalCmd(), NewRootCommand() (+17 more)

### Community 1 - "Community 1"
Cohesion: 0.08
Nodes (23): Architecture, Build, code:bash (# Build), code:bash (./imot search --city София --neighborhood Лозенец --pages 1 ), code:sql (-- Count by type), code:block4 (cmd/imot/main.go        — entry point), code:bash (go build -o imot ./cmd/imot/), code:bash (# Run tests) (+15 more)

### Community 2 - "Community 2"
Cohesion: 0.25
Nodes (13): extractInfo(), extractType(), generateHash(), normalizePhotoURL(), parseBulgarianMonth(), ParseDetail(), parseInfo(), parseListingBlock() (+5 more)

### Community 3 - "Community 3"
Cohesion: 0.21
Nodes (9): dbExecutor, NeighborhoodStats, priceEntry, Stats, Store, appendPriceHistory(), generateListingHash(), insertSyncLog() (+1 more)

### Community 4 - "Community 4"
Cohesion: 0.32
Nodes (7): Client, BuildURL(), buildURLWithSlug(), extractNeighborhoodFromURL(), jitteredSleep(), resolveCitySlug(), resolveTypeSlug()

### Community 5 - "Community 5"
Cohesion: 0.33
Nodes (6): ParseTotalCount(), roundTripFunc, htmlResponse(), TestParseTotalCount(), TestSearchWithMetaIncludesResolvedNeighborhoodSlug(), TestSearchWithMetaMarksPartialOnLaterPageFailure()

### Community 6 - "Community 6"
Cohesion: 0.25
Nodes (8): `cities`, Commands, `local`, `search`, `sql`, `stats`, `sync`, `watch`

### Community 7 - "Community 7"
Cohesion: 0.29
Nodes (6): DetailListing, Listing, SearchError, SearchParams, SearchResult, FormatTimestamp()

### Community 9 - "Community 9"
Cohesion: 0.4
Nodes (3): NormalizeCity(), TestNeighborhoodSlugs(), ToSlug()

### Community 10 - "Community 10"
Cohesion: 0.83
Nodes (3): newTestStore(), TestSyncListingsStoresSameSpecsDifferentFloors(), TestUpsertListingPriceHistoryUsesValidJSON()

## Knowledge Gaps
- **29 isolated node(s):** `DetailListing`, `Listing`, `SearchError`, `SearchResult`, `SearchParams` (+24 more)
  These have ≤1 connection - possible missing edges or undocumented components.
- **1 thin communities (<3 nodes) omitted from report** — run `graphify query` to explore isolated nodes.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **Why does `NewClient()` connect `Community 0` to `Community 4`?**
  _High betweenness centrality (0.237) - this node is a cross-community bridge._
- **Why does `New()` connect `Community 0` to `Community 10`, `Community 3`?**
  _High betweenness centrality (0.178) - this node is a cross-community bridge._
- **What connects `DetailListing`, `Listing`, `SearchError` to the rest of the system?**
  _29 weakly-connected nodes found - possible documentation gaps or missing edges._
- **Should `Community 1` be split into smaller, more focused modules?**
  _Cohesion score 0.08 - nodes in this community are weakly interconnected._