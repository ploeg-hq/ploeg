// Package store is Ploeg's Postgres data layer: work items, leases,
// checkpoints, runs, and the audit log. Every mutation commits its audit row
// in the same transaction (backlog #25); the lease columns are the lock,
// never a held transaction (backlog #23).
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ploeg-hq/ploeg/pkg/followup"
	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// ErrNoWork is returned by Claim when no claimable item exists for the team.
var ErrNoWork = errors.New("no claimable work item")

// ErrUnknownRun is returned when a run token does not match a live lease/run.
var ErrUnknownRun = errors.New("unknown or finished run")

// ExpiredLease carries the result of a single expired lease for callers
// that need the run token for post-expiry cleanup (e.g. LiteLLM key revoke).
type ExpiredLease struct {
	WorkItemID    int64
	Team          string
	RunToken      string
	InfraFailures int
}

// MaxAttempts is the retry threshold after which a repeatedly released item
// goes stale instead of re-queuing (R5).
const MaxAttempts = 3

// MaxInfraFailures is the cap on consecutive infrastructure failures (lease
// expiry without outcome). Beyond this the item goes stale with audit reason
// infra_cap instead of re-queuing (VIK-596).
const MaxInfraFailures = 10

type Store struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close()                         { s.pool.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// UnfinishedRunTokens returns the run tokens of all runs that have not yet
// finished. Used by the boot-time orphan sweep to distinguish live keys from
// leaked ones.
func (s *Store) UnfinishedRunTokens(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT run_token FROM agent_runs WHERE finished_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tokens []string
	for rows.Next() {
		var tok string
		if err := rows.Scan(&tok); err != nil {
			return nil, err
		}
		tokens = append(tokens, tok)
	}
	return tokens, rows.Err()
}

const migrationLockKey = `hashtextextended('ploeg.schema-migrations', 0)`

// Migrate applies embedded migrations in filename order, tracked in
// schema_migrations. Safe to run on every boot and from several processes at
// once: it holds a session advisory lock on one connection for the whole
// check-and-apply loop, so concurrent callers apply each migration once.
func (s *Store) Migrate(ctx context.Context) (err error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(`+migrationLockKey+`)`); err != nil {
		conn.Release()
		return fmt.Errorf("migration lock: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if _, unlockErr := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(`+migrationLockKey+`)`); unlockErr != nil {
			_ = conn.Hijack().Close(unlockCtx)
			if err == nil {
				err = fmt.Errorf("migration unlock: %w", unlockErr)
			}
			return
		}
		conn.Release()
	}()
	return applyMigrations(ctx, conn.Conn())
}

func applyMigrations(ctx context.Context, conn *pgx.Conn) error {
	if _, err := conn.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		var applied bool
		if err := conn.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`, name).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		sql, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

func audit(ctx context.Context, tx pgx.Tx, actor, action string, workItemID *int64, detail any) error {
	b, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO audit_log (actor, action, work_item_id, detail) VALUES ($1, $2, $3, $4)`,
		actor, action, workItemID, b)
	return err
}

