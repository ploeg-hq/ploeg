package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// Tracker binding states (ADR-0186).
const (
	BindingActive          = "active"
	BindingAwaitingWebhook = "awaiting_webhook"
	BindingShadowed        = "shadowed"
	BindingInvalid         = "invalid"
)

// Why a binding is invalid.
const (
	BindingTeamRemoved       = "team_removed"
	BindingTargetUnavailable = "target_unavailable"
	// BindingTeamUnavailable is a binding whose runtime Team is shadowed or
	// invalid (ADR-0198 section 13).
	BindingTeamUnavailable = "team_unavailable"
)

// Where the webhook a binding relies on came from.
const (
	BindingWebhookExisting   = "existing"
	BindingWebhookRegistered = "registered"
	BindingWebhookNone       = "none"
)

// Audit actions of tracker bindings (ADR-0186 section 8).
const (
	AuditBindingCreated           = "tracker_binding.created"
	AuditBindingUpdated           = "tracker_binding.updated"
	AuditBindingRemoved           = "tracker_binding.removed"
	AuditBindingRefused           = "tracker_binding.refused"
	AuditBindingShadowed          = "tracker_binding.shadowed"
	AuditBindingUnshadowed        = "tracker_binding.unshadowed"
	AuditBindingInvalid           = "tracker_binding.invalid"
	AuditBindingRestored          = "tracker_binding.restored"
	AuditBindingWebhookRegistered = "tracker_binding.webhook_registered"
	AuditBindingWebhookRemoved    = "tracker_binding.webhook_removed"
)

// StaticEntry names the static route a binding conflicts with.
type StaticEntry struct {
	Entry      string   `json:"entry"`
	Team       string   `json:"team,omitempty"`
	Repository string   `json:"repository,omitempty"`
	Default    string   `json:"default,omitempty"`
	Allow      []string `json:"allow,omitempty"`
}

// InvalidDetail says which target key made a binding invalid and, for a
// runtime target, that target's own state and reason.
type InvalidDetail struct {
	Target       string `json:"target,omitempty"`
	TargetState  string `json:"targetState,omitempty"`
	TargetReason string `json:"targetReason,omitempty"`
}

// TrackerBindingFields are the fields of a binding a replacement may change.
type TrackerBindingFields struct {
	Default  string
	Allow    []string
	Statuses map[string]string
	Label    string
}

// Same reports whether two field sets are equal; Allow is compared as given,
// so callers sort it.
func (f TrackerBindingFields) Same(o TrackerBindingFields) bool {
	if f.Default != o.Default || f.Label != o.Label || !slices.Equal(f.Allow, o.Allow) || len(f.Statuses) != len(o.Statuses) {
		return false
	}
	for k, v := range f.Statuses {
		if o.Statuses[k] != v {
			return false
		}
	}
	return true
}

// BindingWebhook is the webhook a binding relies on: its last known state,
// its id at the tracker and whether this binding registered it.
type BindingWebhook struct {
	State  string
	ID     string
	Source string
}

// TrackerBinding is one runtime Routing Rule (ADR-0186).
type TrackerBinding struct {
	ID     int64
	Tenant string
	Team   string
	// TeamID is the id of the Team the binding routes to (ADR-0198);
	// empty while its Team has none.
	TeamID     string
	Connection string
	ScopeID    string
	ScopeName  string
	TrackerBindingFields
	Revision      int64
	State         string
	ShadowedBy    *StaticEntry
	InvalidReason string
	InvalidDetail *InvalidDetail
	Webhook       BindingWebhook
	CreatedBy     string
	UpdatedBy     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	RemovedAt     *time.Time
}

// NewTrackerBinding is a binding to create, already probed.
type NewTrackerBinding struct {
	Team       string
	Connection string
	ScopeID    string
	ScopeName  string
	TrackerBindingFields
	State   string
	Webhook BindingWebhook
	// Max is the Team's limit of live bindings.
	Max int
}

