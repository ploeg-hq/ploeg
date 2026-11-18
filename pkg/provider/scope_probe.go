package provider

import (
	"context"
	"errors"
)

// ErrScopeNotFound is what a ScopeProber or ScopeNamer returns when the
// tracker knows no Scope by that id or name.
var ErrScopeNotFound = errors.New("scope not found")

// ErrScopeAmbiguous is what a ScopeNamer returns when more than one Scope
// carries the name.
var ErrScopeAmbiguous = errors.New("more than one scope has this name")

// ScopeProbe is what a tracker connection's credential can do in one Scope
// (ADR-0186).
type ScopeProbe struct {
	ID   string
	Name string
	// CanRead is whether the credential reads the Scope's items.
	CanRead bool
	// CanWrite is whether it writes comments and statuses in the Scope.
	CanWrite bool
	// Members are the tracker users who can see the Scope; nil when they
	// could not be read.
	Members []string
	// Statuses is the Scope's status vocabulary; nil when the tracker has
	// none per Scope.
	Statuses []string
}

// ScopeProber is implemented by a TrackerProvider that can report, before a
// Scope is bound at runtime, whether its credential can work in the Scope
// (ADR-0186). An unknown id is ErrScopeNotFound.
type ScopeProber interface {
	ProbeScope(ctx context.Context, scopeID string) (ScopeProbe, error)
}

// ScopeNamer is implemented by a TrackerProvider that resolves a Scope's
// display name to its id (ADR-0183 step 4). No match is ErrScopeNotFound and
// more than one is ErrScopeAmbiguous.
type ScopeNamer interface {
	ScopeIDByName(ctx context.Context, name string) (string, error)
}

// ScopeWebhookRegistrar is implemented by a TrackerProvider that registers a
// webhook delivering dispatch events for one Scope to endpoint (ADR-0186
// section 7). It refuses without a secrets sink when the tracker generates
// the signing secret.
type ScopeWebhookRegistrar interface {
	RegisterScopeWebhook(ctx context.Context, scopeID, endpoint string, secrets WebhookSecretSink) (string, error)
}

// WebhookRemover is implemented by a TrackerProvider that deletes a webhook
// Ploeg registered, by its id.
type WebhookRemover interface {
	RemoveWebhook(ctx context.Context, webhookID string) error
}
