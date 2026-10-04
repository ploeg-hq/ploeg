package httpapi

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/litellm"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
)

type goneKeyGateway struct {
	mu         sync.Mutex
	keys       map[string]string
	blocked    map[string]bool
	logs       map[string][]float64
	down       bool
	blockFails bool
}

func newGoneKeyGateway(t *testing.T) (*goneKeyGateway, *llmbroker.LiteLLM) {
	t.Helper()
	g := &goneKeyGateway{keys: map[string]string{}, blocked: map[string]bool{}, logs: map[string][]float64{}}
	srv := httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(srv.Close)
	return g, llmbroker.NewLiteLLM(litellm.NewClient(srv.URL, "test-master-key"))
}

func (g *goneKeyGateway) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.down {
		http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.URL.Path {
	case "/key/generate":
		var req litellm.MintRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.KeyAlias == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		key := "sk-" + req.KeyAlias
		g.keys[hashedKey(key)] = req.KeyAlias
		_ = json.NewEncoder(w).Encode(map[string]string{"key": key})
	case "/key/list":
		keys := []litellm.KeyInfo{}
		for token, alias := range g.keys {
			keys = append(keys, litellm.KeyInfo{Token: token, KeyAlias: alias, Blocked: g.blocked[token]})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys, "total_count": len(keys), "total_pages": 1, "current_page": 1})
	case "/key/info":
		key := r.URL.Query().Get("key")
		if strings.HasPrefix(key, "sk-") {
			key = hashedKey(key)
		}
		if _, ok := g.keys[key]; !ok {
			http.Error(w, `{"error":"Key not found in database"}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"info": map[string]any{"spend": 0.01, "max_budget": 1}})
	case "/key/block":
		var req struct {
			Key string `json:"key"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if g.blockFails {
			http.Error(w, "block failed", http.StatusInternalServerError)
			return
		}
		if _, ok := g.keys[req.Key]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		g.blocked[req.Key] = true
		_ = json.NewEncoder(w).Encode(map[string]bool{"blocked": true})
	case "/spend/logs":
		token := r.URL.Query().Get("api_key")
		entries := []map[string]any{}
		for _, spend := range g.logs[token] {
			entries = append(entries, map[string]any{"api_key": token, "spend": spend, "model": "trusted-model"})
		}
		_ = json.NewEncoder(w).Encode(entries)
	default:
		http.NotFound(w, r)
	}
}

func (g *goneKeyGateway) forget(alias string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for token, a := range g.keys {
		if a == alias {
			delete(g.keys, token)
		}
	}
}

func (g *goneKeyGateway) relabel(alias, newAlias string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for token, a := range g.keys {
		if a == alias {
			g.keys[token] = newAlias
		}
	}
}

func (g *goneKeyGateway) set(apply func(*goneKeyGateway)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	apply(g)
}

func unknownAccountWithGatewayKey(t *testing.T) (*goneKeyGateway, *LLMControl, string, int64) {
	t.Helper()
	g, broker := newGoneKeyGateway(t)
	c, token, shiftID := settlementFixture(t, broker, true)
	if err := testStore.MarkLLMUnknown(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	return g, c, token, shiftID
}

func accountState(t *testing.T, token string) string {
	t.Helper()
	a, err := testStore.LLMAccount(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	return a.State
}

func TestBlockMovesAnUnknownAccountWhoseGatewayKeyIsGoneToBlockedAndSettlesItsSpendLogs(t *testing.T) {
	ctx := context.Background()
	g, c, token, shiftID := unknownAccountWithGatewayKey(t)
	a, err := testStore.LLMAccount(ctx, token)
	if err != nil || a.GatewayKeyID == "" {
		t.Fatalf("fixture account=%+v err=%v", a, err)
	}
	g.set(func(g *goneKeyGateway) { g.logs[a.GatewayKeyID] = []float64{0.125, 0.25} })
	g.forget(a.Alias)

	if err := c.Block(ctx, token); err != nil {
		t.Fatalf("a key the gateway reports absent must leave unknown: %v", err)
	}
	if state := accountState(t, token); state != "blocked" {
		t.Fatalf("state=%s want blocked", state)
	}
	var evidence string
	var absent bool
	if err := testPool.QueryRow(ctx, `SELECT detail->>'evidence',(detail->>'gatewayKeyAbsent')::bool FROM audit_log
		WHERE action='llm.blocked' AND detail->>'alias'=$1 ORDER BY id DESC LIMIT 1`, a.Alias).Scan(&evidence, &absent); err != nil {
		t.Fatal(err)
	}
	if !absent || !strings.HasPrefix(evidence, "litellm:key-absent alias="+a.Alias) || !strings.Contains(evidence, "prior-state=unknown") ||
		!strings.Contains(evidence, "recorded-key=true") || strings.Contains(evidence, token) {
		t.Fatalf("absence evidence=%q absent=%v", evidence, absent)
	}
	if l, _ := testStore.Ledger(ctx, shiftID); l.Reserved != 1 || l.Spent != 0 {
		t.Fatalf("a gone key released its hold before settlement: %+v", l)
	}

	if err := c.Settle(ctx, settleCandidate(t, token)); err != nil {
		t.Fatal(err)
	}
	if state := accountState(t, token); state != "reconciled" {
		t.Fatalf("state=%s want reconciled", state)
	}
	if l, _ := testStore.Ledger(ctx, shiftID); l.Reserved != 0 || math.Abs(l.Spent-0.375) > 0.00001 {
		t.Fatalf("a gone key's spend logs were not charged: %+v", l)
	}
}

func TestBlockRevokesAnUnknownAccountWhoseGatewayKeyStillExists(t *testing.T) {
	ctx := context.Background()
	g, c, token, _ := unknownAccountWithGatewayKey(t)
	a, err := testStore.LLMAccount(ctx, token)
	if err != nil {
		t.Fatal(err)
	}

	g.set(func(g *goneKeyGateway) { g.blockFails = true })
	if err := c.Block(ctx, token); err == nil {
		t.Fatal("an unconfirmed revocation reported success")
	}
	if state := accountState(t, token); state != "unknown" {
		t.Fatalf("state=%s: a key the gateway still holds left the alerting state before its block was confirmed", state)
	}

	g.set(func(g *goneKeyGateway) { g.blockFails = false })
	if err := c.Block(ctx, token); err != nil {
		t.Fatal(err)
	}
	if state := accountState(t, token); state != "blocked" {
		t.Fatalf("state=%s want blocked", state)
	}
	if !g.blocked[a.GatewayKeyID] {
		t.Fatal("the live key was not blocked on the gateway")
	}
}

func TestBlockKeepsAnUnknownAccountWhileItsRecordedKeyIsStillOnTheGateway(t *testing.T) {
	ctx := context.Background()
	g, c, token, _ := unknownAccountWithGatewayKey(t)
	a, err := testStore.LLMAccount(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	g.relabel(a.Alias, "renamed-by-hand")
	if err := c.Block(ctx, token); err == nil {
		t.Fatal("a key the gateway still holds under its recorded identity was treated as gone")
	}
	if state := accountState(t, token); state != "unknown" {
		t.Fatalf("state=%s want unknown", state)
	}
}

func TestBlockKeepsAnUnknownAccountWhenTheGatewayIsUnreachable(t *testing.T) {
	ctx := context.Background()
	g, c, token, shiftID := unknownAccountWithGatewayKey(t)
	a, err := testStore.LLMAccount(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	g.forget(a.Alias)
	g.set(func(g *goneKeyGateway) { g.down = true })
	if err := c.Block(ctx, token); err == nil {
		t.Fatal("an unreachable gateway reported success")
	}
	if state := accountState(t, token); state != "unknown" {
		t.Fatalf("state=%s: an unreachable gateway was read as a deleted key", state)
	}
	if l, _ := testStore.Ledger(ctx, shiftID); l.Reserved != 1 {
		t.Fatalf("hold released while the gateway was unreachable: %+v", l)
	}
}