// BindingAudit is who a binding change is attributed to and through which
// consumer and channel.
type BindingAudit struct {
	Attribution Attribution
}

func (a BindingAudit) actor() string {
	if a.Attribution.Actor != "" {
		return a.Attribution.Actor
	}
	return "ploegd"
}

// ErrBindingLimit is a create over the Team's limit of live bindings.
var ErrBindingLimit = errors.New("the team holds its maximum of live tracker bindings")

// BindingExistsError is a create whose key is bound with another body.
type BindingExistsError struct{ Current TrackerBinding }

func (e *BindingExistsError) Error() string { return "a live binding with this key exists" }

// ScopeBoundElsewhereError is a create for a Scope another connection binds.
type ScopeBoundElsewhereError struct{ Other TrackerBinding }

func (e *ScopeBoundElsewhereError) Error() string {
	return "the scope is bound through another connection"
}

// StaleRevisionError is a replacement whose expected revision is not the
// binding's current one.
type StaleRevisionError struct{ Current TrackerBinding }

func (e *StaleRevisionError) Error() string { return "the binding's revision changed" }

const bindingColumns = `id, tenant, team, connection, scope_id, scope_name, default_target, allow_targets,
	statuses, label, revision, state, shadowed_by, invalid_reason, invalid_detail, webhook_state,
	webhook_id, webhook_source, created_by, updated_by, created_at, updated_at, removed_at, coalesce(team_id, '')`

const bindingWriteLock = `SELECT pg_advisory_xact_lock(hashtextextended('ploeg.tracker-bindings', 0))`

func scanBinding(row pgx.Row) (TrackerBinding, error) {
	var b TrackerBinding
	var statuses, shadowed, invalid []byte
	var invalidReason, webhookID *string
	err := row.Scan(&b.ID, &b.Tenant, &b.Team, &b.Connection, &b.ScopeID, &b.ScopeName, &b.Default, &b.Allow,
		&statuses, &b.Label, &b.Revision, &b.State, &shadowed, &invalidReason, &invalid, &b.Webhook.State,
		&webhookID, &b.Webhook.Source, &b.CreatedBy, &b.UpdatedBy, &b.CreatedAt, &b.UpdatedAt, &b.RemovedAt, &b.TeamID)
	if err != nil {
		return b, err
	}
	if b.Allow == nil {
		b.Allow = []string{}
	}
	if err := json.Unmarshal(statuses, &b.Statuses); err != nil {
		return b, err
	}
	if len(shadowed) > 0 {
		b.ShadowedBy = &StaticEntry{}
		if err := json.Unmarshal(shadowed, b.ShadowedBy); err != nil {
			return b, err
		}
	}
	if len(invalid) > 0 {
		b.InvalidDetail = &InvalidDetail{}
		if err := json.Unmarshal(invalid, b.InvalidDetail); err != nil {
			return b, err
		}
	}
	if invalidReason != nil {
		b.InvalidReason = *invalidReason
	}
	if webhookID != nil {
		b.Webhook.ID = *webhookID
	}
	return b, nil
}

