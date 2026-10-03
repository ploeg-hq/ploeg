package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrExecutionConflict = errors.New("execution command conflicts with current state")
var ErrExecutionNotFound = errors.New("execution not found")

type OperatorExecution struct {
	ID            string    `json:"id"`
	SessionID     string    `json:"sessionId"`
	WorkItemID    string    `json:"workItemId"`
	ShiftID       string    `json:"shiftId"`
	RunID         string    `json:"runId"`
	Team          string    `json:"team"`
	Actor         string    `json:"actor"`
	Revision      int64     `json:"revision"`
	Generation    int64     `json:"generation"`
	State         string    `json:"state"`
	Supervision   string    `json:"supervision"`
	Demo          bool      `json:"demo"`
	StopConfirmed bool      `json:"stopConfirmed"`
	BudgetUSD     float64   `json:"budgetUsd"`
	ExpiresAt     time.Time `json:"expiresAt"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	RunToken      string    `json:"-"`
}

type AdmitOperatorExecution struct {
	Source        *OperatorSource `json:"source,omitempty"`
	SessionID     string          `json:"sessionId"`
	Team          string          `json:"team"`
	Title         string          `json:"title"`
	Objective     string          `json:"objective"`
	RepositoryID  string          `json:"repositoryId"`
	RepositoryURL string          `json:"repositoryUrl"`
	BaseBranch    string          `json:"baseBranch"`
	CrewID        string          `json:"crewId"`
	BudgetUSD     float64         `json:"budgetUsd"`
	Demo          bool            `json:"demo"`
}

type OperatorExecutionCommand struct {
	ID               string `json:"commandId"`
	Action           string `json:"action"`
	ExpectedRevision int64  `json:"expectedRevision"`
	Generation       int64  `json:"generation"`
	State            string `json:"state,omitempty"`
	Text             string `json:"text,omitempty"`
	StopConfirmed    bool   `json:"stopConfirmed,omitempty"`
	AuthenticatedBy  string `json:"authenticatedBy,omitempty"`
}

type OperatorExecutionEvent struct {
	Revision int64           `json:"revision"`
	At       time.Time       `json:"at"`
	Actor    string          `json:"actor"`
	Kind     string          `json:"kind"`
	Detail   json.RawMessage `json:"detail"`
}

func executionFingerprint(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

const executionSelect = `SELECT e.id,e.session_id,e.work_item_id::text,e.shift_id::text,e.run_id::text,w.team,e.actor,e.revision,e.generation,e.state,e.supervision,e.demo,e.stop_confirmed,sh.budget,e.expires_at,e.created_at,e.updated_at,r.run_token
FROM operator_executions e JOIN work_items w ON w.id=e.work_item_id JOIN shifts sh ON sh.id=e.shift_id JOIN agent_runs r ON r.id=e.run_id `

func scanOperatorExecution(row pgx.Row) (OperatorExecution, error) {
	var e OperatorExecution
	err := row.Scan(&e.ID, &e.SessionID, &e.WorkItemID, &e.ShiftID, &e.RunID, &e.Team, &e.Actor, &e.Revision, &e.Generation, &e.State, &e.Supervision, &e.Demo, &e.StopConfirmed, &e.BudgetUSD, &e.ExpiresAt, &e.CreatedAt, &e.UpdatedAt, &e.RunToken)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrExecutionNotFound
	}
	return e, err
}

func (s *Store) AdmitOperatorExecution(ctx context.Context, consumer, actor string, input AdmitOperatorExecution, ttl time.Duration) (OperatorExecution, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return OperatorExecution{}, false, err
	}
	defer tx.Rollback(ctx)
	key := consumer + ":" + input.SessionID
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); err != nil {
		return OperatorExecution{}, false, err
	}
	existing, err := scanOperatorExecution(tx.QueryRow(ctx, executionSelect+`WHERE e.consumer=$1 AND e.session_id=$2 FOR UPDATE OF e`, consumer, input.SessionID))
	fingerprint := executionFingerprint(input)
	if err == nil {
		var prior string
		if err = tx.QueryRow(ctx, `SELECT fingerprint FROM operator_executions WHERE id=$1`, existing.ID).Scan(&prior); err != nil {
			return existing, false, err
		}
		if prior != fingerprint || existing.Actor != actor {
			return existing, false, ErrExecutionConflict
		}
		return existing, false, tx.Commit(ctx)
	}
	if !errors.Is(err, ErrExecutionNotFound) {
		return existing, false, err
	}
	id := executionFingerprint(key)[:32]
	token, err := newToken()
	if err != nil {
		return existing, false, err
	}
	var itemID, shiftID, runID int64
	if input.Source != nil {
		itemID, err = adoptOperatorSource(ctx, tx, input)
		if err != nil {
			return existing, false, err
		}
	} else if err = tx.QueryRow(ctx, `INSERT INTO work_items(provider,external_id,revision,team,state,origin,title,description,url,route_rule,operator_owned)
VALUES('manual',$1,$2,$3,'leased','operator',$4,$5,$6,'operator-registration',true) RETURNING id`, key, fingerprint, input.Team, input.Title, input.Objective, input.RepositoryURL).Scan(&itemID); err != nil {
		return existing, false, err
	}
	if err = tx.QueryRow(ctx, `INSERT INTO shifts(work_item_id,team,branch,budget) VALUES($1,$2,$3,$4) RETURNING id`, itemID, input.Team, "vloer/"+input.SessionID, input.BudgetUSD).Scan(&shiftID); err != nil {
		return existing, false, err
	}
	expiry := time.Now().UTC().Add(ttl)
	if err = tx.QueryRow(ctx, `INSERT INTO agent_runs(work_item_id,team,run_token,shift_id,role,writes,state,authorized,expires_at)
VALUES($1,$2,$3,$4,'operator',true,'running',$5,$6) RETURNING id`, itemID, input.Team, token, shiftID, input.BudgetUSD, expiry).Scan(&runID); err != nil {
		return existing, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO leases(work_item_id,team,run_token,shift_id,expires_at) VALUES($1,$2,$3,$4,$5)`, itemID, input.Team, token, shiftID, expiry); err != nil {
		return existing, false, err
	}
	var sourceJSON []byte
	if input.Source != nil {
		sourceJSON, _ = json.Marshal(input.Source)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operator_executions(id,consumer,actor,session_id,work_item_id,shift_id,run_id,fingerprint,demo,expires_at,source,repository_id,repository_url,base_branch)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, id, consumer, actor, input.SessionID, itemID, shiftID, runID, fingerprint, input.Demo, expiry, sourceJSON, input.RepositoryID, input.RepositoryURL, input.BaseBranch); err != nil {
		return existing, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operator_execution_events(execution_id,revision,actor,kind,detail) VALUES($1,1,$2,'execution.admitted',$3)`, id, actor, []byte(fmt.Sprintf(`{"demo":%t}`, input.Demo))); err != nil {
		return existing, false, err
	}
	if err = audit(ctx, tx, "operator:"+consumer+":"+actor, "operator.admitted", &itemID, map[string]any{"executionId": id, "sessionId": input.SessionID, "demo": input.Demo}); err != nil {
		return existing, false, err
	}
	existing, err = scanOperatorExecution(tx.QueryRow(ctx, executionSelect+`WHERE e.id=$1`, id))
	if err != nil {
		return existing, false, err
	}
	return existing, true, tx.Commit(ctx)
}

func (s *Store) OperatorExecution(ctx context.Context, id, consumer, actor string) (OperatorExecution, error) {
	return scanOperatorExecution(s.pool.QueryRow(ctx, executionSelect+`WHERE e.id=$1 AND e.consumer=$2 AND e.actor=$3`, id, consumer, actor))
}

func (s *Store) CommandOperatorExecution(ctx context.Context, id, consumer, actor string, c OperatorExecutionCommand, ttl time.Duration) (OperatorExecution, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return OperatorExecution{}, err
	}
	defer tx.Rollback(ctx)
	e, err := scanOperatorExecution(tx.QueryRow(ctx, executionSelect+`WHERE e.id=$1 AND e.consumer=$2 AND e.actor=$3 FOR UPDATE OF e`, id, consumer, actor))
	if err != nil {
		return e, err
	}
	fingerprint := executionFingerprint(c)
	var prior string
	var response []byte
	err = tx.QueryRow(ctx, `SELECT fingerprint,response FROM operator_execution_commands WHERE execution_id=$1 AND command_id=$2`, id, c.ID).Scan(&prior, &response)
	if err == nil {
		if prior != fingerprint {
			return e, ErrExecutionConflict
		}
		var saved OperatorExecution
		if err = json.Unmarshal(response, &saved); err != nil {
			return e, err
		}
		saved.RunToken = e.RunToken
		return saved, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return e, err
	}
	if c.ExpectedRevision != e.Revision || c.Generation != e.Generation {
		return e, ErrExecutionConflict
	}
	if c.Action == "resume" {
		var candidate bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operator_delivery_candidates WHERE execution_id=$1)`, e.ID).Scan(&candidate); err != nil {
			return e, err
		}
		if candidate {
			return e, ErrExecutionConflict
		}
	}
	expired := !e.ExpiresAt.After(time.Now())
	terminal := e.State == "completed" || e.State == "cancelled" || e.State == "failed"
	switch c.Action {
	case "start":
		if e.State != "admitted" || expired {
			return e, ErrExecutionConflict
		}
		e.State = "running"
		e.StopConfirmed = false
	case "resume":
		if (e.State != "paused" && e.State != "interrupted") || !e.StopConfirmed {
			return e, ErrExecutionConflict
		}
		e.Generation++
		e.State = "running"
		e.StopConfirmed = false
	case "pause":
		if e.State != "running" && e.State != "waiting_input" {
			return e, ErrExecutionConflict
		}
		e.State = "pause_requested"
	case "cancel":
		if terminal {
			return e, ErrExecutionConflict
		}
		if e.State == "admitted" || e.State == "paused" {
			e.State = "cancelled"
		} else {
			e.State = "cancel_requested"
		}
	case "message":
		if terminal || expired {
			return e, ErrExecutionConflict
		}
	case "handback":
		if terminal || expired {
			return e, ErrExecutionConflict
		}
		e.Supervision = "background"
	case "take-control":
		if terminal || expired {
			return e, ErrExecutionConflict
		}
		e.Supervision = "human"
	case "heartbeat":
		if terminal || expired || e.State == "interrupted" {
			return e, ErrExecutionConflict
		}
	case "report":
		if terminal {
			return e, ErrExecutionConflict
		}
		switch c.State {
		case "running", "waiting_input":
			if expired || (e.State != "running" && e.State != "waiting_input") {
				return e, ErrExecutionConflict
			}
		case "paused":
			if (e.State != "pause_requested" && e.State != "interrupted") || !c.StopConfirmed {
				return e, ErrExecutionConflict
			}
		case "cancelled":
			if e.State != "cancel_requested" || !c.StopConfirmed {
				return e, ErrExecutionConflict
			}
		case "interrupted":
			if e.State == "cancel_requested" || e.State == "pause_requested" {
				c.State = e.State
			}
		case "completed", "failed":
			if !c.StopConfirmed || e.State == "pause_requested" || e.State == "cancel_requested" || expired {
				return e, ErrExecutionConflict
			}
		default:
			return e, ErrExecutionConflict
		}
		e.State = c.State
		e.StopConfirmed = c.StopConfirmed
	default:
		return e, ErrExecutionConflict
	}
	e.Revision++
	e.ExpiresAt = time.Now().UTC().Add(ttl)
	e.UpdatedAt = time.Now().UTC()
	if _, err = tx.Exec(ctx, `UPDATE operator_executions SET revision=$2,generation=$3,state=$4,supervision=$5,stop_confirmed=$6,expires_at=$7,updated_at=$8 WHERE id=$1`, e.ID, e.Revision, e.Generation, e.State, e.Supervision, e.StopConfirmed, e.ExpiresAt, e.UpdatedAt); err != nil {
		return e, err
	}
	if _, err = tx.Exec(ctx, `UPDATE agent_runs SET expires_at=$2 WHERE id=$1`, e.RunID, e.ExpiresAt); err != nil {
		return e, err
	}
	if _, err = tx.Exec(ctx, `UPDATE leases SET expires_at=$2,renewed_at=now() WHERE work_item_id=$1`, e.WorkItemID, e.ExpiresAt); err != nil {
		return e, err
	}
	itemState := "leased"
	if e.State == "completed" || e.State == "cancelled" {
		itemState = "done"
	} else if e.State == "paused" || e.State == "interrupted" || e.State == "failed" || e.State == "waiting_input" {
		itemState = "needs_human"
	}
	if _, err = tx.Exec(ctx, `UPDATE work_items SET state=$2,updated_at=now() WHERE id=$1`, e.WorkItemID, itemState); err != nil {
		return e, err
	}
	if e.State == "completed" || e.State == "cancelled" || e.State == "failed" {
		summary := "Operator execution " + e.State
		if supplied := strings.TrimSpace(c.Text); supplied != "" {
			summary = supplied
			if len(summary) > 4096 {
				summary = string([]rune(summary)[:min(len([]rune(summary)), 4096)])
			}
		}
		if err = closeOperatorExecution(ctx, tx, e, summary, "operator_"+e.State); err != nil {
			return e, err
		}
	}
	detail, _ := json.Marshal(map[string]any{"state": e.State, "generation": e.Generation, "supervision": e.Supervision, "stopConfirmed": e.StopConfirmed, "text": c.Text})
	eventActor := actor
	if c.AuthenticatedBy != "" {
		eventActor = c.AuthenticatedBy
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operator_execution_events(execution_id,revision,actor,kind,detail) VALUES($1,$2,$3,$4,$5)`, id, e.Revision, eventActor, "execution."+c.Action, detail); err != nil {
		return e, err
	}
	response, _ = json.Marshal(e)
	if _, err = tx.Exec(ctx, `INSERT INTO operator_execution_commands(execution_id,command_id,fingerprint,response) VALUES($1,$2,$3,$4)`, id, c.ID, fingerprint, response); err != nil {
		return e, err
	}
	return e, tx.Commit(ctx)
}

func closeOperatorExecution(ctx context.Context, tx pgx.Tx, e OperatorExecution, summary, reason string) error {
	var outcome *string
	if e.State != "completed" {
		stuck := "stuck"
		outcome = &stuck
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_runs SET state='finished',finished_at=now(),outcome=$2,summary=$3 WHERE id=$1`, e.RunID, outcome, summary); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE shifts SET closed_at=now(),close_reason=$2 WHERE id=$1`, e.ShiftID, reason); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM leases WHERE work_item_id=$1`, e.WorkItemID)
	return err
}

