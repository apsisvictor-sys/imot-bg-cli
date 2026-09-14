//go:build live

// Live smoke test. It talks to imot.bg through the real MCP transport, so it is
// excluded from the default suite and must be run explicitly:
//
//	go test -tags live ./internal/mcpserver/ -run TestLive -v -timeout 300s
//
// Keep this file small and idempotent: it is the check that the deployed server
// actually works, not a substitute for the unit tests.
package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLiveSearchAndDetail(t *testing.T) {
	t.Setenv("IMOT_MCP_TOKENS", "live:"+testSecret)
	t.Setenv("IMOT_MCP_DB", filepath.Join(t.TempDir(), "live-cache.db"))
	t.Setenv("IMOT_MCP_MIN_SPACING", "1500ms")
	t.Setenv("IMOT_MCP_ADDR", "127.0.0.1:18099")
	// This is the live imot.bg smoke test: it must exercise the legacy source
	// path, not a Radar answer that happens to be configured in the ambient
	// environment.
	t.Setenv("IMOT_MCP_RADAR_DSN", "")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	server, err := New(cfg, testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer server.Close()

	stop := serveForTest(t, server, cfg.Addr)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "live-smoke", Version: "0.0.1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: "http://" + cfg.Addr + "/mcp/" + testSecret,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	searchOut := callTool[SearchListingsOutput](t, ctx, session, ToolSearchListings, map[string]any{
		"city":          "София",
		"neighborhood":  "Яворов",
		"property_type": "2-стаен",
		"rent":          true,
		"pages":         1,
	})

	t.Logf("live search: source=%s returned=%d total=%d sampled=%v median=%.0f",
		searchOut.Source, searchOut.Returned, searchOut.TotalMatching, searchOut.Sampled, searchOut.Stats.MedianEUR)

	if searchOut.Source != "live" {
		t.Fatalf("first search should be live, got %q", searchOut.Source)
	}
	if len(searchOut.Listings) == 0 {
		t.Fatal("live search returned no listings; the parser or the site changed")
	}
	if searchOut.Stats.Count == 0 || searchOut.Stats.MedianEUR == 0 {
		t.Fatalf("stats look empty: %+v", searchOut.Stats)
	}

	agencyListings := 0
	for _, listing := range searchOut.Listings {
		if listing.Agency != "" {
			agencyListings++
		}
		if listing.URL == "" || listing.ID == "" {
			t.Fatalf("listing is missing id or url: %+v", listing)
		}
	}
	if agencyListings == 0 {
		t.Log("warning: no agency listings in this sample; agency data not exercised")
	}

	// The same query must now be served from cache without touching the site.
	second := callTool[SearchListingsOutput](t, ctx, session, ToolSearchListings, map[string]any{
		"city":          "София",
		"neighborhood":  "Яворов",
		"property_type": "2-стаен",
		"rent":          true,
		"pages":         1,
	})
	if second.Source != "cache" {
		t.Fatalf("repeat search should be cached, got %q", second.Source)
	}
	if second.Returned != searchOut.Returned {
		t.Fatalf("cached result differs: %d vs %d listings", second.Returned, searchOut.Returned)
	}

	// Detail lookup for the first listing.
	first := searchOut.Listings[0]
	detailOut := callTool[GetListingOutput](t, ctx, session, ToolGetListing, map[string]any{
		"listing_id": first.ID,
	})
	if detailOut.Detail == nil {
		t.Fatal("get_listing returned no detail")
	}
	detail := detailOut.Detail
	t.Logf("live detail: source=%s description=%d chars features=%v broker=%q phone=%q photos=%d",
		detailOut.Source, len(detail.FullDescription), detail.Features,
		detail.BrokerName, detail.BrokerPhone, len(detail.PhotoURLs))

	if detail.FullDescription == "" {
		t.Error("detail is missing the full description")
	}
	if detail.URL == "" {
		t.Error("detail is missing a URL")
	}
	if len(detail.PhotoURLs) == 0 && detail.PhotoURL == "" {
		t.Error("detail is missing photos")
	}
}

// TestLiveRemote checks an already-running deployment when IMOT_MCP_REMOTE_URL
// is set, for example through an SSH port-forward of the VPS service:
//
//	ssh -N -L 18099:127.0.0.1:8099 root@76.13.137.79 &
//	IMOT_MCP_REMOTE_URL="http://127.0.0.1:18099/mcp/<secret>" \
//	  go test -tags live ./internal/mcpserver/ -run TestLiveRemote -v -timeout 300s
//
// The endpoint is never logged, because it carries the secret.
func TestLiveRemote(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("IMOT_MCP_REMOTE_URL"))
	if endpoint == "" {
		t.Skip("IMOT_MCP_REMOTE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "remote-smoke", Version: "0.0.1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		t.Fatalf("connect to the deployment failed: %v", err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	t.Logf("deployment exposes: %v", names)
	if len(names) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(names))
	}

	searchOut := callTool[SearchListingsOutput](t, ctx, session, ToolSearchListings, map[string]any{
		"city":          "София",
		"neighborhood":  "Яворов",
		"property_type": "2-стаен",
		"rent":          true,
	})
	t.Logf("deployed search: source=%s returned=%d total=%d median=%.0f",
		searchOut.Source, searchOut.Returned, searchOut.TotalMatching, searchOut.Stats.MedianEUR)
	if len(searchOut.Listings) == 0 {
		t.Fatal("the deployment returned no listings; check the VPN egress from the container")
	}

	detailOut := callTool[GetListingOutput](t, ctx, session, ToolGetListing, map[string]any{
		"listing_id": searchOut.Listings[0].ID,
	})
	if detailOut.Detail == nil || detailOut.Detail.FullDescription == "" {
		t.Fatal("detail lookup through the deployment returned nothing useful")
	}
	t.Logf("deployed detail: features=%d broker=%v photos=%d",
		len(detailOut.Detail.Features), detailOut.Detail.BrokerName != "", len(detailOut.Detail.PhotoURLs))
}

// serveForTest starts the real HTTP handler on addr and waits until it answers.
func serveForTest(t *testing.T, server *Server, addr string) func() {
	t.Helper()
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = httpServer.ListenAndServe() }()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			resp.Body.Close()
			return func() {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = httpServer.Shutdown(shutdownCtx)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("server did not become ready on %s", addr)
	return nil
}

// callTool invokes a tool and decodes its structured result.
func callTool[T any](t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, args map[string]any) T {
	t.Helper()
	var out T
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if result.IsError {
		t.Fatalf("%s returned an error result: %+v", name, result.Content)
	}
	if result.StructuredContent == nil {
		t.Fatalf("%s returned no structured content", name)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("%s: marshaling result: %v", name, err)
	}
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("%s: decoding result: %v", name, err)
	}
	return out
}
