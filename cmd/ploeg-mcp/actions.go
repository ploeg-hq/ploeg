package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ploeg-hq/ploeg/pkg/operatorclient"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

const (
	confirmWindow    = 10 * time.Minute
	fallbackVia      = "mcp:ploeg-mcp"
	maxVia           = 128
	notConfirmed     = "Not done: you did not confirm. Nothing changed."
	cannotConfirm    = "This client cannot ask you to confirm an action, so ploeg-mcp did nothing. Use an MCP client that supports elicitation over the 2026-07-28 protocol, or act through Ploeg's operator API or a consumer application."
	confirmFieldName = "confirm"
)

var identityName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

type person struct {
	ID       string
	Username string
	token    string
}

type personKey struct{}

func withPerson(ctx context.Context, p person) context.Context {
	return context.WithValue(ctx, personKey{}, p)
}

func personFrom(ctx context.Context) (person, bool) {
	p, ok := ctx.Value(personKey{}).(person)
	return p, ok && p.ID != ""
}

func via(info *mcp.Implementation) string {
	if info == nil || strings.TrimSpace(info.Name) == "" {
		return fallbackVia
	}
	label := "mcp:" + printable(info.Name)
	if v := printable(info.Version); v != "" {
		label += " " + v
	}
	if len(label) > maxVia {
		label = strings.TrimRight(label[:maxVia], " ")
	}
	return label
}

