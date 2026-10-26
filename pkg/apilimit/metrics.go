package apilimit

import (
	"slices"
	"sync"
	"sync/atomic"
)

type providerCounters struct {
	requests    atomic.Int64
	rateLimited atomic.Int64
	retries     atomic.Int64
	delayed     atomic.Int64
	throttled   atomic.Int64
}

var (
	countersMu sync.Mutex
	byProvider = map[string]*providerCounters{}
)

func counters(provider string) *providerCounters {
	countersMu.Lock()
	defer countersMu.Unlock()
	c, ok := byProvider[provider]
	if !ok {
		c = &providerCounters{}
		byProvider[provider] = c
	}
	return c
}

// Counts is what this process has sent to one provider since it started.
// Requests counts every call sent upstream, retries included. RateLimited
// counts 429 answers, Retries the calls sent again after one, Delayed the
// calls that waited for their bucket and Throttled the calls refused because
// that wait was too long.
type Counts struct {
	Provider    string
	Requests    int64
	RateLimited int64
	Retries     int64
	Delayed     int64
	Throttled   int64
}

// Snapshot returns the counts of every provider in Policies, sorted by name.
func Snapshot() []Counts {
	names := make([]string, 0, len(Policies))
	for name := range Policies {
		names = append(names, name)
	}
	slices.Sort(names)
	out := make([]Counts, 0, len(names))
	for _, name := range names {
		c := counters(name)
		out = append(out, Counts{Provider: name, Requests: c.requests.Load(), RateLimited: c.rateLimited.Load(), Retries: c.retries.Load(), Delayed: c.delayed.Load(), Throttled: c.throttled.Load()})
	}
	return out
}
