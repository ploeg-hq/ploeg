// Package githubapi holds what every GitHub caller in Ploeg shares: where the
// REST API lives for a GitHub web root, a client that waits out GitHub's
// primary and secondary rate limits, and Link-header pagination (ADR-0086).
// GitHub App tokens are minted by pkg/githubapp.
package githubapi

import (
	"bytes"
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
)

// PublicWebURL is github.com's web root.
const PublicWebURL = "https://github.com"

const publicAPIURL = "https://api.github.com"

// APIURL returns the REST API root for a GitHub web root: api.github.com for
// github.com, and <root>/api/v3 for GitHub Enterprise Server and anything
// else, including a worker's loopback forge proxy.
func APIURL(webURL string) string {
	root := strings.TrimRight(webURL, "/")
	u, err := url.Parse(root)
	if err == nil && strings.EqualFold(u.Host, "github.com") {
		return publicAPIURL
	}
	return root + "/api/v3"
}

// APIVersion is the REST API version every request names.
const APIVersion = "2022-11-28"

// Defaults for Client.
const (
	DefaultMaxWait = 60 * time.Second
	DefaultRetries = 3
)

const secondaryLimitWait = 60 * time.Second

// Client sends requests to GitHub's REST API and retries a request GitHub
// refused for a rate limit, after waiting as long as GitHub asked. A wait
// longer than MaxWait is not attempted: the refusal is returned to the caller.
type Client struct {
	HC      *http.Client
	MaxWait time.Duration
	Retries int
	Sleep   func(context.Context, time.Duration) error
	Now     func() time.Time
}

func (c *Client) hc() *http.Client {
	if c != nil && c.HC != nil {
		return c.HC
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c *Client) maxWait() time.Duration {
	if c != nil && c.MaxWait > 0 {
		return c.MaxWait
	}
	return DefaultMaxWait
}

func (c *Client) retries() int {
	if c != nil && c.Retries > 0 {
		return c.Retries
	}
	return DefaultRetries
}

func (c *Client) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c != nil && c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Do sends req with GitHub's API headers and waits out rate limits. A request
// with a body is retried only when the body can be read again (GetBody).
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/vnd.github+json")
	}
	req.Header.Set("X-GitHub-Api-Version", APIVersion)
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "ploeg")
	}
	for attempt := 0; ; attempt++ {
		resp, err := c.hc().Do(req)
		if err != nil {
			return nil, err
		}
		wait, limited := c.RateLimitWait(resp)
		if !limited || attempt >= c.retries() || wait > c.maxWait() || (req.Body != nil && req.GetBody == nil) {
			return resp, nil
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		if err := c.sleep(req.Context(), wait); err != nil {
			return nil, err
		}
		if req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			req.Body = body
		}
	}
}

// RateLimitWait reports whether resp is a rate-limit refusal and how long
// GitHub asked the client to wait: Retry-After when present, the primary
// limit's reset time when the remaining quota is zero, and one minute for a
// secondary limit that names neither.
func (c *Client) RateLimitWait(resp *http.Response) (time.Duration, bool) {
	if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusForbidden {
		return 0, false
	}
	if s := strings.TrimSpace(resp.Header.Get("Retry-After")); s != "" {
		if secs, err := strconv.Atoi(s); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second, true
		}
		if at, err := http.ParseTime(s); err == nil {
			return max(at.Sub(c.now()), 0), true
		}
	}
	if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			return max(time.Unix(reset, 0).Sub(c.now())+time.Second, 0), true
		}
		return secondaryLimitWait, true
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return secondaryLimitWait, true
	}
	return 0, false
}

// NextLink returns the rel="next" URL of a Link header, or "" on the last
// page.
func NextLink(h http.Header) string {
	for _, header := range h.Values("Link") {
		for _, part := range strings.Split(header, ",") {
			target, params, ok := strings.Cut(strings.TrimSpace(part), ";")
			if !ok {
				continue
			}
			for _, param := range strings.Split(params, ";") {
				if strings.ReplaceAll(strings.TrimSpace(param), " ", "") == `rel="next"` {
					return strings.Trim(strings.TrimSpace(target), "<>")
				}
			}
		}
	}
	return ""
}

// ErrStatus is returned for a response outside 2xx. Body is at most 512
// bytes of what GitHub said, which never contains the caller's token.
type ErrStatus struct {
	Method, Path string
	Status       int
	Body         string
}

func (e *ErrStatus) Error() string {
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Body)
}

// StatusOf returns the HTTP status of an ErrStatus in err's chain, or 0.
func StatusOf(err error) int {
	var s *ErrStatus
	if errors.As(err, &s) {
		return s.Status
	}
	return 0
}

// CheckStatus turns a response outside 2xx into an ErrStatus and closes its
// body. A 2xx response is returned untouched.
func CheckStatus(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return &ErrStatus{Method: resp.Request.Method, Path: resp.Request.URL.Path, Status: resp.StatusCode,
		Body: strings.TrimSpace(string(snippet))}
}

// JSON sends one request with authorization and an optional JSON body, and
// decodes a JSON response into out when out is not nil.
func (c *Client) JSON(ctx context.Context, method, target, authorization string, body, out any) error {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = b
	}
	var rdr io.Reader
	if payload != nil {
		rdr = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rdr)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	if err := CheckStatus(resp); err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); err != nil {
		return fmt.Errorf("%s %s: %w", method, req.URL.Path, err)
	}
	return nil
}

// TokenSource supplies the credential ploegd sends to GitHub for one
// repository.
type TokenSource interface {
	Token(ctx context.Context, owner, repo string) (string, error)
}

// StaticToken is a fine-grained personal access token used for every
// repository. It does not expire with a Run and cannot be revoked by Ploeg,
// which makes it the weaker fallback to a GitHub App (ADR-0086).
type StaticToken string

// Token returns the static token.
func (s StaticToken) Token(context.Context, string, string) (string, error) { return string(s), nil }
