package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/ploeg-hq/ploeg/pkg/claudesession"
	"github.com/ploeg-hq/ploeg/pkg/forgebroker"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

type fakeSessionVerifier struct {
	claims claudesession.Claims
	err    error
}

func (f fakeSessionVerifier) Verify(context.Context, string) (claudesession.Claims, error) {
	return f.claims, f.err
}

type countingBroker struct {
	mu      sync.Mutex
	minted  int
	revoked []string
}

func (b *countingBroker) Mint(_ context.Context, req forgebroker.MintRequest) (forgebroker.Credential, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.minted++
	id := fmt.Sprintf("tok-%d", b.minted)
	return forgebroker.Credential{ID: id, Token: "secret-" + id + "-" + req.Owner + "/" + req.Repo}, nil
}

func (b *countingBroker) Revoke(_ context.Context, cred forgebroker.Credential) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.revoked = append(b.revoked, cred.ID)
	return nil
}

const testEnvironment = "ccpool_env1"

func sessionClaims(session string) claudesession.Claims {
	return claudesession.Claims{EnvironmentID: testEnvironment, SessionID: session, Creator: "user:u1", ExpiresAt: time.Now().Add(time.Hour)}
}

func claudeServer(creds forgebroker.Broker, verifier SessionVerifier) http.Handler {
	return (&Server{
		Store:          testStore,
		LeaseTTL:       time.Minute,
		Log:            slog.New(slog.DiscardHandler),
		ForgeCreds:     creds,
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true},
		ClaudeSessions: &ClaudeSessions{Verifier: verifier, GitUser: "agent-builder", ForgeURL: "https://forge.example/"},
	}).Handler()
}

func claimWriter(t *testing.T, h http.Handler, externalID string) string {
	t.Helper()
	targetedWriterShift(t, externalID)
	code, resp := postClaim(t, h, `{"team":"bronze","role":"builder"}`)
	if code != http.StatusOK {
		t.Fatalf("claim returned %d, want 200", code)
	}
	return resp.RunToken
}

func recordSession(t *testing.T, h http.Handler, runToken, environment, session string) int {
	t.Helper()
	body, _ := json.Marshal(claudeSessionRequest{EnvironmentID: environment, SessionID: session})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+runToken+"/claude-session", bytes.NewReader(body)))
	return rec.Code
}

func exchange(t *testing.T, h http.Handler) (int, claudeForgeCredentialResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/claude-sessions/forge-credential", nil)
	req.Header.Set("Authorization", "Bearer sk-ant-cc-anything")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp claudeForgeCredentialResponse
	if rec.Code == http.StatusOK {
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", got)
		}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return rec.Code, resp
}

func leaseTokenID(t *testing.T, runToken string) string {
	t.Helper()
	var id string
	if err := testPool.QueryRow(context.Background(),
		`SELECT forge_token_id FROM leases WHERE run_token = $1`, runToken).Scan(&id); err != nil {
		t.Fatalf("lease: %v", err)
	}
	return id
}

func TestClaudeForgeCredential_MintsForTheSessionsRunAndRevokesTheOneItReplaces(t *testing.T) {
	reset(t)
	broker := &countingBroker{}
	h := claudeServer(broker, fakeSessionVerifier{claims: sessionClaims("abc")})
	run := claimWriter(t, h, "1201")
	if got := leaseTokenID(t, run); got != "tok-1" {
		t.Fatalf("claim recorded %q, want tok-1", got)
	}
	if code := recordSession(t, h, run, testEnvironment, "session_abc"); code != http.StatusNoContent {
		t.Fatalf("record session returned %d, want 204", code)
	}

	code, resp := exchange(t, h)
	if code != http.StatusOK {
		t.Fatalf("exchange returned %d, want 200", code)
	}
	if resp.Token != "secret-tok-2-webgrip/fixture" || resp.Username != "agent-builder" || resp.Branch != "agent/vik-1201" || resp.Remote != "https://forge.example/webgrip/fixture.git" {
		t.Errorf("response = %+v", resp)
	}
	if got := leaseTokenID(t, run); got != "tok-2" {
		t.Errorf("lease records %q, want tok-2", got)
	}

	if code, _ := exchange(t, h); code != http.StatusOK {
		t.Fatalf("second exchange returned %d, want 200", code)
	}
	if !slices.Equal(broker.revoked, []string{"tok-1", "tok-2"}) {
		t.Errorf("revoked %v, want [tok-1 tok-2]: one live credential per Run", broker.revoked)
	}
}

