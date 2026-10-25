package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/litellm"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

var errGatewayRefused = errors.New("gateway refused the call")

type ledgerKey struct {
	plaintext string
	alias     string
	maxBudget float64
	blocked   bool
	spend     float64
	logs      []float64
}

type ledgerGateway struct {
	mu           sync.Mutex
	keys         map[string]*ledgerKey
	mints        map[string]litellm.MintRequest
	minted       int
	loseResponse map[string]bool
}

func newLedgerGateway(t *testing.T) (*ledgerGateway, *llmbroker.LiteLLM) {
	t.Helper()
	g := &ledgerGateway{keys: map[string]*ledgerKey{}, mints: map[string]litellm.MintRequest{}, loseResponse: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(srv.Close)
	return g, llmbroker.NewLiteLLM(litellm.NewClient(srv.URL, "ledger-master"))
}

func (g *ledgerGateway) key(id string) (string, *ledgerKey) {
	if k, ok := g.keys[id]; ok {
		return id, k
	}
	hashed := hashedKey(id)
	return hashed, g.keys[hashed]
}

func (g *ledgerGateway) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	q := r.URL.Query()
	switch r.URL.Path {
	case "/key/generate":
		var req litellm.MintRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.KeyAlias == "" || req.MaxBudget <= 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		g.minted++
		plaintext := fmt.Sprintf("sk-ledger-%s-%d", req.KeyAlias, g.minted)
		g.keys[hashedKey(plaintext)] = &ledgerKey{plaintext: plaintext, alias: req.KeyAlias, maxBudget: req.MaxBudget}
		g.mints[req.KeyAlias] = req
		if g.loseResponse[req.KeyAlias] {
			delete(g.loseResponse, req.KeyAlias)
			http.Error(w, "upstream timeout", http.StatusGatewayTimeout)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"key": plaintext})
	case "/key/list":
		size, _ := strconv.Atoi(q.Get("size"))
		page, _ := strconv.Atoi(q.Get("page"))
		if size < 1 || page < 1 {
			http.Error(w, "invalid paging", http.StatusUnprocessableEntity)
			return
		}
		alias, substring := q.Get("key_alias"), q.Get("substring_matching") == "true"
		matching := []litellm.KeyInfo{}
		for token, k := range g.keys {
			if alias != "" && ((substring && !strings.Contains(k.alias, alias)) || (!substring && k.alias != alias)) {
				continue
			}
			if q.Get("status") == "active" && k.blocked {
				continue
			}
			matching = append(matching, litellm.KeyInfo{Token: token, KeyAlias: k.alias, Blocked: k.blocked})
		}
		sort.Slice(matching, func(i, j int) bool { return matching[i].Token < matching[j].Token })
		start := min((page-1)*size, len(matching))
		end := min(start+size, len(matching))
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": matching[start:end], "total_count": len(matching),
			"total_pages": max(1, (len(matching)+size-1)/size), "current_page": page})
	case "/key/block":
		var req struct {
			Key string `json:"key"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, k := g.key(req.Key)
		if k == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		k.blocked = true
		_ = json.NewEncoder(w).Encode(map[string]bool{"blocked": true})
	case "/key/info":
		_, k := g.key(q.Get("key"))
		if k == nil {
			http.Error(w, `{"error":"Key not found in database"}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"info": map[string]any{"spend": k.spend, "max_budget": k.maxBudget}})
	case "/spend/logs":
		token := q.Get("api_key")
		rows := []map[string]any{}
		if k := g.keys[token]; k != nil {
			for _, usd := range k.logs {
				rows = append(rows, map[string]any{"api_key": token, "spend": usd, "model": "trusted-model", "model_group": "trusted-model",
					"prompt_tokens": 100, "completion_tokens": 10})
			}
		}
		_ = json.NewEncoder(w).Encode(rows)
	case "/spend/logs/ui":
		alias := q.Get("key_alias")
		rows := []map[string]any{}
		for token, k := range g.keys {
			if alias == "" || !strings.Contains(k.alias, alias) {
				continue
			}
			for _, usd := range k.logs {
				rows = append(rows, map[string]any{"api_key": token, "spend": usd, "model": "trusted-model",
					"metadata": map[string]any{"user_api_key_alias": k.alias}})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": rows, "total_pages": 1, "total_is_capped": false})
	default:
		http.NotFound(w, r)
	}
}

