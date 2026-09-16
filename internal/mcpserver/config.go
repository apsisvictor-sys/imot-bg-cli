package mcpserver

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/apsisvictor/imot-cli/internal/radarclient"
)

// Environment names of the published geographic read API behind the two
// Radar geo tools. They are read together: with either one unset the tools
// stay registered and answer with an unavailable-capability error, never with
// a live-source approximation.
const (
	EnvRadarGeoBaseURL = "IMOT_MCP_RADAR_GEO_BASE_URL"
	EnvRadarGeoToken   = "IMOT_MCP_RADAR_GEO_TOKEN"
)

// Config controls caching, politeness, and quota behaviour for the MCP server.
//
// Defaults are deliberately conservative. Every colleague shares one scraping
// egress with Market Radar, so live fetches are serialized and rate limited
// while cached answers are cheap and effectively unmetered.
type Config struct {
	Addr   string // HTTP listen address (behind the reverse proxy)
	DBPath string // SQLite cache path

	SearchCacheTTL time.Duration // how long a search answer stays fresh
	DetailCacheTTL time.Duration // how long a listing detail stays fresh

	DefaultPages int // pages fetched when the caller does not say
	MaxPages     int // hard cap on pages per live search
	DefaultLimit int // listings returned when the caller does not say
	MaxLimit     int // hard cap on returned listings

	MaxConcurrentLive int           // simultaneous live scrape operations
	MinLiveSpacing    time.Duration // minimum gap between live operations

	// JSONResponse returns POST replies as application/json instead of
	// text/event-stream. Some clients (OpenAI's MCP client among them) advertise
	// only application/json and cannot consume an SSE-framed reply.
	JSONResponse bool

	// RadarDSN is the dedicated read-only Postgres connection to the
	// authoritative Market Radar database. It is empty by default, which leaves
	// every request on the labelled live imot.bg fallback. It must never carry
	// the collector writer or CRM credential; the configured role is expected to
	// be least-privilege read-only, and the reader additionally pins the session
	// read-only.
	RadarDSN string
	// RadarQueryTimeout bounds one Radar read.
	RadarQueryTimeout time.Duration
	// RadarFreshness is how old a complete neighbourhood observation may be
	// before results are labelled stale instead of complete.
	RadarFreshness time.Duration
	// RadarEnqueueDSN is the dedicated connection for the constrained
	// collection-request boundary (two fixed SQL functions). The configured role
	// carries no table privileges, so enqueue/status are its only capabilities.
	// It is empty by default, which keeps every tool unconditionally read-only.
	RadarEnqueueDSN string

	// RadarGeoBaseURL is the origin of the published Market Radar geographic
	// read API (contract radar-geo-1) and RadarGeoToken is its read bearer
	// token. Both are empty by default, which leaves radar_geo_search and
	// radar_location registered but unavailable. They are never used as a
	// fallback to scraping imot.bg, and the token is never logged.
	RadarGeoBaseURL string
	RadarGeoToken   string

	LiveQuotaPerWindow   int           // live operations allowed per identity per window
	GlobalQuotaPerWindow int           // live operations allowed across all identities per window
	QuotaWindow          time.Duration // quota window length

	Tokens map[string]string // path secret -> identity label
}

// Identity returns the label bound to a presented secret.
func (c Config) Identity(secret string) (string, bool) {
	if secret == "" {
		return "", false
	}
	label, ok := c.Tokens[secret]
	return label, ok
}

