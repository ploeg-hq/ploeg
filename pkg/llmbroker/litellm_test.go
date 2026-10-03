package llmbroker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/litellm"
)

// fakeAdmin is a schema-strict fake of the LiteLLM admin API, mirroring the
// behaviors the client tests pin (batch delete body, alias capture).
type fakeAdmin struct {
	keys     map[string]string // hashed token -> alias
	minted   []litellm.MintRequest
	deleted  []string
	blocked  map[string]bool
	mintFail bool
	spend    float64 // what /key/info reports for any live key
	logs     map[string][]float64
	entries  map[string][]map[string]any
}

func newFakeAdmin() *fakeAdmin {
	return &fakeAdmin{keys: map[string]string{}, blocked: map[string]bool{}}
}

func (f *fakeAdmin) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/key/block":
			var req struct {
				Key string `json:"key"`
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			key := req.Key
			if _, ok := f.keys[key]; !ok {
				key = fixtureKeyID(key)
			}
			if _, ok := f.keys[key]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			f.blocked[key] = true
			_ = json.NewEncoder(w).Encode(map[string]bool{"blocked": true})
		case "/key/generate":
			if f.mintFail {
				http.Error(w, `{"error":"test error"}`, http.StatusInternalServerError)
				return
			}
			var req litellm.MintRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			for _, m := range req.Models {
				if strings.Contains(m, "/") {
					http.Error(w, "model scope contains '/'", http.StatusBadRequest)
					return
				}
			}
			if req.KeyAlias == "" {
				http.Error(w, "key_alias required", http.StatusBadRequest)
				return
			}
			f.minted = append(f.minted, req)
			key := "sk-" + req.KeyAlias
			f.keys[fixtureKeyID(key)] = req.KeyAlias
			_ = json.NewEncoder(w).Encode(map[string]string{"key": key})
		case "/key/delete":
			var req struct {
				Keys []string `json:"keys"`
				Key  string   `json:"key,omitempty"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if req.Key != "" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			for _, k := range req.Keys {
				f.deleted = append(f.deleted, k)
				delete(f.keys, k)
				delete(f.keys, fixtureKeyID(k)) // plaintext revoke path
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "deleted"})
		case "/key/info":
			key := r.URL.Query().Get("key")
			_, hashed := f.keys[key]
			if _, ok := f.keys[fixtureKeyID(key)]; !ok && !hashed {
				http.Error(w, `{"error":"key not found"}`, http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"info": map[string]any{"spend": f.spend, "max_budget": 2},
			})
		case "/key/list":
			keys := make([]litellm.KeyInfo, 0, len(f.keys))
			for tok, alias := range f.keys {
				keys = append(keys, litellm.KeyInfo{Token: tok, KeyAlias: alias, Blocked: f.blocked[tok]})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"keys": keys, "total_count": len(keys), "total_pages": 1, "current_page": 1,
			})
		case "/spend/logs":
			token := r.URL.Query().Get("api_key")
			entries := []map[string]any{}
			for _, spend := range f.logs[token] {
				entries = append(entries, map[string]any{"api_key": token, "spend": spend})
			}
			for _, entry := range f.entries[token] {
				row := map[string]any{"api_key": token}
				for k, v := range entry {
					row[k] = v
				}
				entries = append(entries, row)
			}
			_ = json.NewEncoder(w).Encode(entries)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeAdmin) broker(t *testing.T) *LiteLLM {
	return NewLiteLLM(litellm.NewClient(f.server(t).URL, "test-master-key"))
}

const runToken = "1cd43e1dfd6c000000000000000000000000000000000000"

func TestMint_AliasFormatIsLoadBearing(t *testing.T) {
	f := newFakeAdmin()
	cred, err := f.broker(t).Mint(context.Background(), MintRequest{
		RunToken: runToken, BudgetUSD: 2, Models: []string{"deepseek-chat"}, TTL: 4 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cred.Alias != "ploeg-1cd43e1dfd6c" {
		t.Errorf("alias = %q, want ploeg-1cd43e1dfd6c (Grafana joins on this)", cred.Alias)
	}
	if len(f.minted) != 1 || f.minted[0].KeyAlias != "ploeg-1cd43e1dfd6c" {
		t.Errorf("minted with alias %+v", f.minted)
	}
	// The TTL must land in `duration`, which is a field LiteLLM actually has.
	// The previous assertion here checked MaxHoursTTL == 4 and passed for
	// months while every key was minted WITHOUT AN EXPIRY, because
	// `max_hours_ttl` is not part of GenerateKeyRequest and pydantic drops
	// unknown keys silently. Asserting a value reaches the wire says nothing
	// about whether the receiver has a field to put it in.
	if f.minted[0].Duration != "14400s" {
		t.Errorf("duration = %q, want 14400s (4h); a key with no expiry has no backstop",
			f.minted[0].Duration)
	}
	if cred.APIKey == "" {
		t.Error("mint returned no key")
	}
}

func TestMint_RefusesAnUncappedKey(t *testing.T) {
	f := newFakeAdmin()
	// max_budget is omitempty, so budget 0 does not mint a zero-spend key —
	// it mints an unlimited one. Fail closed instead.
	for _, budget := range []float64{0, -1} {
		if _, err := f.broker(t).Mint(context.Background(), MintRequest{
			RunToken: runToken, BudgetUSD: budget, TTL: time.Hour,
		}); err == nil {
			t.Errorf("budget %v: expected a refusal, got a minted key", budget)
		}
	}
	if len(f.minted) != 0 {
		t.Errorf("nothing should have been minted, got %+v", f.minted)
	}
}

func TestSpend_ReadsTheGatewaysFigure(t *testing.T) {
	f := newFakeAdmin()
	b := f.broker(t)
	cred, err := b.Mint(context.Background(), MintRequest{
		RunToken: runToken, BudgetUSD: 2, TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.spend = 0.0137
	got, err := b.Spend(context.Background(), cred)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0.0137 {
		t.Errorf("spend = %v, want 0.0137", got)
	}
}

func TestSpend_NoCredentialIsAnError(t *testing.T) {
	f := newFakeAdmin()
	if _, err := f.broker(t).Spend(context.Background(), Credential{}); err == nil {
		t.Fatal("expected an error metering an empty credential")
	}
}

func TestMint_ShortTokenRejected(t *testing.T) {
	f := newFakeAdmin()
	if _, err := f.broker(t).Mint(context.Background(), MintRequest{RunToken: "short"}); err == nil {
		t.Fatal("expected error for a short run token")
	}
}

func TestRevoke_BlocksKeyAndPreservesUsage(t *testing.T) {
	f := newFakeAdmin()
	b := f.broker(t)
	cred, err := b.Mint(context.Background(), MintRequest{RunToken: runToken, BudgetUSD: 1, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Revoke(context.Background(), cred); err != nil {
		t.Fatal(err)
	}
	if len(f.deleted) != 0 || !f.blocked[fixtureKeyID(cred.APIKey)] {
		t.Fatal("revocation did not retain a blocked accounting identity")
	}
	f.spend = 0.7
	if got, err := b.Spend(context.Background(), cred); err != nil || got != 0.7 {
		t.Fatalf("late spend=%v, err=%v", got, err)
	}
}

func TestSettledSpend_ReadsSpendLogsThatOutliveTheKey(t *testing.T) {
	f := newFakeAdmin()
	b := f.broker(t)
	ctx := context.Background()
	cred, err := b.Mint(ctx, MintRequest{RunToken: runToken, BudgetUSD: 1, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	keyID := fixtureKeyID(cred.APIKey)
	f.logs = map[string][]float64{keyID: {0.1, 0.25}}
	f.spend = 0
	got, err := b.SettledSpendForRun(ctx, runToken, []string{keyID})
	if err != nil || math.Abs(got.USD-0.35) > 1e-9 || got.Keys != 1 || got.Entries != 2 {
		t.Fatalf("settled=%+v err=%v; the key's lagging running total must not be the source", got, err)
	}
	delete(f.keys, keyID)
	got, err = b.SettledSpendForRun(ctx, runToken, []string{keyID})
	if err != nil || math.Abs(got.USD-0.35) > 1e-9 {
		t.Fatalf("deleted key lost its spend: %+v %v", got, err)
	}
	if _, err := b.SettledSpendForRun(ctx, runToken, []string{""}); err == nil {
		t.Fatal("no accounting identity settled as zero")
	}
}

func TestSettledSpend_AggregatesUsageAcrossKeys(t *testing.T) {
	f := newFakeAdmin()
	b := f.broker(t)
	ctx := context.Background()
	cred, err := b.Mint(ctx, MintRequest{RunToken: runToken, BudgetUSD: 1, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	keyID := fixtureKeyID(cred.APIKey)
	f.entries = map[string][]map[string]any{
		keyID: {
			{"spend": 0.1, "model": "deepseek-chat", "prompt_tokens": 1000, "completion_tokens": 200},
			{"spend": 0.2, "model": "claude-sonnet", "prompt_tokens": 500, "completion_tokens": 50},
		},
		"older-key-id": {{"spend": 0.05, "model": "deepseek-chat", "prompt_tokens": 100, "completion_tokens": 10}},
	}
	got, err := b.SettledSpendForRun(ctx, runToken, []string{"older-key-id"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Keys != 2 || got.Entries != 3 || math.Abs(got.USD-0.35) > 1e-9 || got.InputTokens != 1600 || got.OutputTokens != 260 {
		t.Fatalf("settled=%+v", got)
	}
	if strings.Join(got.Models, ",") != "claude-sonnet,deepseek-chat" {
		t.Fatalf("models=%v", got.Models)
	}
	if len(got.ByModel) != 2 {
		t.Fatalf("byModel=%+v", got.ByModel)
	}
	sonnet, deepseek := got.ByModel[0], got.ByModel[1]
	if sonnet.Model != "claude-sonnet" || math.Abs(sonnet.USD-0.2) > 1e-9 || sonnet.InputTokens != 500 || sonnet.OutputTokens != 50 {
		t.Fatalf("claude-sonnet share=%+v", sonnet)
	}
	if deepseek.Model != "deepseek-chat" || math.Abs(deepseek.USD-0.15) > 1e-9 || deepseek.InputTokens != 1100 || deepseek.OutputTokens != 210 {
		t.Fatalf("deepseek-chat share summed across keys=%+v", deepseek)
	}
}

func TestRevoke_EmptyCredentialIsNoop(t *testing.T) {
	f := newFakeAdmin()
	if err := f.broker(t).Revoke(context.Background(), Credential{}); err != nil {
		t.Fatal(err)
	}
	if len(f.deleted) != 0 {
		t.Errorf("noop revoke deleted %v", f.deleted)
	}
}

func TestRevokeForRun_ResolvesAliasToHashedTokens(t *testing.T) {
	f := newFakeAdmin()
	b := f.broker(t)
	if _, err := b.Mint(context.Background(), MintRequest{RunToken: runToken, BudgetUSD: 1, TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if err := b.RevokeForRun(context.Background(), runToken); err != nil {
		t.Fatal(err)
	}
	if len(f.keys) != 1 || len(f.blocked) != 1 || len(f.deleted) != 0 {
		t.Fatal("run cleanup must block and retain accounting identity")
	}
}

func TestSweepOrphans_RevokesOnlyDeadRuns(t *testing.T) {
	f := newFakeAdmin()
	b := f.broker(t)
	aliveToken := "aaaaaaaaaaaa0000000000000000000000000000000000ial"
	for _, tok := range []string{runToken, aliveToken} {
		if _, err := b.Mint(context.Background(), MintRequest{RunToken: tok, BudgetUSD: 1, TTL: time.Hour}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := b.SweepOrphans(context.Background(), []string{aliveToken})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("swept %d keys, want 1", n)
	}
	for key, alias := range f.keys {
		if f.blocked[key] == (alias == "ploeg-aaaaaaaaaaaa") {
			t.Fatal("wrong key blocked")
		}
	}
	if len(f.keys) != 2 || len(f.deleted) != 0 {
		t.Fatal("orphan cleanup deleted accounting identity")
	}
	if n, err := b.SweepOrphans(context.Background(), []string{aliveToken}); err != nil || n != 0 {
		t.Fatalf("repeated cleanup=%d, %v", n, err)
	}
}

func TestSweepOrphans_NothingToDo(t *testing.T) {
	f := newFakeAdmin()
	n, err := f.broker(t).SweepOrphans(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("swept %d, want 0", n)
	}
}

func TestStatic_MintEchoesKeyAndKeepsTraceFormat(t *testing.T) {
	cred, err := Static{Key: "sk-byo"}.Mint(context.Background(), MintRequest{RunToken: runToken})
	if err != nil {
		t.Fatal(err)
	}
	if cred.APIKey != "sk-byo" || cred.Alias != "ploeg-1cd43e1dfd6c" {
		t.Errorf("cred = %+v", cred)
	}
	if err := (Static{}).Revoke(context.Background(), cred); err != nil {
		t.Fatal(err)
	}
}

func fixtureKeyID(key string) string {
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:])
}