func (g *ledgerGateway) charge(alias string, usd float64) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, k := range g.keys {
		if k.alias == alias && !k.blocked && k.spend < k.maxBudget {
			k.spend += usd
			k.logs = append(k.logs, usd)
			return nil
		}
	}
	return errGatewayRefused
}

func (g *ledgerGateway) landInFlight(alias string, usd float64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, k := range g.keys {
		if k.alias == alias {
			k.spend += usd
			k.logs = append(k.logs, usd)
			return true
		}
	}
	return false
}

func (g *ledgerGateway) totals() map[string]float64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	byAlias := map[string]float64{}
	for _, k := range g.keys {
		for _, usd := range k.logs {
			byAlias[k.alias] += usd
		}
	}
	return byAlias
}

func (g *ledgerGateway) mintFor(alias string) (litellm.MintRequest, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	m, ok := g.mints[alias]
	return m, ok
}

func round4(v float64) float64 { return math.Round(v*1e4) / 1e4 }

const ledgerRoles = `[{"team":"bronze","role":"reviewer","budgetUsd":0.8,"models":["trusted-model"],"ttl":"1h"},
	{"team":"bronze","role":"tester","budgetUsd":0.8,"models":["trusted-model"],"ttl":"1h"},
	{"team":"silver","role":"reviewer","budgetUsd":0.8,"models":["trusted-model"],"ttl":"1h"},
	{"team":"silver","role":"tester","budgetUsd":0.8,"models":["trusted-model"],"ttl":"1h"}]`

func ledgerShift(t *testing.T, team, externalID string, budget float64) int64 {
	t.Helper()
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: externalID, Team: team, Title: "ledger " + externalID})
	if err != nil {
		t.Fatal(err)
	}
	shiftID, err := testStore.OpenShift(ctx, id, team, "agent/vik-"+externalID, budget)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []store.Role{{Name: "reviewer", Cap: 1}, {Name: "tester", Cap: 1}}); err != nil {
		t.Fatal(err)
	}
	return shiftID
}

func finishLedgerRun(ctx context.Context, token string) error {
	_, err := testStore.ReportOutcome(ctx, token, store.Report(work.OutcomeNoChangeNeeded, "done", "", nil, []byte(`{"costUsd":99}`), nil))
	return err
}

func runLedgerSweeps(ctx context.Context, c *LLMControl) {
	if pending, err := testStore.PendingLLMBlocks(ctx, 0, 100); err == nil {
		for _, p := range pending {
			_ = c.Block(ctx, p.RunToken)
		}
	}
	if unsettled, err := testStore.UnsettledLLMAccounts(ctx, 0, 0, 100); err == nil {
		for _, a := range unsettled {
			_ = c.Settle(ctx, a)
		}
	}
	if correctable, err := testStore.CorrectableLLMAccounts(ctx, 0, 100); err == nil {
		for _, a := range correctable {
			_, _ = c.Correct(ctx, a)
		}
	}
}

type accountingSnapshot struct {
	observed, reconciled, settled map[string]float64
	spent                         map[int64]float64
}

