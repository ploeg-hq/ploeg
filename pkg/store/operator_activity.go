package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// OperatorWorkItemCounts counts a Team's Work Items by their current state.
type OperatorWorkItemCounts struct {
	Queued         int64 `json:"queued"`
	Leased         int64 `json:"leased"`
	AwaitingReview int64 `json:"awaitingReview"`
	NeedsHuman     int64 `json:"needsHuman"`
	Proposed       int64 `json:"proposed"`
	Withdrawn      int64 `json:"withdrawn"`
	Done           int64 `json:"done"`
	Stale          int64 `json:"stale"`
}

// OperatorRunCounts counts current pending and running Runs, and Runs that
// finished inside the summary window by outcome.
type OperatorRunCounts struct {
	Pending  int64 `json:"pending"`
	Running  int64 `json:"running"`
	Finished int64 `json:"finished"`
	Failed   int64 `json:"failed"`
	Stuck    int64 `json:"stuck"`
}

// OperatorSpend is spend settled inside the summary window and the budget
// currently held by live or unresolved Runs, both in USD.
type OperatorSpend struct {
	SettledUSD  float64 `json:"settledUsd"`
	ReservedUSD float64 `json:"reservedUsd"`
}

// OperatorSummaryTotals is the sum of every Team summary in scope.
type OperatorSummaryTotals struct {
	WorkItems OperatorWorkItemCounts `json:"workItems"`
	Runs      OperatorRunCounts      `json:"runs"`
	Spend     OperatorSpend          `json:"spend"`
}

// OperatorTeamSummary is one Team's activity summary.
type OperatorTeamSummary struct {
	Team           string                 `json:"team"`
	WorkItems      OperatorWorkItemCounts `json:"workItems"`
	Runs           OperatorRunCounts      `json:"runs"`
	Spend          OperatorSpend          `json:"spend"`
	LastActivityAt *time.Time             `json:"lastActivityAt"`
}

// OperatorRunUsage is the token usage and models a Run reported or settled.
type OperatorRunUsage struct {
	InputTokens  int64    `json:"inputTokens"`
	OutputTokens int64    `json:"outputTokens"`
	Models       []string `json:"models"`
}

// OperatorRunListItem is one Run in the operator Run list.
type OperatorRunListItem struct {
	ID              string            `json:"id"`
	WorkItemID      string            `json:"workItemId"`
	WorkItemTitle   string            `json:"workItemTitle"`
	ExternalRef     string            `json:"externalRef"`
	Team            string            `json:"team"`
	Role            string            `json:"role"`
	Round           int               `json:"round"`
	Writes          bool              `json:"writes"`
	State           string            `json:"state"`
	Outcome         string            `json:"outcome"`
	Verdict         string            `json:"verdict"`
	FailureReason   string            `json:"failureReason"`
	StartedAt       *time.Time        `json:"startedAt"`
	FinishedAt      *time.Time        `json:"finishedAt"`
	DurationSeconds *int64            `json:"durationSeconds"`
	AuthorizedUSD   *float64          `json:"authorizedUsd"`
	SettledUSD      *float64          `json:"settledUsd"`
	Usage           *OperatorRunUsage `json:"usage"`
	ObservedUSD     *float64          `json:"observedUsd"`
	ReservedModels  []string          `json:"reservedModels"`
	// SettledAt is when the Run's gateway account was first settled.
	SettledAt *time.Time `json:"settledAt,omitempty"`
	// CostFinalAt is when the settled cost stops being corrected: the end of
	// the correction window, or SettledAt when there is none.
	CostFinalAt *time.Time `json:"costFinalAt,omitempty"`
	// CostFinal is true once the Run's cost will no longer change. Before
	// that a settled cost is provisional.
	CostFinal bool `json:"costFinal"`
}

// OperatorRunFilter selects Runs newest first. Before is an exclusive Run id
// cursor; zero means the newest Run.
type OperatorRunFilter struct {
	Teams   []string
	Team    string
	State   string
	Outcome string
	Before  int64
	Limit   int
}

const operatorScope = `($1::text[] IS NULL OR i.team = ANY($1))`