func printable(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 0x20 && r <= 0x7e {
			b.WriteRune(r)
		} else {
			b.WriteRune('?')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func randomID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

type actionOut struct {
	Done        bool   `json:"done" jsonschema:"true when Ploeg made the change"`
	Summary     string `json:"summary"`
	WorkItemID  string `json:"workItemId,omitempty"`
	ExecutionID string `json:"executionId,omitempty"`
	State       string `json:"state,omitempty" jsonschema:"the Work Item's or execution's state after the change"`
	CommandID   string `json:"commandId,omitempty" jsonschema:"pass it back as commandId to repeat this call without acting twice"`
	Replayed    bool   `json:"replayed,omitempty" jsonschema:"true when this commandId had already acted; nothing changed now"`
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// action is one state-changing tool: describe states the exact effect the
// person confirms, act performs it with the confirmed attribution.
type action[In any] struct {
	name      string
	commandID func(In) string
	describe  func(ctx context.Context, who operatorclient.Attribution, in In) (string, error)
	act       func(ctx context.Context, who operatorclient.Attribution, commandID string, in In) (actionOut, error)
	prove     func(ctx context.Context, p person) (context.Context, error)
	confirm   *confirmations
}

type writerKey struct{}

// prove exchanges the person's token for Ploeg's audience and returns ctx
// carrying a client that writes with it, or ctx unchanged when ploeg-mcp has
// no exchange configured (ADR-0150).
func (s *server) prove(ctx context.Context, p person) (context.Context, error) {
	if s.exchange == nil {
		return ctx, nil
	}
	if p.token == "" {
		return ctx, errors.New("ploeg-mcp has no token of yours to show Ploeg; nothing changed")
	}
	exchanged, err := s.exchange.Exchange(ctx, p.token)
	if err != nil {
		return ctx, fmt.Errorf("the identity provider would not vouch for you to Ploeg (%v); nothing changed. Sign in again, or ask an administrator to check PLOEG_MCP_EXCHANGE_*", err)
	}
	w, err := s.c.AsPerson(exchanged)
	if err != nil {
		return ctx, fmt.Errorf("the identity provider's token for Ploeg is unusable (%v); nothing changed", err)
	}
	return context.WithValue(ctx, writerKey{}, w), nil
}

// writer is the client that makes changes: the person's own exchanged token
// when prove set one, else the consumer's.
func (s *server) writer(ctx context.Context) *operatorclient.Client {
	if w, ok := ctx.Value(writerKey{}).(*operatorclient.Client); ok {
		return w
	}
	return s.c
}

func (a action[In]) handler(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, actionOut, error) {
	var out actionOut
	p, ok := personFrom(ctx)
	if !ok {
		return nil, out, errors.New("ploeg-mcp acts only over HTTP for a signed-in person; nothing changed")
	}
	who := operatorclient.Attribution{Actor: p.ID, Via: via(req.ClientInfo())}
	pages := a.confirm.pages
	caps := req.ClientCapabilities()
	if caps == nil || caps.Elicitation == nil {
		return nil, out, errors.New(cannotConfirm)
	}
	if pages != nil && caps.Elicitation.URL == nil {
		return nil, out, errors.New(cannotOpenPage)
	}
	args, _ := json.Marshal(in)
	effect, err := a.describe(ctx, who, in)
	if err != nil {
		return nil, out, err
	}
	if req.Params.InputResponses == nil {
		commandID := a.commandID(in)
		if commandID == "" {
			commandID = randomID("mcp-")
		}
		state := confirmState{Tool: a.name, ID: randomID("confirm-"), CommandID: commandID, Effect: digest(effect), Args: digest(string(args)),
			Actor: p.ID, Expires: a.confirm.now().Add(a.confirm.window).Unix()}
		if pages != nil {
			link, err := pages.offer(state, effect, string(args), p)
			if err != nil {
				return nil, out, err
			}
			return pages.request(state.ID, link, effect, a.confirm.seal(state)), out, nil
		}
		return &mcp.CallToolResult{
			InputRequests: mcp.InputRequestMap{state.ID: &mcp.ElicitParams{Mode: "form", Message: effect, RequestedSchema: confirmSchema}},
			RequestState:  a.confirm.seal(state),
		}, out, nil
	}
	state, err := a.confirm.open(req.Params.RequestState)
	if err != nil {
		return nil, out, err
	}
	if state.Tool != a.name || state.Actor != p.ID || state.Args != digest(string(args)) || !identityName.MatchString(state.CommandID) {
		return nil, out, errForeignConfirmation
	}
	answer, _ := req.Params.InputResponses[state.ID].(*mcp.ElicitResult)
	if pages != nil {
		if answer == nil || answer.Action != "accept" {
			pages.forget(state.ID)
			return nil, actionOut{Summary: notConfirmed}, nil
		}
		switch decision, link := pages.awaitDecision(ctx, state.ID); decision {
		case pageMissing:
			return nil, out, errPageClosed
		case pageDeclined:
			pages.forget(state.ID)
			return nil, actionOut{Summary: notConfirmed}, nil
		case pageWaiting:
			return pages.request(state.ID, link, effect, req.Params.RequestState), out, nil
		}
	}
	if err := a.confirm.spend(state); err != nil {
		return nil, out, err
	}
	if pages != nil {
		pages.forget(state.ID)
	}
	if state.Effect != digest(effect) {
		return nil, out, errors.New("what this would do changed while you decided; nothing changed. Call the tool again to see the new effect")
	}
	if pages == nil && (answer == nil || answer.Action != "accept" || answer.Content[confirmFieldName] != true) {
		return nil, actionOut{Summary: notConfirmed}, nil
	}
	who.Confirmation = state.ID + ":" + state.Effect
	if a.prove != nil {
		if ctx, err = a.prove(ctx, p); err != nil {
			return nil, out, err
		}
	}
	out, err = a.act(ctx, who, state.CommandID, in)
	if err != nil {
		return nil, out, explainWrite(err, state.CommandID)
	}
	out.Done = true
	if out.CommandID == "" {
		out.CommandID = state.CommandID
	}
	return nil, out, nil
}

var confirmSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		confirmFieldName: map[string]any{"type": "boolean", "title": "Do it", "description": "Tick to let Ploeg make exactly this change."},
	},
	"required": []string{confirmFieldName},
}

func explainWrite(err error, commandID string) error {
	var apiErr *operatorclient.Error
	switch {
	case errors.Is(err, operatorclient.ErrTimeout):
		return fmt.Errorf("Ploeg did not answer within 5 seconds, so the change may or may not have happened. Call the tool again with commandId %s to find out without acting twice", commandID)
	case errors.Is(err, operatorclient.ErrUnauthorized):
		return errors.New("Ploeg refused the credential ploeg-mcp called it with; nothing changed. Ask an administrator to check PLOEG_MCP_GROUPS, or the consumer's audiences or clientIds when PLOEG_MCP_EXCHANGE_TOKEN_URL is set")
	case errors.As(err, &apiErr) && apiErr.Message != "":
		return fmt.Errorf("Ploeg refused (%d %s): %s Nothing changed", apiErr.Status, apiErr.Code, apiErr.Message)
	}
	return fmt.Errorf("%v; nothing changed", explain(err))
}

func destructive(v bool) *mcp.ToolAnnotations {
	closed := false
	return &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &v, OpenWorldHint: &closed}
}

