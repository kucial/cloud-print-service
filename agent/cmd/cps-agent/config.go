package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kucial/cloud-print-service/pkg/agentapi"
)

// Config is the agent's on-disk state. Created on first enrollment.
type Config struct {
	ServerURL  string   `json:"server_url"`
	AgentID    string   `json:"agent_id"`
	Site       string   `json:"site"`
	Token      string   `json:"token"` // agent-scoped credential; never logged
	BasePort   int      `json:"base_port"`
	Host       string   `json:"host"` // address the server can reach us on
	PollSecs   int      `json:"poll_seconds"`
	Printers   []string `json:"printers,omitempty"` // pin to these printer ids (default: all found)
	LogPayload bool     `json:"-"`
}

func (c *Config) defaults() {
	// Environment overrides exist for the install-script case: the agent must be able to
	// find the server before a config file exists.
	if v := os.Getenv("CPS_AGENT_SERVER"); v != "" && c.ServerURL == "" {
		c.ServerURL = v
	}
	if v := os.Getenv("CPS_AGENT_SITE"); v != "" && c.Site == "" {
		c.Site = v
	}
	if v := os.Getenv("CPS_AGENT_HOST"); v != "" && c.Host == "" {
		c.Host = v
	}
	if c.ServerURL == "" {
		c.ServerURL = "http://localhost:8080"
	}
	c.ServerURL = strings.TrimRight(c.ServerURL, "/")
	if c.AgentID == "" {
		host, _ := os.Hostname()
		c.AgentID = host
	}
	if c.BasePort == 0 {
		c.BasePort = 19100
	}
	if c.PollSecs == 0 {
		c.PollSecs = 15
	}
	if c.Host == "" {
		c.Host = localAddress()
	}
}

func loadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return c, nil
}

func (c *Config) save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// The file holds a credential: owner-only on unix, restricted ACL on Windows.
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	return restrictToOwner(path)
}

// enrollWith exchanges a one-time bootstrap token for a long-lived agent credential.
func (c *Config) enrollWith(ctx context.Context, token string, log *slog.Logger) error {
	c.defaults()
	body, _ := json.Marshal(map[string]string{
		"agent_id": c.AgentID, "site": c.Site, "hostname": hostname(), "os": runtime.GOOS,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.ServerURL+"/api/v1/admin/agents/enroll", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("enrollment request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("enrollment rejected (%d): %s", resp.StatusCode, readAllLimited(resp))
	}
	var out struct {
		Token string `json:"token"`
		TTL   string `json:"ttl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	if out.Token == "" {
		return errors.New("server returned no agent token")
	}
	c.Token = out.Token
	return nil
}

// ---------------------------------------------------------------- transport

// Backend is how the agent reaches a physical printer. Tiered (see plan §6.3):
// cheapest first, so most Windows packing PCs need no USB access at all.
type Backend interface {
	Name() string
	// Open returns a byte sink for one label stream.
	Open(ctx context.Context) (Sink, error)
	Close() error
}

// Sink is one printer's endpoint. Write reports bytes actually accepted; n>0 with an
// error means the caller must NOT retry — the same contract the network driver has.
type Sink interface {
	Write(ctx context.Context, p []byte) (int, error)
	Status(ctx context.Context) (agentapi.Status, error)
	Close() error
}

// Backend implementations live in backends_*.go, chosen by build tag per platform.
func openBackends(ctx context.Context, log *slog.Logger) ([]Backend, error) {
	return probeBackends(ctx, log)
}

func hostname() string { h, _ := os.Hostname(); return h }

// localAddress is the LAN address the server should dial, not 0.0.0.0 and not localhost.
func localAddress() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
			return ipnet.IP.String()
		}
	}
	return "127.0.0.1"
}

func randomID(prefix string) string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return prefix
	}
	return fmt.Sprintf("%s-%x", prefix, b)
}

func readAllLimited(resp *http.Response) string {
	buf := make([]byte, 512)
	n, _ := resp.Body.Read(buf)
	return strings.TrimSpace(string(buf[:n]))
}