const operatorRunTokens = `CASE WHEN jsonb_typeof(r.usage->'%[1]s') = 'number' AND (r.usage->>'%[1]s')::numeric BETWEEN 0 AND 9000000000000000
	THEN to_jsonb(floor((r.usage->>'%[1]s')::numeric)::bigint) ELSE '0'::jsonb END`

const operatorRunListJSON = `jsonb_build_object(
	'id', r.id::text, 'workItemId', r.work_item_id::text, 'workItemTitle', left(i.title, 4096),
	'provider', i.provider, 'externalId', i.external_id,
	'team', r.team, 'role', r.role, 'round', r.round, 'writes', r.writes, 'state', r.state,
	'outcome', COALESCE(r.outcome, ''), 'verdict', r.verdict, 'failureReason', left(COALESCE(r.failure_reason, ''), 4096),
	'startedAt', r.started_at, 'finishedAt', r.finished_at,
	'durationSeconds', CASE WHEN r.started_at IS NOT NULL AND r.finished_at IS NOT NULL
		THEN GREATEST(0, floor(extract(epoch FROM r.finished_at - r.started_at)))::bigint END,
	'authorizedUsd', CASE WHEN r.state = 'pending' THEN NULL ELSE r.authorized END,
	'settledUsd', CASE WHEN a.run_token IS NOT NULL THEN to_jsonb(CASE WHEN a.cost_known THEN a.reconciled_spend END)
		WHEN r.state = 'finished' AND jsonb_typeof(r.usage->'costUsd') = 'number' THEN r.usage->'costUsd' END,
	'settledAt', a.settled_at,
	'costFinalAt', CASE WHEN a.state = 'reconciled' THEN COALESCE(a.corrections_until, a.settled_at) END,
	'costFinal', CASE WHEN a.run_token IS NULL THEN r.state = 'finished'
		ELSE a.state = 'reconciled' AND COALESCE(a.corrections_until <= now(), true) END,
	'usage', CASE WHEN jsonb_typeof(r.usage) = 'object' THEN jsonb_build_object(
		'inputTokens', %[1]s,
		'outputTokens', %[2]s,
		'models', CASE WHEN jsonb_typeof(r.usage->'models') = 'array' THEN (
			SELECT COALESCE(jsonb_agg(left(m.value #>> '{}', 256) ORDER BY m.ord), '[]'::jsonb)
			FROM jsonb_array_elements(r.usage->'models') WITH ORDINALITY AS m(value, ord)
			WHERE jsonb_typeof(m.value) = 'string' AND m.ord <= 32) ELSE '[]'::jsonb END) END,
	'observedUsd', a.observed_spend,
	'reservedModels', CASE WHEN jsonb_typeof(a.models) = 'array' THEN (
		SELECT COALESCE(jsonb_agg(left(m.value #>> '{}', 256) ORDER BY m.ord), '[]'::jsonb)
		FROM jsonb_array_elements(a.models) WITH ORDINALITY AS m(value, ord)
		WHERE jsonb_typeof(m.value) = 'string' AND m.ord <= 32) ELSE '[]'::jsonb END)`

var operatorRunListProjection = fmt.Sprintf(operatorRunListJSON, fmt.Sprintf(operatorRunTokens, "inputTokens"), fmt.Sprintf(operatorRunTokens, "outputTokens"))