func violations(t *testing.T, name, query string) {
	t.Helper()
	rows, err := testPool.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	defer rows.Close()
	var found []string
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		found = append(found, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if len(found) > 0 {
		t.Errorf("accounting invariant %q violated: %v", name, found)
	}
}

func assertAccountingInvariants(t *testing.T) accountingSnapshot {
	t.Helper()
	violations(t, "spent is the sum of settled charges", `SELECT sh.id::text||' spent='||sh.spent::text||' charged='||c.charged::text
		FROM shifts sh CROSS JOIN LATERAL (SELECT
			COALESCE((SELECT sum(a.reconciled_spend) FROM run_llm_accounts a JOIN agent_runs r USING(run_token) WHERE r.shift_id=sh.id),0)
			+ COALESCE((SELECT sum(COALESCE((r.usage->>'costUsd')::numeric,0)) FROM agent_runs r WHERE r.shift_id=sh.id AND r.state='finished'
				AND NOT EXISTS(SELECT 1 FROM run_llm_accounts a WHERE a.run_token=r.run_token)),0) AS charged) c
		WHERE sh.spent<>c.charged`)
	violations(t, "every charge is audited once", `SELECT 'spent='||s.spent::text||' audited='||a.audited::text FROM
		(SELECT COALESCE(sum(reconciled_spend),0) AS spent FROM run_llm_accounts) s,
		(SELECT COALESCE(sum((detail->>'delta')::numeric),0) AS audited FROM audit_log WHERE action='llm.reconciled') a
		WHERE round(s.spent,4)<>round(a.audited,4)`)
	violations(t, "a settlement is first settlement plus its adjustments", `SELECT a.alias||' reconciled='||a.reconciled_spend::text
		FROM run_llm_accounts a WHERE a.state='reconciled' AND (a.settled_spend IS NULL OR a.settled_at IS NULL
			OR a.reconciled_spend<>COALESCE((SELECT j.spend FROM run_llm_adjustments j WHERE j.run_token=a.run_token ORDER BY j.id DESC LIMIT 1),a.settled_spend))`)
	violations(t, "adjustments chain from the first settlement", `SELECT j.id::text FROM (SELECT j.*, a.settled_spend,
			lag(j.spend) OVER (PARTITION BY j.run_token ORDER BY j.id) AS prior FROM run_llm_adjustments j JOIN run_llm_accounts a USING(run_token)) j
		WHERE j.previous_spend<>COALESCE(j.prior,j.settled_spend) OR j.spend<=j.previous_spend`)
	violations(t, "an unsettled account has no settlement", `SELECT a.alias||' state='||a.state FROM run_llm_accounts a
		WHERE a.state<>'reconciled' AND (a.reconciled_spend IS NOT NULL OR a.settled_spend IS NOT NULL
			OR EXISTS(SELECT 1 FROM run_llm_adjustments j WHERE j.run_token=a.run_token))`)
	violations(t, "only a finished Run settles", `SELECT a.alias FROM run_llm_accounts a JOIN agent_runs r USING(run_token)
		WHERE a.state='reconciled' AND r.state<>'finished'`)
	violations(t, "settlement never undercuts an observation", `SELECT a.alias FROM run_llm_accounts a
		WHERE a.state='reconciled' AND (a.corrections_until IS NULL OR a.corrections_until<=now())
		AND a.reconciled_spend<COALESCE(a.observed_spend,0)`)
	violations(t, "an account never exceeds its Run's authorization", `SELECT a.alias||' account='||a.authorized::text||' run='||r.authorized::text
		FROM run_llm_accounts a JOIN agent_runs r USING(run_token) WHERE r.authorized>0 AND a.authorized>r.authorized`)
	violations(t, "an unsettled account holds at least its authorization", `SELECT h.run_token FROM run_budget_holds h
		JOIN run_llm_accounts a USING(run_token) WHERE a.state<>'reconciled' AND h.reserved<GREATEST(a.authorized,COALESCE(a.observed_spend,0))`)
	return readAccountingSnapshot(t)
}

func readAccountingSnapshot(t *testing.T) accountingSnapshot {
	t.Helper()
	ctx := context.Background()
	s := accountingSnapshot{observed: map[string]float64{}, reconciled: map[string]float64{}, settled: map[string]float64{}, spent: map[int64]float64{}}
	rows, err := testPool.Query(ctx, `SELECT alias, COALESCE(observed_spend,-1)::float8, COALESCE(reconciled_spend,-1)::float8, COALESCE(settled_spend,-1)::float8 FROM run_llm_accounts`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var alias string
		var observed, reconciled, settled float64
		if err := rows.Scan(&alias, &observed, &reconciled, &settled); err != nil {
			t.Fatal(err)
		}
		s.observed[alias], s.reconciled[alias], s.settled[alias] = observed, reconciled, settled
	}
	rows.Close()
	rows, err = testPool.Query(ctx, `SELECT id, spent::float8 FROM shifts`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var spent float64
		if err := rows.Scan(&id, &spent); err != nil {
			t.Fatal(err)
		}
		s.spent[id] = spent
	}
	return s
}

func assertNeverDecreases(t *testing.T, before, after accountingSnapshot, step string) {
	t.Helper()
	for alias, v := range before.observed {
		if after.observed[alias] < v {
			t.Errorf("%s: observed spend of %s fell from %v to %v", step, alias, v, after.observed[alias])
		}
	}
	for alias, v := range before.reconciled {
		if after.reconciled[alias] < v {
			t.Errorf("%s: settled spend of %s fell from %v to %v", step, alias, v, after.reconciled[alias])
		}
	}
	for alias, v := range before.settled {
		if v >= 0 && after.settled[alias] != v {
			t.Errorf("%s: first settlement of %s rewritten from %v to %v", step, alias, v, after.settled[alias])
		}
	}
	for id, v := range before.spent {
		if after.spent[id] < v {
			t.Errorf("%s: Shift %d spent fell from %v to %v", step, id, v, after.spent[id])
		}
	}
}

func assertSettledToGatewayTruth(t *testing.T, g *ledgerGateway) {
	t.Helper()
	ctx := context.Background()
	truth := g.totals()
	rows, err := testPool.Query(ctx, `SELECT a.alias, a.state, a.reconciled_spend::float8, r.shift_id, h.reserved::float8
		FROM run_llm_accounts a JOIN agent_runs r USING(run_token) JOIN run_budget_holds h USING(run_token)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	byShift := map[int64]float64{}
	var gatewayTotal float64
	for rows.Next() {
		var alias, state string
		var reconciled, held float64
		var shiftID int64
		if err := rows.Scan(&alias, &state, &reconciled, &shiftID, &held); err != nil {
			t.Fatalf("%s: unsettled at the end: %v", alias, err)
		}
		if state != "reconciled" || held != 0 {
			t.Errorf("%s ended %s holding %v", alias, state, held)
		}
		if want := round4(truth[alias]); math.Abs(reconciled-want) > 1e-9 {
			t.Errorf("%s settled %v, its own keys' spend logs total %v", alias, reconciled, want)
		}
		byShift[shiftID] += round4(truth[alias])
		gatewayTotal += truth[alias]
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var spentTotal float64
	for shiftID, want := range byShift {
		l, err := testStore.Ledger(ctx, shiftID)
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(l.Spent-want) > 1e-9 || l.Reserved != 0 {
			t.Errorf("Shift %d spent %v held %v, its own Runs' spend logs total %v", shiftID, l.Spent, l.Reserved, want)
		}
		spentTotal += l.Spent
	}
	var unattributed float64
	for alias, usd := range truth {
		var known bool
		if err := testPool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM run_llm_accounts WHERE alias=$1)`, alias).Scan(&known); err != nil {
			t.Fatal(err)
		}
		if !known {
			unattributed += usd
		}
	}
	if unattributed != 0 || math.Abs(spentTotal-round4(gatewayTotal)) > 1e-3 {
		t.Errorf("Shifts were charged %v for %v of gateway spend (%v unattributed)", spentTotal, gatewayTotal, unattributed)
	}
}
