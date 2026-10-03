package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

type fakeGateway struct {
	mu      sync.Mutex
	keys    map[string]string
	blocked map[string]bool
	logs    map[string][]map[string]any
}

func hashedKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func newFakeGateway(t *testing.T) (*fakeGateway, *llmbroker.LiteLLM) {
	t.Helper()
	g := &fakeGateway{keys: map[string]string{}, blocked: map[string]bool{}, logs: map[string][]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		switch r.URL.Path {
		case "/key/generate":
			var req litellm.MintRequest
			if json.NewDecoder(r.Body).Decode(&req) != nil || req.KeyAlias == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			key := "sk-fixture-" + req.KeyAlias
			g.keys[hashedKey(key)] = req.KeyAlias
			_ = json.NewEncoder(w).Encode(map[string]string{"key": key})
		case "/key/list":
			keys := []litellm.KeyInfo{}
			for token, alias := range g.keys {
				keys = append(keys, litellm.KeyInfo{Token: token, KeyAlias: alias, Blocked: g.blocked[token]})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys, "total_count": len(keys), "total_pages": 1, "current_page": 1})
		case "/key/block":
			var req struct {
				Key string `json:"key"`
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			token := req.Key
			if _, ok := g.keys[token]; !ok {
				token = hashedKey(req.Key)
			}
			g.blocked[token] = true
			_ = json.NewEncoder(w).Encode(map[string]bool{"blocked": true})
		case "/key/info":
			_ = json.NewEncoder(w).Encode(map[string]any{"info": map[string]any{"spend": 0, "max_budget": 1}})
		case "/spend/logs":
			token := r.URL.Query().Get("api_key")
			rows := []map[string]any{}
			for _, entry := range g.logs[token] {
				row := map[string]any{"api_key": token}
				for k, v := range entry {
					row[k] = v
				}
				rows = append(rows, row)
			}
			_ = json.NewEncoder(w).Encode(rows)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return g, llmbroker.NewLiteLLM(litellm.NewClient(srv.URL, "fixture-master"))
}

func TestControllerSettlementRecordsGatewayUsageOnTheRun(t *testing.T) {
	ctx := context.Background()
	g, broker := newFakeGateway(t)
	c, token, shiftID := settlementFixture(t, broker, true)
	if _, err := testPool.Exec(ctx, `UPDATE agent_runs SET usage='{"costUsd":0,"sessionId":"fixture-session"}' WHERE run_token=$1`, token); err != nil {
		t.Fatal(err)
	}
	if err := c.Block(ctx, token); err != nil {
		t.Fatal(err)
	}
	candidate := settleCandidate(t, token)
	g.mu.Lock()
	g.logs[candidate.GatewayKeyID] = []map[string]any{
		{"spend": 0.12, "model": "deepseek-chat", "prompt_tokens": 1500, "completion_tokens": 400},
		{"spend": 0.2, "model": "claude-sonnet", "prompt_tokens": 900, "completion_tokens": 120},
		{"spend": 0.01, "model": "deepseek-chat", "prompt_tokens": 100, "completion_tokens": 5},
	}
	g.mu.Unlock()
	if err := c.Settle(ctx, candidate); err != nil {
		t.Fatal(err)
	}

	var raw []byte
	if err := testPool.QueryRow(ctx, `SELECT usage FROM agent_runs WHERE run_token=$1`, token).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var usage struct {
		InputTokens  int64    `json:"inputTokens"`
		OutputTokens int64    `json:"outputTokens"`
		Models       []string `json:"models"`
		ByModel      []struct {
			Model        string  `json:"model"`
			InputTokens  int64   `json:"inputTokens"`
			OutputTokens int64   `json:"outputTokens"`
			CostUSD      float64 `json:"costUsd"`
		} `json:"byModel"`
		CostUSD   float64 `json:"costUsd"`
		SessionID string  `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &usage); err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != 2500 || usage.OutputTokens != 525 || strings.Join(usage.Models, ",") != "claude-sonnet,deepseek-chat" {
		t.Fatalf("usage=%s", raw)
	}
	if math.Abs(usage.CostUSD-0.33) > 1e-9 || usage.SessionID != "fixture-session" {
		t.Fatalf("usage lost the settled cost or the harness session: %s", raw)
	}
	if len(usage.ByModel) != 2 {
		t.Fatalf("a Run that used two models must be split per model: %s", raw)
	}
	sonnet, deepseek := usage.ByModel[0], usage.ByModel[1]
	if sonnet.Model != "claude-sonnet" || sonnet.InputTokens != 900 || sonnet.OutputTokens != 120 || math.Abs(sonnet.CostUSD-0.2) > 1e-9 {
		t.Fatalf("claude-sonnet share=%+v in %s", sonnet, raw)
	}
	if deepseek.Model != "deepseek-chat" || deepseek.InputTokens != 1600 || deepseek.OutputTokens != 405 || math.Abs(deepseek.CostUSD-0.13) > 1e-9 {
		t.Fatalf("deepseek-chat share=%+v in %s", deepseek, raw)
	}
	if l, _ := testStore.Ledger(ctx, shiftID); math.Abs(l.Spent-0.33) > 1e-9 || l.Reserved != 0 {
		t.Fatalf("ledger=%+v", l)
	}
}

func TestControllerSettlementAtZeroLeavesHarnessUsageAlone(t *testing.T) {
	ctx := context.Background()
	_, broker := newFakeGateway(t)
	c, token, _ := settlementFixture(t, broker, false)
	if err := c.Settle(ctx, settleCandidate(t, token)); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := testPool.QueryRow(ctx, `SELECT usage::text FROM agent_runs WHERE run_token=$1`, token).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != `{"costUsd": 0}` {
		t.Fatalf("a Run that never minted gained gateway usage: %s", raw)
	}
}
