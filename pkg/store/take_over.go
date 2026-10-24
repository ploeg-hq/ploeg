package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// AuditWorkItemTakenOver records a person's take-over of a running Work Item
// (ADR-0142).
const AuditWorkItemTakenOver = "operator.work_item.taken_over"

// The states of a take-over, as the Work Item detail shows them.
const (
	TakeOverYielding   = "yielding"
	TakeOverTakenOver  = "taken_over"
	TakeOverHandedBack = "handed_back"
	TakeOverEnded      = "ended"
)

var (
	// ErrNothingToTakeOver reports a Work Item with no open Shift or no
	// running Run in it.
	ErrNothingToTakeOver = errors.New("the work item has no running run to take over")
	// ErrClientCarriedRun reports a running Run the person's own client
	// carries (ADR-0112): it is theirs already.
	ErrClientCarriedRun = errors.New("a running run is carried by a person's client")
	// ErrTakeOverPending reports a Work Item a person already took over.
	ErrTakeOverPending = errors.New("the work item is already taken over")
	// ErrTakeOverConflict reports a command id used for another take-over.
	ErrTakeOverConflict = errors.New("command id was used for a different take-over")
	// ErrNotThePerson reports an actor other than the person a Work Item
	// acts for.
	ErrNotThePerson = errors.New("the actor is not the person the work item acts for")
	// ErrNotTaker reports a hand back by someone other than the person who
	// took the Work Item over.
	ErrNotTaker = errors.New("another person took the work item over")
	// ErrYieldNotRequested reports a yielded outcome from a Run nobody asked
	// to yield.
	ErrYieldNotRequested = errors.New("no person asked this run to yield")
)

// YieldUnsupportedError reports running Runs whose Harness cannot stop at a
// safe point. Harnesses names each, as harness or harness/profile.
type YieldUnsupportedError struct{ Harnesses []string }

func (e *YieldUnsupportedError) Error() string {
	return "the harness of a running run cannot yield: " + strings.Join(e.Harnesses, ", ")
}

// TakeOverCommand is a person's request to take over a Work Item's running
// Runs. Actor is the person (X-Ploeg-Actor) and AuditActor how the audit
// log names the request. ExpectedShiftID zero accepts whichever Shift is
// open.
type TakeOverCommand struct {
	WorkItemID      int64
	Teams           []string
	Consumer        string
	CommandID       string
	Actor           string
	AuditActor      string
	ExpectedShiftID int64
}

// TakenRun is a Run a take-over asked to yield.
type TakenRun struct {
	ID   int64
	Role string
}

// TakeOver is one take-over of a Work Item.
type TakeOver struct {
	ID                int64
	WorkItemID        int64
	ShiftID           int64
	Round             int
	CommandID         string
	Actor             string
	Branch            string
	RequestedAt       time.Time
	YieldedAt         *time.Time
	YieldedCommit     string
	WorkInProgress    string
	Pushed            *bool
	YieldDetail       string
	HandedBackAt      *time.Time
	HandedBackShiftID int64
	ShiftOpen         bool
	ShiftCloseReason  string
	Runs              []TakenRun
	CancelledPending  int64
	Replayed          bool
}

// State is yielding while the Shift is open, taken_over once it closed for
// the person, handed_back after a requeue, and ended when the Shift closed
// for another reason first.
func (t TakeOver) State() string {
	switch {
	case t.HandedBackAt != nil:
		return TakeOverHandedBack
	case t.ShiftOpen:
		return TakeOverYielding
	case t.ShiftCloseReason == work.CloseReasonTakenOver:
		return TakeOverTakenOver
	default:
		return TakeOverEnded
	}
}

// Active reports whether the person holds the Work Item: it is yielding to
// them or parked for them.
func (t TakeOver) Active() bool {
	s := t.State()
	return s == TakeOverYielding || s == TakeOverTakenOver
}

const takeOverColumns = `t.id, t.work_item_id, t.shift_id, t.round, t.command_id, t.actor, t.branch, t.requested_at,
	t.yielded_at, COALESCE(t.yielded_commit, ''), COALESCE(t.work_in_progress, ''), t.pushed, COALESCE(t.yield_detail, ''),
	t.handed_back_at, COALESCE(t.handed_back_shift_id, 0), sh.closed_at IS NULL, COALESCE(sh.close_reason, '')`

func scanTakeOver(row pgx.Row) (TakeOver, error) { return scanTakeOverWith(row) }

