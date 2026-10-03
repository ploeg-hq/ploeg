package store

import (
	"context"
	"errors"
	"time"
)

// UnsettledLLMAccount is a finished Run's inference account that still holds
// Shift budget and may now be settled by the controller.
type UnsettledLLMAccount struct {
	RunID        int64
	RunToken     string
	GatewayKeyID string
	Alias        string
	State        string
	MintBegan    bool
	QuietSince   time.Time
	// ShiftID is the Run's Shift, selected from the existing join so a caller
	// can refresh that Shift's usage report after settling. Nil for a
	// historical Run that predates Shifts (migration 0008); such a row has no
	// report to refresh and the caller skips it.
	ShiftID *int64
}

// UnsettledLLMAccounts pages finished Runs whose account is reserved, or
// blocked and unchanged for at least quietFor. MintBegan is true when any
// durable record shows that an external mint may have started.
func (s *Store) UnsettledLLMAccounts(ctx context.Context, after int64, quietFor time.Duration, limit int) ([]UnsettledLLMAccount, error) {
	if after < 0 || quietFor < 0 || limit < 1 || limit > 100 {
		return nil, errors.New("invalid managed settlement page")
	}
	rows, err := s.pool.Query(ctx, `SELECT r.id,a.run_token,a.gateway_key_id,a.alias,a.state,a.updated_at,r.shift_id,
		a.gateway_key_id<>'' OR COALESCE(a.observed_spend,0)>0 OR EXISTS(SELECT 1 FROM audit_log l
			WHERE l.work_item_id=r.work_item_id AND l.action IN ('llm.minting','llm.issued','llm.unknown') AND l.detail->>'alias'=a.alias)
		FROM run_llm_accounts a JOIN agent_runs r USING(run_token)
		WHERE r.state='finished' AND r.id>$1
		AND (a.state='reserved' OR (a.state='blocked' AND a.updated_at<=now()-make_interval(secs=>$2)))
		ORDER BY r.id LIMIT $3`, after, quietFor.Seconds(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []UnsettledLLMAccount{}
	for rows.Next() {
		var v UnsettledLLMAccount
		if err := rows.Scan(&v.RunID, &v.RunToken, &v.GatewayKeyID, &v.Alias, &v.State, &v.QuietSince, &v.ShiftID, &v.MintBegan); err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}

// CorrectableLLMAccounts pages finished Runs whose account is reconciled and
// still inside its correction window, so the controller can read the
// gateway's spend logs again and record any late charge. QuietSince is the
// first settlement time and MintBegan is always true: only a settlement read
// from the gateway opens a correction window.
func (s *Store) CorrectableLLMAccounts(ctx context.Context, after int64, limit int) ([]UnsettledLLMAccount, error) {
	if after < 0 || limit < 1 || limit > 100 {
		return nil, errors.New("invalid managed correction page")
	}
	rows, err := s.pool.Query(ctx, `SELECT r.id,a.run_token,a.gateway_key_id,a.alias,a.state,a.settled_at,r.shift_id
		FROM run_llm_accounts a JOIN agent_runs r USING(run_token)
		WHERE r.state='finished' AND r.id>$1 AND a.state='reconciled' AND a.corrections_until>now()
		ORDER BY r.id LIMIT $2`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []UnsettledLLMAccount{}
	for rows.Next() {
		v := UnsettledLLMAccount{MintBegan: true}
		if err := rows.Scan(&v.RunID, &v.RunToken, &v.GatewayKeyID, &v.Alias, &v.State, &v.QuietSince, &v.ShiftID); err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}
