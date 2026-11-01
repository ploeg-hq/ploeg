package provider

import (
	"context"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// SessionActivityKind is the kind of one activity a Team reports to a tracker item
// during a Shift (ADR-0187). The kinds follow the native agent-session
// vocabulary, so a tracker with such sessions carries them as they are and
// a tracker without one renders each kind the way its own primitives allow.
type SessionActivityKind string

const (
	// SessionThought is reasoning the Team shares; a tracker that keeps
	// no thoughts may drop it.
	SessionThought SessionActivityKind = "thought"
	// SessionAction names something the Team did or started, such as a
	// Run of a Role.
	SessionAction SessionActivityKind = "action"
	// SessionResponse is an answer to the person: the gate's verdict, a
	// summary, the pull request.
	SessionResponse SessionActivityKind = "response"
	// SessionError says the Team stopped or refused, and why.
	SessionError SessionActivityKind = "error"
	// SessionElicitation is one question the Team needs a person to answer.
	SessionElicitation SessionActivityKind = "elicitation"
)

// SessionActivity is one entry a Team reports to a tracker item. Body is Markdown,
// the dialect every tracker comment is written in; the adapter renders it.
type SessionActivity struct {
	Kind SessionActivityKind
	Body string
}

// ExternalURL is a page outside the tracker that belongs to the work on an
// item, such as the pull request a Shift opened.
type ExternalURL struct {
	Label string
	URL   string
}

// DelegateSourceField is the Delegate source for a Team named by a field on
// the tracker item itself.
const DelegateSourceField = "field"

// AgentIdentity is implemented by a TrackerProvider on which a Team is a
// delegate: a person names the Team on the item, and the Team reports its
// work back to the item as activities (ADR-0187). A tracker with native
// agent sessions implements it directly; one without emulates it over its
// own primitives, and the core cannot tell the two apart.
//
// Delegate returns the Team the item names and where the name came from,
// such as DelegateSourceField; an empty Team says the item names none, so
// the caller falls back to its container pin. PostActivity reports one
// activity on the item. SetExternalURLs publishes the pages that belong to
// the work. An unconfigured adapter names no Team and writes nothing.
type AgentIdentity interface {
	Delegate(ctx context.Context, item work.WorkItem) (team string, source string, err error)
	PostActivity(ctx context.Context, externalID string, activity SessionActivity) error
	SetExternalURLs(ctx context.Context, externalID string, urls []ExternalURL) error
}
