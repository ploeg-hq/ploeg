// Package clickup is a TrackerProvider for ClickUp, the sibling of
// pkg/provider/vikunja: webhook verification and parsing, plus the API
// write-backs that let a run reach a person (backlog #31).
//
// Write-backs are opt-in by configuration. Without BaseURL and Token the
// provider keeps the logging no-op, because a deployment that has not been
// given a tracker credential must degrade to "the board is not updated"
// rather than to "the run fails".
//
// Three ClickUp shapes differ from Vikunja in ways a copy-paste would get
// wrong:
//
//   - Auth is the raw token in Authorization, with no "Bearer " prefix.
//     ClickUp returns 401 for the prefixed form.
//   - There is no global notion of "done". Status is a per-List string chosen
//     by whoever built the list, so SetStatus needs to be TOLD the name; see
//     DoneStatus.
//   - Priority is inverted. ClickUp's id 1 is urgent and 4 is low, while Ploeg
//     (following Vikunja) treats a higher number as more urgent, so the scale
//     is flipped on the way in rather than leaking backwards ordering.
package clickup

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/apilimit"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// DefaultBaseURL is ClickUp's public API root.
const DefaultBaseURL = "https://api.clickup.com/api/v2"

type Provider struct {
	// Secret verifies X-Signature (raw-body HMAC-SHA256, hex). Empty disables
	// verification — local development only.
	Secret string
	// DefaultTeam receives assigned items when no assignee mapping matches.
	DefaultTeam string
	// TeamMap resolves tracker assignee usernames (and email addresses,
	// lowercased) to Ploeg team names.
	TeamMap map[string]string
	// BaseURL is the ClickUp API root; empty uses DefaultBaseURL. Reads and
	// write-backs additionally need Token.
	BaseURL string
	// Token is a ClickUp personal token (pk_…) or OAuth access token. Empty
	// disables reads and write-backs (they log and no-op).
	Token string
	// DoneStatus is the List status name SetStatus applies for a finished
	// item, e.g. "complete" or "done". Empty leaves the board untouched and
	// says so: ClickUp statuses are per-List custom strings, so guessing one
	// would either 400 or move the task to a status nobody chose.
	DoneStatus string
	// Statuses names the List status SetStatus applies for each Ploeg state
	// it is told, e.g. awaiting_review → "in review" and needs_human →
	// "blocked" (ADR-0183). An entry for done overrides DoneStatus. A state
	// without an entry leaves the board untouched. Build it with
	// provider.ParseStatusMap, which refuses a state SetStatus is never
	// called with.
	Statuses map[work.State]string
	// Agents are the usernames or email addresses of the members whose
	// assignment hands a task to Ploeg, compared without case. When set, an
	// assignment or removal of anyone else is ignored, so a person assigning
	// themselves starts nothing; the List's pinned team then picks the Team.
	// Empty dispatches on every assignee.
	Agents []string
	// Workspace is the ClickUp Workspace (the API's team) whose webhooks
	// CheckWebhooks lists and RegisterWebhook writes. Empty reports every
	// List's webhook as unreadable.
	Workspace string
	// HC is optional; nil gets a 30s client.
	HC  *http.Client
	Log *slog.Logger
}

func (p *Provider) Name() string { return "clickup" }

// hookPayload is the (tolerantly parsed) ClickUp webhook body. ClickUp sends a
// THIN payload by design — an event name, the task id, and a diff — so almost
// everything here is a trigger rather than state. FetchItem is the truth.
type hookPayload struct {
	Event  string `json:"event"`
	TaskID string `json:"task_id"`
	// HistoryItems describes what changed. Assignee events are the only ones
	// whose detail the parser needs, to route to a team.
	HistoryItems []struct {
		ID    string `json:"id"`
		Field string `json:"field"`
		After struct {
			Username string `json:"username"`
			Email    string `json:"email"`
		} `json:"after"`
		Before struct {
			Username string `json:"username"`
			Email    string `json:"email"`
		} `json:"before"`
		User struct {
			Username string `json:"username"`
			Email    string `json:"email"`
		} `json:"user"`
		Date string `json:"date"`
	} `json:"history_items"`
}

func (pl hookPayload) changedBy() (string, time.Time) {
	for pass := 0; pass < 2; pass++ {
		for _, h := range pl.HistoryItems {
			if pass == 0 && h.Field != "status" {
				continue
			}
			who := firstNonEmpty(h.User.Username, h.User.Email)
			var at time.Time
			if ms, err := strconv.ParseInt(h.Date, 10, 64); err == nil && ms > 0 {
				at = time.UnixMilli(ms).UTC()
			}
			if who != "" || !at.IsZero() {
				return who, at
			}
		}
	}
	return "", time.Time{}
}

