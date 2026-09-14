package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apsisvictor/imot-cli/internal/scraper"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testSecret = "test-secret-that-is-long-enough-1234"

func testConfig(t *testing.T) Config {
	t.Helper()
	cfg, err := LoadConfig()
	_ = cfg
	t.Setenv("IMOT_MCP_TOKENS", "pilot:"+testSecret)
	t.Setenv("IMOT_MCP_DB", filepath.Join(t.TempDir(), "cache.db"))
	t.Setenv("IMOT_MCP_MIN_SPACING", "1ms")
	t.Setenv("IMOT_MCP_RADAR_DSN", "")
	loaded, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return loaded
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(nopWriter{}, nil))
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestLoadConfigRejectsMissingAndShortTokens(t *testing.T) {
	t.Setenv("IMOT_MCP_TOKENS", "")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected an error when no tokens are configured")
	}

	t.Setenv("IMOT_MCP_TOKENS", "pilot:short")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected an error for a short token")
	}

	t.Setenv("IMOT_MCP_TOKENS", "pilot:"+testSecret)
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig with a valid token: %v", err)
	}
	if identity, ok := cfg.Identity(testSecret); !ok || identity != "pilot" {
		t.Fatalf("Identity = %q, %v", identity, ok)
	}
	if _, ok := cfg.Identity("some-other-secret"); ok {
		t.Fatal("unknown secret must not resolve to an identity")
	}
}

func TestParseTokens(t *testing.T) {
	tokens, err := parseTokens(" a:s1 , b:s2 ,")
	if err != nil {
		t.Fatalf("parseTokens: %v", err)
	}
	if len(tokens) != 2 || tokens["s1"] != "a" || tokens["s2"] != "b" {
		t.Fatalf("tokens = %#v", tokens)
	}
	if _, err := parseTokens("broken"); err == nil {
		t.Fatal("expected an error for a malformed entry")
	}
}

func TestSearchKeyIgnoresCaseAndSpacing(t *testing.T) {
	base := scraper.SearchParams{City: "София", Neighborhood: "Лозенец", Type: "2-стаен", Pages: 1}
	same := scraper.SearchParams{City: "София", Neighborhood: "  лозенец ", Type: "2-стаен", Pages: 1}
	if searchKey("legacy", base, 40) != searchKey("legacy", same, 40) {
		t.Fatal("neighborhood case and spacing must not change the cache key")
	}

	other := base
	other.Pages = 2
	if searchKey("legacy", base, 40) == searchKey("legacy", other, 40) {
		t.Fatal("a different page count must produce a different cache key")
	}

	rented := base
	rented.Rent = true
	if searchKey("legacy", base, 40) == searchKey("legacy", rented, 40) {
		t.Fatal("rent and sale searches must not share a cache key")
	}
}

