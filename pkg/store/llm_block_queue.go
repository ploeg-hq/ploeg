package store

import (
	"context"
	"errors"
)

type PendingLLMBlock struct {
	RunID    int64
	RunToken string
}

func (s *Store) PendingLLMBlocks(ctx context.Context, after int64, limit int) ([]PendingLLMBlock, error) {
	if after < 0 || limit < 1 || limit > 100 {
		return nil, errors.New("invalid managed block page")
	}
	rows, err := s.pool.Query(ctx, `SELECT r.id,a.run_token FROM run_llm_accounts a JOIN agent_runs r USING(run_token)
		WHERE r.state='finished' AND a.state IN ('minting','issued','unknown') AND r.id>$1 ORDER BY r.id LIMIT $2`, after, limit)
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
