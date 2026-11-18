package worker

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTheClaimCarriesBothCredentialIsolations(t *testing.T) {
	var claim map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&claim)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cfg := Config{APIURL: srv.URL, Team: "bronze", LLMKeyIsolation: KeyIsolationProxy, ForgeTokenIsolation: ForgeTokenIsolationProxy}
	if err := New(cfg, nil, nil, slog.New(slog.DiscardHandler)).RunContext(context.Background()); err != nil {
		t.Fatalf("RunContext() = %v, want an empty-handed exit", err)
	}
	if claim["llmKeyIsolation"] != "proxy" || claim["forgeTokenIsolation"] != "proxy" {
		t.Errorf("claim = %v, want llmKeyIsolation and forgeTokenIsolation proxy", claim)
	}
}
