package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// Premise check verdicts (ADR-0123). Skipped means the check gave up and
// dispatch went on without it.
const (
	PremiseProceed = "proceed"
	PremiseAsk     = "ask"
	PremiseTooBig  = "too_big"
	PremiseSkipped = "skipped"
)

// PremiseCheck is the premise check of one Work Item. Verdict is empty
// while every attempt so far failed without an answer.
type PremiseCheck struct {
	Team      string     `json:"team"`
	Model     string     `json:"model"`
	Verdict   string     `json:"verdict"`
	Question  string     `json:"question"`
	Reason    string     `json:"reason"`
	Attempts  int        `json:"attempts"`
	LastError string     `json:"lastError"`
	CheckedAt *time.Time `json:"checkedAt"`
}

// Parks reports whether the verdict hands the Work Item to a person.
func (p PremiseCheck) Parks() bool { return p.Verdict == PremiseAsk || p.Verdict == PremiseTooBig }

// WorkItemPremiseCheck reads a Work Item's premise check; nil when none ran.
func (s *Store) WorkItemPremiseCheck(ctx context.Context, workItemID int64) (*PremiseCheck, error) {
	var p PremiseCheck
	var verdict *string
	err := s.pool.QueryRow(ctx, `SELECT team, model, verdict, question, reason, attempts, last_error, checked_at
		FROM work_item_premise_checks WHERE work_item_id = $1`, workItemID).
		Scan(&p.Team, &p.Model, &verdict, &p.Question, &p.Reason, &p.Attempts, &p.LastError, &p.CheckedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if verdict != nil {
		p.Verdict = *verdict
	}
	return &p, nil
}

// RecordPremiseFailure counts one premise check attempt that got no
// verdict. After giveUpAfter attempts the check is recorded as skipped and
// dispatch goes on. A Work Item that already has a verdict is left alone.
func (s *Store) RecordPremiseFailure(ctx context.Context, workItemID int64, team, model, reason string, giveUpAfter int) (PremiseCheck, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return PremiseCheck{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	reason = truncate(reason, 512)
	var p PremiseCheck
	var verdict *string
	err = tx.QueryRow(ctx, `INSERT INTO work_item_premise_checks (work_item_id, team, model, attempts, last_error)
		VALUES ($1, $2, $3, 1, $4)
		ON CONFLICT (work_item_id) DO UPDATE SET attempts = work_item_premise_checks.attempts + 1,
			last_error = EXCLUDED.last_error, model = EXCLUDED.model, updated_at = now()
			WHERE work_item_premise_checks.verdict IS NULL
		RETURNING team, model, verdict, question, reason, attempts, last_error, checked_at`,
		workItemID, team, model, reason).Scan(&p.Team, &p.Model, &verdict, &p.Question, &p.Reason, &p.Attempts, &p.LastError, &p.CheckedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return PremiseCheck{}, tx.Commit(ctx)
	}
	if err != nil {
		return PremiseCheck{}, err
	}
	if p.Attempts >= giveUpAfter {
		if err := tx.QueryRow(ctx, `UPDATE work_item_premise_checks SET verdict = 'skipped', reason = $2, checked_at = now(), updated_at = now()
			WHERE work_item_id = $1 RETURNING checked_at`, workItemID, reason).Scan(&p.CheckedAt); err != nil {
			return PremiseCheck{}, err
		}
		p.Verdict, p.Reason = PremiseSkipped, reason
		if err := audit(ctx, tx, "ploegd:premise-check", "work_item.premise_checked", &workItemID,
			map[string]any{"verdict": p.Verdict, "model": model, "reason": reason, "attempts": p.Attempts}); err != nil {
			return PremiseCheck{}, err
		}
	}
	return p, tx.Commit(ctx)
}

// RecordPremiseVerdict stores the premise check's verdict for a Work Item.
// An ask or too_big verdict parks the Work Item at needs_human in the same
// transaction, with the question as the reason, when it is still queued
// without a live Shift. It reports whether it parked the Work Item; a Work
// Item that already has a verdict keeps it.
func (s *Store) RecordPremiseVerdict(ctx context.Context, workItemID int64, team, model string, check PremiseCheck) (PremiseCheck, bool, error) {
	switch check.Verdict {
	case PremiseProceed, PremiseAsk, PremiseTooBig, PremiseSkipped:
	default:
		return PremiseCheck{}, false, fmt.Errorf("premise verdict %q", check.Verdict)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return PremiseCheck{}, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	question, reason := truncate(check.Question, 1024), truncate(check.Reason, 512)
	p := PremiseCheck{Team: team, Model: model, Verdict: check.Verdict, Question: question, Reason: reason}
	err = tx.QueryRow(ctx, `INSERT INTO work_item_premise_checks (work_item_id, team, model, verdict, question, reason, attempts, checked_at)
		VALUES ($1, $2, $3, $4, $5, $6, 1, now())
		ON CONFLICT (work_item_id) DO UPDATE SET verdict = EXCLUDED.verdict, question = EXCLUDED.question, reason = EXCLUDED.reason,
			model = EXCLUDED.model, attempts = work_item_premise_checks.attempts + 1, checked_at = now(), updated_at = now()
			WHERE work_item_premise_checks.verdict IS NULL
		RETURNING attempts, checked_at`, workItemID, team, model, check.Verdict, question, reason).Scan(&p.Attempts, &p.CheckedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return PremiseCheck{}, false, tx.Commit(ctx)
	}
	if err != nil {
		return PremiseCheck{}, false, err
	}
	detail := map[string]any{"verdict": p.Verdict, "model": model, "reason": reason}
	if question != "" {
		detail["question"] = question
	}
	if err := audit(ctx, tx, "ploegd:premise-check", "work_item.premise_checked", &workItemID, detail); err != nil {
		return PremiseCheck{}, false, err
	}
	parked := false
	if p.Parks() {
		var waiting bool
		if err := tx.QueryRow(ctx, `SELECT state = 'queued' AND NOT operator_owned AND NOT EXISTS (
				SELECT 1 FROM shifts sh WHERE sh.work_item_id = w.id AND sh.closed_at IS NULL)
			FROM work_items w WHERE w.id = $1 FOR UPDATE`, workItemID).Scan(&waiting); err != nil {
			return PremiseCheck{}, false, err
		}
		if waiting {
			if _, err := settleItemTx(ctx, tx, workItemID, work.StateNeedsHuman, PremiseParkReason(p)); err != nil {
				return PremiseCheck{}, false, err
			}
			parked = true
		}
	}
	return p, parked, tx.Commit(ctx)
}

// PremiseParkReason is the reason a parked Work Item carries.
func PremiseParkReason(p PremiseCheck) string {
	text := p.Question
	if text == "" {
		text = p.Reason
	}
	return "premise check: " + p.Verdict + ": " + text
}
