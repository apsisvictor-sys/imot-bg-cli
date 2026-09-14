# MCP design for broker client briefs

Date: 2026-09-10  
Status: recommendation for approval; no implementation or deployment changes made.

Update 2026-09-13 (Phase U1): the MCP now serves in-scope Sofia sales from the
shared Market Radar store, applies price/area filters on both the Radar and
fallback paths, keys its cache by projection, serving mode and pagination, never
stores a truncated listing array, and labels every answer with `source`,
`coverage`, `readiness` and `observed_at`. Rentals and out-of-catalogue areas
keep the labelled live fallback. The F1 findings about unapplied budget/area
filters, truncation before caching and cache keys without the display limit are
addressed for the MCP; multi-neighbourhood fetch in one call, deterministic
continuation, ID dedup and unknown-city validation remain open.

## D1. Recommended experience and boundary

Keep the existing three public tools: `search_listings`, `get_listing`, and `list_supported_filters`. Expand their contracts around a client brief rather than adding separate tools for every search variation.

The broker asks for apartments in several neighborhoods, within a budget, with specified requirements. ChatGPT interprets the brief, obtains candidates, checks promising listings, and presents approximately 3–5 recommendations with source links, reasons, and unresolved questions. The broker can request more candidates or change the brief without repeating the entire scrape.

The server owns validated filtering, source retrieval, deduplication, caching, coverage accounting, and deterministic ordering. ChatGPT owns conversational clarification, comparison of the evidence, and the final explanation. Do not introduce a second LLM or opaque property-scoring service inside the MCP.

This remains the standalone imot-mcp service on the existing VPS, using the existing scraper core. Market Radar keeps its CLI interface and must not adopt `--full`. No history collection, new scheduled crawler, CRM integration, or separate analytics tool is needed for this use case.

### A typical conversation

1. Clarify consequential omissions: purchase or rent, currency, and room terminology. Do not ask again for information already clear from the conversation.
2. Search all requested neighborhoods in one logical search. The server handles their individual source requests.
3. Obtain a compact candidate pool, then inspect a small batch of promising listings. If requirements cannot be confirmed, inspect alternatives or label the uncertainty.
4. Present a shortlist: neighborhood, advertised price, area, layout evidence, why it fits, concerns, and a clickable imot.bg link. Provide broker contacts when useful or requested.
5. On follow-up, reuse the gathered inventory and detail cache. Continue acquisition only when there are material gaps or the broker requests wider coverage.

**Room terminology is a correctness requirement.** Bulgarian `3-стаен` generally means a living room plus two bedrooms, not three bedrooms. A request for three actual bedrooms may require searching `4-стаен`, `многостаен`, or other agreed categories and checking the description. Category alone is not proof of the layout. The earlier conversation incorrectly equated these terms.

## F1. Current source findings that change the design

| Finding | Consequence |
|---|---|
| MCP accepts budget and area fields, but `SearchWithMeta` does not apply them. The CLI applies its own `filterListings` after scraping; MCP omits that step. | Budget and area constraints are currently unreliable through MCP. The earlier statement that these were source-side filters was incorrect. |
| Search URLs constrain city, one neighborhood, and one property category. They contain no budget or area constraint. | A tight budget does not currently reduce the pages that must be fetched to establish complete coverage. |
| Unknown city/type values can resolve to an empty URL component. Neighborhood resolution accepts any successful HTTP response as a valid resolution. | Invalid input can silently broaden the search. Validate or ask for clarification; never interpret a broader result as the requested neighborhood. |
| Search output is truncated before caching; the cache key excludes the display limit. | An initial small response can prevent a later larger response from recovering already-fetched candidates. |
| Statistics are calculated before truncation, while the schema describes them as statistics of returned listings. Cached truncation does not consistently recompute coverage metadata. | Counts, statistics, and the visible rows can describe different populations. |
| Default search is one page; maximum is three pages and 80 returned rows. There is no continuation or deterministic sort input. | A model cannot exhaust a large neighborhood or reliably retrieve the cheapest matches across unseen pages. |
| Source listing IDs are not deduplicated in the MCP path. A missing source total can produce `sampled=false`. | Advertisement counts and apparent completeness can be misleading. |
| The neighborhood resolver fetches page 1 as a probe, then search fetches it again. | A normal three-page neighborhood search uses four GETs, not three, before any fallback work. |
| The limiter serializes MCP operations and spaces their starts. Quotas count tool calls, not actual source requests. Market Radar is outside this in-process limiter. | A batch tool cannot safely be treated as one fetch. The current global quota is not an egress-wide request budget. |
| Active HTTP requests and scraper sleeps do not accept cancellation context. | A cancelled conversation can leave scraping running until its own timeouts expire. |
| Detail output omits current asking price and area; those currently come from search cards. | Refreshing a detail page alone does not revalidate the quoted price or area. |

