package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/forgebroker"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

// fakeTokenForge is a Forgejo tokens API that remembers what it minted, so a test
// can ask which tokens are still alive after ploegd is done with them.
type fakeTokenForge struct {
	mu           sync.Mutex
	next         int64
	tokens       map[string]int64
	refuseRevoke bool
	beforeMint   func()
	srv          *httptest.Server
}

func newFakeTokenForge(t *testing.T) *fakeTokenForge {
	t.Helper()
	f := &fakeTokenForge{tokens: map[string]int64{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTokenForge) broker() *forgebroker.Forgejo {
	return &forgebroker.Forgejo{BaseURL: f.srv.URL, Bot: "agent-builder", Password: "pw"}
}

func (f *fakeTokenForge) serve(w http.ResponseWriter, r *http.Request) {
	const base = "/api/v1/users/agent-builder/tokens"
	switch {
	case r.Method == http.MethodPost && r.URL.Path == base:
		if f.beforeMint != nil {
			f.beforeMint()
		}
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.next++
		id := f.next
		f.tokens[body.Name] = id
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "name": body.Name, "sha1": "secret-" + strconv.FormatInt(id, 10)})
	case r.Method == http.MethodGet && r.URL.Path == base:
		f.mu.Lock()
		list := []map[string]any{}
		if r.URL.Query().Get("page") == "1" {
			for name, id := range f.tokens {
				list = append(list, map[string]any{"id": id, "name": name})
			}
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(list)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, base+"/"):
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.refuseRevoke {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		target := strings.TrimPrefix(r.URL.Path, base+"/")
		for name, id := range f.tokens {
			if name == target || strconv.FormatInt(id, 10) == target {
				delete(f.tokens, name)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeTokenForge) live() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.tokens)
}

func (f *fakeTokenForge) setRefuseRevoke(v bool) {
	f.mu.Lock()
	f.refuseRevoke = v
	f.mu.Unlock()
}

func workItemIDByExternal(t *testing.T, externalID string) int64 {
	t.Helper()
	var id int64
	if err := testPool.QueryRow(context.Background(),
		`SELECT id FROM work_items WHERE external_id = $1`, externalID).Scan(&id); err != nil {
		t.Fatalf("find work item %s: %v", externalID, err)
	}
	return id
}

func runState(t *testing.T) (state, outcome, reason string) {
	t.Helper()
	if err := testPool.QueryRow(context.Background(),
		`SELECT state, COALESCE(outcome, ''), COALESCE(failure_reason, '') FROM agent_runs WHERE writes`).
		Scan(&state, &outcome, &reason); err != nil {
		t.Fatalf("read the writing Run: %v", err)
	}
	return state, outcome, reason
}

// A mint that returns after the Work Item was withdrawn must not hand the
// worker a token nothing will ever revoke: the withdrawal already released the
// Lease with no token id on it.
func TestClaim_AMintThatOutlivesTheLeaseIsRevokedAndRefused(t *testing.T) {
	reset(t)
	targetedWriterShift(t, "993")
	forge := newFakeTokenForge(t)
	forge.beforeMint = func() {
		id := workItemIDByExternal(t, "993")
		if _, err := testStore.WithdrawWorkItem(context.Background(), id, nil, "test", store.CloseReasonWithdrawnByOperator); err != nil {
			t.Errorf("withdraw during the mint: %v", err)
		}
	}

	code, resp := postClaim(t, forgeClaimServer(forge.broker()), `{"team":"bronze","role":"builder"}`)
	if code != http.StatusNoContent || resp.ForgeToken != "" {
		t.Fatalf("claim = %d with token %q, want 204 and no token", code, resp.ForgeToken)
	}
	if n := forge.live(); n != 0 {
		t.Errorf("%d push credential(s) outlived a withdrawn Lease", n)
	}
}

// A database failure while recording the token is not a reason to hand it out
// unrecorded: the claim fails, the token is revoked and the Run is released.
func TestClaim_AnUnrecordableTokenIsRevokedAndTheRunReleased(t *testing.T) {
	reset(t)
	targetedWriterShift(t, "994")
	forge := newFakeTokenForge(t)
	breakForgeTokenColumn(t)

	code, resp := postClaim(t, forgeClaimServer(forge.broker()), `{"team":"bronze","role":"builder"}`)
	if code != http.StatusNoContent || resp.ForgeToken != "" {
		t.Fatalf("claim = %d with token %q, want 204 and no token", code, resp.ForgeToken)
	}
	if n := forge.live(); n != 0 {
		t.Errorf("%d unrecorded push credential(s) left alive", n)
	}
	if state, outcome, reason := runState(t); state != "finished" || outcome != "failed" || reason != "infra_node" {
		t.Errorf("Run = %s/%s/%s, want finished/failed/infra_node", state, outcome, reason)
	}
}

// When the revoke after a failed record also fails, the token survives only
// at the forge. A ploegd started afterwards finds it there by its name and
// revokes it, because its Run no longer holds a Lease.
func TestForgeSweep_AfterARestartRevokesATokenWhoseRevokeFailed(t *testing.T) {
	reset(t)
	targetedWriterShift(t, "995")
	forge := newFakeTokenForge(t)
	breakForgeTokenColumn(t)
	forge.setRefuseRevoke(true)

	if code, _ := postClaim(t, forgeClaimServer(forge.broker()), `{"team":"bronze","role":"builder"}`); code != http.StatusNoContent {
		t.Fatalf("claim = %d, want 204", code)
	}
	if forge.live() != 1 {
		t.Fatalf("the refused revoke should have left one token at the forge, got %d", forge.live())
	}

	forge.setRefuseRevoke(false)
	restarted := forge.broker()
	n, err := restarted.SweepOrphans(context.Background(), testStore.LeasedRunTokens)
	if err != nil || n != 1 || forge.live() != 0 {
		t.Errorf("sweep after restart = %d, %v with %d token(s) left; want 1 revoked and none left", n, err, forge.live())
	}
}

// The sweep must leave a running writer's push rights alone, recorded or not.
func TestForgeSweep_SparesARunningWritersToken(t *testing.T) {
	reset(t)
	targetedWriterShift(t, "996")
	forge := newFakeTokenForge(t)

	code, resp := postClaim(t, forgeClaimServer(forge.broker()), `{"team":"bronze","role":"builder"}`)
	if code != http.StatusOK || resp.ForgeToken == "" {
		t.Fatalf("claim = %d with token %q, want 200 and a minted token", code, resp.ForgeToken)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE leases SET forge_token_id = ''`); err != nil {
		t.Fatal(err)
	}
	n, err := forge.broker().SweepOrphans(context.Background(), testStore.LeasedRunTokens)
	if err != nil || n != 0 || forge.live() != 1 {
		t.Errorf("sweep = %d, %v with %d token(s) left; want the running writer's token kept", n, err, forge.live())
	}
}

func breakForgeTokenColumn(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := testPool.Exec(ctx,
		`ALTER TABLE leases ADD CONSTRAINT test_forge_token_unrecordable CHECK (forge_token_id = '') NOT VALID`); err != nil {
		t.Fatalf("break the forge token column: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `ALTER TABLE leases DROP CONSTRAINT IF EXISTS test_forge_token_unrecordable`)
	})
}