// IngestAssigned mirrors a tracker item and queues it for a team: upsert on
// (provider, external id). Re-assignment of a live item (queued/leased) only
// refreshes the mirror; re-assignment of a finished item (done, stale,
// needs_human, awaiting_review, withdrawn) is a fresh human mandate — it re-queues the item and resets
// the attempt budget (VIK-588). The returned state is the item's actual
// post-upsert state so callers can log the truth.
func (s *Store) IngestAssigned(ctx context.Context, item work.WorkItem) (int64, work.State, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var t work.Target
	if item.Target != nil {
		t = *item.Target
	}
	var id int64
	var state string
	err = tx.QueryRow(ctx, `
		INSERT INTO work_items (provider, external_id, revision, team, state, origin, priority, title, description, url,
			external_scope, target_forge, target_owner, target_repo, target_base_branch, route_rule, route_hint,
			tracker_created_at, estimate_seconds)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
		ON CONFLICT (provider, external_id) DO UPDATE SET
			revision = EXCLUDED.revision,
			tracker_created_at = COALESCE(EXCLUDED.tracker_created_at, work_items.tracker_created_at),
			estimate_seconds = COALESCE(EXCLUDED.estimate_seconds, work_items.estimate_seconds),
			team     = CASE WHEN work_items.operator_owned THEN work_items.team ELSE EXCLUDED.team END,
			priority = EXCLUDED.priority,
			title    = EXCLUDED.title,
			description = EXCLUDED.description,
			url      = EXCLUDED.url,
			external_scope = EXCLUDED.external_scope,
			-- Never re-target a live run: the worker has already cloned, and a
			-- review round that silently moved repo would orphan its own branch
			-- and PR (R12). A re-target takes effect on the NEXT dispatch.
			target_forge       = CASE WHEN work_items.operator_owned OR work_items.state = 'leased' THEN work_items.target_forge       ELSE EXCLUDED.target_forge       END,
			target_owner       = CASE WHEN work_items.operator_owned OR work_items.state = 'leased' THEN work_items.target_owner       ELSE EXCLUDED.target_owner       END,
			target_repo        = CASE WHEN work_items.operator_owned OR work_items.state = 'leased' THEN work_items.target_repo        ELSE EXCLUDED.target_repo        END,
			target_base_branch = CASE WHEN work_items.operator_owned OR work_items.state = 'leased' THEN work_items.target_base_branch ELSE EXCLUDED.target_base_branch END,
			route_rule         = CASE WHEN work_items.operator_owned OR work_items.state = 'leased' THEN work_items.route_rule         ELSE EXCLUDED.route_rule         END,
			route_hint         = CASE WHEN work_items.operator_owned OR work_items.state = 'leased' THEN work_items.route_hint         ELSE EXCLUDED.route_hint         END,
			state    = CASE WHEN NOT work_items.operator_owned AND work_items.state IN ('ingested', 'stale', 'done', 'needs_human', 'awaiting_review', 'withdrawn') THEN 'queued' ELSE work_items.state END,
			attempts = CASE WHEN NOT work_items.operator_owned AND work_items.state IN ('stale', 'done', 'needs_human', 'awaiting_review', 'withdrawn') THEN 0 ELSE work_items.attempts END,
			next_eligible_at  = CASE WHEN work_items.operator_owned THEN work_items.next_eligible_at ELSE NULL END,
			infra_failures = CASE WHEN NOT work_items.operator_owned AND work_items.state IN ('stale', 'done', 'needs_human', 'awaiting_review', 'withdrawn') THEN 0 ELSE work_items.infra_failures END,
			updated_at = now()
		RETURNING id, state`,
		item.Provider, item.ExternalID, item.Revision, item.Team,
		string(work.StateQueued), string(work.OriginAssignment), item.Priority, item.Title, item.Description, item.URL,
		item.ExternalScope, t.Forge, t.Owner, t.Repo, t.BaseBranch, item.RouteRule, item.RouteHint,
		trackerCreated(item), trackerEstimate(item)).Scan(&id, &state)
	if err != nil {
		return 0, "", err
	}
	action := "work_item.refreshed"
	if state == string(work.StateQueued) {
		action = "work_item.queued"
	}
	detail := map[string]any{"external_id": item.ExternalID, "team": item.Team, "title": item.Title, "state": state,
		"external_scope": item.ExternalScope}
	if t.Resolved() {
		detail["target"] = t.Key()
		detail["route_rule"] = item.RouteRule
	}
	if item.RouteHint != "" {
		detail["route_hint"] = item.RouteHint
	}
	if err := audit(ctx, tx, "webhook:"+item.Provider, action, &id, detail); err != nil {
		return 0, "", err
	}
	return id, work.State(state), tx.Commit(ctx)
}

func trackerCreated(item work.WorkItem) *time.Time {
	if item.TrackerCreatedAt.IsZero() {
		return nil
	}
	at := item.TrackerCreatedAt.UTC()
	return &at
}

func trackerEstimate(item work.WorkItem) *int64 {
	if item.EstimateSeconds == nil || *item.EstimateSeconds < 0 {
		return nil
	}
	return item.EstimateSeconds
}