// RequestTakeOver asks every running Run of the Work Item's open Shift to
// yield to a person at its next safe point, and cancels the Shift's Runs
// nobody claimed yet, so the Round ends with the yield (ADR-0142). It
// refuses before anything changes when a running Run cannot yield. A
// repeated (consumer, command id) returns the first take-over with Replayed
// set; the same id with another request is ErrTakeOverConflict.
func (s *Store) RequestTakeOver(ctx context.Context, cmd TakeOverCommand) (TakeOver, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return TakeOver{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var operatorOwned bool
	var actsFor string
	err = tx.QueryRow(ctx, `SELECT operator_owned, acts_for FROM work_items
		WHERE id = $1 AND ($2::text[] IS NULL OR team = ANY($2)) FOR UPDATE`, cmd.WorkItemID, cmd.Teams).Scan(&operatorOwned, &actsFor)
	if errors.Is(err, pgx.ErrNoRows) {
		return TakeOver{}, ErrWorkItemNotFound
	}
	if err != nil {
		return TakeOver{}, err
	}

	var expected int64
	first, err := scanTakeOverWith(tx.QueryRow(ctx, `SELECT `+takeOverColumns+`, COALESCE(t.expected_shift_id, 0)
		FROM work_item_take_overs t JOIN shifts sh ON sh.id = t.shift_id
		WHERE t.consumer = $1 AND t.command_id = $2`, cmd.Consumer, cmd.CommandID), &expected)
	switch {
	case err == nil:
		if first.WorkItemID != cmd.WorkItemID || first.Actor != cmd.Actor || expected != cmd.ExpectedShiftID {
			return TakeOver{}, ErrTakeOverConflict
		}
		first.Replayed = true
		if first.Runs, err = takenRuns(ctx, tx, first.ShiftID); err != nil {
			return TakeOver{}, err
		}
		return first, tx.Commit(ctx)
	case !errors.Is(err, pgx.ErrNoRows):
		return TakeOver{}, err
	}

	if operatorOwned {
		return TakeOver{}, ErrOperatorOwned
	}
	if actsFor != "" && actsFor != cmd.Actor {
		return TakeOver{}, ErrNotThePerson
	}
	var shiftID int64
	var round int
	var branch string
	err = tx.QueryRow(ctx, `SELECT id, round, branch FROM shifts WHERE work_item_id = $1 AND closed_at IS NULL FOR UPDATE`,
		cmd.WorkItemID).Scan(&shiftID, &round, &branch)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return TakeOver{}, err
	}
	if cmd.ExpectedShiftID != 0 && cmd.ExpectedShiftID != shiftID {
		return TakeOver{}, ErrShiftMismatch
	}
	if shiftID == 0 {
		return TakeOver{}, ErrNothingToTakeOver
	}
	var pending bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM work_item_take_overs WHERE shift_id = $1 AND handed_back_at IS NULL)`,
		shiftID).Scan(&pending); err != nil {
		return TakeOver{}, err
	}
	if pending {
		return TakeOver{}, ErrTakeOverPending
	}

	rows, err := tx.Query(ctx, `SELECT id, role, yields, carrier IS NOT NULL, COALESCE(harness, ''), COALESCE(harness_profile, '')
		FROM agent_runs WHERE shift_id = $1 AND state = 'running' AND role <> 'ask' ORDER BY id`, shiftID)
	if err != nil {
		return TakeOver{}, err
	}
	var runs []TakenRun
	var unsupported []string
	carried := false
	for rows.Next() {
		var r TakenRun
		var yields, client bool
		var name, profile string
		if err := rows.Scan(&r.ID, &r.Role, &yields, &client, &name, &profile); err != nil {
			rows.Close()
			return TakeOver{}, err
		}
		runs = append(runs, r)
		carried = carried || client
		if !yields && !client {
			unsupported = append(unsupported, harnessLabel(name, profile))
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return TakeOver{}, err
	}
	switch {
	case len(runs) == 0:
		return TakeOver{}, ErrNothingToTakeOver
	case carried:
		return TakeOver{}, ErrClientCarriedRun
	case len(unsupported) > 0:
		return TakeOver{}, &YieldUnsupportedError{Harnesses: unsupported}
	}

	if _, err := tx.Exec(ctx, `UPDATE agent_runs SET yield_requested_at = now()
		WHERE shift_id = $1 AND state = 'running' AND role <> 'ask'`, shiftID); err != nil {
		return TakeOver{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE agent_runs SET state = 'finished', finished_at = now(),
		summary = 'cancelled: a person took over the Work Item'
		WHERE shift_id = $1 AND state = 'pending'`, shiftID)
	if err != nil {
		return TakeOver{}, err
	}
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO work_item_take_overs
		(work_item_id, shift_id, round, consumer, command_id, actor, audit_actor, expected_shift_id, branch)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, 0), $9) RETURNING id`,
		cmd.WorkItemID, shiftID, round, cmd.Consumer, cmd.CommandID, cmd.Actor, cmd.AuditActor, cmd.ExpectedShiftID, branch).Scan(&id); err != nil {
		return TakeOver{}, err
	}
	ids := make([]int64, 0, len(runs))
	for _, r := range runs {
		ids = append(ids, r.ID)
	}
	if err := audit(ctx, tx, cmd.AuditActor, AuditWorkItemTakenOver, &cmd.WorkItemID, map[string]any{
		"take_over": id, "shift": shiftID, "round": round, "command_id": cmd.CommandID, "runs": ids,
		"cancelled_pending": tag.RowsAffected(),
	}); err != nil {
		return TakeOver{}, err
	}
	out, err := scanTakeOver(tx.QueryRow(ctx, `SELECT `+takeOverColumns+`
		FROM work_item_take_overs t JOIN shifts sh ON sh.id = t.shift_id WHERE t.id = $1`, id))
	if err != nil {
		return TakeOver{}, err
	}
	out.Runs, out.CancelledPending = runs, tag.RowsAffected()
	return out, tx.Commit(ctx)
}

func scanTakeOverWith(row pgx.Row, extra ...any) (TakeOver, error) {
	var t TakeOver
	dest := []any{&t.ID, &t.WorkItemID, &t.ShiftID, &t.Round, &t.CommandID, &t.Actor, &t.Branch, &t.RequestedAt,
		&t.YieldedAt, &t.YieldedCommit, &t.WorkInProgress, &t.Pushed, &t.YieldDetail,
		&t.HandedBackAt, &t.HandedBackShiftID, &t.ShiftOpen, &t.ShiftCloseReason}
	err := row.Scan(append(dest, extra...)...)
	return t, err
}

func takenRuns(ctx context.Context, q pgx.Tx, shiftID int64) ([]TakenRun, error) {
	rows, err := q.Query(ctx, `SELECT id, role FROM agent_runs WHERE shift_id = $1 AND yield_requested_at IS NOT NULL ORDER BY id`, shiftID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (TakenRun, error) {
		var r TakenRun
		err := row.Scan(&r.ID, &r.Role)
		return r, err
	})
}

func harnessLabel(name, profile string) string {
	switch {
	case name == "":
		return "unknown"
	case profile == "":
		return name
	default:
		return name + "/" + profile
	}
}

// RecordRunYields records that the claimed Run's Harness can stop at a safe
// point for a person (ADR-0142).
func (s *Store) RecordRunYields(ctx context.Context, runToken string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agent_runs SET yields = true WHERE run_token = $1`, runToken)
	return err
}

