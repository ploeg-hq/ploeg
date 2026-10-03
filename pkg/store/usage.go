package store

import (
	"context"
	"encoding/json"
	"time"
)

// RunUsage is one finished Run's accounting as the pull request report needs
// it: who ran, what they were billed for and in what account state. Every
// figure is read from state Ploeg already records — agent_runs.usage (the
// keys ReconcileLLMAccountWithUsage merges) and run_llm_accounts — so the
// report needs no migration and no gateway call.
type RunUsage struct {
	Role    string
	Round   int
	Writes  bool
	Outcome string
	Verdict string
	// Alias is the Run's gateway key alias ploeg-<12hex>, "" when no account.
	Alias string
	// HasUsage is true when agent_runs.usage carried figures. When false the
	// report says the usage is unavailable rather than guessing.
	HasUsage     bool
	Models       []string
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
	// Authorized is what the pool held for this Run. It is never rendered as
	// cost; it is only here so a caller could compare it.
	Authorized float64
	// Duration is how long the Run ran, from started_at to finished_at. It is
	// zero when either timestamp is absent.
	Duration time.Duration
	// AccountState is run_llm_accounts.state, "" when the Run has no managed
	// account (a reading Run mints none, ADR-0013).
	AccountState string
}

// Settled reports whether this Run's gateway account has been reconciled.
// A Run with no account is not unsettled — it was never minted against.
func (r RunUsage) Settled() bool {
	return r.AccountState == "" || r.AccountState == "reconciled"
}

// ShiftUsage is one Shift's ledger plus its finished Runs, in Round then
// claim order. One Store.ShiftUsage call answers the whole report.
type ShiftUsage struct {
	ID          int64
	WorkItemID  int64
	Team        string
	Branch      string
	Round       int
	CloseReason string
	Ledger      ShiftLedger
	Runs        []RunUsage
}

// AllSettled reports whether every Run that minted an account has had it
// reconciled. The report heads with "provisional" until it is true.
func (u ShiftUsage) AllSettled() bool {
	for _, r := range u.Runs {
		if !r.Settled() {
			return false
		}
	}
	return true
}

// usageJSON is the subset of agent_runs.usage the report reads. The keys are
// the ones ReconcileLLMAccountWithUsage writes; other keys a harness reported
// are ignored.
type usageJSON struct {
	InputTokens  int64    `json:"inputTokens"`
	OutputTokens int64    `json:"outputTokens"`
	Models       []string `json:"models"`
	CostUSD      float64  `json:"costUsd"`
}

// ShiftUsage reads a Shift's metadata, ledger and finished Runs in one place.
func (s *Store) ShiftUsage(ctx context.Context, shiftID int64) (ShiftUsage, error) {
	var u ShiftUsage
	if err := s.pool.QueryRow(ctx, `
		SELECT sh.id, sh.work_item_id, sh.team, sh.branch, sh.round, sh.close_reason,
		       sh.budget, sh.spent,
		       COALESCE((SELECT SUM(reserved) FROM run_budget_holds
		                 WHERE shift_id = sh.id), 0)
		FROM shifts sh WHERE sh.id = $1`, shiftID).
		Scan(&u.ID, &u.WorkItemID, &u.Team, &u.Branch, &u.Round, &u.CloseReason,
			&u.Ledger.Budget, &u.Ledger.Spent, &u.Ledger.Reserved); err != nil {
		return ShiftUsage{}, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT r.role, r.round, r.writes, COALESCE(r.outcome, ''), r.verdict,
		       COALESCE(a.alias, ''), COALESCE(a.state, ''),
		       r.authorized, r.usage,
		       COALESCE(EXTRACT(EPOCH FROM (r.finished_at - r.started_at)), 0)::float8
		FROM agent_runs r
		LEFT JOIN run_llm_accounts a USING (run_token)
		WHERE r.shift_id = $1 AND r.state = 'finished'
		ORDER BY r.round, r.id`, shiftID)
	if err != nil {
		return ShiftUsage{}, err
	}
	defer rows.Close()
	u.Runs = []RunUsage{}
	for rows.Next() {
		var r RunUsage
		var raw []byte
		var seconds float64
		if err := rows.Scan(&r.Role, &r.Round, &r.Writes, &r.Outcome, &r.Verdict,
			&r.Alias, &r.AccountState, &r.Authorized, &raw, &seconds); err != nil {
			return ShiftUsage{}, err
		}
		r.Duration = time.Duration(seconds * float64(time.Second))
		if len(raw) > 0 {
			var parsed usageJSON
			if err := json.Unmarshal(raw, &parsed); err == nil {
				r.HasUsage = true
				r.InputTokens = parsed.InputTokens
				r.OutputTokens = parsed.OutputTokens
				r.Models = parsed.Models
				r.CostUSD = parsed.CostUSD
			}
		}
		u.Runs = append(u.Runs, r)
	}
	return u, rows.Err()
}
