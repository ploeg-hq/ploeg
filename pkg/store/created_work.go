package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/followup"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// ErrNotProposed is returned when a decision is asked for a Work Item that
// is not waiting in the proposed state.
var ErrNotProposed = errors.New("work item is not proposed")

// CreatedWorkItem is a Work Item a Run created (ADR-0031).
type CreatedWorkItem struct {
	ID    int64
	Team  string
	State work.State
}

func createWorkItems(ctx context.Context, tx pgx.Tx, runID, sourceID int64, team string, rep harnessReport) ([]CreatedWorkItem, error) {
	if len(rep.Created) == 0 {
		return nil, nil
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('ploeg.created-work:' || $1))`, team); err != nil {
		return nil, err
	}
	var depth, priority int
	var root int64
	var t work.Target
	if err := tx.QueryRow(ctx, `
		SELECT depth, COALESCE(root_work_item_id, id), priority,
			target_forge, target_owner, target_repo, target_base_branch
		FROM work_items WHERE id = $1`, sourceID).
		Scan(&depth, &root, &priority, &t.Forge, &t.Owner, &t.Repo, &t.BaseBranch); err != nil {
		return nil, err
	}
	var open int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM work_items wi JOIN agent_runs r ON r.id = wi.source_run_id
		WHERE r.team = $1 AND wi.state IN ('proposed', 'queued', 'leased')`, team).Scan(&open); err != nil {
		return nil, err
	}
	var allotted float64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(budget_usd), 0)::float8 FROM work_items WHERE root_work_item_id = $1`, root).Scan(&allotted); err != nil {
		return nil, err
	}
	policy := followup.Default()
	if rep.policyFor != nil {
		policy = rep.policyFor(team)
	}
	decisions := followup.Decide(policy, followup.Source{
		Team: team, Outcome: rep.Outcome, Depth: depth, OpenCreated: open, PoolAllotted: allotted,
	}, rep.Created, rep.knownTeam)

	var created []CreatedWorkItem
	for _, d := range decisions {
		detail := map[string]any{
			"index": d.Index, "title": d.Proposal.Title, "kind": string(d.Proposal.Kind), "ready": d.Proposal.Ready,
			"team": d.Team, "depth": d.Depth, "source_work_item_id": sourceID, "source_run_id": runID,
		}
		if !d.Accepted {
			detail["reason"] = d.Reason
			if err := audit(ctx, tx, "team:"+team, "created_work_item.rejected", &sourceID, detail); err != nil {
				return nil, err
			}
			continue
		}
		var id int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO work_items (provider, external_id, team, state, origin, priority, title, description,
				target_forge, target_owner, target_repo, target_base_branch, route_rule,
				source_work_item_id, source_run_id, root_work_item_id, depth, ready, created_kind, budget_usd)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 'created-work', $13, $14, $15, $16, $17, $18, $19)
			RETURNING id`,
			work.ProviderPloeg, fmt.Sprintf("run-%d-%d", runID, d.Index+1), d.Team, string(d.State),
			string(work.OriginFollowUp), priority, d.Proposal.Title, d.Proposal.Description,
			t.Forge, t.Owner, t.Repo, t.BaseBranch,
			sourceID, runID, root, d.Depth, d.Proposal.Ready, string(d.Proposal.Kind), d.BudgetUSD).Scan(&id); err != nil {
			return nil, err
		}
		detail["work_item_id"] = id
		detail["state"] = string(d.State)
		detail["budget_usd"] = d.BudgetUSD
		if err := audit(ctx, tx, "team:"+team, "created_work_item.accepted", &sourceID, detail); err != nil {
			return nil, err
		}
		if err := audit(ctx, tx, "team:"+team, "work_item."+string(d.State), &id, detail); err != nil {
			return nil, err
		}
		created = append(created, CreatedWorkItem{ID: id, Team: d.Team, State: d.State})
	}
	return created, nil
}

// DecideProposed approves or rejects a proposed Work Item on a person's
// behalf. Approval queues it for its Team; rejection closes it as done,
// releases its allotted budget and records the reason. teams scopes the
// decision as it scopes operator reads: nil allows every Team.
func (s *Store) DecideProposed(ctx context.Context, id int64, approve bool, actor, reason string, teams []string) (work.WorkItem, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return work.WorkItem{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var state, team string
	var source, sourceRun *int64
	err = tx.QueryRow(ctx, `SELECT state, team, source_work_item_id, source_run_id FROM work_items WHERE id = $1 FOR UPDATE`, id).
		Scan(&state, &team, &source, &sourceRun)
	if errors.Is(err, pgx.ErrNoRows) {
		return work.WorkItem{}, ErrOperatorNotFound
	}
	if err != nil {
		return work.WorkItem{}, err
	}
	if !teamAllowed(teams, team) {
		return work.WorkItem{}, ErrOperatorNotFound
	}
	if state != string(work.StateProposed) {
		return work.WorkItem{}, ErrNotProposed
	}
	next, action := work.StateQueued, "work_item.approved"
	query := `UPDATE work_items SET state = 'queued', updated_at = now() WHERE id = $1`
	if !approve {
		next, action = work.StateDone, "work_item.rejected"
		query = `UPDATE work_items SET state = 'done', budget_usd = 0, updated_at = now() WHERE id = $1`
	}
	if _, err := tx.Exec(ctx, query, id); err != nil {
		return work.WorkItem{}, err
	}
	if err := audit(ctx, tx, actor, action, &id, map[string]any{
		"reason": reason, "state": string(next), "team": team,
		"source_work_item_id": source, "source_run_id": sourceRun,
	}); err != nil {
		return work.WorkItem{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return work.WorkItem{}, err
	}
	return s.WorkItem(ctx, id)
}

func teamAllowed(teams []string, team string) bool {
	if teams == nil {
		return true
	}
	for _, t := range teams {
		if t == team {
			return true
		}
	}
	return false
}