func (s *Store) OperatorExecutionEvents(ctx context.Context, id string, after int64, limit int) ([]OperatorExecutionEvent, error) {
	rows, err := s.pool.Query(ctx, `SELECT revision,at,actor,kind,detail FROM operator_execution_events WHERE execution_id=$1 AND revision>$2 ORDER BY revision LIMIT $3`, id, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []OperatorExecutionEvent{}
	for rows.Next() {
		var e OperatorExecutionEvent
		if err = rows.Scan(&e.Revision, &e.At, &e.Actor, &e.Kind, &e.Detail); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func (s *Store) ExpireOperatorExecutions(ctx context.Context) ([]OperatorExecution, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, executionSelect+`WHERE e.expires_at<now() AND e.state IN ('running','waiting_input','pause_requested','cancel_requested','interrupted') AND NOT e.stop_confirmed ORDER BY e.last_block_attempt_at NULLS FIRST,e.expires_at,e.id LIMIT 100 FOR UPDATE OF e SKIP LOCKED`)
	if err != nil {
		return nil, err
	}
	executions := []OperatorExecution{}
	for rows.Next() {
		e, err := scanOperatorExecution(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		executions = append(executions, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range executions {
		e := &executions[i]
		if _, err = tx.Exec(ctx, `UPDATE operator_executions SET last_block_attempt_at=now() WHERE id=$1`, e.ID); err != nil {
			return nil, err
		}
		if e.State == "interrupted" || e.State == "cancel_requested" || e.State == "pause_requested" {
			continue
		}
		e.State = "interrupted"
		e.Revision++
		if _, err = tx.Exec(ctx, `UPDATE operator_executions SET state='interrupted',revision=$2,updated_at=now() WHERE id=$1`, e.ID, e.Revision); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `UPDATE work_items SET state='needs_human',updated_at=now() WHERE id=$1`, e.WorkItemID); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO operator_execution_events(execution_id,revision,actor,kind,detail) VALUES($1,$2,'ploegd','execution.expired','{"autoResumed":false,"stopConfirmed":false}')`, e.ID, e.Revision); err != nil {
			return nil, err
		}
	}
	unstarted, err := cancelUnstartedExecutions(ctx, tx)
	if err != nil {
		return nil, err
	}
	return append(executions, unstarted...), tx.Commit(ctx)
}

func cancelUnstartedExecutions(ctx context.Context, tx pgx.Tx) ([]OperatorExecution, error) {
	rows, err := tx.Query(ctx, executionSelect+`WHERE e.state='admitted' AND e.expires_at<now() ORDER BY e.expires_at,e.id LIMIT 100 FOR UPDATE OF e SKIP LOCKED`)
	if err != nil {
		return nil, err
	}
	executions, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (OperatorExecution, error) { return scanOperatorExecution(row) })
	if err != nil {
		return nil, err
	}
	for i := range executions {
		e := &executions[i]
		e.State = "cancelled"
		e.StopConfirmed = true
		e.Revision++
		if _, err = tx.Exec(ctx, `UPDATE operator_executions SET state='cancelled',stop_confirmed=true,revision=$2,updated_at=now() WHERE id=$1`, e.ID, e.Revision); err != nil {
			return nil, err
		}
		if err = closeOperatorExecution(ctx, tx, *e, "Operator admission expired before start", "operator_admission_expired"); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `UPDATE work_items SET state='done',updated_at=now() WHERE id=$1`, e.WorkItemID); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO operator_execution_events(execution_id,revision,actor,kind,detail) VALUES($1,$2,'ploegd','execution.expired','{"started":false,"state":"cancelled","stopConfirmed":true}')`, e.ID, e.Revision); err != nil {
			return nil, err
		}
		id, err := strconv.ParseInt(e.WorkItemID, 10, 64)
		if err != nil {
			return nil, err
		}
		if err = audit(ctx, tx, "ploegd:sweeper", "operator.admission_expired", &id, map[string]any{"executionId": e.ID}); err != nil {
			return nil, err
		}
	}
	return executions, nil
}
