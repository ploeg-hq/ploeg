package store

import (
	"context"
	"errors"
	"time"
)

const pendingBlockSelection = `r.state='finished' AND a.state IN ('minting','issued','unknown')`

type PendingLLMBlock struct {
	RunID    int64
	RunToken string
}

func (s *Store) PendingLLMBlocks(ctx context.Context, after int64, limit int) ([]PendingLLMBlock, error) {
	if after < 0 || limit < 1 || limit > 100 {
		return nil, errors.New("invalid managed block page")
	}
	rows, err := s.pool.Query(ctx, `SELECT r.id,a.run_token FROM run_llm_accounts a JOIN agent_runs r USING(run_token)
		WHERE `+pendingBlockSelection+` AND r.id>$1 ORDER BY r.id LIMIT $2`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []PendingLLMBlock{}
	for rows.Next() {
		var value PendingLLMBlock
		if err := rows.Scan(&value.RunID, &value.RunToken); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// UnsettledAccount is a finished Run whose gateway key the block sweep has
// not been able to block, so its budget hold cannot be released yet. Since is
// when the account last changed state.
type UnsettledAccount struct {
	RunID        int64     `json:"runId,string"`
	WorkItemID   int64     `json:"workItemId,string"`
	Team         string    `json:"team"`
	AccountState string    `json:"accountState"`
	HeldUSD      float64   `json:"heldUsd"`
	Since        time.Time `json:"since"`
}

// UnsettledAccountPage is the oldest unsettled accounts in scope. Count and
// HeldUSD cover every matching account, not only the ones on the page.
type UnsettledAccountPage struct {
	Accounts []UnsettledAccount
	Count    int64
	HeldUSD  float64
}

// UnsettledFinishedAccounts lists, oldest first, the accounts PendingLLMBlocks
// selects, limited to the given Teams (nil means every Team), with the hold
// each one keeps in run_budget_holds. Limit is between 1 and 200.
func (s *Store) UnsettledFinishedAccounts(ctx context.Context, teams []string, limit int) (UnsettledAccountPage, error) {
	page := UnsettledAccountPage{Accounts: []UnsettledAccount{}}
	if limit < 1 || limit > 200 {
		return page, errors.New("invalid unsettled account page")
	}
	rows, err := s.pool.Query(ctx, `SELECT r.id,r.work_item_id,r.team,a.state,h.reserved::float8,a.updated_at,
		count(*) OVER (),(sum(h.reserved) OVER ())::float8
		FROM run_llm_accounts a JOIN agent_runs r USING(run_token) JOIN run_budget_holds h USING(run_token)
		WHERE `+pendingBlockSelection+` AND ($1::text[] IS NULL OR r.team=ANY($1))
		ORDER BY a.updated_at,r.id LIMIT $2`, teams, limit)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var v UnsettledAccount
		if err := rows.Scan(&v.RunID, &v.WorkItemID, &v.Team, &v.AccountState, &v.HeldUSD, &v.Since, &page.Count, &page.HeldUSD); err != nil {
			return page, err
		}
		v.Since = v.Since.UTC()
		page.Accounts = append(page.Accounts, v)
	}
	return page, rows.Err()
}
