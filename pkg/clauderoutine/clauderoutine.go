// Package clauderoutine fires a person's own Claude Code routine through its
// API trigger (ADR-0197 section 7). The routine belongs to the person's
// claude.ai account and runs in the installation's self-hosted environment;
// Ploeg holds only the routine's trigger token, never a claude.ai login.
package clauderoutine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Beta is the dated beta header the /fire endpoint ships under. Anthropic
// keeps the two previous header versions working after a breaking change.
const Beta = "experimental-cc-routine-2026-04-01"

// APIVersion is the anthropic-version header the /fire endpoint takes.
const APIVersion = "2023-06-01"

// DefaultBaseURL is where routines are fired.
const DefaultBaseURL = "https://api.anthropic.com"

// MaxTextBytes bounds the fire text Ploeg sends. Anthropic documents no
// limit; this keeps one Run's prompt from growing without bound.
const MaxTextBytes = 64 << 10

// Why a fire was refused. Each maps to the named reason ADR-0197 section 7
// gives it.
var (
	// ErrTokenRefused is a 401 or 403: the token was deleted, rotated or
	// never belonged to this routine.
	ErrTokenRefused = errors.New("routine_token_refused")
	// ErrNotFound is a 404: the routine or its API trigger is gone.
	ErrNotFound = errors.New("routine_not_found")
	// ErrRateLimited is a 429 from Anthropic, or Ploeg's own count.
	ErrRateLimited = errors.New("routine_rate_limited")
	// ErrUnavailable is any other failure: a 5xx, a network error, or a
	// response Ploeg cannot read under the beta header it sends.
	ErrUnavailable = errors.New("routines_unavailable")
)

var (
	triggerPath = regexp.MustCompile(`^/v1/claude_code/routines/trig_[A-Za-z0-9]{8,64}/fire$`)
	sessionID   = regexp.MustCompile(`^session_[A-Za-z0-9]{8,64}$`)
)

// CheckFireURL accepts a routine's fire URL only under base, over HTTPS (or
// plain HTTP to a loopback test server), at the documented path, with no
// query, fragment or user info. A URL a person pastes is never followed to
// another host.
func CheckFireURL(base, raw string) error {
	if base == "" {
		base = DefaultBaseURL
	}
	b, err := url.Parse(base)
	if err != nil {
		return fmt.Errorf("routine base URL: %w", err)
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return errors.New("the fire URL is not a URL")
	case u.Scheme != b.Scheme || u.Host != b.Host:
		return fmt.Errorf("the fire URL must be on %s", b.Host)
	case u.User != nil || u.RawQuery != "" || u.Fragment != "":
		return errors.New("the fire URL carries no user info, query or fragment")
	case !triggerPath.MatchString(u.EscapedPath()):
		return errors.New("the fire URL must be /v1/claude_code/routines/trig_…/fire")
	}
	return nil
}

// CheckToken accepts a trigger token that is plausibly one: a single
// printable line of reasonable length. Its value is never logged.
func CheckToken(token string) error {
	if len(token) < 16 || len(token) > 512 {
		return errors.New("the trigger token must be 16 to 512 characters")
	}
	for _, r := range token {
		if r <= ' ' || r > '~' {
			return errors.New("the trigger token must be one line of printable ASCII")
		}
	}
	return nil
}

// Session is what a successful fire returns.
type Session struct {
	// ID is the claude_code_session_id, session_….
	ID string
	// URL is the claude_code_session_url a person opens to follow it.
	URL string
}

// Client fires routines.
type Client struct {
	// BaseURL defaults to DefaultBaseURL; a fire URL must be under it.
	BaseURL string
	// HTTP defaults to a client with a 30 second timeout.
	HTTP *http.Client
}

func (c *Client) base() string {
	if c.BaseURL == "" {
		return DefaultBaseURL
	}
	return c.BaseURL
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

type fireRequest struct {
	Text string `json:"text"`
}

type fireResponse struct {
	Type       string `json:"type"`
	SessionID  string `json:"claude_code_session_id"`
	SessionURL string `json:"claude_code_session_url"`
}

// Fire POSTs text to the routine's fire URL with its trigger token. Every
// failure is one of the package's errors, wrapped with the status; no error
// carries the token.
func (c *Client) Fire(ctx context.Context, fireURL, token, text string) (Session, error) {
	if err := CheckFireURL(c.base(), fireURL); err != nil {
		return Session{}, fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	if len(text) > MaxTextBytes {
		return Session{}, fmt.Errorf("fire text is %d bytes, over %d", len(text), MaxTextBytes)
	}
	body, err := json.Marshal(fireRequest{Text: text})
	if err != nil {
		return Session{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fireURL, bytes.NewReader(body))
	if err != nil {
		return Session{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", Beta)
	req.Header.Set("anthropic-version", APIVersion)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http().Do(req)
	if err != nil {
		return Session{}, fmt.Errorf("%w: %v", ErrUnavailable, scrub(err, token))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return Session{}, fmt.Errorf("%w: status %d", ErrTokenRefused, resp.StatusCode)
	case resp.StatusCode == http.StatusNotFound:
		return Session{}, fmt.Errorf("%w: status %d", ErrNotFound, resp.StatusCode)
	case resp.StatusCode == http.StatusTooManyRequests:
		return Session{}, fmt.Errorf("%w: status %d", ErrRateLimited, resp.StatusCode)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return Session{}, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
	var out fireResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return Session{}, fmt.Errorf("%w: unreadable response: %v", ErrUnavailable, err)
	}
	if out.Type != "routine_fire" || !sessionID.MatchString(out.SessionID) {
		return Session{}, fmt.Errorf("%w: response type %q without a session id", ErrUnavailable, out.Type)
	}
	return Session{ID: out.SessionID, URL: out.SessionURL}, nil
}

func scrub(err error, token string) string {
	return strings.ReplaceAll(err.Error(), token, "[trigger token]")
}

// Reason is the named reason of a fire error, ErrUnavailable's for any error
// that is not one of the package's.
func Reason(err error) string {
	for _, known := range []error{ErrTokenRefused, ErrNotFound, ErrRateLimited, ErrUnavailable} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return ErrUnavailable.Error()
}

// Text is the fire text of one Run. Anthropic wraps it in a
// <routine-fire-payload> block that labels it untrusted, so the routine's
// own saved prompt must tell Claude to carry it out. reference names the
// Work Item, run the Ploeg Run, and prompt is the Run's task prompt.
func Text(reference, run, branch, baseBranch, prompt string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Ploeg Run %s for Work Item %s.\n", run, reference)
	fmt.Fprintf(&b, "Work on branch %s, based on %s. Push only that branch.\n\n", branch, baseBranch)
	b.WriteString(prompt)
	return b.String()
}
