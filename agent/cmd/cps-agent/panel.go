package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

//go:embed panel.html
var panelHTML []byte

const panelCookie = "cps_agent_panel"

// panel is the desktop build's setup and settings page. It is served on the loopback
// interface only and is opened from the tray, which knows the one-time key in the URL;
// the key becomes a cookie, so nothing else on the machine — another user, or a web
// page in the same browser — can read the status or change the config.
type panel struct {
	sup  *supervisor
	log  *slog.Logger
	key  string
	host string // 127.0.0.1:port, the only Host header accepted

	// enroll exchanges a one-time token for the agent credential (Config.enrollWith).
	enroll func(ctx context.Context, cfg *Config, token string) error
}

func newPanel(sup *supervisor, log *slog.Logger) *panel {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return &panel{sup: sup, log: log, key: hex.EncodeToString(b),
		enroll: func(ctx context.Context, cfg *Config, token string) error { return cfg.enrollWith(ctx, token, log) }}
}

// Listen starts serving and returns the URL that opens the panel.
func (p *panel) Listen(ctx context.Context) (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	p.host = ln.Addr().String()
	srv := &http.Server{Handler: p.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	return p.URL(), nil
}

func (p *panel) URL() string { return "http://" + p.host + "/?k=" + p.key }

func (p *panel) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", p.page)
	mux.HandleFunc("GET /api/state", p.state)
	mux.HandleFunc("POST /api/enroll", p.enrollHandler)
	mux.HandleFunc("POST /api/config", p.config)
	return p.guard(mux)
}

// guard admits only requests addressed to this listener by name (a DNS-rebinding page
// would send its own host), carrying the session cookie, and — for changes — sent by
// the panel's own page.
func (p *panel) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != p.host {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if k := r.URL.Query().Get("k"); k != "" && r.URL.Path == "/" {
			if subtle.ConstantTimeCompare([]byte(k), []byte(p.key)) != 1 {
				http.Error(w, "this link has expired: open the panel from the tray icon", http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: panelCookie, Value: p.key, Path: "/",
				HttpOnly: true, SameSite: http.SameSiteStrictMode})
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		c, err := r.Cookie(panelCookie)
		if err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(p.key)) != 1 {
			http.Error(w, "open the panel from the tray icon", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet && r.Header.Get("Origin") != "http://"+p.host {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func (p *panel) page(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'")
	_, _ = w.Write(panelHTML)
}

// panelConfig is the config as the panel shows it: everything but the credential.
type panelConfig struct {
	ServerURL string `json:"server_url"`
	AgentID   string `json:"agent_id"`
	Site      string `json:"site"`
	Host      string `json:"host"`
	BasePort  int    `json:"base_port"`
	PollSecs  int    `json:"poll_seconds"`
}

type panelState struct {
	Version    string          `json:"version"`
	ConfigPath string          `json:"config_path"`
	Enrolled   bool            `json:"enrolled"`
	Config     panelConfig     `json:"config"`
	Status     supervisorState `json:"status"`
	Logs       []string        `json:"logs"`
}

func (p *panel) current() Config {
	cfg, _ := loadConfig(p.sup.path) // none yet: defaults below fill the form
	cfg.defaults()
	return cfg
}

func (p *panel) state(w http.ResponseWriter, r *http.Request) {
	cfg := p.current()
	st := panelState{
		Version: version, ConfigPath: p.sup.path, Enrolled: cfg.Token != "",
		Config: panelConfig{ServerURL: cfg.ServerURL, AgentID: cfg.AgentID, Site: cfg.Site,
			Host: cfg.Host, BasePort: cfg.BasePort, PollSecs: cfg.PollSecs},
		Status: p.sup.State(),
		Logs:   []string{},
	}
	if p.sup.logs != nil {
		st.Logs = p.sup.logs.Lines()
	}
	writeJSON(w, http.StatusOK, st)
}

type configRequest struct {
	panelConfig
	Token string `json:"token"` // one-time enrollment token; required to (re-)enroll
}

func (p *panel) enrollHandler(w http.ResponseWriter, r *http.Request) {
	var req configRequest
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Token) == "" {
		panelError(w, http.StatusBadRequest, "enter the enrollment token from the server")
		return
	}
	p.save(w, r, req)
}

func (p *panel) config(w http.ResponseWriter, r *http.Request) {
	var req configRequest
	if !decode(w, r, &req) {
		return
	}
	p.save(w, r, req)
}

// save validates the form, enrolls when a token was given, and restarts the agent.
func (p *panel) save(w http.ResponseWriter, r *http.Request, req configRequest) {
	cfg := p.current()
	before := cfg
	if msg := applyForm(&cfg, req.panelConfig); msg != "" {
		panelError(w, http.StatusBadRequest, msg)
		return
	}
	token := strings.TrimSpace(req.Token)
	// The credential belongs to one agent id on one server; moving either needs a new one.
	if token == "" && (before.Token == "" || cfg.ServerURL != before.ServerURL || cfg.AgentID != before.AgentID) {
		panelError(w, http.StatusBadRequest, "a new enrollment token is needed to change the server or the agent id")
		return
	}
	if token != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		cfg.Token = ""
		if err := p.enroll(ctx, &cfg, token); err != nil {
			panelError(w, http.StatusBadGateway, err.Error())
			return
		}
		p.log.Info("enrolled from the panel", "agent_id", cfg.AgentID, "server", cfg.ServerURL)
	}
	if err := p.sup.Apply(cfg); err != nil {
		panelError(w, http.StatusInternalServerError, "cannot save the config: "+err.Error())
		return
	}
	p.state(w, r)
}

// applyForm copies the form into cfg and returns a message for the first bad field.
func applyForm(cfg *Config, f panelConfig) string {
	u, err := url.Parse(strings.TrimSpace(f.ServerURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "server address must look like http://print.example.com:8080"
	}
	agentID := strings.TrimSpace(f.AgentID)
	if agentID == "" || strings.ContainsAny(agentID, " /\\") {
		return "agent id must not be empty or contain spaces or slashes"
	}
	if f.BasePort != 0 && (f.BasePort < 1024 || f.BasePort > 65000) {
		return "first local port must be between 1024 and 65000"
	}
	if f.PollSecs != 0 && (f.PollSecs < 5 || f.PollSecs > 3600) {
		return "heartbeat interval must be between 5 and 3600 seconds"
	}
	cfg.ServerURL = strings.TrimRight(u.String(), "/")
	cfg.AgentID = agentID
	cfg.Site = strings.TrimSpace(f.Site)
	if h := strings.TrimSpace(f.Host); h != "" {
		cfg.Host = h
	}
	if f.BasePort != 0 {
		cfg.BasePort = f.BasePort
	}
	if f.PollSecs != 0 {
		cfg.PollSecs = f.PollSecs
	}
	return ""
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		panelError(w, http.StatusBadRequest, fmt.Sprintf("bad request: %v", err))
		return false
	}
	return true
}

func panelError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
