package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kucial/cloud-print-service/internal/observ"
	"github.com/kucial/cloud-print-service/internal/token"
)

// A token's kind must survive the trip through verification into the request context:
// it, not the role, decides which routes a credential may reach.
func TestCredentialKindFlowsIntoRequest(t *testing.T) {
	tokSvc := token.NewService(token.NewMemoryStore(), time.Hour)
	met := observ.NewMetrics()
	auth := NewAuth(map[string]KeySpec{
		"ops-key-0123456789abcdef": {ClientID: "ops", Role: "admin"},
	}, tokSvc, 0, 0, met)

	_, bootstrapSecret, err := tokSvc.Issue(context.Background(), token.IssueRequest{
		ClientID: "agent:pc1", Kind: token.KindAgentBootstrap, AgentID: "pc1",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, wmsSecret, err := tokSvc.Issue(context.Background(), token.IssueRequest{ClientID: "wms"})
	if err != nil {
		t.Fatal(err)
	}

	inner := http.NewServeMux()
	inner.HandleFunc("/api/v1/who", func(w http.ResponseWriter, r *http.Request) {
		c := ClientFrom(r.Context())
		writeJSON(w, 200, map[string]any{"kind": c.Kind, "role": c.Role, "id": c.ID})
	})
	inner.Handle("/api/v1/enroll", RequireEnroll(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	inner.Handle("/api/v1/agent", RequireAgent(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	root := http.NewServeMux()
	root.Handle("/api/v1/", auth.Middleware(inner))
	srv := httptest.NewServer(root)
	defer srv.Close()

	call := func(secret, path string) (int, map[string]any) {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		if secret != "" {
			req.Header.Set("Authorization", "Bearer "+secret)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(b, &out)
		return resp.StatusCode, out
	}

	// identity
	if _, out := call("ops-key-0123456789abcdef", "/api/v1/who"); out["kind"] != token.KindBootstrap {
		t.Errorf("config key kind = %v", out["kind"])
	}
	if _, out := call(bootstrapSecret, "/api/v1/who"); out["kind"] != token.KindAgentBootstrap {
		t.Errorf("bootstrap kind = %v", out["kind"])
	}
	if _, out := call(wmsSecret, "/api/v1/who"); out["kind"] != token.KindBusiness {
		t.Errorf("business kind = %v", out["kind"])
	}

	// gates: who may reach what
	cases := []struct {
		name, secret, path string
		want               int
	}{
		{"admin enrolls an agent", "ops-key-0123456789abcdef", "/api/v1/enroll", http.StatusOK},
		{"bootstrap enrolls itself", bootstrapSecret, "/api/v1/enroll", http.StatusOK},
		{"business cannot enroll", wmsSecret, "/api/v1/enroll", http.StatusForbidden},
		{"bootstrap can register", bootstrapSecret, "/api/v1/agent", http.StatusOK},
		{"business cannot register", wmsSecret, "/api/v1/agent", http.StatusForbidden},
		{"admin is not an agent", "ops-key-0123456789abcdef", "/api/v1/agent", http.StatusForbidden},
		{"no credential", "", "/api/v1/agent", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		if code, _ := call(tc.secret, tc.path); code != tc.want {
			t.Errorf("%s: status %d, want %d", tc.name, code, tc.want)
		}
	}
}
