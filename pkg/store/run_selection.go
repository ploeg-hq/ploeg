package store

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
)

// RoleSelection is what a Role may run and call when its Run is created
// (ADR-0098, ADR-0103). Harnesses are the Harness keys an admission may give
// the Role, Models the model aliases its inference policy lists. An empty
// default stores nothing on the Run.
type RoleSelection struct {
	Harnesses      []string
	DefaultHarness string
	Models         []string
	DefaultModel   string
	// ClientCarried lists the Harness keys whose Runs the person's own client
	// carries (ADR-0112): such a Run is never claimed by a worker.
	ClientCarried []string
}

// CarrierClient is the carrier of a Run the person's own client carries.
const CarrierClient = "client"

// NeedsHumanStartFromClient is the needs_human reason code of a Work Item
// whose client-carried Run waits for its person to start it (ADR-0112).
const NeedsHumanStartFromClient = "start_from_claude_code"

// Resolve returns the Harness key and model a Run of this Role stores: the
// requested one when the Role allows it, otherwise the Role's default.
func (r RoleSelection) Resolve(requestedHarness, requestedModel string) (harness, model string) {
	harness, model = r.DefaultHarness, r.DefaultModel
	if requestedHarness != "" && slices.Contains(r.Harnesses, requestedHarness) {
		harness = requestedHarness
	}
	if requestedModel != "" && slices.Contains(r.Models, requestedModel) {
		model = requestedModel
	}
	return harness, model
}

// HarnessKey is the key a Harness and agent profile are selected by, as the
// chart's ploeg.qualificationHarnessName derives it: acp/<profile> for acp,
// whose empty profile is opencode, and the Harness name otherwise.
func HarnessKey(name, profile string) string {
	if name != "acp" {
		return name
	}
	if profile == "" {
		profile = "opencode"
	}
	return "acp/" + profile
}

func insertPendingRun(ctx context.Context, tx pgx.Tx, workItemID, shiftID int64, team string, round int, r Role, requestedHarness, requestedModel string) error {
	token, err := newToken()
	if err != nil {
		return err
	}
	harness, model := r.Selection.Resolve(requestedHarness, requestedModel)
	carrier := ""
	if harness != "" && slices.Contains(r.Selection.ClientCarried, harness) {
		carrier = CarrierClient
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO agent_runs (work_item_id, shift_id, team, role, round, writes, shadow, state, run_token, started_at, harness_requested, model_selected, carrier)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', $8, NULL, NULLIF($9, ''), NULLIF($10, ''), NULLIF($11, ''))`,
		workItemID, shiftID, team, r.Name, round, r.Writes, r.Shadow, token, harness, model, carrier)
	if err != nil || carrier == "" {
		return err
	}
	return waitForClient(ctx, tx, workItemID, team, r.Name, round)
}

// waitForClient parks a Work Item whose new Run a client carries at
// needs_human, naming the person it acts for: no worker will claim the Run,
// so a person has to start it from their own Claude Code (ADR-0112).
func waitForClient(ctx context.Context, tx pgx.Tx, workItemID int64, team, role string, round int) error {
	var person, handedBy string
	err := tx.QueryRow(ctx, `
		UPDATE work_items SET state = 'needs_human', updated_at = now()
		WHERE id = $1 AND NOT operator_owned
		RETURNING acts_for, handed_by`, workItemID).Scan(&person, &handedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return audit(ctx, tx, "team:"+team, "work_item.needs_human", &workItemID, map[string]any{
		"reason": NeedsHumanStartFromClient, "message": ClientWaitMessage(person, handedBy),
		"actsFor": person, "handedBy": handedBy, "role": role, "round": round,
	})
}

// ClientWaitMessage is the sentence that tells people a client-carried Run
// waits for its person (ADR-0112). operatorClientWaitJSON renders the same
// sentences in SQL.
func ClientWaitMessage(person, handedBy string) string {
	switch {
	case person != "":
		return fmt.Sprintf(clientWaitForPerson, person)
	case handedBy != "":
		return fmt.Sprintf(clientWaitForTrackerUser, handedBy)
	default:
		return clientWaitForNobody
	}
}

const (
	clientWaitForPerson      = "Start this from your Claude Code: it acts for %s, who claims it and dispatches it with their own Claude login."
	clientWaitForTrackerUser = "Start this from your Claude Code: it acts for tracker user %s, whom Ploeg cannot match to a person, so nobody can claim it until configuration maps them to one."
	clientWaitForNobody      = "Start this from your Claude Code: Ploeg does not know which person it acts for, so nobody can claim it."
)

func runRequest(ctx context.Context, tx pgx.Tx, workItemID int64) (harness, model string, err error) {
	err = tx.QueryRow(ctx, `SELECT COALESCE(harness_requested, ''), COALESCE(model_requested, '') FROM work_items WHERE id = $1`,
		workItemID).Scan(&harness, &model)
	return harness, model, err
}

// PendingHarnessRuns counts the pending Runs of a team and Role that store
// one Harness key.
type PendingHarnessRuns struct {
	Team    string
	Role    string
	Harness string
	Pending int
}

// PendingRunsByHarness counts pending Runs per team, Role and stored Harness
// key; Runs that store none are left out.
func (s *Store) PendingRunsByHarness(ctx context.Context) ([]PendingHarnessRuns, error) {
	return pendingRunsByHarness(ctx, s.pool)
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func pendingRunsByHarness(ctx context.Context, q querier) ([]PendingHarnessRuns, error) {
	rows, err := q.Query(ctx, `SELECT team, role, harness_requested, count(*) FROM agent_runs
		WHERE state = 'pending' AND harness_requested IS NOT NULL GROUP BY team, role, harness_requested ORDER BY team, role, harness_requested`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingHarnessRuns
	for rows.Next() {
		var p PendingHarnessRuns
		if err := rows.Scan(&p.Team, &p.Role, &p.Harness, &p.Pending); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ObservedHarnessVersions returns, per team, Role and Harness key, the newest
// Harness version a Run recorded.
func (s *Store) ObservedHarnessVersions(ctx context.Context, teams []string) (map[string]map[string]map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT ON (team, role, key) team, role, key, harness_version FROM (
			SELECT id, team, role, harness_version,
				CASE WHEN harness = 'acp' THEN 'acp/' || COALESCE(NULLIF(harness_profile, ''), 'opencode') ELSE harness END AS key
			FROM agent_runs WHERE harness_version IS NOT NULL AND harness IS NOT NULL AND team = ANY($1)) r
		ORDER BY team, role, key, id DESC`, teams)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]map[string]string{}
	for rows.Next() {
		var team, role, key, version string
		if err := rows.Scan(&team, &role, &key, &version); err != nil {
			return nil, err
		}
		if out[team] == nil {
			out[team] = map[string]map[string]string{}
		}
		if out[team][role] == nil {
			out[team][role] = map[string]string{}
		}
		out[team][role][key] = version
	}
	return out, rows.Err()
}