// RefuseRoute records a tracker item that routing refused (ADR-0038): an
// audit row and no Work Item, so nothing is queued anywhere. code is a
// target.Refusal code and allowed the repo/ labels the board allows.
func (s *Store) RefuseRoute(ctx context.Context, item work.WorkItem, code, reason string, allowed []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if allowed == nil {
		allowed = []string{}
	}
	detail := map[string]any{"external_id": item.ExternalID, "team": item.Team, "title": item.Title,
		"external_scope": item.ExternalScope, "labels": item.Labels, "reason": reason,
		"code": code, "allowed_labels": allowed}
	if err := audit(ctx, tx, "webhook:"+item.Provider, "work_item.route_refused", nil, detail); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Claimed is what a worker gets back from Claim: the item plus the run token
// it must use for renew/checkpoint/outcome calls.
type Claimed struct {
	Item     work.WorkItem
	RunToken string
	Deadline time.Time
}

// Claim atomically leases the highest-priority queued item for a team using
// FOR UPDATE SKIP LOCKED, committed immediately (backlog #23). Returns
// ErrNoWork when the queue is empty — the empty-handed worker convention
// (backlog #49).
func (s *Store) Claim(ctx context.Context, team string, ttl time.Duration) (*Claimed, error) {
	return s.ClaimWithin(ctx, team, ttl, 0)
}

const nextClaimableQuery = `
			SELECT id FROM work_items
			WHERE team = $1 AND state = 'queued' AND NOT operator_owned AND (next_eligible_at IS NULL OR next_eligible_at <= now())
			  AND NOT EXISTS (SELECT 1 FROM shifts sh WHERE sh.work_item_id = work_items.id AND sh.closed_at IS NULL)
			ORDER BY priority DESC, created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		`

// ClaimWithin is Claim bounded by the team's concurrency cap. With
// maxRunning > 0 it returns ErrNoWork while the team already has maxRunning
// running Runs, exactly as if the queue were empty; 0 means unlimited.
//
// A Work Item with a live Shift is never leased here: its Runs are claimed
// through ClaimRoleWithin, against the Shift's pool.
func (s *Store) ClaimWithin(ctx context.Context, team string, ttl time.Duration, maxRunning int) (*Claimed, error) {
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if full, err := teamAtCapacity(ctx, tx, team, maxRunning); err != nil {
		return nil, err
	} else if full {
		return nil, ErrNoWork
	}

	var it work.WorkItem
	var id int64
	var t work.Target
	err = tx.QueryRow(ctx, `
		UPDATE work_items SET state = 'leased', attempts = attempts + 1, updated_at = now()
		WHERE id = (`+nextClaimableQuery+`)
		RETURNING id, provider, external_id, revision, team, origin, priority, title, description, url,
			external_scope, target_forge, target_owner, target_repo, target_base_branch, route_rule, route_hint,
			COALESCE(source_work_item_id::text, ''), source_branch, source_pr`,
		team).Scan(&id, &it.Provider, &it.ExternalID, &it.Revision, &it.Team, &it.Origin, &it.Priority, &it.Title, &it.Description, &it.URL,
		&it.ExternalScope, &t.Forge, &t.Owner, &t.Repo, &t.BaseBranch, &it.RouteRule, &it.RouteHint,
		&it.SourceWorkItemID, &it.SourceBranch, &it.SourcePR)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoWork
	}
	if err != nil {
		return nil, err
	}
	it.ID = fmt.Sprint(id)
	it.State = work.StateLeased
	if t.Resolved() {
		it.Target = &t
	}

	deadline := time.Now().Add(ttl)
	if _, err := tx.Exec(ctx,
		`INSERT INTO leases (work_item_id, team, run_token, expires_at) VALUES ($1, $2, $3, $4)`,
		id, team, token, deadline); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO agent_runs (work_item_id, team, run_token) VALUES ($1, $2, $3)`,
		id, team, token); err != nil {
		return nil, err
	}
	leaseDetail := map[string]any{"expires_at": deadline}
	if t.Resolved() {
		// Per-run target history without a schema change: the item may be
		// re-targeted later, but this row records where THIS run went.
		leaseDetail["target"] = t.Key()
	}
	if err := audit(ctx, tx, "team:"+team, "lease.acquired", &id, leaseDetail); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &Claimed{Item: it, RunToken: token, Deadline: deadline}, nil
}

// Renew extends the deadline held by a run token (backlog #13).
//
// Two deadlines move together. The Lease is the writer's exclusivity; the
// Run's own expires_at is its liveness (ADR-0010 — readers hold no Lease, so
// lease expiry could never detect a dead reader). A reader renews only its
// Run; a writer renews both, or ExpireRuns would reclaim its Run out from
// under a perfectly live Lease. Legacy runs (no expires_at) renew only the
// Lease, exactly as before.
func (s *Store) Renew(ctx context.Context, runToken string, ttl time.Duration) (time.Time, error) {
	deadline := time.Now().Add(ttl)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	runTag, err := tx.Exec(ctx,
		`UPDATE agent_runs SET expires_at = $1
		 WHERE run_token = $2 AND state = 'running' AND expires_at IS NOT NULL`,
		deadline, runToken)
	if err != nil {
		return time.Time{}, err
	}
	leaseTag, err := tx.Exec(ctx,
		`UPDATE leases SET expires_at = $1, renewed_at = now() WHERE run_token = $2`,
		deadline, runToken)
	if err != nil {
		return time.Time{}, err
	}
	if runTag.RowsAffected() == 0 && leaseTag.RowsAffected() == 0 {
		return time.Time{}, ErrUnknownRun
	}
	return deadline, tx.Commit(ctx)
}

// Checkpoint records durable progress for the item owned by the run token.
// It returns ErrCheckpointRefused for a checkpoint that names another branch
// than the Run's or a pull request outside the Work Item's repository.
func (s *Store) Checkpoint(ctx context.Context, runToken string, cp work.Checkpoint) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Resolve via the Lease when one exists; a reader holds none (ADR-0010),
	// so fall back to its running Run — progress records must not be a
	// writer-only privilege.
	var id int64
	var team string
	err = tx.QueryRow(ctx,
		`SELECT work_item_id, team FROM leases WHERE run_token = $1`, runToken).Scan(&id, &team)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx,
			`SELECT work_item_id, team FROM agent_runs WHERE run_token = $1 AND state = 'running'`,
			runToken).Scan(&id, &team)
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUnknownRun
		}
		return err
	}
	if binding, running, err := loadRunBinding(ctx, tx, runToken); err != nil {
		return err
	} else if running {
		if why := checkpointMismatch(cp, binding); why != "" {
			return fmt.Errorf("%w: %s", ErrCheckpointRefused, why)
		}
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO checkpoints (work_item_id, phase, branch, pr_url, node_name, pod_uid) VALUES ($1, $2, $3, $4, $5, $6)`,
		id, cp.Phase, cp.Branch, cp.PRURL, cp.NodeName, cp.PodUID); err != nil {
		return err
	}
	detail := map[string]any{"phase": cp.Phase, "branch": cp.Branch, "pr_url": cp.PRURL, "node_name": cp.NodeName, "pod_uid": cp.PodUID}
	if cp.Commit != "" {
		detail["commit"] = cp.Commit
	}
	if len(cp.InstructionFiles) > 0 {
		detail["instruction_files"] = cp.InstructionFiles
	}
	if err := audit(ctx, tx, "team:"+team, "checkpoint.written", &id, detail); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// OutcomeResult tells the caller what a report landed on, so the outcome
