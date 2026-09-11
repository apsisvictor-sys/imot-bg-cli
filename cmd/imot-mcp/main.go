// Command imot-mcp serves imot.bg market data to MCP clients such as ChatGPT.
//
// It is the interactive counterpart to the imot CLI: same scraping core, but
// cache-first, rate limited, and read-only. Market Radar keeps using the CLI.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/apsisvictor/imot-cli/internal/mcpserver"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := mcpserver.LoadConfig()
	if err != nil {
		logger.Error("configuration error", "error", err)
		os.Exit(1)
	}

	server, err := mcpserver.New(cfg, logger)
	if err != nil {
		logger.Error("startup failed", "error", err)
		os.Exit(1)
	}
	defer server.Close()

	if err := prune(server); err != nil {
		logger.Warn("initial cache prune failed", "error", err)
	}
	stopPrune := startPruner(server, logger)
	defer stopPrune()

	httpServer := &http.Server{
		Addr:    cfg.Addr,
		Handler: server.Handler(),
		// A streamable MCP session can stay open, so there is no write timeout;
		// slow or dead clients are bounded by the header and idle timeouts.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	logger.Info("imot-mcp listening",
		"addr", cfg.Addr,
		"db", cfg.DBPath,
		"identities", len(cfg.Tokens),
		"search_ttl", cfg.SearchCacheTTL.String(),
		"detail_ttl", cfg.DetailCacheTTL.String(),
		"max_concurrent_live", cfg.MaxConcurrentLive,
		"min_live_spacing", cfg.MinLiveSpacing.String(),
		"live_quota_per_identity", cfg.LiveQuotaPerWindow,
		"live_quota_global", cfg.GlobalQuotaPerWindow,
		"quota_window", cfg.QuotaWindow.String(),
		"default_pages", cfg.DefaultPages,
		"max_pages", cfg.MaxPages,
	)

	errCh := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		logger.Error("http server stopped", "error", err)
		os.Exit(1)
	case <-ctx.Done():
		logger.Info("shutdown requested")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("graceful shutdown failed", "error", err)
	}
}

func prune(server *mcpserver.Server) error {
	return server.PruneCache(7*24*time.Hour, 7*24*time.Hour, 30*24*time.Hour)
}

func startPruner(server *mcpserver.Server, logger *slog.Logger) func() {
	ticker := time.NewTicker(time.Hour)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				if err := prune(server); err != nil {
					logger.Warn("cache prune failed", "error", err)
				}
			case <-done:
				ticker.Stop()
				return
			}
		}
	}()
	return func() { close(done) }
}
