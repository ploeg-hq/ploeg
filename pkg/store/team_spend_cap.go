package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/spendcap"
)

// ErrTeamSpendCapReached means a claim would take its Team over its spend cap
// for the current period. Like ErrBudgetExhausted it is a gate outcome: no
// Run starts, no key is minted and no attempt is burned.
var ErrTeamSpendCapReached = errors.New("team spend cap reached")

// SpendCapRefusal is the ErrTeamSpendCapReached a claim returns, with the
// figures that refused it.
type SpendCapRefusal struct {
	Team         string
	Cap          spendcap.Cap
	PeriodStart  time.Time
	SettledUSD   float64
	HeldUSD      float64
	RequestedUSD float64
	WorkItemID   int64
	RunID        int64
}

// Error states the refusal as the operator reads it.
func (r *SpendCapRefusal) Error() string { return r.Reason() }

// Is makes a refusal match ErrTeamSpendCapReached.
func (r *SpendCapRefusal) Is(target error) bool { return target == ErrTeamSpendCapReached }

// Reason names the team, the cap, its period and the figures that refused
// the claim.
func (r *SpendCapRefusal) Reason() string {
	return fmt.Sprintf("team %s reached its spend cap of %.2f USD for the %s starting %s: %.2f USD settled and %.2f USD held, and the Run would hold %.2f USD",
		r.Team, r.Cap.USD, r.Cap.Period, r.PeriodStart.Format(time.DateOnly), r.SettledUSD, r.HeldUSD, r.RequestedUSD)
}

// TeamPeriodSpend is a capped Team's spend in its current period.
type TeamPeriodSpend struct {
	Team        string
	Cap         spendcap.Cap
	PeriodStart time.Time
	PeriodEnd   time.Time
	SettledUSD  float64
	HeldUSD     float64
}

// SetTeamSpendCaps replaces the spend caps the claim paths enforce. ploegd
// sets them once at boot, before it serves a claim.
func (s *Store) SetTeamSpendCaps(caps spendcap.Caps) {
	s.spendCaps = spendcap.Caps{}
	for team, c := range caps {
		if c.Capped() {
			s.spendCaps[team] = c
		}
	}
}

// TeamSpendCap returns the team's spend cap; a zero cap when it has none.
func (s *Store) TeamSpendCap(team string) spendcap.Cap { return s.spendCaps[team] }

func (s *Store) spendNow() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now()
}

const teamPeriodSettledQuery = `
	SELECT COALESCE(sum(v), 0)::float8 FROM (
		SELECT (a.detail->>'delta')::numeric AS v FROM audit_log a JOIN work_items i ON i.id = a.work_item_id
		WHERE a.action = 'llm.reconciled' AND a.at >= $2 AND jsonb_typeof(a.detail->'delta') = 'number' AND i.team = $1
		UNION ALL
		SELECT (r.usage->>'costUsd')::numeric FROM agent_runs r JOIN work_items i ON i.id = r.work_item_id
		WHERE r.finished_at IS NOT NULL AND r.finished_at >= $2 AND jsonb_typeof(r.usage->'costUsd') = 'number' AND i.team = $1
		AND NOT EXISTS (SELECT 1 FROM run_llm_accounts x WHERE x.run_token = r.run_token)) settled`

const teamHeldQuery = `
	SELECT COALESCE(sum(h.reserved), 0)::float8 FROM run_budget_holds h
	JOIN agent_runs r ON r.run_token = h.run_token JOIN work_items i ON i.id = r.work_item_id
	WHERE i.team = $1 AND h.run_token IN (
		SELECT run_token FROM agent_runs WHERE state = 'running'
		UNION SELECT run_token FROM run_llm_accounts WHERE state <> 'reconciled' OR COALESCE(observed_spend, 0) > reconciled_spend)`

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func teamPeriodSpend(ctx context.Context, q rowQuerier, team string, since time.Time) (settled, held float64, err error) {
	if err := q.QueryRow(ctx, teamPeriodSettledQuery, team, since).Scan(&settled); err != nil {
		return 0, 0, err
	}
	if err := q.QueryRow(ctx, teamHeldQuery, team).Scan(&held); err != nil {
		return 0, 0, err
	}
	return settled, held, nil
}

// TeamPeriodSpends reads every capped Team's spend in its current period,
// sorted by team.
func (s *Store) TeamPeriodSpends(ctx context.Context) ([]TeamPeriodSpend, error) {
	teams := make([]string, 0, len(s.spendCaps))
	for team := range s.spendCaps {
		teams = append(teams, team)
	}
	sort.Strings(teams)
	now := s.spendNow()
	out := make([]TeamPeriodSpend, 0, len(teams))
	for _, team := range teams {
		c := s.spendCaps[team]
		start := c.Period.Start(now)
		settled, held, err := teamPeriodSpend(ctx, s.pool, team, start)
		if err != nil {
			return nil, err
		}
		out = append(out, TeamPeriodSpend{Team: team, Cap: c, PeriodStart: start, PeriodEnd: c.Period.End(now), SettledUSD: settled, HeldUSD: held})
	}
	return out, nil
}

// checkTeamSpendCap refuses a claim whose hold would take its Team over the
// cap. The advisory lock serialises the Team's claims, so two claims on
// different Shifts cannot both see the same headroom.
func checkTeamSpendCap(ctx context.Context, tx pgx.Tx, p pendingRun, requested float64) error {
	if !p.spendCap.Capped() {
		return nil
	}
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('ploeg.team-spend:' || $1, 0))`, p.team); err != nil {
		return err
	}
	start := p.spendCap.Period.Start(p.now)
	settled, held, err := teamPeriodSpend(ctx, tx, p.team, start)
	if err != nil {
		return err
	}
	if settled+held+requested <= p.spendCap.USD+spendCapTolerance {
		return nil
	}
	return &SpendCapRefusal{Team: p.team, Cap: p.spendCap, PeriodStart: start,
		SettledUSD: settled, HeldUSD: held, RequestedUSD: requested, WorkItemID: p.workItemID, RunID: p.runID}
}

const spendCapTolerance = 0.00005

// recordSpendCapRefusal audits a refused claim once per Run and period, so a
// worker that keeps polling does not grow the audit log.
func (s *Store) recordSpendCapRefusal(ctx context.Context, actor string, r *SpendCapRefusal) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	periodStart := r.PeriodStart.Format(time.DateOnly)
	var seen bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM audit_log WHERE work_item_id = $1 AND action = 'team.spend_capped'
		AND detail->>'periodStart' = $2 AND (detail->>'run')::bigint = $3)`, r.WorkItemID, periodStart, r.RunID).Scan(&seen); err != nil {
		return err
	}
	if seen {
		return nil
	}
	workItemID := r.WorkItemID
	if err := audit(ctx, tx, actor, "team.spend_capped", &workItemID, map[string]any{
		"team": r.Team, "period": string(r.Cap.Period), "periodStart": periodStart, "capUsd": r.Cap.USD,
		"settledUsd": r.SettledUSD, "heldUsd": r.HeldUSD, "requestedUsd": r.RequestedUSD,
		"run": r.RunID, "reason": r.Reason(),
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) refuseOverCap(ctx context.Context, actor string, err error) error {
	var refusal *SpendCapRefusal
	if !errors.As(err, &refusal) {
		return err
	}
	if auditErr := s.recordSpendCapRefusal(ctx, actor, refusal); auditErr != nil {
		return errors.Join(err, auditErr)
	}
	return err
}