func addAction[In any](srv *mcp.Server, s *server, tool *mcp.Tool, a action[In]) {
	tool.Annotations.Title = tool.Title
	a.name = tool.Name
	a.prove = s.prove
	a.confirm = s.confirm
	mcp.AddTool(srv, tool, a.handler)
}

func registerActions(srv *mcp.Server, s *server) {
	addAction(srv, s, &mcp.Tool{Name: "ploeg_submit_work", Title: "Submit work", Annotations: destructive(false),
		Description: "Submit a new Work Item to a Team on a registered target; Ploeg queues it and starts a budgeted Shift. Asks the person to confirm first."},
		action[submitIn]{commandID: func(in submitIn) string { return in.CommandID }, describe: s.describeSubmit, act: s.submit})
	addAction(srv, s, &mcp.Tool{Name: "ploeg_approve_work", Title: "Approve proposed work", Annotations: destructive(false),
		Description: "Approve a proposed Work Item so its Team starts it. Asks the person to confirm first."},
		action[decideIn]{commandID: func(decideIn) string { return "" }, describe: s.describeDecision(true), act: s.decide(true)})
	addAction(srv, s, &mcp.Tool{Name: "ploeg_reject_work", Title: "Reject proposed work", Annotations: destructive(true),
		Description: "Reject a proposed Work Item with a reason; it closes as done and its budget is released. Asks the person to confirm first."},
		action[decideIn]{commandID: func(decideIn) string { return "" }, describe: s.describeDecision(false), act: s.decide(false)})
	addAction(srv, s, &mcp.Tool{Name: "ploeg_retry_work", Title: "Retry work", Annotations: destructive(false),
		Description: "Restart a needs_human, stale or awaiting_review Work Item, optionally from a later Round, with an optional note. Asks the person to confirm first."},
		action[retryIn]{commandID: func(in retryIn) string { return in.CommandID }, describe: s.describeRetry, act: s.retry})
	addAction(srv, s, &mcp.Tool{Name: "ploeg_stop_work", Title: "Stop work", Annotations: destructive(true),
		Description: "Stop a Work Item: withdraw it, cancel its pending Runs and stop its running ones. Asks the person to confirm first."},
		action[refIn]{commandID: func(in refIn) string { return in.CommandID }, describe: s.describeStop, act: s.stop})
	addAction(srv, s, &mcp.Tool{Name: "ploeg_add_note", Title: "Add a note", Annotations: destructive(false),
		Description: "Leave an instruction that the next Run of a live Work Item reads. Asks the person to confirm first."},
		action[noteIn]{commandID: func(in noteIn) string { return in.CommandID }, describe: s.describeNote, act: s.note})
	addAction(srv, s, &mcp.Tool{Name: "ploeg_steer_execution", Title: "Steer an attended execution", Annotations: destructive(true),
		Description: "Send a message to, pause, resume or cancel an attended execution you own, whichever application started it, when its Team is in your access. Find its executionId in ploeg_get_work, which lists a Work Item's executions and marks yours. Asks the person to confirm first."},
		action[steerIn]{commandID: func(in steerIn) string { return in.CommandID }, describe: s.describeSteer, act: s.steer})
}

