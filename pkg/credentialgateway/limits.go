package credentialgateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// FamilyLimit is a Run's token buckets for one route family.
type FamilyLimit struct {
	RequestsPerSecond float64 `json:"requestsPerSecond"`
	Burst             int     `json:"burst"`
	BytesPerSecond    float64 `json:"bytesPerSecond"`
}

// Limits are the per-Run and per-namespace limits of ADR-0197 section 5.
type Limits struct {
	Forge                  FamilyLimit `json:"forge"`
	Model                  FamilyLimit `json:"model"`
	Ploegd                 FamilyLimit `json:"ploegd"`
	MCP                    FamilyLimit `json:"mcp"`
	MaxUpstreamConnections int         `json:"maxUpstreamConnections"`
}

// ParseLimits reads PLOEG_GATEWAY_LIMITS; empty is the defaults.
func ParseLimits(raw string) (Limits, error) {
	var l Limits
	if raw == "" {
		return l.withDefaults(), nil
	}
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		return l, fmt.Errorf("PLOEG_GATEWAY_LIMITS: %w", err)
	}
	return l.withDefaults(), nil
}

func (f FamilyLimit) or(def FamilyLimit) FamilyLimit {
	if f.RequestsPerSecond <= 0 {
		f.RequestsPerSecond = def.RequestsPerSecond
	}
	if f.Burst <= 0 {
		f.Burst = def.Burst
	}
	if f.BytesPerSecond <= 0 {
		f.BytesPerSecond = def.BytesPerSecond
	}
	return f
}

func (l Limits) withDefaults() Limits {
	l.Forge = l.Forge.or(FamilyLimit{RequestsPerSecond: 20, Burst: 40, BytesPerSecond: 50 << 20})
	l.Model = l.Model.or(FamilyLimit{RequestsPerSecond: 10, Burst: 20, BytesPerSecond: 20 << 20})
	l.Ploegd = l.Ploegd.or(FamilyLimit{RequestsPerSecond: 10, Burst: 20, BytesPerSecond: 10 << 20})
	l.MCP = l.MCP.or(FamilyLimit{RequestsPerSecond: 5, Burst: 20, BytesPerSecond: 5 << 20})
	if l.MaxUpstreamConnections <= 0 {
		l.MaxUpstreamConnections = 256
	}
	return l
}

func (l Limits) family(name string) FamilyLimit {
	switch name {
	case FamilyForge:
		return l.Forge
	case FamilyPloegd:
		return l.Ploegd
	case FamilyMCP:
		return l.MCP
	default:
		return l.Model
	}
}

type buckets struct {
	requests *rate.Limiter
	bytes    *rate.Limiter
	seen     time.Time
}

type limiter struct {
	limits      Limits
	now         func() time.Time
	connections chan struct{}

	mu      sync.Mutex
	buckets map[string]*buckets
}

func newLimiter(l Limits, now func() time.Time) *limiter {
	return &limiter{limits: l, now: now, connections: make(chan struct{}, l.MaxUpstreamConnections), buckets: map[string]*buckets{}}
}

func (l *limiter) bucketsFor(key, family string) *buckets {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for k, b := range l.buckets {
		if now.Sub(b.seen) > time.Hour {
			delete(l.buckets, k)
		}
	}
	b, ok := l.buckets[key+"\x00"+family]
	if !ok {
		f := l.limits.family(family)
		burstBytes := int(f.BytesPerSecond)
		b = &buckets{requests: rate.NewLimiter(rate.Limit(f.RequestsPerSecond), f.Burst), bytes: rate.NewLimiter(rate.Limit(f.BytesPerSecond), burstBytes)}
		l.buckets[key+"\x00"+family] = b
	}
	b.seen = now
	return b
}

func (l *limiter) admit(key, family string, r *http.Request) (string, bool) {
	b := l.bucketsFor(key, family)
	now := l.now()
	if !b.requests.AllowN(now, 1) {
		return fmt.Sprintf("%g requests per second", l.limits.family(family).RequestsPerSecond), false
	}
	if b.bytes.Tokens() <= 0 {
		return fmt.Sprintf("%g request bytes per second", l.limits.family(family).BytesPerSecond), false
	}
	if r.Body != nil && r.Body != http.NoBody {
		r.Body = &meteredBody{ReadCloser: r.Body, bytes: b.bytes, now: l.now}
	}
	return "", true
}

func (l *limiter) connection(ctx context.Context) (func(), bool) {
	select {
	case l.connections <- struct{}{}:
		return func() { <-l.connections }, true
	case <-ctx.Done():
		return nil, false
	}
}

type meteredBody struct {
	io.ReadCloser
	bytes *rate.Limiter
	now   func() time.Time
}

func (m *meteredBody) Read(p []byte) (int, error) {
	n, err := m.ReadCloser.Read(p)
	if n > 0 {
		m.bytes.ReserveN(m.now(), min(n, m.bytes.Burst()))
	}
	return n, err
}
