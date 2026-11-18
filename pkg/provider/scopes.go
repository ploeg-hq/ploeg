package provider

import (
	"context"
	"errors"
	"time"
)

// MaxScopeChildren bounds how many children one ScopeLister call returns, so
// a tracker with a very wide container cannot make one read unbounded
// (ADR-0196).
const MaxScopeChildren = 1000

// ScopeNode is one container a tracker connection can reach. Kind and ID
// together name it to the adapter that listed it; ID alone is the routing
// Scope id when Bindable is true. Name is the tracker's own label, tracker
// text that is never a routing key.
type ScopeNode struct {
	Scope
	// Bindable is true for a container a Routing Rule can name: a ClickUp
	// List, a Vikunja project, a GitHub repository.
	Bindable bool
	// Browsable is true when ListScopes accepts this node as a parent.
	Browsable bool
}

// ScopeLister is implemented by a TrackerProvider that can list the
// containers its credential reaches, one level at a time, so a person can
// pick a Scope to bind without a tracker credential of their own
// (ADR-0196). A zero parent lists the top level. Each call returns at most
// MaxScopeChildren nodes, in the tracker's own order.
type ScopeLister interface {
	ListScopes(ctx context.Context, parent Scope) ([]ScopeNode, error)
}

// ScopeItem is one open item in a Scope, as a preview shows it. Title and
// Status are tracker text.
type ScopeItem struct {
	ID        string
	Title     string
	Status    string
	URL       string
	UpdatedAt time.Time
}

// ScopeItemPage is one page of a Scope's open items. NextPage is the page to
// ask for next, or 0 when this page was the last.
type ScopeItemPage struct {
	Items    []ScopeItem
	NextPage int
}

// ScopeItemLister is implemented by a TrackerProvider that can list the open
// items of one bindable Scope, a page at a time in the tracker's own page
// size, at most 100 items (ADR-0196). Pages count from 1.
type ScopeItemLister interface {
	ListOpenItems(ctx context.Context, scopeID string, page int) (ScopeItemPage, error)
}

// ErrNotFound marks an error for something the tracker says does not exist
// or the connection's credential cannot see.
var ErrNotFound = errors.New("not found at the tracker")

type notFound struct{ err error }

func (e notFound) Error() string   { return e.err.Error() }
func (e notFound) Unwrap() []error { return []error{e.err, ErrNotFound} }

// NotFound marks err as ErrNotFound and keeps its message.
func NotFound(err error) error {
	if err == nil {
		return nil
	}
	return notFound{err: err}
}