// LoadConfig reads configuration from the environment, applies defaults, and
// rejects configurations that would be unsafe to expose.
func LoadConfig() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, fmt.Errorf("resolving home directory: %w", err)
	}

	cfg := Config{
		Addr:                 envStr("IMOT_MCP_ADDR", "127.0.0.1:8099"),
		DBPath:               envStr("IMOT_MCP_DB", filepath.Join(home, ".imot", "mcp-cache.db")),
		SearchCacheTTL:       envDuration("IMOT_MCP_SEARCH_TTL", 30*time.Minute),
		DetailCacheTTL:       envDuration("IMOT_MCP_DETAIL_TTL", 6*time.Hour),
		DefaultPages:         envInt("IMOT_MCP_DEFAULT_PAGES", 1),
		MaxPages:             envInt("IMOT_MCP_MAX_PAGES", 3),
		DefaultLimit:         envInt("IMOT_MCP_DEFAULT_LIMIT", 40),
		MaxLimit:             envInt("IMOT_MCP_MAX_LIMIT", 80),
		MaxConcurrentLive:    envInt("IMOT_MCP_MAX_CONCURRENT", 1),
		MinLiveSpacing:       envDuration("IMOT_MCP_MIN_SPACING", 2500*time.Millisecond),
		LiveQuotaPerWindow:   envInt("IMOT_MCP_LIVE_QUOTA", 30),
		GlobalQuotaPerWindow: envInt("IMOT_MCP_GLOBAL_QUOTA", 150),
		QuotaWindow:          envDuration("IMOT_MCP_QUOTA_WINDOW", time.Hour),
		JSONResponse:         envBool("IMOT_MCP_JSON_RESPONSE", true),
		RadarDSN:             envStr("IMOT_MCP_RADAR_DSN", ""),
		RadarQueryTimeout:    envDuration("IMOT_MCP_RADAR_TIMEOUT", 5*time.Second),
		RadarFreshness:       envDuration("IMOT_MCP_RADAR_FRESHNESS", 26*time.Hour),
		RadarEnqueueDSN:      envStr("IMOT_MCP_RADAR_ENQUEUE_DSN", ""),
		RadarGeoBaseURL:      envStr(EnvRadarGeoBaseURL, ""),
		RadarGeoToken:        envStr(EnvRadarGeoToken, ""),
	}

	tokens, err := parseTokens(os.Getenv("IMOT_MCP_TOKENS"))
	if err != nil {
		return Config{}, err
	}
	cfg.Tokens = tokens

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	if len(c.Tokens) == 0 {
		return fmt.Errorf("IMOT_MCP_TOKENS is required, format \"label:secret\" separated by commas")
	}
	seen := make(map[string]bool, len(c.Tokens))
	for secret, label := range c.Tokens {
		// Length matters more than entropy policy here: the secret is the only
		// gate on a publicly reachable scraping endpoint until OAuth lands.
		if len(secret) < 24 {
			return fmt.Errorf("token for %q is too short: use at least 24 characters", label)
		}
		if label == "" {
			return fmt.Errorf("token %q has an empty label", secret[:8]+"...")
		}
		if seen[label] {
			return fmt.Errorf("duplicate token label %q", label)
		}
		seen[label] = true
	}
	if c.MaxPages < 1 {
		return fmt.Errorf("IMOT_MCP_MAX_PAGES must be at least 1")
	}
	if c.DefaultPages < 1 || c.DefaultPages > c.MaxPages {
		return fmt.Errorf("IMOT_MCP_DEFAULT_PAGES must be between 1 and IMOT_MCP_MAX_PAGES")
	}
	if c.MaxLimit < 1 || c.DefaultLimit < 1 || c.DefaultLimit > c.MaxLimit {
		return fmt.Errorf("limit configuration is invalid: default must be between 1 and max")
	}
	if c.MaxConcurrentLive < 1 {
		return fmt.Errorf("IMOT_MCP_MAX_CONCURRENT must be at least 1")
	}
	if c.LiveQuotaPerWindow < 1 || c.GlobalQuotaPerWindow < 1 {
		return fmt.Errorf("quota limits must be positive")
	}
	if c.RadarQueryTimeout <= 0 {
		return fmt.Errorf("IMOT_MCP_RADAR_TIMEOUT must be positive")
	}
	if c.RadarFreshness <= 0 {
		return fmt.Errorf("IMOT_MCP_RADAR_FRESHNESS must be positive")
	}
	// The geographic API is optional, but it is not half-configurable: an
	// origin without a token (or a token without an origin) is a mistake that
	// would otherwise surface as a confusing tool-time failure.
	if (c.RadarGeoBaseURL == "") != (c.RadarGeoToken == "") {
		return fmt.Errorf("%s and %s must be set together", EnvRadarGeoBaseURL, EnvRadarGeoToken)
	}
	if c.RadarGeoBaseURL != "" {
		if err := radarclient.ValidateBaseURL(c.RadarGeoBaseURL); err != nil {
			return fmt.Errorf("%s: %w", EnvRadarGeoBaseURL, err)
		}
	}
	return nil
}

// parseTokens parses "label:secret,label2:secret2".
func parseTokens(raw string) (map[string]string, error) {
	tokens := make(map[string]string)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return tokens, nil
	}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		label, secret, ok := strings.Cut(entry, ":")
		if !ok {
			return nil, fmt.Errorf("malformed token entry %q: expected label:secret", entry)
		}
		tokens[strings.TrimSpace(secret)] = strings.TrimSpace(label)
	}
	return tokens, nil
}

func envStr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}

func envBool(key string, fallback bool) bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch raw {
	case "", "default":
		return fallback
	case "0", "false", "no", "off":
		return false
	case "1", "true", "yes", "on":
		return true
	default:
		return fallback
	}
}

func envDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}
	return v
}