// ParseWebhook verifies the signature against the RAW body before JSON
// parsing (backlog #2) and returns normalized events.
func (p *Provider) ParseWebhook(r *http.Request) ([]provider.TrackerEvent, error) {
	body, err := provider.ReadWebhookBody(r, maxPayloadBytes)
	if err != nil {
		return nil, err
	}
	if p.Secret == "" {
		return nil, errors.New("no webhook secret configured; set PLOEG_CLICKUP_SECRET")
	}
	if !verify(p.Secret, body, r.Header.Get("X-Signature")) {
		return nil, errors.New("invalid webhook signature")
	}

	var pl hookPayload
	if err := json.Unmarshal(body, &pl); err != nil {
		return nil, fmt.Errorf("parse payload: %w", err)
	}
	if pl.TaskID == "" {
		return nil, errors.New("payload has no task")
	}

	// Route on whoever was ADDED as an assignee; on removal the relevant
	// identity is the one that left. With Agents set, only an agent member's
	// assignment or removal counts, and one that names none is dropped.
	var actor string
	removed := false
	for _, h := range pl.HistoryItems {
		if h.Field != "assignee" && h.Field != "assignee_add" && h.Field != "assignee_rem" {
			continue
		}
		who := firstNonEmpty(h.After.Username, h.After.Email)
		if who == "" {
			who = firstNonEmpty(h.Before.Username, h.Before.Email)
		}
		if who == "" || (len(p.Agents) > 0 && !p.isAgent(h.After.Username, h.After.Email, h.Before.Username, h.Before.Email)) {
			continue
		}
		actor, removed = who, h.Field == "assignee_rem"
		break
	}
	if pl.Event == "taskAssigneeUpdated" && len(p.Agents) > 0 && actor == "" {
		p.log().Debug("clickup assignment ignored: no agent member was assigned or removed", "task", pl.TaskID)
		return nil, nil
	}
	team := p.DefaultTeam
	if t, ok := p.TeamMap[strings.ToLower(actor)]; ok {
		team = t
	}

	// The webhook carries no List, so the Scope — the container the core
	// resolves a Work Target from — is unknown until FetchItem runs. Leaving
	// it empty is honest; inventing one from the task id would be the vendor
	// leak R7 forbids.
	item := &work.WorkItem{
		Provider:   p.Name(),
		ExternalID: pl.TaskID,
		Team:       team,
		Origin:     work.OriginAssignment,
		URL:        p.TaskURL(pl.TaskID),
	}

	switch pl.Event {
	case "taskAssigneeUpdated":
		// One ClickUp event covers both directions; the history item's field
		// is what separates them.
		kind := provider.TrackerAssigned
		if len(p.Agents) > 0 {
			if removed {
				kind = provider.TrackerUnassigned
			}
		} else {
			for _, h := range pl.HistoryItems {
				if h.Field == "assignee_rem" {
					kind = provider.TrackerUnassigned
				}
			}
		}
		changer, at := pl.changedBy()
		return []provider.TrackerEvent{{Kind: kind, ExternalID: pl.TaskID, Team: team, Item: item, Actor: changer, At: at}}, nil
	case "taskUpdated", "taskStatusUpdated", "taskPriorityUpdated":
		changer, at := pl.changedBy()
		kind := provider.TrackerUpdated
		if pl.Event == "taskStatusUpdated" && p.confirmedClosed(r.Context(), pl.TaskID) {
			kind = provider.TrackerClosed
		}
		return []provider.TrackerEvent{{Kind: kind, ExternalID: pl.TaskID, Team: team, Item: item, Actor: changer, At: at}}, nil
	default:
		// Unhandled events are dropped, not errors: providers subscribe wider
		// than the core consumes.
		return nil, nil
	}
}

func (p *Provider) isAgent(identities ...string) bool {
	for _, id := range identities {
		if id == "" {
			continue
		}
		for _, agent := range p.Agents {
			if strings.EqualFold(strings.TrimSpace(agent), id) {
				return true
			}
		}
	}
	return false
}

func (p *Provider) confirmedClosed(ctx context.Context, externalID string) bool {
	if !p.configured() {
		return false
	}
	t, err := p.readTask(ctx, externalID)
	if err != nil {
		p.log().Warn("clickup status change left as an update: the task status could not be read", "external_id", externalID, "err", err)
		return false
	}
	return t.closed()
}

