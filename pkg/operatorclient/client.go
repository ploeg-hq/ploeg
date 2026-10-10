// Package operatorclient is a read-only Go client for Ploeg's operator API
// (docs/contracts/operator-api.v1.schema.json). It sends one consumer's bearer
// credential, refuses redirects, bounds every call by time and size, and never
// puts the credential into an error.
package operatorclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

// MaxResponseBytes is the largest operator response the client reads, the
// same bound ploegd puts on what it sends.
const MaxResponseBytes = 16 << 20

// DefaultTimeout bounds one call, matching the operator API's own deadline.
const DefaultTimeout = 5 * time.Second

// ErrUnauthorized, ErrForbidden, ErrNotFound and ErrConflict let a caller
// branch on the operator API's answer with errors.Is.
var (
	ErrUnauthorized = errors.New("operator credential refused")
	ErrForbidden    = errors.New("operator consumer cannot read this")
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrTimeout      = errors.New("operator API did not answer in time")
)

// Error is a non-2xx operator answer. Code and Message come from the
// response's error object when it has one.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("operator API %d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("operator API answered %d", e.Status)
}

// Is maps a status to the sentinel errors above.
func (e *Error) Is(target error) bool {
	switch target {
	case ErrUnauthorized:
		return e.Status == http.StatusUnauthorized
	case ErrForbidden:
		return e.Status == http.StatusForbidden
	case ErrNotFound:
		return e.Status == http.StatusNotFound
	case ErrConflict:
		return e.Status == http.StatusConflict
	}
	return false
}

// Client calls one ploegd's operator API as one Operator Consumer.
type Client struct {
	base  *url.URL
	token string
	http  *http.Client
}

// New returns a Client for baseURL (scheme and host, optionally a path
// prefix) authenticating with token.
func New(baseURL, token string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("operator URL must be an http(s) URL without credentials, query or fragment")
	}
	if len(token) < 32 || len(token) > 4096 || strings.ContainsAny(token, " \t\r\n,") {
		return nil, errors.New("operator token must be 32 to 4096 bytes without whitespace or commas")
	}
	return &Client{base: u, token: token, http: &http.Client{
		Timeout:       DefaultTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

// Teams answers GET /teams.
func (c *Client) Teams(ctx context.Context) ([]store.OperatorTeam, error) {
	var out struct {
		Teams []store.OperatorTeam `json:"teams"`
	}
	return out.Teams, c.get(ctx, "teams", nil, &out)
}

// Summary is GET /summary for one window.
type Summary struct {
	GeneratedAt time.Time                   `json:"generatedAt"`
	Window      string                      `json:"window"`
	Teams       []store.OperatorTeamSummary `json:"teams"`
	Totals      store.OperatorSummaryTotals `json:"totals"`
}

// Summary answers GET /summary; window is 24h, 7d or 30d, empty meaning 7d.
func (c *Client) Summary(ctx context.Context, window string) (Summary, error) {
	q := url.Values{}
	if window != "" {
		q.Set("window", window)
	}
	var out Summary
	return out, c.get(ctx, "summary", q, &out)
}

// ItemFilter selects Work Items. After is the previous page's NextCursor.
type ItemFilter struct {
	Team       string
	State      string
	NeedsHuman bool
	Provider   string
	ExternalID string
	After      string
	Limit      int
}

// ItemPage is one page of Work Items; NextCursor is empty on the last page.
type ItemPage struct {
	Items      []store.OperatorItem `json:"items"`
	NextCursor *string              `json:"nextCursor"`
}

// WorkItems answers GET /work-items.
func (c *Client) WorkItems(ctx context.Context, f ItemFilter) (ItemPage, error) {
	q := url.Values{}
	set(q, "team", f.Team)
	set(q, "state", f.State)
	set(q, "provider", f.Provider)
	set(q, "externalId", f.ExternalID)
	set(q, "after", f.After)
	if f.NeedsHuman {
		q.Set("needsHuman", "true")
	}
	if f.Limit > 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	var out ItemPage
	return out, c.get(ctx, "work-items", q, &out)
}

// WorkItem answers GET /work-items/{id}: the item with its Shifts, Runs,
// Checkpoints and Events.
func (c *Client) WorkItem(ctx context.Context, id string) (store.OperatorDetail, error) {
	if !numericID(id) {
		return store.OperatorDetail{}, &Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: "Work Item id must be a positive integer."}
	}
	var out store.OperatorDetail
	return out, c.get(ctx, "work-items/"+id, nil, &out)
}

// RunFilter selects Runs newest first. Before is the previous page's NextCursor.
type RunFilter struct {
	Team    string
	State   string
	Outcome string
	Before  string
	Limit   int
}

// RunPage is one page of Runs.
type RunPage struct {
	Runs       []store.OperatorRunListItem `json:"runs"`
	NextCursor *string                     `json:"nextCursor"`
}

// Runs answers GET /runs.
func (c *Client) Runs(ctx context.Context, f RunFilter) (RunPage, error) {
	q := url.Values{}
	set(q, "team", f.Team)
	set(q, "state", f.State)
	set(q, "outcome", f.Outcome)
	set(q, "before", f.Before)
	if f.Limit > 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	var out RunPage
	return out, c.get(ctx, "runs", q, &out)
}

// Run answers GET /runs/{id}.
func (c *Client) Run(ctx context.Context, id string) (store.OperatorRun, error) {
	if !numericID(id) {
		return store.OperatorRun{}, &Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: "Run id must be a positive integer."}
	}
	var out struct {
		Run store.OperatorRun `json:"run"`
	}
	return out.Run, c.get(ctx, "runs/"+id, nil, &out)
}

