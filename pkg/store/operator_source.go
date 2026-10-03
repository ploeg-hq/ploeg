package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type OperatorSource struct {
	WorkItemID        string         `json:"workItemId"`
	Provider          string         `json:"provider"`
	ExternalID        string         `json:"externalId"`
	ExpectedScope     string         `json:"expectedScope"`
	ExpectedBaseURL   string         `json:"expectedBaseUrl"`
	ExpectedRevision  string         `json:"expectedRevision"`
	ExpectedUpdatedAt time.Time      `json:"expectedUpdatedAt"`
	ExpectedTarget    OperatorTarget `json:"expectedTarget"`
}

const operatorPristine = `i.state='queued' AND i.origin='assignment' AND NOT i.operator_owned
AND NOT EXISTS(SELECT 1 FROM operator_executions e WHERE e.work_item_id=i.id)
AND NOT EXISTS(SELECT 1 FROM leases l WHERE l.work_item_id=i.id)
AND NOT EXISTS(SELECT 1 FROM checkpoints c WHERE c.work_item_id=i.id)
AND NOT EXISTS(SELECT 1 FROM shifts sh WHERE sh.work_item_id=i.id AND (sh.closed_at IS NOT NULL OR sh.spent<>0))
AND NOT EXISTS(SELECT 1 FROM agent_runs r WHERE r.work_item_id=i.id AND (r.state<>'pending' OR r.started_at IS NOT NULL OR r.authorized<>0))
AND NOT EXISTS(SELECT 1 FROM run_llm_accounts a JOIN agent_runs r USING(run_token) WHERE r.work_item_id=i.id)`

func (s *Store) OperatorSourceLookup(ctx context.Context, provider, externalID string, teams []string) (OperatorItem, string, error) {
	var item OperatorItem
	var scope string
	var raw []byte
	var pristine bool
	err := s.pool.QueryRow(ctx, `SELECT `+operatorItemJSON+`,i.external_scope,(`+operatorPristine+`) FROM work_items i
		WHERE i.provider=$1 AND i.external_id=$2 AND ($3::text[] IS NULL OR i.team=ANY($3))`, provider, externalID, teams).Scan(&raw, &scope, &pristine)
	if errors.Is(err, pgx.ErrNoRows) {
		return item, scope, ErrOperatorNotFound
	}
	if err != nil {
		return item, scope, err
	}
	if err = operatorDecode(raw, &item); err != nil {
		return item, scope, err
	}
	if !pristine || item.Revision == "" || item.Target == nil || item.Target.Forge == "" || item.Target.BaseBranch == "" {
		return item, scope, ErrExecutionConflict
	}
	return item, scope, nil
}

func adoptOperatorSource(ctx context.Context, tx pgx.Tx, input AdmitOperatorExecution) (int64, error) {
	source := input.Source
	var id int64
	var provider, externalID, revision, scope, team string
	var updated time.Time
	var target OperatorTarget
	err := tx.QueryRow(ctx, `SELECT id,provider,external_id,revision,external_scope,team,updated_at,target_forge,target_owner,target_repo,target_base_branch
		FROM work_items WHERE id=$1 FOR UPDATE`, source.WorkItemID).Scan(&id, &provider, &externalID, &revision, &scope, &team, &updated, &target.Forge, &target.Owner, &target.Repo, &target.BaseBranch)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrExecutionNotFound
	}
	if err != nil {
		return 0, err
	}
	if provider != source.Provider || externalID != source.ExternalID || revision == "" || revision != source.ExpectedRevision || scope != source.ExpectedScope || team != input.Team || !updated.Equal(source.ExpectedUpdatedAt) || target != source.ExpectedTarget || target.BaseBranch != input.BaseBranch {
		return 0, ErrExecutionConflict
	}
	for _, query := range []string{
		`SELECT id FROM shifts WHERE work_item_id=$1 ORDER BY id FOR UPDATE NOWAIT`,
		`SELECT id FROM agent_runs WHERE work_item_id=$1 ORDER BY id FOR UPDATE NOWAIT`,
	} {
		rows, err := tx.Query(ctx, query, id)
		if err != nil {
			return 0, operatorSourceLockError(err)
		}
		for rows.Next() {
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return 0, operatorSourceLockError(err)
		}
	}
	var pristine bool
	if err := tx.QueryRow(ctx, `SELECT (`+operatorPristine+`) FROM work_items i WHERE i.id=$1`, id).Scan(&pristine); err != nil {
		return 0, err
	}
	if !pristine {
		return 0, ErrExecutionConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_runs SET state='finished',finished_at=now(),summary='Unstarted Run retired for operator binding' WHERE work_item_id=$1 AND state='pending'`, id); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE shifts SET closed_at=now(),close_reason='operator_adopted' WHERE work_item_id=$1 AND closed_at IS NULL`, id); err != nil {
		return 0, err
	}
	_, err = tx.Exec(ctx, `UPDATE work_items SET state='leased',operator_owned=true,updated_at=now() WHERE id=$1`, id)
	return id, err
}

func operatorSourceLockError(err error) error {
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && (pgerr.Code == "55P03" || pgerr.Code == "23505") {
		return ErrExecutionConflict
	}
	return err
}

func (s *Store) OperatorAdmissionReplay(ctx context.Context, consumer, actor string, input AdmitOperatorExecution) (bool, error) {
	var existingActor, fingerprint string
	err := s.pool.QueryRow(ctx, `SELECT actor,fingerprint FROM operator_executions WHERE consumer=$1 AND session_id=$2`, consumer, input.SessionID).Scan(&existingActor, &fingerprint)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if existingActor != actor || fingerprint != executionFingerprint(input) {
		return false, ErrExecutionConflict
	}
	return true, nil
}
