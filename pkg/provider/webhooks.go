package provider

import "context"

// Webhook states a WebhookChecker reports for one Scope.
const (
	WebhookPresent    = "present"
	WebhookMissing    = "missing"
	WebhookUnreadable = "unreadable"
)

// ScopeWebhook is what a webhook check found for one Scope: whether a
// webhook delivers the events dispatch needs to Ploeg, and the tracker users
// who can see the Scope. Members is nil when they could not be read.
type ScopeWebhook struct {
	Scope   string
	Webhook string
	Members []string
	// Err says why Webhook is WebhookUnreadable.
	Err error
}

// WebhookChecker is implemented by a TrackerProvider that can tell whether
// each of its Scopes has a webhook delivering dispatch events to endpoint,
// or, when endpoint is empty, to any URL on the tracker's webhook path
// (ADR-0183). It reports every Scope it was given, in order.
type WebhookChecker interface {
	CheckWebhooks(ctx context.Context, scopes []string, endpoint string) []ScopeWebhook
}

// WebhookSecretSink keeps the signing secret of a webhook Ploeg registered,
// for a tracker that generates the secret and returns it only when the
// webhook is created.
type WebhookSecretSink interface {
	StoreWebhookSecret(ctx context.Context, webhookID, secret string) error
}

// WebhookRegistrar is implemented by a TrackerProvider that can register a
// webhook delivering dispatch events to endpoint. It refuses without a
// secrets sink when the tracker generates the signing secret.
type WebhookRegistrar interface {
	RegisterWebhook(ctx context.Context, endpoint string, secrets WebhookSecretSink) (string, error)
}