// OperatorRuns lists Runs newest first by id, scoped by the owning Work
// Item's Team. The boolean reports whether older Runs remain.
func (s *Store) OperatorRuns(ctx context.Context, f OperatorRunFilter) ([]OperatorRunListItem, bool, error) {
	if f.Limit < 1 || f.Limit > 200 || f.Before < 0 {
		return nil, false, errors.New("invalid operator pagination")
	}
	rows, err := s.pool.Query(ctx, `SELECT `+operatorRunListProjection+` FROM agent_runs r
		JOIN work_items i ON i.id = r.work_item_id
		LEFT JOIN run_llm_accounts a ON a.run_token = r.run_token
		WHERE `+operatorScope+` AND ($2 = '' OR i.team = $2) AND ($3 = '' OR r.state = $3)
		AND ($4 = '' OR r.outcome = $4) AND ($5::bigint = 0 OR r.id < $5)
		ORDER BY r.id DESC LIMIT $6`, f.Teams, f.Team, f.State, f.Outcome, f.Before, f.Limit+1)
	if err != nil {
		return nil, false, err
	}
	type row struct {
		OperatorRunListItem
		Provider   string `json:"provider"`
		ExternalID string `json:"externalId"`
	}
	raw, err := operatorDecodeRows[row](rows)
	if err != nil {
		return nil, false, err
	}
	runs := make([]OperatorRunListItem, 0, len(raw))
	for _, r := range raw {
		run := r.OperatorRunListItem
		if r.Provider != "manual" && r.ExternalID != "" {
			run.ExternalRef = work.Reference(work.WorkItem{Provider: r.Provider, ExternalID: r.ExternalID})
		}
		run.StartedAt, run.FinishedAt = operatorUTC(run.StartedAt), operatorUTC(run.FinishedAt)
		if run.Usage != nil && run.Usage.Models == nil {
			run.Usage.Models = []string{}
		}
		if run.ReservedModels == nil {
			run.ReservedModels = []string{}
		}
		runs = append(runs, run)
	}
	return operatorPage(runs, f.Limit)
}