// EventFilter selects audit Events. After pages ascending; Before with
// Desc pages newest first.
type EventFilter struct {
	Team       string
	WorkItemID string
	After      string
	Before     string
	Desc       bool
	Limit      int
}

// EventPage is one page of Events. LastCursor is what to pass as After to
// read only what happened since.
type EventPage struct {
	Events     []store.OperatorEvent `json:"events"`
	NextCursor *string               `json:"nextCursor"`
	LastCursor string                `json:"lastCursor"`
	HasMore    bool                  `json:"hasMore"`
}

// Events answers GET /events.
func (c *Client) Events(ctx context.Context, f EventFilter) (EventPage, error) {
	q := url.Values{}
	set(q, "team", f.Team)
	set(q, "workItemId", f.WorkItemID)
	set(q, "after", f.After)
	set(q, "before", f.Before)
	if f.Desc {
		q.Set("order", "desc")
	}
	if f.Limit > 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	var out EventPage
	return out, c.get(ctx, "events", q, &out)
}

func (c *Client) get(ctx context.Context, path string, q url.Values, into any) error {
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + "/api/v1/operator/" + path
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return errors.New("could not build the operator request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return c.transportError(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return c.transportError(err)
	}
	if len(body) > MaxResponseBytes {
		return &Error{Status: resp.StatusCode, Code: "too_large", Message: "Operator response exceeded 16 MiB."}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &envelope)
		return &Error{Status: resp.StatusCode, Code: c.redact(envelope.Error.Code), Message: c.redact(envelope.Error.Message)}
	}
	if err := json.Unmarshal(body, into); err != nil {
		return &Error{Status: resp.StatusCode, Code: "invalid_response", Message: "Operator response did not match the contract."}
	}
	return nil
}

func (c *Client) transportError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return ErrTimeout
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	return fmt.Errorf("operator API unreachable: %s", c.redact(err.Error()))
}

func (c *Client) redact(s string) string {
	return strings.ReplaceAll(s, c.token, "[redacted]")
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

func set(q url.Values, key, value string) {
	if value != "" {
		q.Set(key, value)
	}
}

func numericID(id string) bool {
	n, err := strconv.ParseInt(id, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == id
}
