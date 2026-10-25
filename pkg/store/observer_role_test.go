package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var observerReads = []string{
	`SELECT state, team, count(*), min(created_at), max(attempts) FROM work_items GROUP BY 1, 2`,
	`SELECT count(*) FROM work_items WHERE team = 't' AND state = 'queued' AND NOT operator_owned AND (next_eligible_at IS NULL OR next_eligible_at <= now())`,
	`SELECT count(*) FROM agent_runs WHERE team = 't' AND role = 'r' AND state = 'pending' AND (harness_requested IS NULL OR harness_requested = 'h')`,
	`SELECT r.trace_alias, r.team, r.outcome, r.failure_reason, r.started_at, r.finished_at, w.provider, w.external_id, a.state, a.observed_spend
		FROM agent_runs r JOIN work_items w ON w.id = r.work_item_id LEFT JOIN run_llm_accounts a ON a.alias = r.trace_alias`,
	`SELECT team, count(*), min(renewed_at), count(*) FILTER (WHERE expires_at < now()) FROM leases GROUP BY team`,
	`SELECT team, count(*), sum(budget), sum(spent) FROM shifts WHERE closed_at IS NULL GROUP BY team`,
	`SELECT shift_id, sum(reserved) FROM run_budget_holds GROUP BY shift_id`,
	`SELECT phase, count(*) FROM checkpoints WHERE created_at >= now() - interval '30 days' GROUP BY phase`,
	`SELECT p.state, count(*) FROM pull_requests p JOIN pull_request_deployments d ON d.pull_request_id = p.id GROUP BY 1`,
	`SELECT action, count(*) FROM audit_log GROUP BY action`,
	`SELECT state, count(*) FROM work_item_reviews GROUP BY state`,
}

var observerRefusals = []string{
	`SELECT run_token FROM agent_runs`,
	`SELECT summary FROM agent_runs`,
	`SELECT findings, problem, solution FROM agent_runs`,
	`SELECT questions, question_answers FROM agent_runs`,
	`SELECT description FROM work_items`,
	`SELECT run_token, forge_token_id FROM leases`,
	`SELECT run_token, gateway_key_id FROM run_llm_accounts`,
	`SELECT run_token FROM run_budget_holds`,
	`SELECT detail FROM audit_log`,
	`SELECT body FROM work_item_reviews`,
	`SELECT pr_url, branch FROM checkpoints`,
	`SELECT count(*) FROM run_transcript_entries`,
	`SELECT count(*) FROM work_item_context`,
	`SELECT count(*) FROM operator_notes`,
	`SELECT count(*) FROM question_answers`,
	`SELECT count(*) FROM work_item_note_outbox`,
	`SELECT count(*) FROM claude_sessions`,
	`SELECT count(*) FROM asks`,
	`SELECT count(*) FROM chats`,
	`SELECT count(*) FROM run_llm_operator_settlements`,
}

func TestObserverRoleReadsMetadataAndNeverText(t *testing.T) {
	ctx := context.Background()
	createObserverRole(t)
	s, err := New(ctx, scratchDatabase(t, "ploeg_observer_grants"), PoolConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	for _, query := range observerReads {
		if err := asObserver(ctx, s, query); err != nil {
			t.Errorf("ploeg_observer cannot run %q: %v", query, err)
		}
	}
	for _, query := range observerRefusals {
		if err := asObserver(ctx, s, query); !isPermissionDenied(err) {
			t.Errorf("ploeg_observer ran %q: err = %v, want permission denied", query, err)
		}
	}
}

func TestObserverGrantsAreANoOpWithoutTheRoleAndApplyOnceItExists(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx, scratchDatabase(t, "ploeg_observer_absent"), PoolConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate without ploeg_observer: %v", err)
	}
	var granted bool
	if err := s.pool.QueryRow(ctx, `SELECT ploeg_grant_observer()`).Scan(&granted); err != nil {
		t.Fatal(err)
	}
	if granted {
		t.Fatal("ploeg_grant_observer() granted to a role that does not exist")
	}

	createObserverRole(t)
	t.Cleanup(func() { _, _ = s.pool.Exec(context.Background(), `DROP OWNED BY ploeg_observer`) })
	if err := asObserver(ctx, s, `SELECT count(*) FROM work_items`); !isPermissionDenied(err) {
		t.Fatalf("a role created after the migration could read before any grant: %v", err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT ploeg_grant_observer()`).Scan(&granted); err != nil {
		t.Fatal(err)
	}
	if !granted {
		t.Fatal("ploeg_grant_observer() reported no grant with the role present")
	}
	if err := asObserver(ctx, s, `SELECT state, count(*) FROM work_items GROUP BY state`); err != nil {
		t.Errorf("after ploeg_grant_observer(): %v", err)
	}
}

func createObserverRole(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := testStore.pool.Exec(ctx, `CREATE ROLE ploeg_observer NOLOGIN`); err != nil {
		t.Fatalf("create ploeg_observer: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testStore.pool.Exec(context.Background(), `DROP ROLE IF EXISTS ploeg_observer`)
	})
}

func asObserver(ctx context.Context, s *Store, query string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE ploeg_observer`); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, query)
		if err != nil {
			return err
		}
		rows.Close()
		return rows.Err()
	})
}

func isPermissionDenied(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42501"
}