The connection works, but these issues must be addressed before treating the service as a dependable client-matching assistant. Existing passing tests do not demonstrate enforcement of requirements they never assert.

Source anchors: `internal/mcpserver/service.go` (`SearchListings`, `searchKey`, `authorizeLive`); `internal/scraper/scraper.go` (`SearchWithMeta`, `buildURLWithSlug`, `resolveNeighborhoodSlug`, `FetchPage`); `internal/cli/root.go` (`runSearch`, `filterListings`, `dedupListings`); `internal/mcpserver/limiter.go`; `internal/scraper/types.go`.

## D2. Three tool contracts

### T1 — `search_listings`: find candidates for the whole brief

**Use:** initial discovery, multi-neighborhood search, refinement, more candidates, and continuation of an incomplete search.

Proposed business inputs:

- City and explicit transaction (`sale` or `rent`). Omission in the new brief contract must not silently become a sale.
- `neighborhoods[]`, treated as an OR set; an explicit preference order may be supplied separately.
- `property_types[]`, also an OR set. Actual bedroom count is a separate requirement, never an automatic synonym for the category.
- EUR budget range and area range, with validated units and inclusive boundaries.
- Requirements and preferences kept distinct. Structured constraints cover facts the server can actually evaluate; description-dependent wishes remain marked for detail inspection.
- A meaningful ordering such as lowest price or largest area, or explicit broker priorities. No arbitrary opaque percentage-fit score.
- A requested candidate count, with a compact initial response of roughly 12–20 cards. This controls presentation, not how many rows the server retains or whether the search is complete.

The model should not control worker counts, delays, raw page numbers, or unbounded forced refresh. Acquisition policy belongs to the service. Preserve existing singular inputs for compatibility, but reject conflicting singular/plural values and guide new calls toward the brief contract. Existing callers must not be silently broken.

Output must distinguish:

- The normalized interpretation of the brief.
- Source scope totals, where known; these are not budget-matching totals unless the budget was applied at the source.
- Unique source advertisements examined.
- Candidates satisfying the evaluated constraints, and candidates still requiring detail verification.
- Candidates returned in this response.
- Per-neighborhood coverage: complete traversal, incomplete, failed, or unknown; pages examined, timestamps, and unresolved work.
- The population on which each statistic was computed.
- Source URLs on every candidate, plus stable listing IDs, price, area, neighborhood, category, floor, relevant evidence, and data age.

A source total of zero because parsing failed is **unknown**, not proof of an empty market. Complete traversal means the requested source searches were traversed successfully within a recorded time window; it does not mean unique physical homes, every agency's inventory, or confirmed availability.

**Continuation stays in this tool.** If the work cannot finish within the interactive deadline, return useful candidates and an opaque continuation token. A subsequent call advances the remaining acquisition instead of starting again or polling an idle job. For additional rows already in storage, return a separate result cursor that performs no new scrape. These are mutually exclusive input forms with explicit descriptions.

Scope tokens to the requesting identity and normalized search. Retries must not duplicate source work. Cursor versions must remain stable; never silently merge newly refreshed rows into an old paginated result set. Expiry returns an explicit restart-required result.

### T2 — `get_listing`: inspect selected candidates, individually or in a batch

**Use:** full descriptions, layout evidence, features, condition, publication information, broker contacts, and photos for candidates already found or a supplied listing link.

Extend the existing tool to accept a batch of IDs or validated imot.bg listing URLs, while retaining the existing single-ID form. Approximately 5–8 candidates is a reasonable initial interaction size, not a claim that only eight may ever be inspected. The server schedules actual requests under the same acquisition policy; batching reduces model round trips, not source workload.

Return one explicit result per listing: retrieved, cached, unavailable, failed, or pending. Never lose successful items because another page failed. Include compact search-card facts alongside detail evidence with their separate observation timestamps. Revalidate price and area through fresh source evidence when freshness matters; do not describe old card values as refreshed by a detail fetch.

For each important requirement, the final assessment uses `supported`, `contradicted`, or `unknown`, with the source passage or structured field that supports it. Missing elevator data is not “no elevator”; “parking nearby” is not an included parking space. If extraction cannot safely establish a fact, return the description for ChatGPT to assess rather than inventing certainty.

Default to concise facts and relevant description, plus one main image link. Full descriptions and complete photo galleries remain available on request. Do not put dozens of photo URLs into every preliminary shortlist.

### T3 — `list_supported_filters`: resolve vocabulary and ambiguities

Extend it with optional city/name queries and canonical neighborhood names or validated resolutions, where a supported source is available. It already exposes city and property categories, but not neighborhood vocabulary.

