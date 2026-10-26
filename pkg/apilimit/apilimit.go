// Package apilimit keeps Ploeg's tracker and forge API calls inside each
// upstream's rate limit: a token bucket per provider, host and credential,
// and a bounded wait-and-retry when the upstream answers 429, or 503 with
// Retry-After. It never waits longer than MaxWait or than the request's
// context allows; past that the call returns at once and the caller's next
// tick retries.
package apilimit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/ploeg-hq/ploeg/pkg/httpclient"
)

// MaxWait is the longest a single call waits for its bucket or for one
// upstream-requested pause before it gives up.
const MaxWait = 5 * time.Second

const (
	maxRetries      = 2
	fallbackPause   = time.Second
	bucketIdleTTL   = time.Hour
	bucketPruneSize = 1024
)

// Policy is one provider's limit. PerMinute is the most calls the bucket
// lets through in any one-minute window, Burst of them at once. Retry turns
// the wait-and-retry on; a provider that retries on its own leaves it off.
type Policy struct {
	PerMinute int
	Burst     int
	Retry     bool
}

func (p Policy) limiter() *rate.Limiter {
	refill := float64(p.PerMinute-p.Burst) / 60
	if refill <= 0 {
		refill = float64(p.PerMinute) / 60
	}
	return rate.NewLimiter(rate.Limit(refill), p.Burst)
}

// Policies are the limits per provider name. ClickUp allows 100 requests per
// minute per token on its Free, Unlimited and Business plans. Vikunja's
// limiter is off by default and allows 100 per user per minute when an
// operator turns it on; Ploeg leaves headroom above that so a default Vikunja
// is not slowed. GitHub allows 5,000 requests per hour per token. GitLab.com
// allows 2,000 per minute per user and Forgejo has no API limit by default;
// both get a ceiling that only a runaway loop reaches.
var Policies = map[string]Policy{
	"clickup": {PerMinute: 100, Burst: 20, Retry: true},
	"vikunja": {PerMinute: 300, Burst: 30, Retry: true},
	"github":  {PerMinute: 80, Burst: 20, Retry: false},
	"gitlab":  {PerMinute: 600, Burst: 30, Retry: true},
	"forgejo": {PerMinute: 600, Burst: 30, Retry: true},
}

// ThrottledError is returned when a call would have to wait for its bucket
// longer than MaxWait or than its context allows.
type ThrottledError struct {
	Provider string
	Wait     time.Duration
}

func (e *ThrottledError) Error() string {
	return fmt.Sprintf("%s: client-side rate limit, next slot in %s", e.Provider, e.Wait.Round(time.Millisecond))
}

// Client returns an HTTP client for provider on the shared transport, with
// the provider's Policy and the given overall timeout.
func Client(provider string, timeout time.Duration) *http.Client {
	return &http.Client{Transport: New(provider, Policies[provider], httpclient.Transport()), Timeout: timeout}
}

// New wraps base with policy's bucket and retry for provider.
func New(provider string, policy Policy, base http.RoundTripper) http.RoundTripper {
	return &transport{provider: provider, policy: policy, base: base, buckets: registry}
}

type transport struct {
	provider string
	policy   Policy
	base     http.RoundTripper
	buckets  *buckets
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	c := counters(t.provider)
	bucket := t.buckets.get(bucketKey(t.provider, req), t.policy)
	for attempt := 0; ; attempt++ {
		if err := t.take(req.Context(), bucket, c); err != nil {
			return nil, err
		}
		c.requests.Add(1)
		resp, err := t.base.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			c.rateLimited.Add(1)
		}
		if !t.policy.Retry || attempt >= maxRetries {
			return resp, nil
		}
		pause, ok := retryPause(req, resp, attempt, time.Now())
		if !ok || !fits(req.Context(), pause) {
			return resp, nil
		}
		next, ok := replay(req)
		if !ok {
			return resp, nil
		}
		drain(resp)
		if !sleep(req.Context(), pause) {
			return nil, req.Context().Err()
		}
		c.retries.Add(1)
		req = next
	}
}

