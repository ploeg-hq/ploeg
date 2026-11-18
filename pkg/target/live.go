package target

import "sync/atomic"

// Live is a Resolver whose routing table is replaced while ploegd runs
// (ADR-0186): every Route reads the table stored last, and Store swaps it
// atomically, so an item never sees half of a rebuild.
type Live struct {
	current atomic.Pointer[MapResolver]
}

// NewLive starts a Live resolver on m.
func NewLive(m *MapResolver) *Live {
	l := &Live{}
	l.current.Store(m)
	return l
}

// Store replaces the routing table.
func (l *Live) Store(m *MapResolver) { l.current.Store(m) }

// Current returns the routing table in force.
func (l *Live) Current() *MapResolver { return l.current.Load() }

// Route resolves through the routing table in force.
func (l *Live) Route(req Request) (Route, bool, error) {
	return l.current.Load().Route(req)
}