const maxPayloadBytes = 1 << 20

// DeliveryID returns a synthetic id, because ClickUp puts no delivery id on
// its webhooks. It is built from the event name, the task, the ids of the
// history items the event carries and the latest of their dates. A
// redelivered or replayed body yields the same id; an event without history
// items yields none and is not deduplicated.
func (p *Provider) DeliveryID(r *http.Request) string {
	body, err := provider.ReadWebhookBody(r, maxPayloadBytes)
	if err != nil {
		return ""
	}
	var pl hookPayload
	if err := json.Unmarshal(body, &pl); err != nil || pl.TaskID == "" {
		return ""
	}
	ids := make([]string, 0, len(pl.HistoryItems))
	var latest string
	for _, h := range pl.HistoryItems {
		ids = append(ids, h.ID)
		if len(h.Date) > len(latest) || len(h.Date) == len(latest) && h.Date > latest {
			latest = h.Date
		}
	}
	object := "task " + pl.TaskID + " history " + strings.Join(ids, ",")
	return provider.SyntheticDeliveryID(p.Name(), pl.Event, object, latest)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func verify(secret string, body []byte, sigHex string) bool {
	sig, err := hex.DecodeString(strings.TrimSpace(sigHex))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(sig, mac.Sum(nil))
}

// configured reports whether API calls are possible at all.
func (p *Provider) configured() bool { return p.Token != "" }

func (p *Provider) baseURL() string {
	if p.BaseURL != "" {
		return strings.TrimRight(p.BaseURL, "/")
	}
	return DefaultBaseURL
}

func (p *Provider) client() *http.Client {
	if p.HC != nil {
		return p.HC
	}
	return apilimit.Client("clickup", 30*time.Second)
}

func (p *Provider) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}