func TestCacheRespectsTTL(t *testing.T) {
	cache, err := OpenCache(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	defer cache.Close()

	key := searchKey("legacy", scraper.SearchParams{City: "София"}, 40)
	if err := cache.PutSearch(key, []byte(`{"ok":true}`), 3); err != nil {
		t.Fatalf("PutSearch: %v", err)
	}

	payload, _, ok, err := cache.GetSearch(key, time.Hour)
	if err != nil || !ok {
		t.Fatalf("GetSearch within ttl: ok=%v err=%v", ok, err)
	}
	if string(payload) != `{"ok":true}` {
		t.Fatalf("payload = %s", payload)
	}

	if _, _, ok, _ := cache.GetSearch(key, time.Nanosecond); ok {
		t.Fatal("an expired entry must report a miss")
	}

	if err := cache.PutDetail("abc123", []byte(`{"url":"x"}`)); err != nil {
		t.Fatalf("PutDetail: %v", err)
	}
	if _, _, ok, err := cache.GetDetail("abc123", time.Hour); err != nil || !ok {
		t.Fatalf("GetDetail: ok=%v err=%v", ok, err)
	}
}

func TestUsageCountsOnlyLiveCalls(t *testing.T) {
	cache, err := OpenCache(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	defer cache.Close()

	since := time.Now().Add(-time.Minute)
	for i := 0; i < 3; i++ {
		if err := cache.RecordUsage("pilot", ToolSearchListings, false); err != nil {
			t.Fatalf("RecordUsage: %v", err)
		}
	}
	if err := cache.RecordUsage("pilot", ToolSearchListings, true); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	if err := cache.RecordUsage("other", ToolGetListing, false); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}

	mine, err := cache.LiveCount("pilot", since)
	if err != nil {
		t.Fatalf("LiveCount: %v", err)
	}
	if mine != 3 {
		t.Fatalf("LiveCount for pilot = %d, want 3 (cache hits must not count)", mine)
	}

	all, err := cache.LiveCountAll(since)
	if err != nil {
		t.Fatalf("LiveCountAll: %v", err)
	}
	if all != 4 {
		t.Fatalf("LiveCountAll = %d, want 4", all)
	}
}

func TestAuthorizeLiveEnforcesBothBudgets(t *testing.T) {
	cfg := testConfig(t)
	cache, err := OpenCache(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	defer cache.Close()

	cfg.LiveQuotaPerWindow = 2
	cfg.GlobalQuotaPerWindow = 100
	svc := newService(cfg, cache, NewLimiter(1, 0), nil, "pilot", testLogger())

	if err := svc.authorizeLive(ToolSearchListings); err != nil {
		t.Fatalf("first live call should be allowed: %v", err)
	}
	_ = cache.RecordUsage("pilot", ToolSearchListings, false)
	_ = cache.RecordUsage("pilot", ToolSearchListings, false)

	if err := svc.authorizeLive(ToolSearchListings); err == nil {
		t.Fatal("expected the per-identity budget to be exhausted")
	}

	// A different identity still has room while the global budget holds.
	other := newService(cfg, cache, NewLimiter(1, 0), nil, "second", testLogger())
	if err := other.authorizeLive(ToolSearchListings); err != nil {
		t.Fatalf("a second identity should still be allowed: %v", err)
	}

	cfg.GlobalQuotaPerWindow = 1
	global := newService(cfg, cache, NewLimiter(1, 0), nil, "third", testLogger())
	if err := global.authorizeLive(ToolSearchListings); err == nil {
		t.Fatal("expected the shared budget to be exhausted")
	}
}

func TestLimiterSpacingAndConcurrency(t *testing.T) {
	limiter := NewLimiter(1, 60*time.Millisecond)
	ctx := context.Background()

	release, err := limiter.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	blocked := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		rel, err := limiter.Acquire(ctx)
		if err != nil {
			blocked <- -1
			return
		}
		defer rel()
		blocked <- time.Since(start)
	}()

	select {
	case <-blocked:
		t.Fatal("a second acquire must wait while the only slot is held")
	case <-time.After(30 * time.Millisecond):
	}

	release()
	select {
	case waited := <-blocked:
		if waited < 0 {
			t.Fatal("second acquire failed")
		}
		// The second acquire waits for the slot and the spacing floor.
		if waited < 50*time.Millisecond {
			t.Fatalf("second acquire waited %v, expected at least the spacing floor", waited)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second acquire never returned")
	}
}

func TestSearchListingsServesCachedPayloadWithoutScraping(t *testing.T) {
	cfg := testConfig(t)
	cache, err := OpenCache(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	defer cache.Close()

	params := scraper.SearchParams{City: "София", Neighborhood: "Лозенец", Type: "2-стаен", Pages: 1}
	cached := SearchListingsOutput{
		Query:         QuerySummary{City: "София", Neighborhood: "Лозенец", PropertyType: "2-стаен", Pages: 1},
		TotalMatching: 120,
		Returned:      2,
		Listings: []ListingSummary{
			{ID: "a1", Type: "2-СТАЕН", PriceEUR: 600, SizeSqM: 50, PricePerSqM: 12, Seller: "agency"},
			{ID: "a2", Type: "2-СТАЕН", PriceEUR: 700, SizeSqM: 60, PricePerSqM: 11.7, Seller: "private"},
		},
	}
	payload, err := json.Marshal(cached)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := cache.PutSearch(searchKey("legacy", params, cfg.DefaultLimit), payload, len(cached.Listings)); err != nil {
		t.Fatalf("PutSearch: %v", err)
	}

	svc := newService(cfg, cache, NewLimiter(1, 0), nil, "pilot", testLogger())
	out, err := svc.SearchListings(context.Background(), SearchListingsInput{
		City:         "софия", // lower case must still hit the same cache entry
		Neighborhood: "Лозенец",
		PropertyType: "2-стаен",
	})
	if err != nil {
		t.Fatalf("SearchListings from cache: %v", err)
	}
	if out.Source != "cache" {
		t.Fatalf("Source = %q, want cache", out.Source)
	}
	if out.Query.City != "София" {
		t.Fatalf("city was not normalized: %q", out.Query.City)
	}
	if out.Returned != 2 {
		t.Fatalf("Returned = %d, want 2", out.Returned)
	}
	// Cached hits must not consume the live budget.
	if count, _ := cache.LiveCount("pilot", time.Now().Add(-time.Hour)); count != 0 {
		t.Fatalf("live count = %d, want 0 after a cache hit", count)
	}
}

func TestToSummariesProjectsAgencyAndSeller(t *testing.T) {
	summaries := toSummaries([]scraper.Listing{
		{ID: "a1", Type: "2-СТАЕН", PriceEUR: 600, SizeSqM: 50, Agency: "iHOME", Phone: "0888", YearBuilt: "2007"},
		{ID: "a2", Type: "1-СТАЕН", PriceEUR: 400, SizeSqM: 40},
	})
	if len(summaries) != 2 {
		t.Fatalf("len = %d", len(summaries))
	}
	if summaries[0].Seller != "agency" || summaries[0].Agency != "iHOME" || summaries[0].PricePerSqM != 12 {
		t.Fatalf("agency listing projected wrong: %+v", summaries[0])
	}
	if summaries[1].Seller != "private" || summaries[1].Agency != "" {
		t.Fatalf("private listing projected wrong: %+v", summaries[1])
	}
}

func TestListingIDFromURL(t *testing.T) {
	cases := map[string]string{
		"https://www.imot.bg/obiava-2c178773245606768-dava-pod-naem-dvustaen": "2c178773245606768",
		"https://www.imot.bg/obiava-2c178773245606768":                        "2c178773245606768",
		"2c178773245606768":                 "",
		"https://www.imot.bg/not-a-listing": "",
	}
	for input, want := range cases {
		if got := listingIDFromURL(input); got != want {
			t.Errorf("listingIDFromURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRefreshNotesFlagSamplingHonestly(t *testing.T) {
	out := SearchListingsOutput{
		Query:         QuerySummary{Rent: true},
		TotalMatching: 120,
		Returned:      40,
		Sampled:       true,
		Partial:       true,
	}
	notes := strings.Join(refreshNotes(out, false), " | ")
	for _, want := range []string{"Sampled result", "40 of 120", "incomplete", "monthly rent"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes missing %q: %s", want, notes)
		}
	}

	empty := refreshNotes(SearchListingsOutput{}, false)
	if len(empty) == 0 || !strings.Contains(strings.Join(empty, " "), "No listings matched") {
		t.Errorf("expected guidance for an empty result, got %v", empty)
	}
}

func TestSecretFromRequestAndRedaction(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/mcp/"+testSecret, nil)
	if got := secretFromRequest(req); got != testSecret {
		t.Fatalf("path secret = %q", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+testSecret)
	if got := secretFromRequest(req); got != testSecret {
		t.Fatalf("bearer secret = %q", got)
	}

	if redactPath("/mcp/"+testSecret) != "/mcp/<redacted>" {
		t.Fatal("the secret must not survive into logs")
	}
	if redactPath("/healthz") != "/healthz" {
		t.Fatal("public paths should log unchanged")
	}
}

// TestEndToEndProtocol exercises the real MCP handshake, auth gate, tool
// registration and schema validation through the SDK client. It uses
// list_supported_filters, which reads no network data.
func TestEndToEndProtocol(t *testing.T) {
	cfg := testConfig(t)
	server, err := New(cfg, testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer server.Close()

	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	// Without a token the endpoint refuses the request.
	unauthorized, err := http.Post(ts.URL+"/mcp/wrong-secret", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("unauthenticated request: %v", err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", unauthorized.StatusCode)
	}

	health, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d", health.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp/" + testSecret}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := make(map[string]bool, len(tools.Tools))
	for _, tool := range tools.Tools {
		names[tool.Name] = true
		if tool.InputSchema == nil {
			t.Errorf("tool %s has no input schema", tool.Name)
		}
	}
	for _, want := range []string{ToolSearchListings, ToolGetListing, ToolListFilters} {
		if !names[want] {
			t.Errorf("tool %s was not registered", want)
		}
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      ToolListFilters,
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("list_supported_filters returned an error result: %+v", result.Content)
	}
	if result.StructuredContent == nil {
		t.Fatal("expected structured content in the tool result")
	}

	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var filters ListFiltersOutput
	if err := json.Unmarshal(encoded, &filters); err != nil {
		t.Fatalf("unmarshal structured content: %v", err)
	}
	if len(filters.Cities) == 0 || len(filters.PropertyTypes) == 0 {
		t.Fatalf("filters look empty: %+v", filters)
	}
	found := false
	for _, city := range filters.Cities {
		if city == "София" {
			found = true
		}
	}
	if !found {
		t.Fatal("София should be among the supported cities")
	}

	// A missing required argument must surface as a tool error, not a crash.
	bad, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      ToolSearchListings,
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool with missing city: %v", err)
	}
	if !bad.IsError {
		t.Fatal("search_listings without a city should return an error result")
	}
}

// TestSelfDescriptionGuidesTheModel guards the contract that made this server
// usable from a chat client at all: the model must learn what the server is,
// which tool to pick, and when to avoid built-in browsing, from the server's own
// metadata rather than from instructions a colleague has to type.
func TestSelfDescriptionGuidesTheModel(t *testing.T) {
	if strings.TrimSpace(serverInstructions) == "" {
		t.Fatal("server instructions must not be empty")
	}
	if len(serverInstructions) > 1400 {
		t.Errorf("instructions are %d chars; clients truncate and only the opening is surfaced", len(serverInstructions))
	}

	// Clients surface the opening of the instructions when the app is added, so
	// the first 512 characters must stand alone.
	opening := serverInstructions
	if len(opening) > 512 {
		opening = opening[:512]
	}
	for _, want := range []string{"imot.bg", ToolSearchListings, ToolGetListing, "EUR"} {
		if !strings.Contains(opening, want) {
			t.Errorf("the first 512 characters must stand alone but do not mention %q", want)
		}
	}

	// The model has to be told to prefer this data over its own browsing.
	if !strings.Contains(serverInstructions, "web search") {
		t.Error("instructions should tell the model to prefer these tools over web search")
	}
	// And how to behave when the shared live budget is spent.
	if !strings.Contains(serverInstructions, "limit") {
		t.Error("instructions should say what to do when the live-fetch limit is reported")
	}

	for name, description := range map[string]string{
		ToolSearchListings: toolSearchDescription,
		ToolGetListing:     toolGetDescription,
		ToolListFilters:    toolFiltersDescription,
	} {
		if strings.TrimSpace(description) == "" {
			t.Errorf("%s has no description", name)
			continue
		}
		if len(description) > 450 {
			t.Errorf("%s description is %d chars, too long to be read reliably", name, len(description))
		}
		if !strings.Contains(description, "Use this") {
			t.Errorf("%s description should say when to use it", name)
		}
	}

	annotations := readOnlyAnnotations()
	if !annotations.ReadOnlyHint || !annotations.IdempotentHint {
		t.Error("tools must be marked read-only and idempotent so clients do not gate them behind write confirmation")
	}
}

func TestAcceptCompatibleWidensNarrowClients(t *testing.T) {
	cases := map[string]struct{ json, stream bool }{
		"":                                    {true, true},
		"application/json":                    {true, true},
		"text/event-stream":                   {true, true},
		"application/json, text/event-stream": {true, true},
		"*/*":                                 {true, true},
	}
	for accept, want := range cases {
		var got string
		handler := acceptCompatible(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			got = r.Header.Get("Accept")
		}))
		req := httptest.NewRequest(http.MethodPost, "/mcp/x", nil)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		handler.ServeHTTP(httptest.NewRecorder(), req)
		if want.json && !want.stream {
			// no such case; both are always required after widening
		}
		jsonOK, streamOK := acceptSatisfies(got)
		if want.json && !jsonOK {
			t.Errorf("Accept %q -> %q, which the transport would reject (no JSON type)", accept, got)
		}
		if want.stream && !streamOK {
			t.Errorf("Accept %q -> %q, which the transport would reject (no event-stream type)", accept, got)
		}
	}
}

// TestJSONOnlyClientCanInitialize reproduces the failure that broke connector
// creation in ChatGPT: the streamable transport returns 400 unless Accept
// advertises both media types, and it framed replies as SSE, which a JSON-only
// client cannot read. Both are fixed, so a narrow client must get a plain JSON
// initialize result.
func TestJSONOnlyClientCanInitialize(t *testing.T) {
	cfg := testConfig(t)
	server, err := New(cfg, testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer server.Close()

	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"narrow","version":"1"}}}`
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp/"+testSecret, strings.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json") // the narrow case that used to fail

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, payload)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json so a JSON-only client can read it", ct)
	}
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if strings.Contains(string(payload), "event: message") {
		t.Error("reply is SSE-framed; a JSON-only client cannot parse it")
	}
	var parsed struct {
		Result struct {
			Instructions string `json:"instructions"`
			ServerInfo   struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("initialize reply is not plain JSON (%v): %s", err, payload)
	}
	if parsed.Result.ServerInfo.Name != ServerName || parsed.Result.Instructions == "" {
		t.Errorf("initialize result is incomplete: %+v", parsed.Result)
	}
}

// TestServerDiscoverAdvertisesLatestProtocol uses the exact request OpenAI's MCP
// client was captured sending. It opens with `server/discover` (the 2026-07-28
// replacement for `initialize`) and requires the server to offer 2026-07-28 in
// the reply. The streamable transport only claims that version when it runs
// stateless, so this guards a failure whose only symptom was "Error creating
// connector" in ChatGPT: the handshake succeeded, the version was simply absent
// from the list, and the client retried forever.
func TestServerDiscoverAdvertisesLatestProtocol(t *testing.T) {
	cfg := testConfig(t)
	server, err := New(cfg, testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer server.Close()

	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	const wantVersion = "2026-07-28"
	body := `{"jsonrpc":"2.0","id":"openai-mcp-discover","method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"` + wantVersion + `","io.modelcontextprotocol/clientInfo":{"name":"openai-mcp","version":"1.0.0"},"io.modelcontextprotocol/clientCapabilities":{"experimental":{"openai/visibility":{"enabled":true}}}}}}`

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp/"+testSecret, strings.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", wantVersion)
	// 2026-07-28 routes on this header; the captured request carries it.
	req.Header.Set("Mcp-Method", "server/discover")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("server/discover: %v", err)
	}
	defer resp.Body.Close()

	payload, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, payload)
	}

	var parsed struct {
		Result struct {
			SupportedVersions []string `json:"supportedVersions"`
			Instructions      string   `json:"instructions"`
		} `json:"result"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("reply is not plain JSON (%v): %s", err, payload)
	}

	found := false
	for _, v := range parsed.Result.SupportedVersions {
		if v == wantVersion {
			found = true
		}
	}
	if !found {
		t.Fatalf("server/discover advertises %v, which excludes %s; a client that only speaks %s cannot negotiate and will retry instead of connecting",
			parsed.Result.SupportedVersions, wantVersion, wantVersion)
	}
	if parsed.Result.Instructions == "" {
		t.Error("server/discover reply carries no instructions")
	}
}