func TestClaudeForgeCredential_Refuses(t *testing.T) {
	cases := []struct {
		name     string
		verifier SessionVerifier
		creds    func() forgebroker.Broker
		setup    func(t *testing.T, h http.Handler)
		want     int
	}{
		{
			name:     "an invalid session token",
			verifier: fakeSessionVerifier{err: fmt.Errorf("%w: expired", claudesession.ErrInvalid)},
			want:     http.StatusUnauthorized,
		},
		{
			name:     "keys that cannot be fetched",
			verifier: fakeSessionVerifier{err: fmt.Errorf("claude session keys: dial tcp: refused")},
			want:     http.StatusServiceUnavailable,
		},
		{
			name:     "a session no Run recorded",
			verifier: fakeSessionVerifier{claims: sessionClaims("never-recorded")},
			setup:    func(t *testing.T, h http.Handler) { claimWriter(t, h, "1210") },
			want:     http.StatusNotFound,
		},
		{
			name:     "a Run whose Lease lapsed",
			verifier: fakeSessionVerifier{claims: sessionClaims("lapsed")},
			setup: func(t *testing.T, h http.Handler) {
				run := claimWriter(t, h, "1211")
				recordSession(t, h, run, testEnvironment, "cse_lapsed")
				if _, err := testPool.Exec(context.Background(),
					`UPDATE leases SET expires_at = now() - interval '1 second' WHERE run_token = $1`, run); err != nil {
					t.Fatal(err)
				}
			},
			want: http.StatusNotFound,
		},
		{
			name:     "a reading Run",
			verifier: fakeSessionVerifier{claims: sessionClaims("reader")},
			setup: func(t *testing.T, h http.Handler) {
				shiftFixture(t, "1212", 10, []store.Role{{Name: "reviewer", Cap: 1}})
				code, resp := postClaim(t, h, `{"team":"bronze","role":"reviewer"}`)
				if code != http.StatusOK {
					t.Fatalf("reviewer claim returned %d", code)
				}
				recordSession(t, h, resp.RunToken, testEnvironment, "cse_reader")
			},
			want: http.StatusForbidden,
		},
		{
			name:     "a deployment with only the shared credential",
			verifier: fakeSessionVerifier{claims: sessionClaims("shared")},
			creds:    func() forgebroker.Broker { return forgebroker.Static{Token: "the-shared-org-wide-pat"} },
			setup: func(t *testing.T, h http.Handler) {
				run := claimWriter(t, h, "1213")
				recordSession(t, h, run, testEnvironment, "cse_shared")
			},
			want: http.StatusConflict,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reset(t)
			var creds forgebroker.Broker = &countingBroker{}
			if tc.creds != nil {
				creds = tc.creds()
			}
			h := claudeServer(creds, tc.verifier)
			if tc.setup != nil {
				tc.setup(t, h)
			}
			code, resp := exchange(t, h)
			if code != tc.want {
				t.Errorf("exchange returned %d, want %d", code, tc.want)
			}
			if resp.Token != "" {
				t.Errorf("a refused exchange carried a token")
			}
		})
	}
}

func TestClaudeSessionRoutes_AreOffWithoutConfiguration(t *testing.T) {
	reset(t)
	h := forgeClaimServer(&countingBroker{})
	run := claimWriter(t, h, "1220")
	if code := recordSession(t, h, run, testEnvironment, "cse_x"); code != http.StatusNotFound {
		t.Errorf("record session returned %d, want 404", code)
	}
	if code, _ := exchange(t, h); code != http.StatusNotFound {
		t.Errorf("exchange returned %d, want 404", code)
	}
}

func TestRecordClaudeSession(t *testing.T) {
	reset(t)
	h := claudeServer(&countingBroker{}, fakeSessionVerifier{})
	run := claimWriter(t, h, "1230")
	other := claimWriter(t, h, "1231")

	steps := []struct {
		name              string
		run, env, session string
		want              int
	}{
		{"binds the session", run, testEnvironment, "session_one", http.StatusNoContent},
		{"is idempotent across id prefixes", run, testEnvironment, "cse_one", http.StatusNoContent},
		{"refuses a second session for the Run", run, testEnvironment, "session_two", http.StatusConflict},
		{"refuses the session for another Run", other, testEnvironment, "session_one", http.StatusConflict},
		{"refuses an environment that is not a ccpool id", other, "env1", "session_three", http.StatusBadRequest},
		{"refuses a session id without a prefix", other, testEnvironment, "three", http.StatusBadRequest},
		{"refuses an unknown Run", "not-a-run", testEnvironment, "session_four", http.StatusNotFound},
	}
	for _, s := range steps {
		if code := recordSession(t, h, s.run, s.env, s.session); code != s.want {
			t.Errorf("%s: returned %d, want %d", s.name, code, s.want)
		}
	}
}

func TestClaudeSessionBodiesMatchTheRunAPIContract(t *testing.T) {
	path, err := filepath.Abs("../../docs/contracts/run-api.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for def, body := range map[string]any{
		"claudeSessionRequest":          claudeSessionRequest{EnvironmentID: testEnvironment, SessionID: "session_abc"},
		"claudeForgeCredentialResponse": claudeForgeCredentialResponse{Username: "agent-builder", Token: "t", Branch: "agent/vik-1", Remote: "https://forge.example/o/r.git"},
	} {
		schema, err := jsonschema.NewCompiler().Compile(path + "#/$defs/" + def)
		if err != nil {
			t.Fatalf("%s: %v", def, err)
		}
		b, _ := json.Marshal(body)
		instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(instance); err != nil {
			t.Errorf("%s violates the contract: %v", def, err)
		}
	}
}