// do issues one authenticated API call and decodes an optional result.
func (p *Provider) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL()+path, rdr)
	if err != nil {
		return err
	}
	// Raw token, no "Bearer " — ClickUp 401s the prefixed form.
	req.Header.Set("Authorization", p.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("clickup: %s %s: HTTP %d: %s", method, path, resp.StatusCode, bytes.TrimSpace(snippet))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// task is the subset of ClickUp's task representation Ploeg mirrors.
type task struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// URL is the page a person opens for the task, as ClickUp names it.
	URL string `json:"url"`
	// MarkdownDescription is returned only when the read asks for it with
	// include_markdown_description=true. Description and TextContent are
	// the same body with its formatting stripped.
	MarkdownDescription string          `json:"markdown_description"`
	Description         string          `json:"description"`
	TextContent         string          `json:"text_content"`
	DateUpdated         string          `json:"date_updated"`
	DateCreated         string          `json:"date_created"`
	TimeEstimate        json.RawMessage `json:"time_estimate"`
	Archived            bool            `json:"archived"`
	Status              struct {
		Status string `json:"status"`
		Type   string `json:"type"`
	} `json:"status"`
	// Priority is null on an unprioritised task, hence the pointer: a value
	// type would silently read as urgent.
	Priority *struct {
		ID       string `json:"id"`
		Priority string `json:"priority"`
	} `json:"priority"`
	List struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"list"`
	Tags []struct {
		Name string `json:"name"`
	} `json:"tags"`
}

func (t task) closed() bool {
	return t.Status.Type == "closed" || t.Status.Type == "done"
}

func (t task) tagNames() []string {
	names := make([]string, 0, len(t.Tags))
	for _, tag := range t.Tags {
		names = append(names, tag.Name)
	}
	return names
}

func (p *Provider) readTask(ctx context.Context, externalID string) (task, error) {
	var t task
	if err := p.do(ctx, http.MethodGet, "/task/"+url.PathEscape(externalID)+"?include_markdown_description=true", nil, &t); err != nil {
		return task{}, err
	}
	if t.ID == "" {
		return task{}, fmt.Errorf("clickup: task %s not found", externalID)
	}
	return t, nil
}

// FetchItem reads authoritative task state — the thin-payload rule's "truth"
// half (backlog #7), and the only place the List (the Scope) becomes known,
// since ClickUp's webhook does not carry it.
//
// Unconfigured, it errors so the caller falls back to the webhook snapshot.
func (p *Provider) FetchItem(ctx context.Context, externalID string) (work.WorkItem, error) {
	result, err := p.FetchExecutionItem(ctx, externalID)
	return result.Item, err
}

func (p *Provider) TrackerAPIBaseURL() string { return p.baseURL() }

// AppURL is the root of ClickUp's web app, where a task's page lives.
const AppURL = "https://app.clickup.com"

// TaskURL is the page a person opens for a task: AppURL/t/<id>, which
// ClickUp redirects to the task wherever it sits. It is empty for an empty
// id. A read prefers the url ClickUp returns, which also covers a Workspace
// that shows custom task ids.
func (p *Provider) TaskURL(externalID string) string {
	if strings.TrimSpace(externalID) == "" {
		return ""
	}
	return AppURL + "/t/" + url.PathEscape(externalID)
}

func (p *Provider) FetchExecutionItem(ctx context.Context, externalID string) (provider.ExecutionItem, error) {
	if !p.configured() {
		return provider.ExecutionItem{}, errors.New("clickup: no API credentials configured (falling back to the webhook snapshot)")
	}
	t, err := p.readTask(ctx, externalID)
	if err != nil {
		return provider.ExecutionItem{}, err
	}
	// Only markdown_description keeps headings, lists, code and links; the
	// other two are plain text and may each be empty where another is not.
	desc := firstNonEmpty(t.MarkdownDescription, t.Description, t.TextContent)
	link := t.URL
	if link == "" {
		link = p.TaskURL(t.ID)
	}
	return provider.ExecutionItem{Open: !t.Archived && (t.Status.Type == "open" || t.Status.Type == "custom"), Item: work.WorkItem{
		Provider:    p.Name(),
		ExternalID:  t.ID,
		Revision:    t.DateUpdated,
		Origin:      work.OriginAssignment,
		Priority:    priority(t),
		Title:       t.Name,
		Description: desc,
		URL:         link,
		// The List is ClickUp's own container for the task — the scope the
		// core resolves a Work Target from. Team is the caller's routing
		// decision, not the tracker's view; httpapi.mirror overwrites it.
		ExternalScope:    t.List.ID,
		Labels:           t.tagNames(),
		TrackerCreatedAt: unixMillis(t.DateCreated),
		EstimateSeconds:  estimateSeconds(t.TimeEstimate),
		Closed:           t.closed(),
	}}, nil
}

func unixMillis(raw string) time.Time {
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func estimateSeconds(raw json.RawMessage) *int64 {
	text := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if text == "" || text == "null" {
		return nil
	}
	ms, err := strconv.ParseInt(text, 10, 64)
	if err != nil || ms < 0 {
		return nil
	}
	seconds := ms / 1000
	return &seconds
}

// priority flips ClickUp's scale. ClickUp: 1 urgent, 2 high, 3 normal, 4 low,
// absent = unset. Ploeg: higher is more urgent, 0 unset.
func priority(t task) int {
	if t.Priority == nil {
		return 0
	}
	switch t.Priority.ID {
	case "1":
		return 4
	case "2":
		return 3
	case "3":
		return 2
	case "4":
		return 1
	}
	// Fall back to the label when the id is missing or unrecognised — the
	// field is user-facing and has been renumbered before.
	switch strings.ToLower(t.Priority.Priority) {
	case "urgent":
		return 4
	case "high":
		return 3
	case "normal":
		return 2
	case "low":
		return 1
	}
	return 0
}

// Scope reports the List a task belongs to, for callers that need the Work
// Target without a full mirror. Empty Scope when unconfigured or unknown.
func (p *Provider) Scope(ctx context.Context, externalID string) (provider.Scope, error) {
	if !p.configured() {
		return provider.Scope{}, errors.New("clickup: no API credentials configured")
	}
	var t task
	if err := p.do(ctx, http.MethodGet, "/task/"+externalID, nil, &t); err != nil {
		return provider.Scope{}, err
	}
	if t.List.ID == "" {
		return provider.Scope{}, nil
	}
	return provider.Scope{Kind: "list", ID: t.List.ID, Name: t.List.Name}, nil
}

// Comment writes back to the task's conversation — how a finished Shift
// reaches a person with the pull request link.
//
// The body is the Markdown every tracker is given. ClickUp shows
// comment_text verbatim, so the comment goes as rich-text blocks (bold,
// code, links, lists, small print as italic) with comment_text carrying the
// same words without markup for any reader of the plain field.
func (p *Provider) Comment(ctx context.Context, externalID, body string) error {
	if !p.configured() {
		p.log().Info("clickup write-back skipped (no API credentials)", "action", "comment", "external_id", externalID)
		return nil
	}
	plain, blocks := richComment(body)
	// notify_all false: a bot narrating its own runs should not mail everyone
	// watching the task on every round.
	return p.do(ctx, http.MethodPost, "/task/"+url.PathEscape(externalID)+"/comment",
		map[string]any{"comment_text": plain, "comment": blocks, "notify_all": false}, nil)
}

// SetStatus applies Ploeg's state to the tracker as the List status the
// operator named for it (ADR-0183).
//
// Which status name means done, in review or blocked is a per-List decision
// in ClickUp, so it is configuration rather than a guess here (R7). A state
// nobody named leaves the board untouched: needs_human and stale are NOT
// done, and without a mapping the only safe move is none.
func (p *Provider) SetStatus(ctx context.Context, externalID string, state work.State) error {
	if !p.configured() {
		p.log().Info("clickup write-back skipped (no API credentials)", "action", "set_status", "external_id", externalID, "state", state)
		return nil
	}
	status := p.statusFor(state)
	if status == "" {
		p.log().Debug("clickup status write-back skipped (no List status configured for this state)", "external_id", externalID, "state", state)
		return nil
	}
	return p.do(ctx, http.MethodPut, "/task/"+url.PathEscape(externalID),
		map[string]any{"status": status}, nil)
}

func (p *Provider) statusFor(state work.State) string {
	if name := strings.TrimSpace(p.Statuses[state]); name != "" {
		return name
	}
	if state == work.StateDone {
		return strings.TrimSpace(p.DoneStatus)
	}
	return ""
}

// ListsByName lists a Space's lists as name → id, so configuration can name a
// board the way a human does and ploegd resolves the id — the ClickUp analogue
// of the Vikunja provider's ProjectsByName, and for the same reason: a bare id
// in a values file tells a reader nothing and cannot be reviewed.
func (p *Provider) ListsByName(ctx context.Context, spaceID string) (map[string]string, error) {
	if !p.configured() {
		return nil, errors.New("clickup: no API credentials configured; cannot resolve list names")
	}
	if spaceID == "" {
		return nil, errors.New("clickup: space id is required to list lists")
	}
	var folderless struct {
		Lists []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"lists"`
	}
	// Folderless lists only. Lists inside a Folder are a second call the
	// caller can make per folder; conflating them here would hide which
	// container a name came from when two folders reuse a list name.
	if err := p.do(ctx, http.MethodGet, "/space/"+spaceID+"/list?archived=false", nil, &folderless); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(folderless.Lists))
	for _, l := range folderless.Lists {
		out[l.Name] = l.ID
	}
	return out, nil
}

