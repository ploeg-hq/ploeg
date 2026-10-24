package operatorclient

import (
	"context"
	"net/http"
	"regexp"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

// Attribution names the person and channel of a change (ADR-0108). Actor is
// the person; ActingUser a person acting on Actor's behalf; Via the channel,
// such as "mcp:Claude Code 2.1.296"; Confirmation "<id>:<sha256 hex>" of the
// text the person agreed to. ploegd records all four on the change's audit
// events and trusts the consumer to name the person truthfully.
type Attribution struct {
	Actor        string
	ActingUser   string
	Via          string
	Confirmation string
}

var identity = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func (a Attribution) valid() bool {
	return identity.MatchString(a.Actor) && (a.ActingUser == "" || identity.MatchString(a.ActingUser))
}

func (a Attribution) apply(h http.Header) {
	h.Set("X-Ploeg-Actor", a.Actor)
	if a.ActingUser != "" {
		h.Set("X-Ploeg-Acting-User", a.ActingUser)
	}
	if a.Via != "" {
		h.Set("X-Ploeg-Via", a.Via)
	}
	if a.Confirmation != "" {
		h.Set("X-Ploeg-Confirmation", a.Confirmation)
	}
}

func (c *Client) write(ctx context.Context, path string, who Attribution, body, into any) error {
	if !who.valid() {
		return &Error{Status: http.StatusBadRequest, Code: "actor_required", Message: "A write needs an actor of 1 to 128 identifier characters."}
	}
	return c.do(ctx, http.MethodPost, path, nil, &who, body, into)
}

// Submission admits a manual-origin Work Item (POST /work-items, ADR-0102).
// RequestID is the idempotency key: the same body again returns the first
// Work Item with Replayed true. BudgetUSD nil means the consumer's maximum.
type Submission struct {
	RequestID   string   `json:"requestId"`
	Team        string   `json:"team"`
	Target      string   `json:"target"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	BudgetUSD   *float64 `json:"budgetUsd,omitempty"`
}

// Submitted is the admitted Work Item.
type Submitted struct {
	WorkItem struct {
		ID        string   `json:"id"`
		Team      string   `json:"team"`
		State     string   `json:"state"`
		ShiftID   *string  `json:"shiftId"`
		BudgetUSD float64  `json:"budgetUsd"`
		PoolUSD   *float64 `json:"poolUsd"`
	} `json:"workItem"`
	Created  bool `json:"created"`
	Replayed bool `json:"replayed"`
}

// SubmitWorkItem answers POST /work-items.
func (c *Client) SubmitWorkItem(ctx context.Context, who Attribution, s Submission) (Submitted, error) {
	var out Submitted
	return out, c.write(ctx, "work-items", who, s, &out)
}

// Decision is the answer to approving or rejecting a proposed Work Item.
type Decision struct {
	WorkItemID string `json:"workItemId"`
	Team       string `json:"team"`
	State      string `json:"state"`
	Approved   bool   `json:"approved"`
}

// Approve answers POST /work-items/{id}/approve. The route takes no command
// id: approving an item that is no longer proposed answers ErrConflict.
func (c *Client) Approve(ctx context.Context, who Attribution, id, reason string) (Decision, error) {
	return c.decide(ctx, who, id, "approve", reason)
}

// Reject answers POST /work-items/{id}/reject; reason is required.
func (c *Client) Reject(ctx context.Context, who Attribution, id, reason string) (Decision, error) {
	return c.decide(ctx, who, id, "reject", reason)
}

func (c *Client) decide(ctx context.Context, who Attribution, id, verb, reason string) (Decision, error) {
	var out struct {
		Decision Decision `json:"decision"`
	}
	if !numericID(id) {
		return out.Decision, badID()
	}
	err := c.write(ctx, "work-items/"+id+"/"+verb, who, map[string]string{"reason": reason}, &out)
	return out.Decision, err
}

// Requeue restarts stopped work (ADR-0044). CommandID is required; a repeated
// CommandID returns the first result with Replayed true. FromRound 0 means 1.
type Requeue struct {
	CommandID     string   `json:"commandId"`
	FromRound     int      `json:"fromRound,omitempty"`
	PoolUSD       *float64 `json:"poolUsd,omitempty"`
	Note          string   `json:"note,omitempty"`
	ExpectedState string   `json:"expectedState,omitempty"`
}

// Requeued is the restarted Work Item.
type Requeued struct {
	WorkItemID      string  `json:"workItemId"`
	Team            string  `json:"team"`
	State           string  `json:"state"`
	FromRound       int     `json:"fromRound"`
	PoolUSD         float64 `json:"poolUsd"`
	ShiftID         *string `json:"shiftId"`
	PreviousShiftID *string `json:"previousShiftId"`
	CommandID       *string `json:"commandId"`
	Replayed        bool    `json:"replayed"`
}

// Requeue answers POST /work-items/{id}/requeue.
func (c *Client) Requeue(ctx context.Context, who Attribution, id string, r Requeue) (Requeued, error) {
	var out struct {
		Requeue Requeued `json:"requeue"`
	}
	if !numericID(id) {
		return out.Requeue, badID()
	}
	err := c.write(ctx, "work-items/"+id+"/requeue", who, r, &out)
	return out.Requeue, err
}

// Cancelled is the withdrawn Work Item. Withdrawn false means it had already
// settled; Replayed true means commandID already withdrew it.
type Cancelled struct {
	WorkItemID    string  `json:"workItemId"`
	State         string  `json:"state"`
	Withdrawn     bool    `json:"withdrawn"`
	ShiftID       *string `json:"shiftId"`
	CancelledRuns int     `json:"cancelledRuns"`
	StoppedRuns   int     `json:"stoppedRuns"`
	KeysBlocked   bool    `json:"keysBlocked"`
	CommandID     *string `json:"commandId"`
	Replayed      bool    `json:"replayed"`
}

// Cancel answers POST /work-items/{id}/cancel with a commandId body.
// expectedShiftID, when not empty, must be the item's open Shift.
func (c *Client) Cancel(ctx context.Context, who Attribution, id, commandID, expectedShiftID string) (Cancelled, error) {
	var out struct {
		Cancellation Cancelled `json:"cancellation"`
	}
	if !numericID(id) {
		return out.Cancellation, badID()
	}
	body := map[string]any{"commandId": commandID}
	if expectedShiftID != "" {
		body["expectedShiftId"] = expectedShiftID
	}
	err := c.write(ctx, "work-items/"+id+"/cancel", who, body, &out)
	return out.Cancellation, err
}

// TakenOver is a take-over of a Work Item's running Runs (ADR-0142). State
// is yielding until they stop at a safe point; the Work Item detail's
// takeOver follows it from there.
type TakenOver struct {
	ID                   string         `json:"id"`
	WorkItemID           string         `json:"workItemId"`
	ShiftID              string         `json:"shiftId"`
	Round                int            `json:"round"`
	Branch               string         `json:"branch"`
	State                string         `json:"state"`
	Runs                 []TakenOverRun `json:"runs"`
	CancelledPendingRuns int            `json:"cancelledPendingRuns"`
	CommandID            string         `json:"commandId"`
	Replayed             bool           `json:"replayed"`
}

// TakenOverRun is a running Run a take-over asked to yield.
type TakenOverRun struct {
	ID   string `json:"id"`
	Role string `json:"role"`
}

// TakeOver answers POST /work-items/{id}/take-over: who.Actor takes over the
// Work Item's running Runs, and hands it back later with Requeue and Resume.
// expectedShiftID, when not empty, must be the item's open Shift.
func (c *Client) TakeOver(ctx context.Context, who Attribution, id, commandID, expectedShiftID string) (TakenOver, error) {
	var out struct {
		TakeOver TakenOver `json:"takeOver"`
	}
	if !numericID(id) {
		return out.TakeOver, badID()
	}
	body := map[string]any{"commandId": commandID}
	if expectedShiftID != "" {
		body["expectedShiftId"] = expectedShiftID
	}
	err := c.write(ctx, "work-items/"+id+"/take-over", who, body, &out)
	return out.TakeOver, err
}

// Note is an instruction stored for the next Run of a Work Item.
type Note struct {
	ID         string  `json:"id"`
	WorkItemID string  `json:"workItemId"`
	CommandID  string  `json:"commandId"`
	ShiftID    *string `json:"shiftId"`
	Consumed   bool    `json:"consumed"`
	Replayed   bool    `json:"replayed"`
}

// AddNote answers POST /work-items/{id}/notes. A repeated commandID with the
// same text returns the first note with Replayed true.
func (c *Client) AddNote(ctx context.Context, who Attribution, id, commandID, text, expectedShiftID string) (Note, error) {
	var out struct {
		Note Note `json:"note"`
	}
	if !numericID(id) {
		return out.Note, badID()
	}
	body := map[string]any{"commandId": commandID, "text": text}
	if expectedShiftID != "" {
		body["expectedShiftId"] = expectedShiftID
	}
	err := c.write(ctx, "work-items/"+id+"/notes", who, body, &out)
	return out.Note, err
}

var executionID = regexp.MustCompile(`^[A-Za-z0-9]{32}$`)

// Execution answers GET /executions/{id} for the execution's owner, who is
// who.Actor; ActingUser, when set, is the person reading on their behalf.
func (c *Client) Execution(ctx context.Context, who Attribution, id string) (store.OperatorExecution, error) {
	var out struct {
		Execution store.OperatorExecution `json:"execution"`
	}
	if !executionID.MatchString(id) {
		return out.Execution, &Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: "Execution id must be 32 letters or digits."}
	}
	if !who.valid() {
		return out.Execution, &Error{Status: http.StatusBadRequest, Code: "actor_required", Message: "An execution read needs its owner as actor."}
	}
	err := c.do(ctx, http.MethodGet, "executions/"+id, nil, &who, nil, &out)
	return out.Execution, err
}

// ExecutionCommand answers POST /executions/{id}/commands. cmd.ID is the
// command id; the same command again returns the first answer, and a stale
// ExpectedRevision or Generation answers ErrConflict.
func (c *Client) ExecutionCommand(ctx context.Context, who Attribution, id string, cmd store.OperatorExecutionCommand) (store.OperatorExecution, error) {
	var out struct {
		Execution store.OperatorExecution `json:"execution"`
	}
	if !executionID.MatchString(id) {
		return out.Execution, &Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: "Execution id must be 32 letters or digits."}
	}
	cmd.AuthenticatedBy = ""
	err := c.write(ctx, "executions/"+id+"/commands", who, cmd, &out)
	return out.Execution, err
}

func badID() error {
	return &Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: "Work Item id must be a positive integer."}
}
