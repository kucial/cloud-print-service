package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kucial/cloud-print-service/pkg/agentapi"
	"github.com/kucial/cloud-print-service/server/internal/config"
	"github.com/kucial/cloud-print-service/server/internal/token"
)

// Agent-facing endpoints.
//
// Topology (D2): the server and the packing PCs share a LAN and the server pulls, so an
// agent (a) listens per printer for the server to connect to, and (b) sends these three
// messages. Nothing here can dispatch a job or read business data: an agent token is
// scoped to the agent routes only.

func (s *Server) agentRegister(w http.ResponseWriter, r *http.Request) {
	if s.agents == nil {
		writeErr(w, http.StatusNotImplemented, "not_configured", "agent support is not enabled")
		return
	}
	var p agentapi.RegisterRequest
	if err := decodeJSON(r, &p); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	agent, err := s.agents.Register(p)
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	// Newly registered printers need live drivers; Sync is idempotent for existing ones.
	if err := s.mgr.Sync(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, "sync_failed", err.Error())
		return
	}

	out := map[string]any{
		"agent":    agent,
		"printers": len(p.Printers),
		"note":     "your printers are now dispatchable; the server will connect to agent_addr",
	}

	// First contact with a bootstrap credential: exchange it for a durable one and
	// revoke it, so the token from the install script is spent.
	if cl := ClientFrom(r.Context()); cl.Kind == token.KindAgentBootstrap && s.tokens != nil && cl.TokenID != "" {
		if _, err := s.tokens.Revoke(r.Context(), cl.TokenID); err != nil {
			s.log.Warn("could not revoke bootstrap token", "token_id", cl.TokenID, "err", err)
		} else {
			ttl := s.agentTTL
			if ttl <= 0 {
				ttl = 365 * 24 * time.Hour
			}
			t, secret, err := s.tokens.Issue(r.Context(), token.IssueRequest{
				ClientID: "agent:" + p.AgentID, Role: token.RoleClient, Kind: token.KindAgent,
				AgentID: p.AgentID, TTL: ttl, Note: "agent credential",
			})
			if err != nil {
				writeErr(w, http.StatusInternalServerError, "token_failed", err.Error())
				return
			}
			out["token"] = secret
			out["token_id"] = t.ID
			out["expires"] = t.ExpiresAt
			s.log.Info("agent credential exchanged", "agent_id", p.AgentID, "token_id", t.ID)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) agentHeartbeat(w http.ResponseWriter, r *http.Request) {
	var p agentapi.HeartbeatRequest
	if err := decodeJSON(r, &p); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	if err := s.agents.Heartbeat(p); err != nil {
		writeErr(w, http.StatusNotFound, "agent_not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "at": time.Now().UTC()})
}

func (s *Server) agentPrinterStatus(w http.ResponseWriter, r *http.Request) {
	var st agentapi.Status
	if err := decodeJSON(r, &st); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	if err := s.agents.Status(r.PathValue("agent_id"), r.PathValue("printer_id"), st); err != nil {
		writeErr(w, http.StatusNotFound, "agent_not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	items := s.agents.List()
	secs, _ := strconv.Atoi(r.URL.Query().Get("healthy_within_seconds"))
	ttl := 60 * time.Second
	if secs > 0 {
		ttl = time.Duration(secs) * time.Second
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":   items,
		"count":   len(items),
		"healthy": s.agents.Healthy(ttl),
	})
}

func (s *Server) deleteAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agent_id")
	gone := s.agents.Deregister(id)
	// Anything queued for that agent's printers will never print: fail it loudly instead
	// of leaving it to look like work in progress.
	failed := s.jobs.FailOnAgentLost(r.Context(), gone, "agent "+id+" was deregistered")
	_ = s.mgr.Sync(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"agent":            id,
		"printers_removed": gone,
		"jobs_failed":      failed,
	})
}

// enrollAgent mints a one-time bootstrap token for a new edge agent. A stolen bootstrap
// token is worthless after a single use, which is what makes it safe to paste into an
// install script.
func (s *Server) enrollAgent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AgentID  string `json:"agent_id"`
		Site     string `json:"site"`
		Hostname string `json:"hostname"`
		OS       string `json:"os"`
		TTL      string `json:"ttl"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	if s.tokens == nil {
		writeErr(w, http.StatusNotImplemented, "not_configured", "token service unavailable")
		return
	}
	agentID := strings.TrimSpace(body.AgentID)
	if agentID == "" {
		agentID = strings.TrimSpace(body.Hostname)
	}
	if agentID == "" {
		writeErr(w, http.StatusUnprocessableEntity, "invalid_request",
			"agent_id (or hostname) is required: it names the fleet entry")
		return
	}
	req := token.IssueRequest{
		ClientID: "agent:" + agentID,
		Role:     token.RoleClient,
		Kind:     token.KindAgentBootstrap,
		Note:     strings.TrimSpace(strings.Join([]string{body.Site, body.OS}, " ")),
		AgentID:  agentID,
	}
	if body.TTL != "" {
		d, err := config.ParseDuration(body.TTL)
		if err != nil {
			writeErr(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
			return
		}
		req.TTL = d
	}
	t, secret, err := s.tokens.Issue(r.Context(), req)
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	s.log.Info("agent credential issued", "agent_id", agentID, "site", body.Site, "token_id", t.ID)
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":    secret,
		"agent_id": agentID,
		"prefix":   token.Display(t),
		"expires":  t.ExpiresAt,
		"enroll":   "cps-agent -enroll " + secret,
		"warning":  "one-time token: it stops working as soon as the agent enrolls",
	})
}