// YieldRequested reports whether a person asked the running Run holding
// runToken to yield.
func (s *Store) YieldRequested(ctx context.Context, runToken string) (bool, error) {
	var asked bool
	err := s.pool.QueryRow(ctx, `SELECT yield_requested_at IS NOT NULL FROM agent_runs
		WHERE run_token = $1 AND state = 'running'`, runToken).Scan(&asked)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return asked, err
}

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// ValidateYieldReport checks a yield report's fields at the boundary.
func ValidateYieldReport(y *harness.YieldReport) error {
	if y == nil {
		return nil
	}
	if strings.TrimSpace(y.Branch) == "" || len(y.Branch) > 255 {
		return errors.New("yield.branch must be 1 to 255 characters")
	}
	for name, v := range map[string]string{"commit": y.Commit, "workInProgress": y.WorkInProgress} {
		if v != "" && !commitPattern.MatchString(v) {
			return fmt.Errorf("yield.%s must be a full commit id", name)
		}
	}
	if len([]rune(y.Detail)) > 2000 {
		return errors.New("yield.detail is at most 2000 characters")
	}
	return nil
}

// RecordYield stores where the yielded Run holding runToken left its work on
// its Shift's take-over. A Run nobody asked to yield is ErrYieldNotRequested.
func (s *Store) RecordYield(ctx context.Context, runToken string, y *harness.YieldReport) error {
	var shiftID *int64
	var asked bool
	err := s.pool.QueryRow(ctx, `SELECT shift_id, yield_requested_at IS NOT NULL FROM agent_runs WHERE run_token = $1`,
		runToken).Scan(&shiftID, &asked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUnknownRun
	}
	if err != nil {
		return err
	}
	if !asked || shiftID == nil {
		return ErrYieldNotRequested
	}
	if y == nil {
		y = &harness.YieldReport{}
	}
	_, err = s.pool.Exec(ctx, `UPDATE work_item_take_overs SET yielded_at = COALESCE(yielded_at, now()),
		yielded_commit = NULLIF($2, ''), work_in_progress = NULLIF($3, ''), pushed = $4, yield_detail = NULLIF($5, '')
		WHERE shift_id = $1 AND handed_back_at IS NULL`, *shiftID, y.Commit, y.WorkInProgress, y.Pushed, y.Detail)
	return err
}