// BoardStatus reads the task's List, its status and its tags through
// GET /task/{id} (ADR-0051).
func (p *Provider) BoardStatus(ctx context.Context, externalID string) (provider.BoardStatus, error) {
	if !p.configured() {
		return provider.BoardStatus{}, errors.New("clickup: no API credentials configured; cannot read the board")
	}
	t, err := p.readTask(ctx, externalID)
	if err != nil {
		return provider.BoardStatus{}, err
	}
	out := provider.BoardStatus{Scope: t.List.ID, Statuses: []string{}, Labels: t.tagNames(), Created: unixMillis(t.DateCreated),
		Estimates: true, EstimateSeconds: estimateSeconds(t.TimeEstimate)}
	if t.Status.Status != "" {
		out.Statuses = append(out.Statuses, t.Status.Status)
	}
	return out, nil
}

// BoardComments reads the task's most recent comments through
// GET /task/{id}/comment, which returns at most 25, newest first.
func (p *Provider) BoardComments(ctx context.Context, externalID string) ([]provider.BoardComment, error) {
	if !p.configured() {
		return nil, errors.New("clickup: no API credentials configured; cannot read comments")
	}
	var body struct {
		Comments []struct {
			CommentText string `json:"comment_text"`
			Date        string `json:"date"`
			User        struct {
				Username string `json:"username"`
				Email    string `json:"email"`
			} `json:"user"`
		} `json:"comments"`
	}
	if err := p.do(ctx, http.MethodGet, "/task/"+url.PathEscape(externalID)+"/comment", nil, &body); err != nil {
		return nil, err
	}
	out := make([]provider.BoardComment, 0, len(body.Comments))
	for _, c := range body.Comments {
		comment := provider.BoardComment{Text: c.CommentText, Author: firstNonEmpty(c.User.Username, c.User.Email)}
		if ms, err := strconv.ParseInt(c.Date, 10, 64); err == nil && ms > 0 {
			comment.At = time.UnixMilli(ms).UTC()
		}
		out = append(out, comment)
	}
	return out, nil
}
