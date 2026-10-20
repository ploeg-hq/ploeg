package store

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

const lookupIndexesSeed = `
INSERT INTO work_items (provider, external_id, team, state)
SELECT 'vikunja', g::text, 'team' || (g % 5), CASE WHEN g % 10 = 0 THEN 'awaiting_review' ELSE 'done' END
FROM generate_series(1, 2000) g;
INSERT INTO shifts (work_item_id, team, closed_at)
SELECT w.id, w.team, CASE WHEN s < 10 OR w.id % 40 <> 0 THEN now() END
FROM work_items w CROSS JOIN generate_series(1, 10) s;
INSERT INTO agent_runs (run_token, team, work_item_id, shift_id, role, state, finished_at)
SELECT 'run-' || sh.id, sh.team, sh.work_item_id, sh.id, 'coder',
       CASE WHEN sh.id % 500 = 1 THEN 'running' ELSE 'finished' END,
       CASE WHEN sh.id % 500 = 1 THEN NULL ELSE now() END
FROM shifts sh;
INSERT INTO run_llm_accounts (run_token, alias, authorized, models, ttl_seconds, state)
SELECT r.run_token, 'alias-' || r.id, 1, '[]', 600, CASE WHEN r.id % 1000 = 0 THEN 'reserved' ELSE 'reconciled' END
FROM agent_runs r;
INSERT INTO audit_log (actor, action, detail)
SELECT 'ploegd', CASE WHEN g % 50 = 0 THEN 'llm.issued' ELSE 'run.finished' END, jsonb_build_object('alias', 'alias-' || g)
FROM generate_series(1, 100000) g;
INSERT INTO pull_requests (forge, repo_owner, repo_name, number, work_item_id, shift_id, state)
SELECT 'github', 'o', 'r', sh.id, sh.work_item_id, sh.id, 'merged' FROM shifts sh WHERE sh.id % 2 = 0;
INSERT INTO operator_executions (id, consumer, actor, session_id, work_item_id, shift_id, run_id, fingerprint, expires_at, state)
SELECT 'exec-' || r.id, 'operator', 'a', 's-' || r.id, r.work_item_id, r.shift_id, r.id, 'f', now(), 'completed'
FROM (SELECT DISTINCT ON (work_item_id) * FROM agent_runs ORDER BY work_item_id, id) r;
INSERT INTO work_item_reviews (work_item_id, provider, repo, reviewer, state)
SELECT w.id, 'github', 'o/r', 'reviewer' || s, CASE WHEN s % 3 = 0 THEN 'changes_requested' ELSE 'approved' END
FROM work_items w CROSS JOIN generate_series(1, 10) s;
ANALYZE;
`

func TestLookupIndexesAreChosenByThePlanner(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx, scratchDatabase(t, "ploeg_lookup_index_plans"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, lookupIndexesSeed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var liveShiftItem int64
	if err := s.pool.QueryRow(ctx, `SELECT work_item_id FROM shifts WHERE closed_at IS NULL ORDER BY id LIMIT 1`).Scan(&liveShiftItem); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		index string
		query string
		args  []any
	}{
		{"AwaitingReview latest Shift", "shifts_by_work_item", awaitingReviewQuery, nil},
		{"UnsettledLLMAccounts mint lookup", "audit_log_llm_mint_alias", unsettledLLMAccountsQuery, []any{int64(0), float64(60), 100}},
		{"open Shift Runs without a pull request", "pull_requests_by_shift", shiftRunsWithoutPullRequestQuery, nil},
		{"team running Runs", "operator_executions_by_run", runningRunsQuery, []any{"team1"}},
		{"LiveShiftForItem", "operator_executions_by_shift", liveShiftForItemQuery, []any{liveShiftItem}},
		{"changes requested by", "work_item_reviews_by_work_item",
			`SELECT ` + operatorChangesRequestedByJSON + ` FROM work_items i WHERE i.state = 'awaiting_review'`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := explain(t, s, tc.query, tc.args...)
			if !strings.Contains(plan, tc.index) {
				t.Errorf("plan does not use %s:\n%s", tc.index, plan)
			}
		})
	}
}

func explain(t *testing.T, s *Store, query string, args ...any) string {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), "EXPLAIN (COSTS OFF) "+query, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	return strings.Join(lines, "\n")
}