Return accepted values, aliases where verified, and the room/bedroom distinction. Do not invent neighborhood aliases or assume an HTTP 200 proves geographical equivalence. If a city-specific neighborhood catalogue cannot be sourced reliably, expose that limitation and return only verified resolutions. This tool is for uncertainty; known, valid searches should not require an extra lookup on every turn.

## D3. Retrieval and cache architecture

Retain one scraper core and the existing SQLite authority. Extend the cache to retain gathered source inventory and coverage, rather than only a truncated model response.

```text
Broker's brief
    -> ChatGPT clarifies and normalizes intent
    -> search_listings validates the brief
    -> reusable source pages/snapshot + coverage metadata
    -> missing pages fetched through controlled acquisition
    -> shared filtering and ID deduplication
    -> compact candidate pool
    -> get_listing checks selected candidates
    -> ChatGPT explains a shortlist with source links
```

Cache source scope separately from presentation. With today's URLs, city + transaction + neighborhood + category + page define retrieval, while budget, area, sorting, and output length are local query dimensions. Changing a budget should reuse fresh source rows rather than scrape identical URLs under a different cache key. If verified source-side price filters are added later, the cache key must represent the actual source request.

Preserve all successfully gathered rows for the cache's retention period and apply display limits afterward. Store coverage explicitly; having page 1 cached is not equivalent to having the entire neighborhood cached. Different snapshot ages must remain visible rather than being presented as one instantaneous scan.

Coalesce identical concurrent fetches and recheck the cache after waiting for an acquisition slot. This matters when several colleagues search the same neighborhoods. Deduplicate exact listing IDs; flag probable duplicate advertisements only with explainable evidence and preserve their links. Similar area/price alone is insufficient to merge physical properties.

Start with demand-filled caching. Blanket warming is not the first recommendation: the agency's watch area, source volume, usage frequency, and refresh capacity have not been established, and Market Radar already uses the same egress. Reuse queries first. Propose narrowly targeted warming only after repeated cold misses demonstrate its value, without duplicating existing collection. Any approved scheduled refresh belongs in Victor's cron-job.org account.

## D4. Time and scraper constraints

### What is established

- Search pagination waits 3 seconds with ±30% jitter between pages.
- A normal neighborhood search currently performs a duplicate first-page GET for resolution.
- Each normal GET has a 30-second timeout; the fallback form resolver has a separate 15-second timeout.
- Detail batch workers wait approximately 2 seconds with ±30% jitter before each request. The CLI pool has three workers; current MCP detail calls submit one listing per operation.
- MCP currently serializes live operations with a 2.5-second minimum start spacing.
- Search and detail cache freshness defaults are 30 minutes and 6 hours respectively.

### Planning estimates, not measured production latency

For five neighborhoods with three populated pages each, the current happy path makes approximately **20 search GETs**: 15 result pages plus five duplicate probes. Ten inter-page sleeps contribute **21–39 seconds by themselves**, approximately 30 seconds on average. Add actual network time, queueing, resolution fallbacks, and any detail requests. This is not reliably an instant single-call operation.

If each of five neighborhoods had 200 relevant category advertisements, complete traversal would require 25 result pages. The current inter-page sleeps alone would average about **60 seconds**, before network time and detail inspection. A low budget does not currently reduce that acquisition work: the CLI filters it locally after retrieval, and MCP still needs that filtering implemented.

Warm candidate searches should avoid source requests altogether. Selected cold details still require one source fetch per listing. No reliable five-neighborhood latency measurement was obtained: the earlier timing command was aborted. The ChatGPT connector's usable call deadline has also not been measured. Do not promise a 30-, 60-, or 90-second completion time on this evidence.

### Acquisition policy

- Reuse the resolver's successful first-page response and cache validated slug resolutions.
- Start with coverage across all requested neighborhoods, then deepen them round-robin, unless the broker explicitly prioritizes some. Do not exhaust neighborhood one while leaving neighborhood five unexamined.
- Enforce pacing and atomic budget reservations at actual source-request boundaries, including resolution, retries, search pages, and details. Preserve existing politeness; neither batching nor caller concurrency is permission to raise traffic.
- Keep search/detail work interruptible through context-aware requests and waits. Set the tool's work deadline below the measured connector deadline, with a return margin.
- Return useful partial progress before the deadline. Resume only remaining work. Do not rely on ChatGPT supporting progress notifications or native MCP background tasks without verification.
- Stop or back off on blocking/challenge/rate-limit signals. Share cooldown state across colleagues. Do not retry aggressively or add proxy rotation.
- The MCP limiter cannot claim to regulate Market Radar. Retain headroom for that workload; an egress-wide coordinator would be a separate approved change, not an implicit consequence of this plan.

