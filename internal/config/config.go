// Package config loads and validates service configuration.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kongkx/cloud-print-service/internal/domain"
)

// Duration is a time.Duration that reads from JSON as "15s" / "2m".
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string like \"15s\": %w", err)
	}
	v, err := ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// ParseDuration accepts Go durations ("90h", "1m30s"), day suffixes ("90d") and bare
// numbers (seconds), because operators configure TTLs in days far more often than in
// hours and "30d" reading as an error is needless friction.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return time.Duration(n * float64(time.Second)), nil
	}
	if strings.HasSuffix(s, "d") || strings.HasSuffix(s, "D") {
		if n, err := strconv.ParseFloat(strings.TrimSuffix(s[:len(s)-1], "D"), 64); err == nil {
			return time.Duration(n * 24 * float64(time.Hour)), nil
		}
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("bad duration %q: want 15s, 90h, 90d or a number of seconds", s)
	}
	return v, nil
}

func (d Duration) D() time.Duration { return time.Duration(d) }

// UIConfig configures the embedded operator panel.
type UIConfig struct {
	Enabled bool   `json:"enabled"`
	Path    string `json:"path"`  // default /ui/
	Title   string `json:"title"` // injected into index.html
}

// DiscoveryConfig controls printer discovery.
type DiscoveryConfig struct {
	Enabled         bool     `json:"enabled"`
	Types           []string `json:"types"`            // DNS-SD service types to browse
	Interval        Duration `json:"interval"`         // sweep period
	CandidateTTL    Duration `json:"candidate_ttl"`    // forget devices that stop answering
	Verify          *bool    `json:"verify"`           // ask each hit `~HI` (default true)
	Site            string   `json:"site"`             // recorded on candidates
	IdentifyTimeout Duration `json:"identify_timeout"` // one `~HI` probe
	BrowseTimeout   Duration `json:"browse_timeout"`   // one browse pass
}

// RenderConfig selects the ZPL render engine used for label previews.
type RenderConfig struct {
	Backend     string `json:"backend"`      // auto|zebrash|labelize
	LabelizeURL string `json:"labelize_url"` // only for backend=labelize
}

// ClientKey is a bootstrap API key: token -> identity. In production keys live in the DB
// (hashed); config is only the bootstrap/emergency path.
type ClientKey struct {
	ClientID string `json:"client_id"`
	Role     string `json:"role"` // client | admin
}

type Config struct {
	Addr        string `json:"addr"`
	StoreDriver string `json:"store_driver"` // memory | postgres
	PostgresDSN string `json:"postgres_dsn"`
	LogLevel    string `json:"log_level"`

	StatusPollInterval  Duration `json:"status_poll_interval"`
	StatusStaleAfter    Duration `json:"status_stale_after"`
	PrinterDialTimeout  Duration `json:"printer_dial_timeout"`
	PrinterWriteTimeout Duration `json:"printer_write_timeout"`
	PrinterReadTimeout  Duration `json:"printer_read_timeout"`

	QueueSizePerPrinter int      `json:"queue_size_per_printer"`
	MaxAttempts         int      `json:"max_attempts"`
	ConfirmTimeout      Duration `json:"confirm_timeout"`
	JobQueueTTL         Duration `json:"job_queue_ttl"`
	BaseBackoff         Duration `json:"base_backoff"`
	MaxPayloadBytes     int64    `json:"max_payload_bytes"`
	MaxBatchSize        int      `json:"max_batch_size"`
	RejectWhenNotUsable bool     `json:"reject_when_not_usable"`
	RateLimitRPS        float64  `json:"rate_limit_rps"`
	RateLimitBurst      int      `json:"rate_limit_burst"`

	APIKeys  map[string]ClientKey `json:"api_keys"`
	Printers []domain.Printer     `json:"printers"`

	// UI is the embedded operator panel (same binary, same origin as the API).
	UI UIConfig `json:"ui"`
	// Render configures the ZPL preview engine (POST /api/v1/admin/preview).
	Render RenderConfig `json:"render"`
	// Discovery finds printers on the LAN. Findings are candidates until adopted.
	Discovery DiscoveryConfig `json:"discovery"`

	// TokenDefaultTTL applies to tokens issued for business systems when none is given.
	TokenDefaultTTL Duration `json:"token_default_ttl"`
}

