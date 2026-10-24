package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

// fakeExchange is an identity provider's token endpoint and key set: it trades
// a subject token for a token for audience "ploeg" whose azp is the client
// that authenticated, sub the person and act the given actor.
type fakeExchange struct {
	t        *testing.T
	srv      *httptest.Server
	key      *ecdsa.PrivateKey
	mu       sync.Mutex
	subjects []string
	refuse   bool
}

func newFakeExchange(t *testing.T) *fakeExchange {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeExchange{t: t, key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		raw, _ := key.PublicKey.Bytes()
		b := base64.RawURLEncoding.EncodeToString
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "EC", "kid": "p1", "use": "sig", "crv": "P-256", "x": b(raw[1:33]), "y": b(raw[33:])}}})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		client, secret, ok := r.BasicAuth()
		if !ok || client != "ploeg-mcp" || secret != "exchange-secret" || r.ParseForm() != nil {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
			return
		}
		f.mu.Lock()
		f.subjects = append(f.subjects, r.PostForm.Get("subject_token"))
		refuse := f.refuse
		f.mu.Unlock()
		if refuse || r.PostForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:token-exchange" || r.PostForm.Get("audience") != "ploeg" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		now := time.Now()
		tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{"iss": f.issuer(), "sub": "subject-1", "aud": "ploeg", "azp": client,
			"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "act": map[string]any{"sub": "ploeg-mcp-agent"}})
		tok.Header["kid"] = "p1"
		signed, err := tok.SignedString(key)
		if err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": signed, "issued_token_type": "urn:ietf:params:oauth:token-type:access_token", "token_type": "Bearer", "expires_in": 300})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeExchange) issuer() string { return f.srv.URL + "/application/o/ploeg/" }

func exchangingRemote(t *testing.T, f *fakeExchange) acting {
	t.Helper()
	return newActingRemoteWith(t, actingOptions{
		stewardClientIDs: []string{"ploeg-mcp"},
		personTokens: func(consumers []httpapi.OperatorConsumer) *httpapi.PersonTokens {
			tokens, err := httpapi.NewPersonTokens(httpapi.PersonTokenConfig{Issuer: f.issuer(), Audience: "ploeg", JWKSURL: f.srv.URL + "/jwks", SubjectNamespace: personNamespace}, consumers)
			if err != nil {
				t.Fatal(err)
			}
			return tokens
		},
		env: map[string]string{
			"PLOEG_MCP_EXCHANGE_TOKEN_URL":     f.srv.URL + "/token",
			"PLOEG_MCP_EXCHANGE_CLIENT_ID":     "ploeg-mcp",
			"PLOEG_MCP_EXCHANGE_CLIENT_SECRET": "exchange-secret",
			"PLOEG_MCP_EXCHANGE_AUDIENCE":      "ploeg",
		},
	})
}

func TestAnActionCallsPloegWithThePersonsExchangedToken(t *testing.T) {
	f := newFakeExchange(t)
	a := exchangingRemote(t, f)
	yes := accept()
	cs := a.client(t, []string{"ploeg-stewards"}, yes)
	submitted, res := act(t, cs, "ploeg_submit_work", map[string]any{"team": "pewter", "target": "app", "title": "Prove the person", "description": "Exchange first.", "budgetUsd": 1})
	if res.IsError || !submitted.Done {
		t.Fatalf("submit: %s %+v", textOf(res), submitted)
	}
	got, _ := lastAttribution(t, submitted.WorkItemID, "operator.work_item.admitted")
	wantConfirmedBy(t, got, yes.last())
	if got.Proof != httpapi.ProofProven || got.Act != "ploeg-mcp-agent" {
		t.Fatalf("attribution = %+v, want proven with act ploeg-mcp-agent", got)
	}
	a.mu.Lock()
	callerToken := a.accessJWTs[len(a.accessJWTs)-1]
	a.mu.Unlock()
	f.mu.Lock()
	subjects := append([]string(nil), f.subjects...)
	f.mu.Unlock()
	if len(subjects) != 1 || subjects[0] != callerToken {
		t.Fatalf("exchanged %d subject tokens; want exactly the caller's, once, after confirmation", len(subjects))
	}
	if _, res := act(t, cs, "ploeg_overview", map[string]any{}); res.IsError {
		t.Fatalf("a read still works with the group's consumer: %s", textOf(res))
	}
	f.mu.Lock()
	reads := len(f.subjects)
	f.mu.Unlock()
	if reads != 1 {
		t.Fatalf("a read exchanged a token: %d exchanges", reads)
	}
}

func TestARefusedExchangeChangesNothing(t *testing.T) {
	f := newFakeExchange(t)
	f.refuse = true
	a := exchangingRemote(t, f)
	cs := a.client(t, []string{"ploeg-stewards"}, accept())
	_, res := act(t, cs, "ploeg_submit_work", map[string]any{"team": "pewter", "target": "app", "title": "Refused", "description": "No.", "budgetUsd": 1})
	if !res.IsError || !strings.Contains(textOf(res), "nothing changed") || !strings.Contains(textOf(res), "invalid_grant") {
		t.Fatalf("a refused exchange: %s", textOf(res))
	}
	items, _, err := testStore.OperatorItems(t.Context(), store.OperatorFilter{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range items {
		if i.Title == "Refused" {
			t.Fatal("a Work Item was admitted although the exchange was refused")
		}
	}
}

func TestExchangeConfigurationIsAllOrNothing(t *testing.T) {
	for _, env := range []map[string]string{
		{"PLOEG_MCP_EXCHANGE_TOKEN_URL": "https://id.example/token"},
		{"PLOEG_MCP_EXCHANGE_TOKEN_URL": "https://id.example/token", "PLOEG_MCP_EXCHANGE_CLIENT_ID": "a", "PLOEG_MCP_EXCHANGE_CLIENT_SECRET": "b"},
		{"PLOEG_MCP_EXCHANGE_TOKEN_URL": "not a url", "PLOEG_MCP_EXCHANGE_CLIENT_ID": "a", "PLOEG_MCP_EXCHANGE_CLIENT_SECRET": "b", "PLOEG_MCP_EXCHANGE_AUDIENCE": "ploeg"},
	} {
		if _, err := loadExchanger(func(k string) string { return env[k] }); err == nil {
			t.Errorf("%v accepted", env)
		}
	}
	if ex, err := loadExchanger(func(string) string { return "" }); ex != nil || err != nil {
		t.Fatalf("unset exchange: %v %v", ex, err)
	}
}