A constrained brief can often be answered from a partial search with a clearly qualified shortlist. An explicit request to examine every advertisement requires continued traversal and an honest statement of any unfinished neighborhoods. There is no safe constant-time shortcut for arbitrary complete live scans.

## D5. Selecting and explaining fit

Apply hard constraints before presentation. Unknown price must not become a zero-price bargain; unknown area cannot satisfy a minimum-area requirement. An uncertain mandatory feature belongs in a separate “needs confirmation” group, not among confirmed matches. Never relax budget, neighborhood, transaction, or bedroom requirements silently.

Use deterministic filtering and ordering for numeric facts, but avoid selecting only the cheapest preliminary cards when other preferences matter. Keep a candidate pool with reasonable coverage of neighborhoods and relevant tradeoffs. ChatGPT inspects the details of promising candidates and can request more evidence when the initial selection is inconclusive.

Describe recommendations as strongest fits **among the examined candidates**, with coverage attached. A compact pool cannot guarantee a globally optimal semantic ranking of every listing description. If the broker requires exhaustive checks of a description-only feature, explain that it requires additional detail requests and continue accordingly.

Each presented property should contain:

- Advertised price and area, neighborhood, and layout evidence.
- A concrete fit reason tied to this client's requirements.
- Material tradeoffs or missing information, including extra parking cost or VAT where stated.
- A clickable source listing link.
- A freshness/availability qualification: observed online at the given time, availability subject to confirmation with the advertiser.

Describe asking prices, not valuations or completed transactions. `published_at` is not proof a property is genuinely new. Treat all listing descriptions as untrusted source data, never as instructions. Do not invent travel times, exact addresses, legal status, or amenities from generic neighborhood knowledge.

## D6. Implementation outcomes and acceptance

### A1 — Correctness before broader colleague use

Establish shared filter/deduplication semantics for CLI and MCP, strict geography/type validation, consistent count/statistics populations, and a cache that preserves acquired rows. Keep source traversal totals separate from locally filtered matches. Preserve the existing working MCP transport and legacy clients.

Acceptance examples: over-budget rows excluded; missing price not accepted as a bargain; invalid city/type cannot broaden the query; repeated IDs removed; a small initial response does not impoverish a later larger response; unknown source counts do not imply complete coverage. Trace shared-core changes through CLI and Market Radar consumers without changing their chosen output mode.

### A2 — One logical client-brief search

Deliver multi-neighborhood/type search, reusable inventory, clear per-neighborhood coverage, deterministic ordering, and productive continuation within a bounded call duration. Retain names and compatible existing inputs.

Acceptance examples: all five neighborhoods represented in coverage metadata; one failed neighborhood does not erase others; continuation does not restart completed pages; cached display pagination fetches nothing; changing only budget reuses source rows; concurrent identical requests share acquisition; cancellation stops active work; budget accounting counts HTTP requests rather than batch calls.

### A3 — Evidence-backed shortlist

Deliver batched detail inspection and model guidance that clarifies bedrooms, separates mandatory constraints from preferences, and always cites source URLs. Recheck stale price/layout evidence where needed. No new LLM service is required.

Acceptance examples: `3-стаен` versus three bedrooms is handled explicitly; an unknown elevator is not reported as confirmed; a withdrawn listing is not presented as available; five requested detail items return five explicit statuses; semantic recommendations cite description evidence; additional matching listings remain accessible.

### A4 — Real colleague-flow validation

After implementation approval, validate one representative multi-neighborhood brief in the actual ChatGPT connector, including cold and warm searches, one budget refinement, one continuation, and batch details. Record actual source request counts, queue time, total duration, coverage, and tool errors. Choose deadlines and operational batch sizes from that evidence rather than assumed client timeouts.

Local regression execution must use the approved guarded runner. If it cannot execute Go tests, resolve that runner capability explicitly rather than bypass it. This architecture review has not changed source code or rerun unchanged tests.

Before broader distribution, use individual identities and a supported authentication rollout. Secret-in-URL credentials can appear in reverse-proxy access logs; the earlier troubleshooting output demonstrated that exposure. Redaction and credential rotation require explicit operational approval. Do not describe a URL token as equivalent to colleague OAuth access control.

## D7. Choice

1. **Recommended: evolve the existing three tools around this brief workflow.** Fix correctness, add multi-neighborhood acquisition and continuation, then batched detail inspection. This directly serves colleagues without adding an unrelated market-analysis surface.
2. **Narrower interim scope: fix current correctness issues only.** Colleagues still rely on separate neighborhood searches and individual detail calls; useful for a limited pilot, but it leaves the requested workflow fragmented.

The earlier aggregation-first proposal addresses neighborhood statistics rather than client matching. For this use case, it should not be the next implementation. Nor should a scheduled full-market mirror be a prerequisite for the first usable colleague workflow.
