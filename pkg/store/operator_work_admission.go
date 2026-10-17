package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// ErrRequestConflict reports that a consumer reused a request identity with
// another payload or actor, or one that already names an Operator Execution.
var ErrRequestConflict = errors.New("admission request conflicts with an earlier request")

// OperatorWorkAdmissionRouteRule is the route rule pinned on a Work Item an
// Operator Consumer admitted.
const OperatorWorkAdmissionRouteRule = "operator-admission"

// AdmitOperatorWorkItem is the request through which an Operator Consumer
// admits a manual-origin Work Item that ploeg-worker executes (ADR-0102).
type AdmitOperatorWorkItem struct {
	RequestID   string   `json:"requestId"`
	Team        string   `json:"team"`
	Target      string   `json:"target"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	BudgetUSD   *float64 `json:"budgetUsd,omitempty"`
	// Harness is the requested Harness key and Model the requested model
	// alias (ADR-0098, ADR-0103); empty requests each Role's default.
	Harness string `json:"harness,omitempty"`
	Model   string `json:"model,omitempty"`
}

// OperatorWorkAdmission is an admitted Work Item as its consumer reads it.
// ShiftID and PoolUSD describe the Work Item's most recent Shift and are nil
// before one opens.
type OperatorWorkAdmission struct {
	WorkItemID int64
	Team       string
	State      work.State
	BudgetUSD  float64
	ShiftID    *int64
	PoolUSD    *float64
	Created    bool
}

func (in AdmitOperatorWorkItem) fingerprint() string {
	in.RequestID = ""
	return executionFingerprint(in)
}

// AdmitOperatorWorkItem records an Admission and its queued, not
// operator-owned Work Item in one transaction. A replay with the same actor
// and payload returns the admitted Work Item without changing it; any other
// reuse of the request identity is ErrRequestConflict.
func (s *Store) AdmitOperatorWorkItem(ctx context.Context, consumer, actor string, input AdmitOperatorWorkItem, target work.Target, budgetUSD float64) (OperatorWorkAdmission, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return OperatorWorkAdmission{}, err
	}
	defer tx.Rollback(ctx)
	key := consumer + ":" + input.RequestID
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); err != nil {
		return OperatorWorkAdmission{}, err
	}
	fingerprint := input.fingerprint()
	var priorActor, priorFingerprint string
	var itemID int64
	err = tx.QueryRow(ctx, `SELECT actor,fingerprint,work_item_id FROM operator_work_admissions WHERE consumer=$1 AND request_id=$2`, consumer, input.RequestID).Scan(&priorActor, &priorFingerprint, &itemID)
	if err == nil {
		if priorActor != actor || priorFingerprint != fingerprint {
			return OperatorWorkAdmission{}, ErrRequestConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return OperatorWorkAdmission{}, err
		}
		return s.AdmittedWorkItem(ctx, itemID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return OperatorWorkAdmission{}, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO work_items(provider,external_id,revision,team,state,origin,title,description,
		target_forge,target_owner,target_repo,target_base_branch,route_rule,budget_usd,harness_requested,model_requested)
	VALUES('manual',$1,$2,$3,'queued','operator',$4,$5,$6,$7,$8,$9,$10,$11,NULLIF($12,''),NULLIF($13,''))
	ON CONFLICT (provider,external_id) DO NOTHING RETURNING id`,
		key, fingerprint, input.Team, input.Title, input.Description,
		target.Forge, target.Owner, target.Repo, target.BaseBranch, OperatorWorkAdmissionRouteRule, budgetUSD, input.Harness, input.Model).Scan(&itemID)
	if errors.Is(err, pgx.ErrNoRows) {
		return OperatorWorkAdmission{}, ErrRequestConflict
	}
	if err != nil {
		return OperatorWorkAdmission{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operator_work_admissions(consumer,request_id,actor,fingerprint,work_item_id) VALUES($1,$2,$3,$4,$5)`,
		consumer, input.RequestID, actor, fingerprint, itemID); err != nil {
		return OperatorWorkAdmission{}, err
	}
	details := map[string]any{"request_id": input.RequestID, "team": input.Team}
	if input.Harness != "" {
		details["harness"] = input.Harness
	}
	if input.Model != "" {
		details["model"] = input.Model
	}
	if err = audit(ctx, tx, "operator:"+consumer+":"+actor, "operator.work_item.admitted", &itemID, details); err != nil {
		return OperatorWorkAdmission{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return OperatorWorkAdmission{}, err
	}
	admitted, err := s.AdmittedWorkItem(ctx, itemID)
	admitted.Created = true
	return admitted, err
}

// AdmittedWorkItem reads an admitted Work Item's state, allotment and most
// recent Shift.
func (s *Store) AdmittedWorkItem(ctx context.Context, workItemID int64) (OperatorWorkAdmission, error) {
	a := OperatorWorkAdmission{WorkItemID: workItemID}
	err := s.pool.QueryRow(ctx, `SELECT w.team,w.state,w.budget_usd::float8,sh.id,sh.budget::float8
		FROM work_items w LEFT JOIN LATERAL (SELECT id,budget FROM shifts WHERE work_item_id=w.id ORDER BY id DESC LIMIT 1) sh ON true
		WHERE w.id=$1`, workItemID).Scan(&a.Team, &a.State, &a.BudgetUSD, &a.ShiftID, &a.PoolUSD)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrWorkItemNotFound
	}
	return a, err
}

// OperatorWorkAdmitted reports whether the consumer already used requestID for
// an Admission, so a replay is answered without checking the request again.
func (s *Store) OperatorWorkAdmitted(ctx context.Context, consumer, requestID string) (bool, error) {
	var admitted bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operator_work_admissions WHERE consumer=$1 AND request_id=$2)`, consumer, requestID).Scan(&admitted)
	return admitted, err
}