// handler knows whether a Shift may want evaluating.
type OutcomeResult struct {
	WorkItemID int64
	// ShiftID is nil for a legacy (shift-less) run.
	ShiftID *int64
	// ForgeTokenID is the per-run push credential to revoke, if one was
	// minted (ADR-0013 tier 2). Empty for readers and for deployments
	// without a forge admin credential.
	ForgeTokenID string
	// Created lists the Work Items this report created, in proposal order.
	// Rejected proposals are absent here and recorded in the audit log.
	Created []CreatedWorkItem
}

// ReportOutcome ends the run: records the outcome and findings, releases the
// lease, and settles spend. A stuck outcome requires a reason (R4).
//
// Who moves the Work Item depends on who owns its lifecycle. A legacy run is
// the item's whole engagement, so its report transitions the item per
// StateForOutcome, exactly as always. A Shift run is one voice among several —
// three readers reporting must not flip the item's state three times — so the
// item moves only when the shift engine closes the Shift.
func (s *Store) ReportOutcome(ctx context.Context, runToken string, rep harnessReport) (OutcomeResult, error) {
	if rep.Outcome == work.OutcomeStuck && rep.StuckReason == "" {
		return OutcomeResult{}, errors.New("stuck outcome requires a stuck_reason (R4)")
	}
	rep = rep.normalized()
	digest, err := rep.digest()
	if err != nil {
		return OutcomeResult{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return OutcomeResult{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var admitted deliveryAdmission
	binding, running, err := loadRunBinding(ctx, tx, runToken)
	if err != nil {
		return OutcomeResult{}, err
	}
	if running {
		rep, admitted = admitDelivery(rep, binding)
	}
	next := work.StateForOutcome(rep.Outcome)
	var verification, delivery []byte
	if rep.Verification != nil {
		if verification, err = json.Marshal(rep.Verification); err != nil {
			return OutcomeResult{}, err
		}
	}
	if rep.Delivery != nil {
		if delivery, err = json.Marshal(rep.Delivery); err != nil {
			return OutcomeResult{}, err
		}
	}
	var deliverySource *string
	if admitted.source != "" {
		deliverySource = &admitted.source
	}

	// The advance-once compare-and-swap. This used to be the lease DELETE, but
	// readers hold no Lease (ADR-0010), so the lease can no longer be the thing
	// exactly one transaction wins. The Run's state transition can: only one
	// caller moves a row out of 'running', and a swept or replayed token finds
	// nothing to move.
	var id, runID int64
	var team string
	var shiftID *int64
	if err := tx.QueryRow(ctx, `
		UPDATE agent_runs
		SET state = 'finished', finished_at = now(), outcome = $1, summary = $2,
		    stuck_reason = $3, links = $4, usage = $5, failure_reason = $6, findings = $7,
		    verdict = CASE WHEN writes THEN '' ELSE $8 END, outcome_digest = $10,
		    problem = CASE WHEN writes THEN $11 ELSE '' END, solution = CASE WHEN writes THEN $12 ELSE '' END,
		    verification = CASE WHEN writes THEN $13::jsonb ELSE NULL END,
		    evidence_version = $14, delivery = $15::jsonb, delivery_source = $16
		WHERE run_token = $9 AND state = 'running'
		RETURNING id, work_item_id, team, shift_id`,
		string(rep.Outcome), rep.Summary, rep.StuckReason, rep.Links, rep.Usage,
		rep.FailureReason, rep.Findings, rep.Verdict, runToken, digest, rep.Problem, rep.Solution, verification,
		CurrentEvidenceVersion, delivery, deliverySource).Scan(&runID, &id, &team, &shiftID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if err := tx.QueryRow(ctx, `SELECT work_item_id,shift_id FROM agent_runs
				WHERE run_token=$1 AND state='finished' AND outcome_digest=$2
				AND EXISTS (SELECT 1 FROM run_llm_accounts WHERE run_token=$1)`, runToken, digest).
				Scan(&id, &shiftID); err == nil {
				return OutcomeResult{WorkItemID: id, ShiftID: shiftID}, nil
			}
			return OutcomeResult{}, ErrUnknownRun
		}
		return OutcomeResult{}, err
	}
	// Writers hold a Lease; readers do not, so zero rows here is normal rather
	// than an error. Releasing it revokes the push credential minted with it
	// (ADR-0013).
	var forgeTokenID string
	if err := tx.QueryRow(ctx,
		`DELETE FROM leases WHERE run_token = $1 RETURNING forge_token_id`, runToken).
		Scan(&forgeTokenID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return OutcomeResult{}, err
	}
	// Settlement (ADR-0012): record what was actually spent. The authorization
	// needs no explicit release — `reserved` is summed over running Runs, and
	// this one is no longer running.
	if shiftID != nil {
		if _, err := tx.Exec(ctx, `
			UPDATE shifts SET spent = spent + COALESCE(($2::jsonb->>'costUsd')::numeric, 0)
			WHERE id = $1 AND NOT EXISTS (SELECT 1 FROM run_llm_accounts WHERE run_token = $3)`, *shiftID, rep.Usage, runToken); err != nil {
			return OutcomeResult{}, err
		}
	}
	if shiftID == nil {
		// A failed outcome re-queues until the retry threshold, then stale (R5).
		if next == work.StateQueued {
			if _, err := tx.Exec(ctx, `
				UPDATE work_items
				SET state = CASE WHEN attempts >= $2 THEN 'stale' ELSE 'queued' END, updated_at = now()
				WHERE id = $1`, id, MaxAttempts); err != nil {
				return OutcomeResult{}, err
			}
		} else {
			if _, err := tx.Exec(ctx,
				`UPDATE work_items SET state = $2, updated_at = now() WHERE id = $1`, id, string(next)); err != nil {
				return OutcomeResult{}, err
			}
		}
	}
	outcomeDetail := map[string]any{"summary": rep.Summary, "stuck_reason": rep.StuckReason, "links": rep.Links}
	if admitted.source != "" {
		outcomeDetail["delivery_source"] = admitted.source
	}
	if rep.Delivery != nil {
		outcomeDetail["delivery"] = rep.Delivery
	}
	if admitted.changed != "" {
		outcomeDetail["delivery_changed_outcome"] = admitted.changed
	}
	if err := audit(ctx, tx, "team:"+team, "outcome."+string(rep.Outcome), &id, outcomeDetail); err != nil {
		return OutcomeResult{}, err
	}
	created, err := createWorkItems(ctx, tx, runID, id, team, rep)
	if err != nil {
		return OutcomeResult{}, err
	}
	return OutcomeResult{WorkItemID: id, ShiftID: shiftID, ForgeTokenID: forgeTokenID, Created: created}, tx.Commit(ctx)
}

// IsOutcomeReplay reports whether rep repeats the outcome that already
// finished the run, which ReportOutcome answers with the original success.
// The outcome handler asks before writing an inline checkpoint, because a
// finished run no longer accepts checkpoints.
func (s *Store) IsOutcomeReplay(ctx context.Context, runToken string, rep harnessReport) (bool, error) {
	digest, err := rep.normalized().digest()
	if err != nil {
		return false, err
	}
	var replay bool
	err = s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM agent_runs
		WHERE run_token=$1 AND state='finished' AND outcome_digest=$2
		AND EXISTS (SELECT 1 FROM run_llm_accounts WHERE run_token=$1))`, runToken, digest).Scan(&replay)
	return replay, err
}

func (r harnessReport) normalized() harnessReport {
	if r.Links == nil {
		r.Links = []string{}
	}
	return r
}

func (r harnessReport) digest() (string, error) {
	encoded, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded)), nil
}

// harnessReport mirrors harness.OutcomeReport without importing the package
// (store stays ignorant of transport shapes). Usage is opaque JSON
// (harness.Usage), persisted as-is into agent_runs.usage. FailureReason is
// the ploeg-internal failure taxonomy (VIK-597) — never transmitted to a
// harness.
type harnessReport struct {
	Outcome       work.Outcome
	Summary       string
	StuckReason   string
	Links         []string
	Usage         json.RawMessage
	FailureReason *string                   // nil = unclassified; set for failed outcomes (infra_llm, lease_lost, etc.)
	Findings      string                    // a reading Run's blackboard contribution (ADR-0011); empty for most writers
	Verdict       string                    // a reading Run's approve/request_changes (ADR-0017); empty = no opinion
	Problem       string                    `json:",omitempty"` // a writing Run's account of its change (ADR-0042)
	Solution      string                    `json:",omitempty"`
	Created       []harness.CreatedWorkItem `json:",omitempty"`
	Verification  *harness.Verification     `json:",omitempty"` // the worker's own check run; stored for a writing Run only
	Delivery      *harness.Delivery         `json:",omitempty"` // the worker's forge observation (ADR-0059); bound to the Run before it is stored
	policyFor     func(team string) followup.Policy
	knownTeam     func(string) bool
}

// WithCreatedWork attaches the Work Items a Run proposes (ADR-0031).
// policyFor returns the source Team's limits; nil means followup.Default.
// knownTeam reports whether a requested Team exists.
func (r harnessReport) WithCreatedWork(items []harness.CreatedWorkItem, policyFor func(team string) followup.Policy, knownTeam func(string) bool) harnessReport {
	r.Created = items
	r.policyFor = policyFor
	r.knownTeam = knownTeam
	return r
}

// Report is the store-level outcome input. usage and failureReason may be nil.
func Report(outcome work.Outcome, summary, stuckReason string, links []string, usage json.RawMessage, failureReason *string) harnessReport {
	return harnessReport{Outcome: outcome, Summary: summary, StuckReason: stuckReason, Links: links, Usage: usage, FailureReason: failureReason}
}

// WithFindings attaches a reading Run's findings to the report.
func (r harnessReport) WithFindings(findings string) harnessReport {
	r.Findings = findings
	return r
}

// WithVerdict attaches a reading Run's verdict (ADR-0017).
func (r harnessReport) WithVerdict(verdict string) harnessReport {
	r.Verdict = verdict
	return r
}

// WithProblemAndSolution attaches a writing Run's account of its change
// (ADR-0042). ReportOutcome drops it from a reading Run.
func (r harnessReport) WithProblemAndSolution(problem, solution string) harnessReport {
	r.Problem, r.Solution = problem, solution
	return r
}

// WithVerification attaches the worker's structured verification of a
// writing Run. ReportOutcome drops it from a reading Run.
func (r harnessReport) WithVerification(v *harness.Verification) harnessReport {
	r.Verification = v
	return r
}

// WithDelivery attaches the worker's delivery record (ADR-0059).
// ReportOutcome checks it against the Run before it stores it.
func (r harnessReport) WithDelivery(d *harness.Delivery) harnessReport {
	r.Delivery = d
	return r
}

// AuditForgeEvent records a normalized forge event. The audit log is the
// whole of what this change does with one: the trail exists from the day the
// endpoint is wired, so when routing lands there is evidence of what has been
// arriving rather than a guess.
//
// The event BODY is deliberately not stored here. It is text written outside
// the factory (backlog #9), and an audit row is read by humans and by future
// prompts alike; the metadata is what routing will need. Since ADR-0045 that
// metadata includes who acted, the review verdict and the merge facts; a
// field the forge did not report is left out of the row.
func (s *Store) AuditForgeEvent(ctx context.Context, provider string, ev ForgeEventAudit) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	detail := map[string]any{"repo": ev.Repo, "pr": ev.PR, "branch": ev.Branch}
	for key, value := range map[string]string{
		"actor": ev.Actor, "review": ev.Review, "head_sha": ev.HeadSHA,
		"merge_commit_sha": ev.MergeCommitSHA, "merged_by": ev.MergedBy,
	} {
		if value != "" {
			detail[key] = value
		}
	}
	if ev.MergedAt != nil {
		detail["merged_at"] = ev.MergedAt.UTC().Format(time.RFC3339)
	}
	if err := audit(ctx, tx, "webhook:"+provider, "forge."+ev.Kind, nil, detail); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ForgeEventAudit is one normalized forge event as the audit log records it.
// Empty strings and a nil MergedAt are facts the forge did not report.
type ForgeEventAudit struct {
	Kind           string
	Repo           string
	Branch         string
	PR             int
	Actor          string
	Review         string
	HeadSHA        string
	MergeCommitSHA string
	MergedBy       string
	MergedAt       *time.Time
}

// WorkItem reads one item by id — what the shift engine needs to know where
// to publish: the Work Target names the repository, the provider names the
// tracker to write back to.
func (s *Store) WorkItem(ctx context.Context, id int64) (work.WorkItem, error) {
	var it work.WorkItem
	var t work.Target
	err := s.pool.QueryRow(ctx, `
		SELECT provider, external_id, revision, team, state, origin, priority, title, description, url,
			external_scope, target_forge, target_owner, target_repo, target_base_branch, route_rule, route_hint,
			COALESCE(source_work_item_id::text, ''), source_branch, source_pr
		FROM work_items WHERE id = $1`, id).
		Scan(&it.Provider, &it.ExternalID, &it.Revision, &it.Team, &it.State, &it.Origin,
			&it.Priority, &it.Title, &it.Description, &it.URL,
			&it.ExternalScope, &t.Forge, &t.Owner, &t.Repo, &t.BaseBranch, &it.RouteRule, &it.RouteHint,
			&it.SourceWorkItemID, &it.SourceBranch, &it.SourcePR)
	if err != nil {
		return work.WorkItem{}, err
	}
	it.ID = fmt.Sprint(id)
	if t.Resolved() {
		it.Target = &t
	}
	return it, nil
}

// SettleItem moves a Work Item to its post-Shift state, with the reason on
// the audit row. This is the shift engine's transition: for Shift runs
// ReportOutcome leaves the item alone, and the engine moves it exactly once —
// at close, at a terminal stuck, or when the pool runs dry (R4: the reason
// travels with it).
//
// A failed outcome still respects the retry threshold, so a Shift that ends
// in failure re-queues and stales exactly like a legacy run (R5).
//
// A withdrawn item is left withdrawn: the withdrawal closed its Shift, and a
// late evaluator must not settle it back into the queue.
//
// Returns the state actually written, which is not always the one asked for:
// queued coerces to stale at the attempt cap. The caller needs the difference
// — "failed, retrying" is not terminal and must not notify the tracker, while
// "failed, gave up" is and must. The audit row records the effective state for
// the same reason: it used to claim work_item.queued for a row that went stale.
func (s *Store) SettleItem(ctx context.Context, workItemID int64, next work.State, reason string) (work.State, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	settled, err := settleItemTx(ctx, tx, workItemID, next, reason)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return settled, nil
}

func settleItemTx(ctx context.Context, tx pgx.Tx, workItemID int64, next work.State, reason string) (work.State, error) {
	var operatorOwned bool
	var current string
	if err := tx.QueryRow(ctx, `SELECT operator_owned, state FROM work_items WHERE id=$1 FOR UPDATE`, workItemID).Scan(&operatorOwned, &current); err != nil {
		return "", err
	}
	if operatorOwned {
		return "", ErrExecutionConflict
	}
	if work.State(current) == work.StateWithdrawn {
		return work.StateWithdrawn, nil
	}

	var written string
	if next == work.StateQueued {
		if err := tx.QueryRow(ctx, `
			UPDATE work_items
			SET state = CASE WHEN attempts >= $2 THEN 'stale' ELSE 'queued' END, updated_at = now()
			WHERE id = $1
			RETURNING state`, workItemID, MaxAttempts).Scan(&written); err != nil {
			return "", err
		}
	} else if err := tx.QueryRow(ctx,
		`UPDATE work_items SET state = $2, updated_at = now() WHERE id = $1 RETURNING state`,
		workItemID, string(next)).Scan(&written); err != nil {
		return "", err
	}
	if err := audit(ctx, tx, "ploegd:shift-engine", "work_item."+written, &workItemID,
		map[string]any{"reason": reason}); err != nil {
		return "", err
	}
	return work.State(written), nil
}

// ExpireLeases releases every overdue lease: the run is recorded as failed
// (reason lease_expired) and the item re-queues or goes stale per the retry
// threshold — the crash-safety mechanic (R2/R5). Returns expired lease info
// including run tokens so callers can perform post-expiry cleanup (e.g.
// LiteLLM key revoke).
//
// Shift leases (shift_id set) are deliberately excluded: a Shift writer's
// liveness is its Run's expires_at, reclaimed by ExpireRuns — which also
// drops the lease — and its Work Item belongs to the shift engine. Processing
// it here would bounce the item back to 'queued' mid-Shift and charge the
// infra-failure backoff for a lifecycle this sweep does not own.
func (s *Store) ExpireLeases(ctx context.Context) ([]ExpiredLease, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	rows, err := tx.Query(ctx,
		`DELETE FROM leases WHERE expires_at < now() AND shift_id IS NULL
		 RETURNING work_item_id, team, run_token`)
	if err != nil {
		return nil, err
	}
	var exp []ExpiredLease
	for rows.Next() {
		var e ExpiredLease
		if err := rows.Scan(&e.WorkItemID, &e.Team, &e.RunToken); err != nil {
			return nil, err
		}
		exp = append(exp, e)
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}

	for i := range exp {
		e := &exp[i]
		// state = 'finished' matters as much as finished_at: it closes the
		// advance-once CAS in ReportOutcome so a swept run cannot report, and
		// it releases the run's budget authorization, which is summed over
		// running rows only (ADR-0012).
		if _, err := tx.Exec(ctx, `
			UPDATE agent_runs SET state = 'finished', finished_at = now(), outcome = 'failed',
			    summary = 'lease expired', failure_reason = 'lease_lost'
			WHERE run_token = $1 AND finished_at IS NULL`, e.RunToken); err != nil {
			return nil, err
		}
		// Infra failure: lease expired without a reported outcome — refund the
		// attempt that Claim already charged and apply the infra-failure backoff.
		// Agent failures (reported outcomes) go through ReportOutcome instead.
		var newState string
		if err := tx.QueryRow(ctx, `
			UPDATE work_items
			SET state = CASE WHEN infra_failures + 1 >= $2 THEN 'stale' ELSE 'queued' END,
			    attempts = GREATEST(attempts - 1, 0),
			    infra_failures = infra_failures + 1,
			    next_eligible_at = CASE
				WHEN infra_failures + 1 >= $2 THEN NULL
				ELSE now() + (CASE infra_failures
				    WHEN 0 THEN INTERVAL '1 minute'
				    WHEN 1 THEN INTERVAL '5 minutes'
				    WHEN 2 THEN INTERVAL '15 minutes'
				    ELSE INTERVAL '60 minutes'
				END)
			    END,
			    updated_at = now()
			WHERE id = $1 AND state = 'leased'
			RETURNING state, infra_failures`,
			e.WorkItemID, MaxInfraFailures).Scan(&newState, &e.InfraFailures); err != nil {
			return nil, err
		}
		action := "lease.expired"
		if newState == "stale" {
			action = "infra_cap"
		}
		if err := audit(ctx, tx, "ploegd:sweeper", action, &e.WorkItemID,
			map[string]any{"team": e.Team, "infra_failures": e.InfraFailures}); err != nil {
			return nil, err
		}
	}
	return exp, tx.Commit(ctx)
}

// QueueSnapshot lists a team's queue (and everything else non-done) for
// operator visibility — deliberately not a board.
func (s *Store) QueueSnapshot(ctx context.Context, team string) ([]work.WorkItem, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, provider, external_id, revision, team, state, origin, priority, title, description, url, created_at, updated_at,
			external_scope, target_forge, target_owner, target_repo, target_base_branch, route_rule
		FROM work_items
		WHERE team = $1 AND state <> 'done'
		ORDER BY priority DESC, created_at`, team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []work.WorkItem
	for rows.Next() {
		var it work.WorkItem
		var id int64
		var t work.Target
		if err := rows.Scan(&id, &it.Provider, &it.ExternalID, &it.Revision, &it.Team, &it.State,
			&it.Origin, &it.Priority, &it.Title, &it.Description, &it.URL, &it.CreatedAt, &it.UpdatedAt,
			&it.ExternalScope, &t.Forge, &t.Owner, &t.Repo, &t.BaseBranch, &it.RouteRule); err != nil {
			return nil, err
		}
		it.ID = fmt.Sprint(id)
		if t.Resolved() {
			it.Target = &t
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

func newToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
