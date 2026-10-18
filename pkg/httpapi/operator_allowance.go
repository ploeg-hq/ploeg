package httpapi

import (
	"math"
	"net/http"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// DefaultAskAllowanceUSD is the monthly Ask Allowance a Team's period opens
// with (PLOEG_ASK_ALLOWANCE_USD, ADR-0082).
const DefaultAskAllowanceUSD = 2.00

// DefaultAskBudgetUSD is the per-Ask Budget (PLOEG_ASK_BUDGET_USD, ADR-0082).
const DefaultAskBudgetUSD = 0.02

func (s *Server) askAllowanceUSD() float64 {
	if s.AskAllowanceUSD > 0 {
		return s.AskAllowanceUSD
	}
	return DefaultAskAllowanceUSD
}

// askBudget is the per-Ask Budget for a team: the configured budget, lowered
// by the (team, ask) inference policy's budget when that is smaller. ok is
// false when the team has no such policy and therefore no Asks.
func (s *Server) askBudget(team string) (LLMPolicy, float64, bool) {
	budget := DefaultAskBudgetUSD
	if s.AskBudgetUSD > 0 {
		budget = s.AskBudgetUSD
	}
	if s.LLMControl == nil {
		return LLMPolicy{}, budget, false
	}
	policy, ok := s.LLMControl.Policy(team, work.AskRole)
	if !ok {
		return policy, budget, false
	}
	return policy, math.Min(budget, policy.BudgetUSD), true
}

type allowanceView struct {
	store.Allowance
	AskCount     int64   `json:"askCount"`
	AskBudgetUSD float64 `json:"askBudgetUsd"`
	AsksEnabled  bool    `json:"asksEnabled"`
}

func (s *Server) allowanceView(a store.Allowance) allowanceView {
	_, budget, enabled := s.askBudget(a.ScopeID)
	return allowanceView{Allowance: a, AskCount: a.Count, AskBudgetUSD: budget, AsksEnabled: enabled}
}

func (s *Server) handleOperatorAllowance(w http.ResponseWriter, r *http.Request) {
	if !operatorGET(w, r) {
		return
	}
	q := r.URL.Query()
	for key, values := range q {
		if (key != "team" && key != "purpose") || len(values) != 1 {
			operatorError(w, 400, "invalid_request", "Allowances take exactly one team and at most one purpose, ask or chat.")
			return
		}
	}
	team := q.Get("team")
	purpose := store.AllowancePurposeAsk
	if q.Has("purpose") {
		purpose = q.Get("purpose")
	}
	if !operatorName.MatchString(team) || (purpose != store.AllowancePurposeAsk && purpose != store.AllowancePurposeChat) {
		operatorError(w, 400, "invalid_request", "Allowances take exactly one team and at most one purpose, ask or chat.")
		return
	}
	principal, _ := OperatorPrincipalFromContext(r.Context())
	if !principal.AllowsTeam(team) {
		operatorError(w, 403, "forbidden", "The consumer cannot read this team.")
		return
	}
	limit := s.askAllowanceUSD()
	if purpose == store.AllowancePurposeChat {
		limit = s.chatAllowanceUSD()
	}
	a, err := s.Store.Allowance(r.Context(), purpose, store.AllowanceScopeTeam, team, time.Now(), limit)
	if err != nil {
		operatorReadError(w, err)
		return
	}
	if purpose == store.AllowancePurposeChat {
		operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "allowance": s.chatAllowanceView(a)})
		return
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "allowance": s.allowanceView(a)})
}