type submitIn struct {
	Team        string   `json:"team"`
	Target      string   `json:"target" jsonschema:"a registered target key"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	BudgetUSD   *float64 `json:"budgetUsd,omitempty" jsonschema:"at most this group's maximum; default that maximum"`
	CommandID   string   `json:"commandId,omitempty" jsonschema:"optional; reuse the commandId of an earlier call to repeat it without acting twice"`
}

func (s *server) describeSubmit(_ context.Context, _ operatorclient.Attribution, in submitIn) (string, error) {
	budget := "up to the maximum budget of your access"
	if in.BudgetUSD != nil {
		budget = "up to " + usd(*in.BudgetUSD)
	}
	return fmt.Sprintf("Submit a new Work Item to Team %s on target %s: %q. Ploeg queues it and its Team starts a Shift that may spend %s on models.",
		in.Team, in.Target, clip(in.Title, 200), budget), nil
}

func (s *server) submit(ctx context.Context, who operatorclient.Attribution, commandID string, in submitIn) (actionOut, error) {
	got, err := s.writer(ctx).SubmitWorkItem(ctx, who, operatorclient.Submission{RequestID: commandID, Team: in.Team, Target: in.Target, Title: in.Title, Description: in.Description, BudgetUSD: in.BudgetUSD})
	if err != nil {
		return actionOut{}, err
	}
	summary := fmt.Sprintf("Submitted Work Item %s to Team %s; it is %s.", got.WorkItem.ID, got.WorkItem.Team, got.WorkItem.State)
	if got.Replayed {
		summary = fmt.Sprintf("Work Item %s was already submitted with this commandId; nothing changed now.", got.WorkItem.ID)
	}
	return actionOut{Summary: summary, WorkItemID: got.WorkItem.ID, State: got.WorkItem.State, Replayed: got.Replayed}, nil
}

type refIn struct {
	Ref       string `json:"ref" jsonschema:"Ploeg id, provider:externalId or tracker task URL"`
	CommandID string `json:"commandId,omitempty" jsonschema:"optional; reuse the commandId of an earlier call to repeat it without acting twice"`
}

func (s *server) item(ctx context.Context, ref string) (store.OperatorItem, error) {
	id, err := s.resolve(ctx, ref)
	if err != nil {
		return store.OperatorItem{}, err
	}
	d, err := s.c.WorkItem(ctx, id)
	if err != nil {
		return store.OperatorItem{}, explain(err)
	}
	return d.Item, nil
}

func named(i store.OperatorItem) string {
	return fmt.Sprintf("Work Item %s %q (Team %s, now %s)", i.ID, clip(i.Title, 200), i.Team, i.State)
}

type decideIn struct {
	Ref    string `json:"ref" jsonschema:"Ploeg id, provider:externalId or tracker task URL of a proposed Work Item"`
	Reason string `json:"reason,omitempty" jsonschema:"why; required to reject"`
}

func (s *server) describeDecision(approve bool) func(context.Context, operatorclient.Attribution, decideIn) (string, error) {
	return func(ctx context.Context, _ operatorclient.Attribution, in decideIn) (string, error) {
		if !approve && strings.TrimSpace(in.Reason) == "" {
			return "", errors.New("a rejection needs a reason; nothing changed")
		}
		i, err := s.item(ctx, in.Ref)
		if err != nil {
			return "", err
		}
		if approve {
			return fmt.Sprintf("Approve %s. Ploeg queues it and its Team starts work on it, which spends model budget.", named(i)), nil
		}
		return fmt.Sprintf("Reject %s with the reason %q. It closes as done and its budget is released.", named(i), clip(in.Reason, 300)), nil
	}
}

func (s *server) decide(approve bool) func(context.Context, operatorclient.Attribution, string, decideIn) (actionOut, error) {
	return func(ctx context.Context, who operatorclient.Attribution, _ string, in decideIn) (actionOut, error) {
		i, err := s.item(ctx, in.Ref)
		if err != nil {
			return actionOut{}, err
		}
		call, verb := s.writer(ctx).Reject, "Rejected"
		if approve {
			call, verb = s.writer(ctx).Approve, "Approved"
		}
		d, err := call(ctx, who, i.ID, in.Reason)
		if err != nil {
			return actionOut{}, err
		}
		return actionOut{Summary: fmt.Sprintf("%s Work Item %s; it is %s.", verb, d.WorkItemID, d.State), WorkItemID: d.WorkItemID, State: d.State}, nil
	}
}

type retryIn struct {
	Ref       string `json:"ref" jsonschema:"Ploeg id, provider:externalId or tracker task URL"`
	FromRound int    `json:"fromRound,omitempty" jsonschema:"the Round to restart from; default 1"`
	Note      string `json:"note,omitempty" jsonschema:"what the restarted Runs are told"`
	CommandID string `json:"commandId,omitempty" jsonschema:"optional; reuse the commandId of an earlier call to repeat it without acting twice"`
}

func (in retryIn) round() int { return max(in.FromRound, 1) }

func (s *server) describeRetry(ctx context.Context, _ operatorclient.Attribution, in retryIn) (string, error) {
	i, err := s.item(ctx, in.Ref)
	if err != nil {
		return "", err
	}
	effect := fmt.Sprintf("Retry %s from Round %d. Ploeg opens a new Shift on the same branch that spends model budget from its Team's pool.", named(i), in.round())
	if note := strings.TrimSpace(in.Note); note != "" {
		effect += fmt.Sprintf(" Its Runs are told: %q.", clip(note, 500))
	}
	return effect, nil
}

func (s *server) retry(ctx context.Context, who operatorclient.Attribution, commandID string, in retryIn) (actionOut, error) {
	i, err := s.item(ctx, in.Ref)
	if err != nil {
		return actionOut{}, err
	}
	r, err := s.writer(ctx).Requeue(ctx, who, i.ID, operatorclient.Requeue{CommandID: commandID, FromRound: in.round(), Note: strings.TrimSpace(in.Note)})
	if err != nil {
		return actionOut{}, err
	}
	summary := fmt.Sprintf("Restarted Work Item %s from Round %d; it is %s.", r.WorkItemID, r.FromRound, r.State)
	if r.Replayed {
		summary = fmt.Sprintf("Work Item %s was already restarted with this commandId; nothing changed now.", r.WorkItemID)
	}
	return actionOut{Summary: summary, WorkItemID: r.WorkItemID, State: r.State, Replayed: r.Replayed}, nil
}

func (s *server) describeStop(ctx context.Context, _ operatorclient.Attribution, in refIn) (string, error) {
	i, err := s.item(ctx, in.Ref)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Stop %s. Ploeg withdraws it, cancels its pending Runs, stops its running Runs and blocks their model keys. Money already spent stays spent; assign or submit it again to start over.", named(i)), nil
}

func (s *server) stop(ctx context.Context, who operatorclient.Attribution, commandID string, in refIn) (actionOut, error) {
	i, err := s.item(ctx, in.Ref)
	if err != nil {
		return actionOut{}, err
	}
	c, err := s.writer(ctx).Cancel(ctx, who, i.ID, commandID, "")
	if err != nil {
		return actionOut{}, err
	}
	summary := fmt.Sprintf("Stopped Work Item %s: %d pending Runs cancelled, %d running Runs stopped; it is %s.", c.WorkItemID, c.CancelledRuns, c.StoppedRuns, c.State)
	switch {
	case c.Replayed:
		summary = fmt.Sprintf("Work Item %s was already stopped with this commandId; nothing changed now.", c.WorkItemID)
	case !c.Withdrawn:
		summary = fmt.Sprintf("Work Item %s had already settled as %s; nothing changed.", c.WorkItemID, c.State)
	}
	return actionOut{Summary: summary, WorkItemID: c.WorkItemID, State: c.State, Replayed: c.Replayed}, nil
}

type noteIn struct {
	Ref       string `json:"ref" jsonschema:"Ploeg id, provider:externalId or tracker task URL"`
	Text      string `json:"text" jsonschema:"the instruction, 1 to 4096 characters"`
	CommandID string `json:"commandId,omitempty" jsonschema:"optional; reuse the commandId of an earlier call to repeat it without acting twice"`
}

func (s *server) describeNote(ctx context.Context, _ operatorclient.Attribution, in noteIn) (string, error) {
	text := strings.TrimSpace(in.Text)
	if text == "" || len([]rune(text)) > store.MaxOperatorNoteRunes {
		return "", fmt.Errorf("a note is 1 to %d characters; nothing changed", store.MaxOperatorNoteRunes)
	}
	i, err := s.item(ctx, in.Ref)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Leave this note for the next Run of %s: %q. The note costs nothing itself; the Run that reads it spends as planned.", named(i), clip(text, 1000)), nil
}

func (s *server) note(ctx context.Context, who operatorclient.Attribution, commandID string, in noteIn) (actionOut, error) {
	i, err := s.item(ctx, in.Ref)
	if err != nil {
		return actionOut{}, err
	}
	n, err := s.writer(ctx).AddNote(ctx, who, i.ID, commandID, strings.TrimSpace(in.Text), "")
	if err != nil {
		return actionOut{}, err
	}
	summary := fmt.Sprintf("Left a note on Work Item %s for its next Run.", n.WorkItemID)
	if n.Replayed {
		summary = fmt.Sprintf("This note was already left on Work Item %s with this commandId; nothing changed now.", n.WorkItemID)
	}
	return actionOut{Summary: summary, WorkItemID: n.WorkItemID, State: i.State, Replayed: n.Replayed}, nil
}

type steerIn struct {
	ExecutionID string `json:"executionId" jsonschema:"the attended execution's 32-character id"`
	Action      string `json:"action" jsonschema:"message, pause, resume or cancel"`
	Text        string `json:"text,omitempty" jsonschema:"the message, for action message"`
	CommandID   string `json:"commandId,omitempty" jsonschema:"optional; reuse the commandId of an earlier call to repeat it without acting twice"`
}

var steerEffects = map[string]string{
	"message": "Send this message to attended execution %s (Work Item %s, now %s), which reads it at its next prompt turn: %q.",
	"pause":   "Pause attended execution %s (Work Item %s, now %s). Its agent stops at a safe point and keeps its session for a bounded time; it spends nothing until it resumes.",
	"resume":  "Resume attended execution %s (Work Item %s, now %s). Its agent continues and spends model budget again.",
	"cancel":  "Cancel attended execution %s (Work Item %s, now %s). Its agent stops for good; money already spent stays spent.",
}

func (s *server) describeSteer(ctx context.Context, who operatorclient.Attribution, in steerIn) (string, error) {
	format, ok := steerEffects[in.Action]
	if !ok {
		return "", errors.New("action must be message, pause, resume or cancel; nothing changed")
	}
	text := strings.TrimSpace(in.Text)
	if (in.Action == "message") != (text != "") {
		return "", errors.New("a message needs text, and only a message takes text; nothing changed")
	}
	e, err := s.c.Execution(ctx, who, in.ExecutionID)
	if err != nil {
		return "", explainExecution(err)
	}
	if in.Action == "message" {
		return fmt.Sprintf(format, e.ID, e.WorkItemID, e.State, clip(text, 1000)), nil
	}
	return fmt.Sprintf(format, e.ID, e.WorkItemID, e.State), nil
}

func (s *server) steer(ctx context.Context, who operatorclient.Attribution, commandID string, in steerIn) (actionOut, error) {
	e, err := s.c.Execution(ctx, who, in.ExecutionID)
	if err != nil {
		return actionOut{}, explainExecution(err)
	}
	cmd := store.OperatorExecutionCommand{ID: commandID, Action: in.Action, ExpectedRevision: e.Revision, Generation: e.Generation, Text: strings.TrimSpace(in.Text)}
	after, err := s.writer(ctx).ExecutionCommand(ctx, who, e.ID, cmd)
	if err != nil {
		return actionOut{}, err
	}
	return actionOut{Summary: fmt.Sprintf("Sent %s to execution %s; it is %s.", in.Action, after.ID, after.State), ExecutionID: after.ID, WorkItemID: after.WorkItemID, State: after.State}, nil
}

func explainExecution(err error) error {
	if errors.Is(err, operatorclient.ErrNotFound) {
		return errors.New("no attended execution with that id is yours in this group's Teams; nothing changed. ploeg_get_work lists a Work Item's executions and marks yours")
	}
	return explain(err)
}
