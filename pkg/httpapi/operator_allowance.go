package httpapi

import (
	"math"
	"net/http"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// DefaultAskAllowanceUSD is the monthly Ask Allowance a Team's period opens
// with (PLOEG_ASK_ALLOWANCE_USD, ADR-0081).
const DefaultAskAllowanceUSD = 2.00

// DefaultAskBudgetUSD is the per-Ask Budget (PLOEG_ASK_BUDGET_USD, ADR-0081).
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
	AskBudgetUSD float64 `json:"askBudgetUsd"`
	AsksEnabled  bool    `json:"asksEnabled"`
}

func (s *Server) allowanceView(a store.Allowance) allowanceView {
	_, budget, enabled := s.askBudget(a.ScopeID)
	return allowanceView{Allowance: a, AskBudgetUSD: budget, AsksEnabled: enabled}
}

func (s *Server) handleOperatorAllowance(w http.ResponseWriter, r *http.Request) {
	if !operatorGET(w, r) {
		return
	}
	q := r.URL.Query()
	for key, values := range q {
		if key != "team" || len(values) != 1 {
			operatorError(w, 400, "invalid_request", "Allowances take exactly one team.")
			return
		}
	}
	team := q.Get("team")
	if !operatorName.MatchString(team) {
		operatorError(w, 400, "invalid_request", "Allowances take exactly one team.")
		return
	}
	principal, _ := OperatorPrincipalFromContext(r.Context())
	if !principal.AllowsTeam(team) {
		operatorError(w, 403, "forbidden", "The consumer cannot read this team.")
		return
	}
	a, err := s.Store.Allowance(r.Context(), store.AllowancePurposeAsk, store.AllowanceScopeTeam, team, time.Now(), s.askAllowanceUSD())
	if err != nil {
		operatorReadError(w, err)
		return
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "allowance": s.allowanceView(a)})
}