func defaults() Config {
	return Config{
		Addr:                ":8080",
		StoreDriver:         "memory",
		LogLevel:            "info",
		StatusPollInterval:  Duration(15 * time.Second),
		StatusStaleAfter:    Duration(45 * time.Second),
		PrinterDialTimeout:  Duration(3 * time.Second),
		PrinterWriteTimeout: Duration(5 * time.Second),
		PrinterReadTimeout:  Duration(3 * time.Second),
		QueueSizePerPrinter: 50,
		MaxAttempts:         3,
		ConfirmTimeout:      Duration(10 * time.Second),
		JobQueueTTL:         Duration(5 * time.Minute),
		BaseBackoff:         Duration(500 * time.Millisecond),
		MaxPayloadBytes:     1 << 20,
		MaxBatchSize:        200,
		RateLimitRPS:        30,
		RateLimitBurst:      100,
		TokenDefaultTTL:     Duration(90 * 24 * time.Hour),
		UI:                  UIConfig{Enabled: true, Path: "/ui/", Title: "Cloud Print Service"},
		Render:              RenderConfig{Backend: "auto"},
		Discovery:           DiscoveryConfig{Interval: Duration(time.Minute), CandidateTTL: Duration(10 * time.Minute), BrowseTimeout: Duration(2 * time.Second), IdentifyTimeout: Duration(2 * time.Second)},
		Printers:            []domain.Printer{},
		APIKeys:             map[string]ClientKey{},
	}
}

// Load reads config from path (or $CPS_CONFIG, or ./config.json) and fills defaults.
func Load(path string) (Config, error) {
	cfg := defaults()
	if path == "" {
		path = os.Getenv("CPS_CONFIG")
	}
	if path == "" {
		path = "config.json"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

func (c Config) Validate() error {
	switch c.StoreDriver {
	case "memory", "postgres":
	default:
		return fmt.Errorf("store_driver must be memory or postgres, got %q", c.StoreDriver)
	}
	if c.StoreDriver == "postgres" && c.PostgresDSN == "" {
		return fmt.Errorf("postgres_dsn is required when store_driver=postgres")
	}
	if c.MaxAttempts < 1 {
		return fmt.Errorf("max_attempts must be >= 1")
	}
	if c.QueueSizePerPrinter < 1 {
		return fmt.Errorf("queue_size_per_printer must be >= 1")
	}
	if c.MaxPayloadBytes < 64 {
		return fmt.Errorf("max_payload_bytes must be >= 64")
	}
	if c.Discovery.Interval.D() > 0 && c.Discovery.Interval.D() < 10*time.Second {
		return fmt.Errorf("discovery.interval must be >= 10s, got %s", c.Discovery.Interval.D())
	}
	switch c.Render.Backend {
	case "", "auto", "zebrash", "labelize":
	default:
		return fmt.Errorf("render.backend must be auto|zebrash|labelize, got %q", c.Render.Backend)
	}
	if c.Render.Backend == "labelize" && c.Render.LabelizeURL == "" {
		return fmt.Errorf("render.labelize_url is required when render.backend=labelize")
	}
	if c.UI.Path != "" && !strings.HasPrefix(c.UI.Path, "/") {
		return fmt.Errorf("ui.path must start with /, got %q", c.UI.Path)
	}
	if len(c.APIKeys) == 0 {
		return fmt.Errorf("no api_keys configured: the API would be unauthenticated")
	}
	for secret, k := range c.APIKeys {
		if len(secret) < 16 {
			return fmt.Errorf("api key for client %q is too short (min 16 chars)", k.ClientID)
		}
		if k.ClientID == "" {
			return fmt.Errorf("api key with empty client_id")
		}
		if k.Role != "client" && k.Role != "admin" {
			return fmt.Errorf("api key for %q has role %q, want client|admin", k.ClientID, k.Role)
		}
	}
	seen := map[string]bool{}
	for i := range c.Printers {
		p := &c.Printers[i]
		if p.PrinterID == "" {
			return fmt.Errorf("printers[%d]: printer_id is required", i)
		}
		if strings.TrimSpace(p.Name) == "" {
			p.Name = p.PrinterID
		}
		if p.Port == 0 {
			p.Port = 9100
		}
		if p.Driver == "" {
			return fmt.Errorf("printers[%d] (%s): driver is required (zebra9100|mock)", i, p.PrinterID)
		}
		if p.Driver != "mock" && p.Host == "" {
			return fmt.Errorf("printers[%d] (%s): host is required", i, p.PrinterID)
		}
		if seen[p.PrinterID] {
			return fmt.Errorf("duplicate printer_id %q", p.PrinterID)
		}
		seen[p.PrinterID] = true
	}
	if c.StatusStaleAfter.D() <= c.StatusPollInterval.D() {
		return fmt.Errorf("status_stale_after must be greater than status_poll_interval")
	}
	return nil
}