func collectBindings(rows pgx.Rows) ([]TrackerBinding, error) {
	defer rows.Close()
	out := []TrackerBinding{}
	for rows.Next() {
		b, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// LiveTrackerBindings returns every binding not removed, oldest first.
func (s *Store) LiveTrackerBindings(ctx context.Context) ([]TrackerBinding, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+bindingColumns+` FROM tracker_bindings WHERE removed_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return collectBindings(rows)
}

// TrackerBindingRevision returns the highest revision any binding was given,
// removals included, so a replica can tell whether to rebuild.
func (s *Store) TrackerBindingRevision(ctx context.Context) (int64, error) {
	var rev int64
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(max(revision), 0) FROM tracker_bindings`).Scan(&rev)
	return rev, err
}

// CreateTrackerBinding stores a binding unless its key is live (ADR-0186):
// the same body returns the live binding with created false, another body
// is a *BindingExistsError, a Scope another connection binds is a
// *ScopeBoundElsewhereError, and a Team at its limit is ErrBindingLimit.
func (s *Store) CreateTrackerBinding(ctx context.Context, in NewTrackerBinding, audit BindingAudit) (TrackerBinding, bool, error) {
	statuses, err := json.Marshal(nonNilStatuses(in.Statuses))
	if err != nil {
		return TrackerBinding{}, false, err
	}
	var out TrackerBinding
	created := false
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, bindingWriteLock); err != nil {
			return err
		}
		current, err := scanBinding(tx.QueryRow(ctx, `SELECT `+bindingColumns+` FROM tracker_bindings
			WHERE removed_at IS NULL AND tenant = '' AND connection = $1 AND scope_id = $2 AND team = $3`, in.Connection, in.ScopeID, in.Team))
		switch {
		case err == nil && current.TrackerBindingFields.Same(in.TrackerBindingFields):
			out = current
			return nil
		case err == nil:
			return &BindingExistsError{Current: current}
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		other, err := scanBinding(tx.QueryRow(ctx, `SELECT `+bindingColumns+` FROM tracker_bindings
			WHERE removed_at IS NULL AND tenant = '' AND scope_id = $1 AND connection <> $2 ORDER BY id LIMIT 1`, in.ScopeID, in.Connection))
		switch {
		case err == nil:
			return &ScopeBoundElsewhereError{Other: other}
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		var live int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tracker_bindings WHERE removed_at IS NULL AND tenant = '' AND team = $1`, in.Team).Scan(&live); err != nil {
			return err
		}
		if live >= in.Max {
			return ErrBindingLimit
		}
		out, err = scanBinding(tx.QueryRow(ctx, `INSERT INTO tracker_bindings (team, connection, scope_id, scope_name,
			default_target, allow_targets, statuses, label, state, webhook_state, webhook_id, webhook_source, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $13) RETURNING `+bindingColumns,
			in.Team, in.Connection, in.ScopeID, in.ScopeName, in.Default, nonNilAllow(in.Allow), statuses, in.Label,
			in.State, in.Webhook.State, nullable(in.Webhook.ID), in.Webhook.Source, audit.actor()))
		if err != nil {
			return err
		}
		created = true
		if err := auditBinding(ctx, tx, AuditBindingCreated, out, audit, nil); err != nil {
			return err
		}
		if in.Webhook.Source == BindingWebhookRegistered {
			return auditBinding(ctx, tx, AuditBindingWebhookRegistered, out, audit, map[string]any{"webhookId": in.Webhook.ID})
		}
		return nil
	})
	return out, created, err
}

// TrackerBindingByID returns a binding of team by id, removed or not.
func (s *Store) TrackerBindingByID(ctx context.Context, team string, id int64) (TrackerBinding, error) {
	b, err := scanBinding(s.pool.QueryRow(ctx, `SELECT `+bindingColumns+` FROM tracker_bindings WHERE id = $1 AND team = $2 AND tenant = ''`, id, team))
	if errors.Is(err, pgx.ErrNoRows) {
		return b, ErrOperatorNotFound
	}
	return b, err
}

// BindingReplacement is a probed replacement of a binding's fields.
type BindingReplacement struct {
	Team     string
	ID       int64
	Revision int64
	TrackerBindingFields
	State   string
	Webhook BindingWebhook
}

// ReplaceTrackerBinding replaces a live binding's fields when its revision is
// still r.Revision, else a *StaleRevisionError. A binding that is not live is
// ErrOperatorNotFound.
func (s *Store) ReplaceTrackerBinding(ctx context.Context, r BindingReplacement, audit BindingAudit) (TrackerBinding, error) {
	statuses, err := json.Marshal(nonNilStatuses(r.Statuses))
	if err != nil {
		return TrackerBinding{}, err
	}
	var out TrackerBinding
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, bindingWriteLock); err != nil {
			return err
		}
		current, err := scanBinding(tx.QueryRow(ctx, `SELECT `+bindingColumns+` FROM tracker_bindings
			WHERE id = $1 AND team = $2 AND tenant = '' AND removed_at IS NULL`, r.ID, r.Team))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrOperatorNotFound
		}
		if err != nil {
			return err
		}
		if current.Revision != r.Revision {
			return &StaleRevisionError{Current: current}
		}
		out, err = scanBinding(tx.QueryRow(ctx, `UPDATE tracker_bindings SET default_target = $2, allow_targets = $3,
			statuses = $4, label = $5, state = $6, shadowed_by = NULL, invalid_reason = NULL, invalid_detail = NULL,
			webhook_state = $7, webhook_id = $8, webhook_source = $9, updated_by = $10, updated_at = now(),
			revision = nextval('tracker_binding_revisions') WHERE id = $1 RETURNING `+bindingColumns,
			r.ID, r.Default, nonNilAllow(r.Allow), statuses, r.Label, r.State, r.Webhook.State, nullable(r.Webhook.ID),
			r.Webhook.Source, audit.actor()))
		if err != nil {
			return err
		}
		return auditBinding(ctx, tx, AuditBindingUpdated, out, audit, map[string]any{"oldRevision": current.Revision, "newRevision": out.Revision})
	})
	return out, err
}

// RemoveTrackerBinding removes a binding of team by id. removed is false when
// it was already removed; an unknown id is ErrOperatorNotFound. detail is
// added to the removal event.
func (s *Store) RemoveTrackerBinding(ctx context.Context, team string, id int64, audit BindingAudit, detail func(TrackerBinding) map[string]any) (TrackerBinding, bool, error) {
	var out TrackerBinding
	removed := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, bindingWriteLock); err != nil {
			return err
		}
		current, err := scanBinding(tx.QueryRow(ctx, `SELECT `+bindingColumns+` FROM tracker_bindings
			WHERE id = $1 AND team = $2 AND tenant = ''`, id, team))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrOperatorNotFound
		}
		if err != nil {
			return err
		}
		if current.RemovedAt != nil {
			out = current
			return nil
		}
		out, err = scanBinding(tx.QueryRow(ctx, `UPDATE tracker_bindings SET removed_at = now(), removed_by = $2,
			updated_at = now(), revision = nextval('tracker_binding_revisions') WHERE id = $1 RETURNING `+bindingColumns,
			id, audit.actor()))
		if err != nil {
			return err
		}
		removed = true
		var extra map[string]any
		if detail != nil {
			extra = detail(out)
		}
		return auditBinding(ctx, tx, AuditBindingRemoved, out, audit, extra)
	})
	return out, removed, err
}

// BindingStateChange is the state the routing rebuild computed for a binding.
type BindingStateChange struct {
	State         string
	ShadowedBy    *StaticEntry
	InvalidReason string
	InvalidDetail *InvalidDetail
}

// SetTrackerBindingState records the state the routing rebuild computed for a
// live binding whose state is still from, and writes action when the state
// changes. It reports whether this call changed the state, so of several
// replicas computing the same change only one writes the event.
func (s *Store) SetTrackerBindingState(ctx context.Context, id int64, from string, to BindingStateChange, action string) (bool, error) {
	var shadowed, invalid []byte
	var err error
	if to.ShadowedBy != nil {
		if shadowed, err = json.Marshal(to.ShadowedBy); err != nil {
			return false, err
		}
	}
	if to.InvalidDetail != nil {
		if invalid, err = json.Marshal(to.InvalidDetail); err != nil {
			return false, err
		}
	}
	changed := false
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		b, err := scanBinding(tx.QueryRow(ctx, `UPDATE tracker_bindings SET state = $3, shadowed_by = $4, invalid_reason = $5,
			invalid_detail = $6 WHERE id = $1 AND removed_at IS NULL AND state = $2 RETURNING `+bindingColumns,
			id, from, to.State, shadowed, nullable(to.InvalidReason), invalid))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if from == to.State || action == "" {
			return nil
		}
		changed = true
		detail := map[string]any{"from": from}
		if to.ShadowedBy != nil {
			detail["shadowedBy"] = to.ShadowedBy
		}
		if to.InvalidReason != "" {
			detail["invalidReason"] = to.InvalidReason
		}
		if to.InvalidDetail != nil {
			detail["invalidDetail"] = to.InvalidDetail
		}
		return auditBinding(ctx, tx, action, b, BindingAudit{}, detail)
	})
	return changed, err
}

// SetTrackerBindingWebhook records what a later webhook check found for a
// live binding and moves it between awaiting_webhook and active with it.
func (s *Store) SetTrackerBindingWebhook(ctx context.Context, id int64, webhookState string) error {
	_, err := s.pool.Exec(ctx, `UPDATE tracker_bindings SET webhook_state = $2,
		state = CASE WHEN state = 'awaiting_webhook' AND $2 = 'present' THEN 'active'
		             WHEN state = 'active' AND $2 <> 'present' THEN 'awaiting_webhook' ELSE state END,
		webhook_source = CASE WHEN webhook_source = 'none' AND $2 = 'present' THEN 'existing' ELSE webhook_source END
		WHERE id = $1 AND removed_at IS NULL`, id, webhookState)
	return err
}

// AuditTrackerBinding writes one tracker binding event that changes no row,
// such as a refusal or a webhook removal.
func (s *Store) AuditTrackerBinding(ctx context.Context, action string, b TrackerBinding, audit BindingAudit, extra map[string]any) error {
	return auditBinding(ctx, s.pool, action, b, audit, extra)
}

func auditBinding(ctx context.Context, db execer, action string, b TrackerBinding, audit BindingAudit, extra map[string]any) error {
	detail := map[string]any{
		"team": b.Team,
		"binding": map[string]any{
			"id": fmt.Sprint(b.ID), "connection": b.Connection, "scopeId": b.ScopeID, "team": b.Team,
			"default": b.Default, "allow": nonNilAllow(b.Allow), "revision": b.Revision, "state": b.State,
		},
	}
	if b.ID == 0 {
		detail["binding"] = map[string]any{"connection": b.Connection, "scopeId": b.ScopeID, "team": b.Team}
	}
	if audit.Attribution.Consumer != "" {
		detail["consumer"] = audit.Attribution.Consumer
		detail["channel"] = channelOf(audit.Attribution)
		detail[AuditAttributionKey] = audit.Attribution
	}
	for k, v := range extra {
		detail[k] = v
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `INSERT INTO audit_log (actor, action, detail) VALUES ($1, $2, $3)`, audit.actor(), action, raw)
	return err
}

func channelOf(a Attribution) string {
	if a.Via != "" {
		return a.Via
	}
	return "http"
}

func nonNilAllow(allow []string) []string {
	if allow == nil {
		return []string{}
	}
	return allow
}

func nonNilStatuses(statuses map[string]string) map[string]string {
	if statuses == nil {
		return map[string]string{}
	}
	return statuses
}

// TrackerBindingWebhookUsers counts the live bindings on a connection, other
// than except, that rely on the webhook id.
func (s *Store) TrackerBindingWebhookUsers(ctx context.Context, connection, webhookID string, except int64) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM tracker_bindings WHERE removed_at IS NULL AND tenant = ''
		AND connection = $1 AND webhook_id = $2 AND id <> $3`, connection, webhookID, except).Scan(&n)
	return n, err
}

// RegisteredTrackerBindingWebhook returns the webhook a live binding on the
// connection registered for the Scope, or "".
func (s *Store) RegisteredTrackerBindingWebhook(ctx context.Context, connection, scope string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `SELECT webhook_id FROM tracker_bindings WHERE removed_at IS NULL AND tenant = ''
		AND connection = $1 AND scope_id = $2 AND webhook_source = 'registered' AND webhook_id IS NOT NULL
		ORDER BY id LIMIT 1`, connection, scope).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}