func (t *transport) take(ctx context.Context, bucket *rate.Limiter, c *providerCounters) error {
	r := bucket.Reserve()
	wait := r.Delay()
	if wait == 0 {
		return nil
	}
	if !r.OK() || !fits(ctx, wait) {
		r.Cancel()
		c.throttled.Add(1)
		return &ThrottledError{Provider: t.provider, Wait: wait}
	}
	c.delayed.Add(1)
	if !sleep(ctx, wait) {
		r.Cancel()
		return ctx.Err()
	}
	return nil
}

func fits(ctx context.Context, wait time.Duration) bool {
	if wait > MaxWait {
		return false
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= wait {
		return false
	}
	return true
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func retryPause(req *http.Request, resp *http.Response, attempt int, now time.Time) (time.Duration, bool) {
	var pause time.Duration
	asked, hasAsk := askedPause(resp, now)
	switch {
	case resp.StatusCode == http.StatusTooManyRequests && hasAsk:
		pause = asked
	case resp.StatusCode == http.StatusTooManyRequests:
		pause = fallbackPause << attempt
	case resp.StatusCode == http.StatusServiceUnavailable && hasAsk && idempotent(req.Method):
		pause = asked
	default:
		return 0, false
	}
	return pause + jitter(pause), true
}

func jitter(pause time.Duration) time.Duration {
	spread := pause/5 + 50*time.Millisecond
	return rand.N(spread)
}

func askedPause(resp *http.Response, now time.Time) (time.Duration, bool) {
	if s := strings.TrimSpace(resp.Header.Get("Retry-After")); s != "" {
		if secs, err := strconv.Atoi(s); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second, true
		}
		if at, err := http.ParseTime(s); err == nil {
			return max(at.Sub(now), 0), true
		}
	}
	if s := strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset")); s != "" {
		if epoch, err := strconv.ParseInt(s, 10, 64); err == nil {
			return max(time.Unix(epoch, 0).Sub(now), 0), true
		}
	}
	if s := strings.TrimSpace(resp.Header.Get("RateLimit-Reset")); s != "" {
		if epoch, err := strconv.ParseInt(s, 10, 64); err == nil && epoch > now.Unix() {
			return time.Unix(epoch, 0).Sub(now), true
		}
	}
	return 0, false
}

func idempotent(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete:
		return true
	}
	return false
}

func replay(req *http.Request) (*http.Request, bool) {
	next := req.Clone(req.Context())
	if req.Body == nil || req.Body == http.NoBody {
		return next, true
	}
	if req.GetBody == nil {
		return nil, false
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, false
	}
	next.Body = body
	return next, true
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
}

func bucketKey(provider string, req *http.Request) string {
	credential := req.Header.Get("Authorization") + "\x00" + req.Header.Get("PRIVATE-TOKEN")
	sum := sha256.Sum256([]byte(credential))
	return provider + "\x00" + req.URL.Host + "\x00" + hex.EncodeToString(sum[:8])
}

type bucketEntry struct {
	limiter  *rate.Limiter
	lastUsed time.Time
}

type buckets struct {
	mu      sync.Mutex
	entries map[string]*bucketEntry
}

var registry = &buckets{entries: map[string]*bucketEntry{}}

func (b *buckets) get(key string, policy Policy) *rate.Limiter {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	e, ok := b.entries[key]
	if !ok {
		if len(b.entries) >= bucketPruneSize {
			b.prune(now)
		}
		e = &bucketEntry{limiter: policy.limiter()}
		b.entries[key] = e
	}
	e.lastUsed = now
	return e.limiter
}

func (b *buckets) prune(now time.Time) {
	for k, e := range b.entries {
		if now.Sub(e.lastUsed) > bucketIdleTTL {
			delete(b.entries, k)
		}
	}
}