// LatestTakeOver returns the Work Item's newest take-over.
func (s *Store) LatestTakeOver(ctx context.Context, workItemID int64) (TakeOver, bool, error) {
	return latestTakeOver(ctx, s.pool, workItemID)
}

// ActiveTakeOver returns the Work Item's take-over while the person holds
// it: yielding to them or parked for them.
func (s *Store) ActiveTakeOver(ctx context.Context, workItemID int64) (TakeOver, bool, error) {
	t, ok, err := latestTakeOver(ctx, s.pool, workItemID)
	if err != nil || !ok || !t.Active() {
		return TakeOver{}, false, err
	}
	return t, true, nil
}

func latestTakeOver(ctx context.Context, q queryRower, workItemID int64) (TakeOver, bool, error) {
	t, err := scanTakeOver(q.QueryRow(ctx, `SELECT `+takeOverColumns+`
		FROM work_item_take_overs t JOIN shifts sh ON sh.id = t.shift_id
		WHERE t.work_item_id = $1 ORDER BY t.id DESC LIMIT 1`, workItemID))
	if errors.Is(err, pgx.ErrNoRows) {
		return TakeOver{}, false, nil
	}
	if err != nil {
		return TakeOver{}, false, err
	}
	return t, true, nil
}

// Handback is where the first writing Round of a Shift a person handed back
// continues: Round is the Round the hand back reopened.
type Handback struct {
	work.Handback
	Round int
	Actor string
}

// HandbackFor returns the hand back that opened shiftID.
func (s *Store) HandbackFor(ctx context.Context, shiftID int64) (Handback, bool, error) {
	var h Handback
	err := s.pool.QueryRow(ctx, `SELECT t.branch, COALESCE(t.yielded_commit, ''), t.actor, q.from_round
		FROM work_item_take_overs t JOIN work_item_requeues q ON q.shift_id = t.handed_back_shift_id
		WHERE t.handed_back_shift_id = $1 ORDER BY t.id DESC LIMIT 1`, shiftID).Scan(&h.Branch, &h.YieldedCommit, &h.Actor, &h.Round)
	if errors.Is(err, pgx.ErrNoRows) {
		return h, false, nil
	}
	return h, err == nil, err
}

// activeTakeOverFor returns the Work Item's active take-over inside a
// requeue, refusing anyone but the person who took it over: only they hand
// it back. It returns a zero TakeOver when none is active.
func activeTakeOverFor(ctx context.Context, tx pgx.Tx, workItemID int64, person string) (TakeOver, error) {
	t, ok, err := latestTakeOver(ctx, tx, workItemID)
	if err != nil || !ok || !t.Active() {
		return TakeOver{}, err
	}
	if person != t.Actor {
		return TakeOver{}, ErrNotTaker
	}
	return t, nil
}

func recordHandBack(ctx context.Context, tx pgx.Tx, takeOverID, shiftID int64) error {
	_, err := tx.Exec(ctx, `UPDATE work_item_take_overs SET handed_back_at = now(), handed_back_shift_id = NULLIF($2, 0)
		WHERE id = $1`, takeOverID, shiftID)
	return err
}

// yieldedUnspent is what the Runs a person took over in shiftID were
// authorized and did not spend, while their model-key accounts still hold
// the whole authorization until settlement: the authorization less the spend
// the gateway observed. A hand back adds it to the resumed Shift's pool, so
// a take-over does not cost the work its unspent budget (ADR-0142). A Run
// without an account, or whose account settled, already returned it.
func (s *Store) yieldedUnspent(ctx context.Context, shiftID int64) (float64, error) {
	var unspent float64
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(SUM(GREATEST(a.authorized - COALESCE(a.observed_spend, 0), 0)), 0)::float8
		FROM agent_runs r JOIN run_llm_accounts a USING (run_token)
		WHERE r.shift_id = $1 AND r.state = 'finished' AND r.yield_requested_at IS NOT NULL AND a.state <> 'reconciled'`,
		shiftID).Scan(&unspent)
	return unspent, err
}
