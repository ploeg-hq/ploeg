package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// MergeState is whether a pull request merges into its base branch
// (ADR-0040).
type MergeState string

const (
	MergeUnknown    MergeState = "unknown"
	MergeClean      MergeState = "clean"
	MergeConflicted MergeState = "conflicted"
)

// ConflictConfirmPolls is how many polls in a row at one head must report a
// conflict before a pull request counts as conflicted.
const ConflictConfirmPolls = 2

// MergeCheck is the merge state Ploeg holds for one pull request: the head it
// was read at and how many polls in a row at that head reported a conflict.
type MergeCheck struct {
	State            MergeState
	HeadSHA          string
	UnmergeablePolls int
}

// NextMergeCheck applies one forge poll to prev. mergeable true is clean.
// mergeable false at the head prev was read at counts one more conflicted
// poll, and ConflictConfirmPolls of them make the state conflicted; at a new
// head the count starts again. Anything else, a forge that has not computed
// mergeability or a poll without a head, is unknown.
func NextMergeCheck(prev MergeCheck, mergeable *bool, headSHA string) MergeCheck {
	next := MergeCheck{State: MergeUnknown, HeadSHA: headSHA}
	switch {
	case mergeable == nil:
	case *mergeable:
		next.State = MergeClean
	case headSHA == "":
	default:
		next.UnmergeablePolls = 1
		if headSHA == prev.HeadSHA {
			next.UnmergeablePolls = prev.UnmergeablePolls + 1
		}
		if next.UnmergeablePolls >= ConflictConfirmPolls {
			next.State = MergeConflicted
		}
	}
	return next
}

// MergeObservation is one forge poll of a pull request's mergeability.
// Mergeable is nil when the forge had not computed it; BaseBranch is empty
// when the forge did not say.
type MergeObservation struct {
	Mergeable  *bool
	HeadSHA    string
	BaseBranch string
}

// RecordMergeCheck applies obs, through NextMergeCheck, to the pull request
// recorded under key for workItemID and stamps it checked at at. A move into
// or out of conflicted writes one audit row, pull_request.conflicted or
// pull_request.mergeable, carrying the Work Item's id. recorded is false, and
// nothing is stored, when no such pull request is recorded.
func (s *Store) RecordMergeCheck(ctx context.Context, key PullRequestKey, workItemID int64, obs MergeObservation, at time.Time) (check MergeCheck, recorded bool, err error) {
	owner, name, ok := splitRepo(key.Repo)
	if !ok || key.Number <= 0 || key.Forge == "" {
		return MergeCheck{}, false, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return MergeCheck{}, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var id int64
	var prev MergeCheck
	var state string
	err = tx.QueryRow(ctx, `
		SELECT id, COALESCE(merge_state, 'unknown'), COALESCE(merge_head_sha, ''), unmergeable_polls
		FROM pull_requests
		WHERE forge = $1 AND repo_owner = $2 AND repo_name = $3 AND number = $4 AND work_item_id = $5
		FOR UPDATE`, key.Forge, owner, name, key.Number, workItemID).Scan(&id, &state, &prev.HeadSHA, &prev.UnmergeablePolls)
	if errors.Is(err, pgx.ErrNoRows) {
		return MergeCheck{}, false, nil
	}
	if err != nil {
		return MergeCheck{}, false, err
	}
	prev.State = MergeState(state)
	next := NextMergeCheck(prev, obs.Mergeable, truncate(obs.HeadSHA, 128))
	if _, err := tx.Exec(ctx, `
		UPDATE pull_requests SET merge_state = $2, merge_head_sha = NULLIF($3, ''), unmergeable_polls = $4,
			base_branch = COALESCE(NULLIF(left($5, 1024), ''), base_branch), merge_checked_at = $6
		WHERE id = $1`, id, string(next.State), next.HeadSHA, next.UnmergeablePolls, obs.BaseBranch, at); err != nil {
		return MergeCheck{}, false, err
	}
	if (prev.State == MergeConflicted) != (next.State == MergeConflicted) {
		action := "pull_request.mergeable"
		if next.State == MergeConflicted {
			action = "pull_request.conflicted"
		}
		if err := audit(ctx, tx, "ploegd:review", action, &workItemID, map[string]any{
			"forge": key.Forge, "repo": key.Repo, "number": key.Number,
			"headSha": next.HeadSHA, "baseBranch": obs.BaseBranch, "mergeState": string(next.State),
		}); err != nil {
			return MergeCheck{}, false, err
		}
	}
	return next, true, tx.Commit(ctx)
}