// OperatorSummary summarises every Team in scope that has a Work Item or is
// registered. Work Item counts, pending and running Runs, and reserved spend
// are current; finished Runs and settled spend count from since onwards.
func (s *Store) OperatorSummary(ctx context.Context, teams []string, registered map[string][]string, since time.Time) ([]OperatorTeamSummary, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	byTeam := map[string]*OperatorTeamSummary{}
	ensure := func(team string) *OperatorTeamSummary {
		if byTeam[team] == nil {
			byTeam[team] = &OperatorTeamSummary{Team: team}
		}
		return byTeam[team]
	}
	for team := range registered {
		if teams == nil || containsString(teams, team) {
			ensure(team)
		}
	}
	scan := func(query string, args []any, each func(pgx.Rows) error) error {
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if err := each(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	if err := scan(`SELECT i.team, i.state, count(*) FROM work_items i WHERE `+operatorScope+` GROUP BY i.team, i.state`, []any{teams}, func(rows pgx.Rows) error {
		var team, state string
		var n int64
		if err := rows.Scan(&team, &state, &n); err != nil {
			return err
		}
		c := &ensure(team).WorkItems
		switch state {
		case "queued":
			c.Queued = n
		case "leased":
			c.Leased = n
		case "awaiting_review":
			c.AwaitingReview = n
		case "needs_human":
			c.NeedsHuman = n
		case "proposed":
			c.Proposed = n
		case "withdrawn":
			c.Withdrawn = n
		case "done":
			c.Done = n
		case "stale":
			c.Stale = n
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := scan(`SELECT i.team, max(la.at) FROM work_items i
		CROSS JOIN LATERAL (SELECT a.at FROM audit_log a WHERE a.work_item_id = i.id ORDER BY a.id DESC LIMIT 1) la
		WHERE `+operatorScope+` GROUP BY i.team`, []any{teams}, func(rows pgx.Rows) error {
		var team string
		var at time.Time
		if err := rows.Scan(&team, &at); err != nil {
			return err
		}
		at = at.UTC()
		ensure(team).LastActivityAt = &at
		return nil
	}); err != nil {
		return nil, err
	}
	if err := scan(`SELECT i.team, r.state, count(*) FROM agent_runs r JOIN work_items i ON i.id = r.work_item_id
		WHERE r.state IN ('pending', 'running') AND `+operatorScope+` GROUP BY i.team, r.state`, []any{teams}, func(rows pgx.Rows) error {
		var team, state string
		var n int64
		if err := rows.Scan(&team, &state, &n); err != nil {
			return err
		}
		if state == "pending" {
			ensure(team).Runs.Pending = n
		} else {
			ensure(team).Runs.Running = n
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := scan(`SELECT i.team, count(*), count(*) FILTER (WHERE r.outcome = 'failed'), count(*) FILTER (WHERE r.outcome = 'stuck')
		FROM agent_runs r JOIN work_items i ON i.id = r.work_item_id
		WHERE r.finished_at IS NOT NULL AND r.finished_at >= $2 AND `+operatorScope+` GROUP BY i.team`, []any{teams, since}, func(rows pgx.Rows) error {
		var team string
		var finished, failed, stuck int64
		if err := rows.Scan(&team, &finished, &failed, &stuck); err != nil {
			return err
		}
		runs := &ensure(team).Runs
		runs.Finished, runs.Failed, runs.Stuck = finished, failed, stuck
		return nil
	}); err != nil {
		return nil, err
	}
	if err := scan(`SELECT team, COALESCE(sum(v), 0)::float8 FROM (
		SELECT i.team, (a.detail->>'delta')::numeric AS v FROM audit_log a JOIN work_items i ON i.id = a.work_item_id
		WHERE a.action = 'llm.reconciled' AND a.at >= $2 AND jsonb_typeof(a.detail->'delta') = 'number' AND `+operatorScope+`
		UNION ALL
		SELECT i.team, (r.usage->>'costUsd')::numeric FROM agent_runs r JOIN work_items i ON i.id = r.work_item_id
		WHERE r.finished_at IS NOT NULL AND r.finished_at >= $2
		AND jsonb_typeof(r.usage->'costUsd') = 'number' AND `+operatorScope+`
		AND NOT EXISTS (SELECT 1 FROM run_llm_accounts x WHERE x.run_token = r.run_token)) settled
		GROUP BY team`, []any{teams, since}, func(rows pgx.Rows) error {
		var team string
		var v float64
		if err := rows.Scan(&team, &v); err != nil {
			return err
		}
		ensure(team).Spend.SettledUSD = v
		return nil
	}); err != nil {
		return nil, err
	}
	if err := scan(`SELECT i.team, COALESCE(sum(h.reserved), 0)::float8 FROM run_budget_holds h
		JOIN agent_runs r ON r.run_token = h.run_token JOIN work_items i ON i.id = r.work_item_id
		WHERE h.run_token IN (
			SELECT run_token FROM agent_runs WHERE state = 'running'
			UNION SELECT run_token FROM run_llm_accounts WHERE state <> 'reconciled' OR COALESCE(observed_spend, 0) > reconciled_spend)
		AND `+operatorScope+` GROUP BY i.team`, []any{teams}, func(rows pgx.Rows) error {
		var team string
		var v float64
		if err := rows.Scan(&team, &v); err != nil {
			return err
		}
		ensure(team).Spend.ReservedUSD = v
		return nil
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	out := make([]OperatorTeamSummary, 0, len(byTeam))
	for _, summary := range byTeam {
		out = append(out, *summary)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Team < out[j].Team })
	return out, nil
}

// OperatorSummaryTotal sums Team summaries.
func OperatorSummaryTotal(teams []OperatorTeamSummary) OperatorSummaryTotals {
	var t OperatorSummaryTotals
	for _, s := range teams {
		w := s.WorkItems
		t.WorkItems.Queued += w.Queued
		t.WorkItems.Leased += w.Leased
		t.WorkItems.AwaitingReview += w.AwaitingReview
		t.WorkItems.NeedsHuman += w.NeedsHuman
		t.WorkItems.Proposed += w.Proposed
		t.WorkItems.Withdrawn += w.Withdrawn
		t.WorkItems.Done += w.Done
		t.WorkItems.Stale += w.Stale
		t.Runs.Pending += s.Runs.Pending
		t.Runs.Running += s.Runs.Running
		t.Runs.Finished += s.Runs.Finished
		t.Runs.Failed += s.Runs.Failed
		t.Runs.Stuck += s.Runs.Stuck
		t.Spend.SettledUSD += s.Spend.SettledUSD
		t.Spend.ReservedUSD += s.Spend.ReservedUSD
	}
	return t
}

func containsString(values []string, v string) bool {
	for _, value := range values {
		if value == v {
			return true
		}
	}
	return false
}

func operatorUTC(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
