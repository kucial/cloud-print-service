package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/kucial/cloud-print-service/internal/config"
	"github.com/kucial/cloud-print-service/internal/token"
)

// Token administration for business-system integrations.
//
// Lifecycle: an operator (or CI) issues a token, hands the secret to the ERP/WMS team
// once over a secure channel, and later rotates or revokes it. The secret is never
// readable again — only a prefix is stored.

func (s *Server) issueToken(w http.ResponseWriter, r *http.Request) {
	if s.tokens == nil {
		writeErr(w, http.StatusNotImplemented, "not_configured", "token store is not enabled")
		return
	}
	var body struct {
		ClientID string `json:"client_id"`
		Role     string `json:"role"`
		TTL      string `json:"ttl"`  // Go duration, e.g. "720h"; empty = default
		Note     string `json:"note"` // e.g. "wms pilot, ticket OPS-1234"
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	req := token.IssueRequest{ClientID: body.ClientID, Role: body.Role, Note: body.Note}
	if body.TTL != "" {
		d, err := config.ParseDuration(body.TTL)
		if err != nil {
			writeErr(w, http.StatusUnprocessableEntity, "invalid_request",
				"ttl must look like 720h or 90d")
			return
		}
		req.TTL = d
	}
	t, secret, err := s.tokens.Issue(r.Context(), req)
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	s.log.Info("token issued", "event", "token.issued", "token_id", t.ID,
		"client_id", t.ClientID, "role", t.Role, "issued_by", ClientFrom(r.Context()).ID,
		"prefix", token.Display(t), "expires_at", t.ExpiresAt)
	s.met.Inc("cps_tokens_issued_total", "client_id", t.ClientID, "role", t.Role)
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":        t.ID,
		"client_id": t.ClientID,
		"role":      t.Role,
		"note":      t.Note,
		// The only time the secret is ever returned.
		"token":      secret,
		"prefix":     token.Display(t),
		"created_at": t.CreatedAt,
		"expires_at": t.ExpiresAt,
		"usage":      "Authorization: Bearer " + secret,
		"warning":    "store this secret now; it cannot be retrieved again",
	})
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	if s.tokens == nil {
		writeErr(w, http.StatusNotImplemented, "not_configured", "token store is not enabled")
		return
	}
	items, err := s.tokens.List(r.Context(), r.URL.Query().Get("client_id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	now := time.Now().UTC()
	active := 0
	for i := range items {
		if items[i].Active(now) {
			active++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "count": len(items), "active": active,
	})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	if s.tokens == nil {
		writeErr(w, http.StatusNotImplemented, "not_configured", "token store is not enabled")
		return
	}
	t, err := s.tokens.Revoke(r.Context(), r.PathValue("token_id"))
	if err != nil {
		writeTokenErr(w, err)
		return
	}
	s.log.Info("token revoked", "event", "token.revoked", "token_id", t.ID,
		"client_id", t.ClientID, "revoked_by", ClientFrom(r.Context()).ID)
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) rotateToken(w http.ResponseWriter, r *http.Request) {
	if s.tokens == nil {
		writeErr(w, http.StatusNotImplemented, "not_configured", "token store is not enabled")
		return
	}
	oldID := r.PathValue("token_id")
	t, secret, err := s.tokens.Rotate(r.Context(), oldID)
	if err != nil {
		writeTokenErr(w, err)
		return
	}
	s.log.Info("token rotated", "event", "token.rotated", "old_token_id", oldID,
		"token_id", t.ID, "client_id", t.ClientID, "rotated_by", ClientFrom(r.Context()).ID)
	s.met.Inc("cps_tokens_issued_total", "client_id", t.ClientID, "role", t.Role)
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": t.ID, "client_id": t.ClientID, "role": t.Role,
		"token": secret, "prefix": token.Display(t),
		"created_at": t.CreatedAt, "expires_at": t.ExpiresAt,
		"rotated_from": oldID,
		"usage":        "Authorization: Bearer " + secret,
	})
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request) {
	if s.tokens == nil {
		writeErr(w, http.StatusNotImplemented, "not_configured", "token store is not enabled")
		return
	}
	id := r.PathValue("token_id")
	if err := s.tokens.Delete(r.Context(), id); err != nil {
		writeTokenErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
}

func writeTokenErr(w http.ResponseWriter, err error) {
	if errors.Is(err, token.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "token_not_found", err.Error())
		return
	}
	writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
}
