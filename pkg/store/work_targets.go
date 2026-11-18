package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Runtime Work Target states (ADR-0193 section 4).
const (
	WorkTargetActive   = "active"
	WorkTargetShadowed = "shadowed"
	WorkTargetInvalid  = "invalid"
)

// Why a runtime Work Target is invalid.
const (
	WorkTargetGrantRevoked          = "grant_revoked"
	WorkTargetInstallationSuspended = "installation_suspended"
	WorkTargetConnectionUnusable    = "connection_unusable"
	WorkTargetRepositoryGone        = "repository_gone"
	WorkTargetRepositoryMoved       = "repository_moved"
)

// Audit actions of runtime Work Targets (ADR-0193 section 8).
const (
	AuditWorkTargetRegistered = "work_target.registered"
	AuditWorkTargetUpdated    = "work_target.updated"
	AuditWorkTargetRemoved    = "work_target.removed"
	AuditWorkTargetRefused    = "work_target.refused"
	AuditWorkTargetShadowed   = "work_target.shadowed"
	AuditWorkTargetUnshadowed = "work_target.unshadowed"
	AuditWorkTargetInvalid    = "work_target.invalid"
	AuditWorkTargetRestored   = "work_target.restored"
)

// TargetReadiness is the readiness verdict of a runtime target's last
// inspection, never a secret.
type TargetReadiness struct {
	Ready            bool       `json:"ready"`
	Unknown          bool       `json:"unknown,omitempty"`
	Reasons          []string   `json:"reasons,omitempty"`
	Reason           string     `json:"reason,omitempty"`
	PrepareAvailable bool       `json:"prepareAvailable,omitempty"`
	CheckedAt        *time.Time `json:"checkedAt,omitempty"`
}

// WorkTarget is one Work Target registered at runtime (ADR-0193).
type WorkTarget struct {
	ID            int64
	Tenant        string
	Key           string
	Connection    string
	Forge         string
	Repository    string
	RepositoryID  string
	Branch        string
	Label         string
	Revision      int64
	State         string
	ShadowedBy    *StaticEntry
	InvalidReason string
	Readiness     TargetReadiness
	CreatedBy     string
	UpdatedBy     string
	RemovedBy     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	RemovedAt     *time.Time
}

// SameBody reports whether a create for the same identity asks for this
// target again: the same key, connection, branch and label.
func (t WorkTarget) SameBody(o NewWorkTarget) bool {
	return t.Key == o.Key && t.Connection == o.Connection && t.Branch == o.Branch && t.Label == o.Label
}

// NewWorkTarget is a runtime target to create, already probed.
type NewWorkTarget struct {
	Tenant       string
	Key          string
	Connection   string
	Forge        string
	Repository   string
	RepositoryID string
	Branch       string
	Label        string
	Readiness    TargetReadiness
	// Max is the Tenant's limit of live runtime targets.
	Max int
}

// WorkTargetAudit is who a runtime target change is attributed to: a proven
// person through a consumer, or Actor alone for a change Ploeg found.
type WorkTargetAudit struct {
	Attribution Attribution
	Actor       string
}

func (a WorkTargetAudit) actor() string {
	switch {
	case a.Attribution.Actor != "":
		return a.Attribution.Actor
	case a.Actor != "":
		return a.Actor
	}
	return "ploegd"
}

// ErrWorkTargetLimit is a create over the Tenant's limit.
var ErrWorkTargetLimit = errors.New("the tenant holds its maximum of live runtime targets")

// WorkTargetExistsError is a create whose identity is registered with
// another body.
type WorkTargetExistsError struct{ Current WorkTarget }

func (e *WorkTargetExistsError) Error() string {
	return "a live runtime target with this identity exists"
}

// WorkTargetKeyTakenError is a create whose key another live runtime target
// holds.
type WorkTargetKeyTakenError struct{ Holder WorkTarget }

func (e *WorkTargetKeyTakenError) Error() string { return "another live runtime target holds this key" }

// WorkTargetStaleError is a replacement whose expected revision is not the
// target's current one.
type WorkTargetStaleError struct{ Current WorkTarget }

func (e *WorkTargetStaleError) Error() string { return "the target's revision changed" }

const workTargetColumns = `id, tenant, target_key, connection, forge, repository, repository_id, branch, label,
	revision, state, shadowed_by, invalid_reason, readiness, created_by, updated_by, removed_by,
	created_at, updated_at, removed_at`

const workTargetWriteLock = `SELECT pg_advisory_xact_lock(hashtextextended('ploeg.work-targets', 0))`

func scanWorkTarget(row pgx.Row) (WorkTarget, error) {
	var t WorkTarget
	var shadowed, readiness []byte
	var invalidReason, removedBy *string
	err := row.Scan(&t.ID, &t.Tenant, &t.Key, &t.Connection, &t.Forge, &t.Repository, &t.RepositoryID, &t.Branch,
		&t.Label, &t.Revision, &t.State, &shadowed, &invalidReason, &readiness, &t.CreatedBy, &t.UpdatedBy, &removedBy,
		&t.CreatedAt, &t.UpdatedAt, &t.RemovedAt)
	if err != nil {
		return t, err
	}
	if len(shadowed) > 0 {
		t.ShadowedBy = &StaticEntry{}
		if err := json.Unmarshal(shadowed, t.ShadowedBy); err != nil {
			return t, err
		}
	}
	if len(readiness) > 0 {
		if err := json.Unmarshal(readiness, &t.Readiness); err != nil {
			return t, err
		}
	}
	if invalidReason != nil {
		t.InvalidReason = *invalidReason
	}
	if removedBy != nil {
		t.RemovedBy = *removedBy
	}
	return t, nil
}

func collectWorkTargets(rows pgx.Rows) ([]WorkTarget, error) {
	defer rows.Close()
	out := []WorkTarget{}
	for rows.Next() {
		t, err := scanWorkTarget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// LiveWorkTargets returns every runtime target of tenant not removed, oldest
// first.
func (s *Store) LiveWorkTargets(ctx context.Context, tenant string) ([]WorkTarget, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+workTargetColumns+` FROM work_targets
		WHERE removed_at IS NULL AND tenant = $1 ORDER BY id`, tenant)
	if err != nil {
		return nil, err
	}
	return collectWorkTargets(rows)
}

// WorkTargetRevision returns the highest revision any runtime target was
// given, removals and state changes included, so a replica can tell whether
// to rebuild.
func (s *Store) WorkTargetRevision(ctx context.Context) (int64, error) {
	var rev int64
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(max(revision), 0) FROM work_targets`).Scan(&rev)
	return rev, err
}

// LiveWorkTarget returns the live runtime target of tenant under key; none
// is ErrOperatorNotFound.
func (s *Store) LiveWorkTarget(ctx context.Context, tenant, key string) (WorkTarget, error) {
	t, err := scanWorkTarget(s.pool.QueryRow(ctx, `SELECT `+workTargetColumns+` FROM work_targets
		WHERE removed_at IS NULL AND tenant = $1 AND lower(target_key) = lower($2)`, tenant, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrOperatorNotFound
	}
	return t, err
}

// WorkTargetForRepository returns the runtime target of tenant that names
// repository on forge: the live one, else the one removed last. It reads the
// row afresh, so a claim never trusts a replica's cached registry
// (ADR-0193 section 4). found is false when no runtime target ever named it.
func (s *Store) WorkTargetForRepository(ctx context.Context, tenant, forge, repository string) (WorkTarget, bool, error) {
	t, err := scanWorkTarget(s.pool.QueryRow(ctx, `SELECT `+workTargetColumns+` FROM work_targets
		WHERE tenant = $1 AND forge = $2 AND lower(repository) = lower($3)
		ORDER BY (removed_at IS NULL) DESC, id DESC LIMIT 1`, tenant, forge, repository))
	if errors.Is(err, pgx.ErrNoRows) {
		return t, false, nil
	}
	return t, err == nil, err
}

// LiveWorkTargetsByConnection returns the live runtime targets that name the
// forge connection id.
func (s *Store) LiveWorkTargetsByConnection(ctx context.Context, tenant, connection string) ([]WorkTarget, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+workTargetColumns+` FROM work_targets
		WHERE removed_at IS NULL AND tenant = $1 AND connection = $2 ORDER BY id`, tenant, connection)
	if err != nil {
		return nil, err
	}
	return collectWorkTargets(rows)
}

// CreateWorkTarget stores a runtime target unless its identity is live
// (ADR-0193 section 3): the same body returns the live target with created
// false, another body is a *WorkTargetExistsError, a key another live target
// holds is a *WorkTargetKeyTakenError, and a Tenant at its limit is
// ErrWorkTargetLimit.
func (s *Store) CreateWorkTarget(ctx context.Context, in NewWorkTarget, audit WorkTargetAudit) (WorkTarget, bool, error) {
	readiness, err := json.Marshal(in.Readiness)
	if err != nil {
		return WorkTarget{}, false, err
	}
	var out WorkTarget
	created := false
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, workTargetWriteLock); err != nil {
			return err
		}
		current, err := scanWorkTarget(tx.QueryRow(ctx, `SELECT `+workTargetColumns+` FROM work_targets
			WHERE removed_at IS NULL AND tenant = $1 AND forge = $2 AND lower(repository) = lower($3)`, in.Tenant, in.Forge, in.Repository))
		switch {
		case err == nil && current.SameBody(in):
			out = current
			return nil
		case err == nil:
			return &WorkTargetExistsError{Current: current}
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		holder, err := scanWorkTarget(tx.QueryRow(ctx, `SELECT `+workTargetColumns+` FROM work_targets
			WHERE removed_at IS NULL AND tenant = $1 AND lower(target_key) = lower($2)`, in.Tenant, in.Key))
		switch {
		case err == nil:
			return &WorkTargetKeyTakenError{Holder: holder}
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		var live int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM work_targets WHERE removed_at IS NULL AND tenant = $1`, in.Tenant).Scan(&live); err != nil {
			return err
		}
		if live >= in.Max {
			return ErrWorkTargetLimit
		}
		out, err = scanWorkTarget(tx.QueryRow(ctx, `INSERT INTO work_targets (tenant, target_key, connection, forge,
			repository, repository_id, branch, label, state, readiness, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'active', $9, $10, $10) RETURNING `+workTargetColumns,
			in.Tenant, in.Key, in.Connection, in.Forge, in.Repository, in.RepositoryID, in.Branch, in.Label, readiness, audit.actor()))
		if err != nil {
			return err
		}
		created = true
		return auditWorkTarget(ctx, tx, AuditWorkTargetRegistered, out, audit, nil)
	})
	return out, created, err
}

// WorkTargetReplacement is a probed replacement of a target's branch and
// label.
type WorkTargetReplacement struct {
	Tenant    string
	Key       string
	Revision  int64
	Branch    string
	Label     string
	Readiness TargetReadiness
}

// ReplaceWorkTarget replaces a live target's branch and label when its
// revision is still r.Revision, else a *WorkTargetStaleError. A target that
// is not live is ErrOperatorNotFound.
func (s *Store) ReplaceWorkTarget(ctx context.Context, r WorkTargetReplacement, audit WorkTargetAudit) (WorkTarget, error) {
	readiness, err := json.Marshal(r.Readiness)
	if err != nil {
		return WorkTarget{}, err
	}
	var out WorkTarget
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, workTargetWriteLock); err != nil {
			return err
		}
		current, err := scanWorkTarget(tx.QueryRow(ctx, `SELECT `+workTargetColumns+` FROM work_targets
			WHERE removed_at IS NULL AND tenant = $1 AND lower(target_key) = lower($2)`, r.Tenant, r.Key))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrOperatorNotFound
		}
		if err != nil {
			return err
		}
		if current.Revision != r.Revision {
			return &WorkTargetStaleError{Current: current}
		}
		out, err = scanWorkTarget(tx.QueryRow(ctx, `UPDATE work_targets SET branch = $2, label = $3, readiness = $4,
			updated_by = $5, updated_at = now(), revision = nextval('work_target_revisions')
			WHERE id = $1 RETURNING `+workTargetColumns, current.ID, r.Branch, r.Label, readiness, audit.actor()))
		if err != nil {
			return err
		}
		return auditWorkTarget(ctx, tx, AuditWorkTargetUpdated, out, audit, map[string]any{
			"oldRevision": current.Revision, "newRevision": out.Revision, "oldBranch": current.Branch})
	})
	return out, err
}

// RemoveWorkTarget removes the live target of tenant under key. removed is
// false when no live target holds the key but one held it before; a key no
// runtime target ever held is ErrOperatorNotFound. extra is added to the
// removal event.
func (s *Store) RemoveWorkTarget(ctx context.Context, tenant, key string, audit WorkTargetAudit, extra map[string]any) (WorkTarget, bool, error) {
	var out WorkTarget
	removed := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, workTargetWriteLock); err != nil {
			return err
		}
		current, err := scanWorkTarget(tx.QueryRow(ctx, `SELECT `+workTargetColumns+` FROM work_targets
			WHERE tenant = $1 AND lower(target_key) = lower($2) ORDER BY (removed_at IS NULL) DESC, id DESC LIMIT 1`, tenant, key))
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
		out, err = scanWorkTarget(tx.QueryRow(ctx, `UPDATE work_targets SET removed_at = now(), removed_by = $2,
			updated_at = now(), revision = nextval('work_target_revisions') WHERE id = $1 RETURNING `+workTargetColumns,
			current.ID, audit.actor()))
		if err != nil {
			return err
		}
		removed = true
		return auditWorkTarget(ctx, tx, AuditWorkTargetRemoved, out, audit, extra)
	})
	return out, removed, err
}

// WorkTargetStateChange is the state Ploeg found a live runtime target in.
type WorkTargetStateChange struct {
	State         string
	ShadowedBy    *StaticEntry
	InvalidReason string
	// Readiness replaces the stored verdict when not nil, as when a restored
	// target's probe read it again.
	Readiness *TargetReadiness
}

// SetWorkTargetState records the state Ploeg found a live target in when its
// state and reason are still from's, bumps its revision so every replica
// rebuilds, and writes action. It reports whether this call changed the row,
// so of several replicas finding the same change only one writes the event.
func (s *Store) SetWorkTargetState(ctx context.Context, id int64, from WorkTarget, to WorkTargetStateChange, action string, audit WorkTargetAudit, extra map[string]any) (bool, error) {
	var shadowed []byte
	var err error
	if to.ShadowedBy != nil {
		if shadowed, err = json.Marshal(to.ShadowedBy); err != nil {
			return false, err
		}
	}
	readiness := from.Readiness
	if to.Readiness != nil {
		readiness = *to.Readiness
	}
	readinessJSON, err := json.Marshal(readiness)
	if err != nil {
		return false, err
	}
	changed := false
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		t, err := scanWorkTarget(tx.QueryRow(ctx, `UPDATE work_targets SET state = $4, shadowed_by = $5, invalid_reason = $6,
			readiness = $7, updated_at = now(), updated_by = $8, revision = nextval('work_target_revisions')
			WHERE id = $1 AND removed_at IS NULL AND state = $2 AND COALESCE(invalid_reason, '') = $3
			RETURNING `+workTargetColumns,
			id, from.State, from.InvalidReason, to.State, shadowed, nullable(to.InvalidReason), readinessJSON, audit.actor()))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		changed = true
		if action == "" {
			return nil
		}
		detail := map[string]any{"from": from.State}
		if from.InvalidReason != "" {
			detail["fromReason"] = from.InvalidReason
		}
		if to.ShadowedBy != nil {
			detail["shadowedBy"] = to.ShadowedBy
		}
		if to.InvalidReason != "" {
			detail["invalidReason"] = to.InvalidReason
		}
		for k, v := range extra {
			detail[k] = v
		}
		return auditWorkTarget(ctx, tx, action, t, audit, detail)
	})
	return changed, err
}

// SetWorkTargetReadiness records the verdict of a target's latest
// inspection. It changes no routing, so it bumps no revision.
func (s *Store) SetWorkTargetReadiness(ctx context.Context, id int64, readiness TargetReadiness) error {
	raw, err := json.Marshal(readiness)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `UPDATE work_targets SET readiness = $2 WHERE id = $1 AND removed_at IS NULL`, id, raw)
	return err
}

// AuditWorkTarget writes one runtime target event that changes no row, such
// as a refusal.
func (s *Store) AuditWorkTarget(ctx context.Context, action string, t WorkTarget, audit WorkTargetAudit, extra map[string]any) error {
	return auditWorkTarget(ctx, s.pool, action, t, audit, extra)
}

func auditWorkTarget(ctx context.Context, db execer, action string, t WorkTarget, audit WorkTargetAudit, extra map[string]any) error {
	target := map[string]any{"key": t.Key, "connection": t.Connection, "forge": t.Forge, "repository": t.Repository}
	if t.ID != 0 {
		target["id"] = t.ID
		target["repositoryId"] = t.RepositoryID
		target["branch"] = t.Branch
		target["revision"] = t.Revision
		target["state"] = t.State
	}
	detail := map[string]any{"target": target}
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

// WorkTargetStateCounts counts live runtime targets per state, for
// ploeg_work_targets{state}.
func (s *Store) WorkTargetStateCounts(ctx context.Context) (map[string]int, error) {
	out := map[string]int{WorkTargetActive: 0, WorkTargetShadowed: 0, WorkTargetInvalid: 0}
	rows, err := s.pool.Query(ctx, `SELECT state, count(*) FROM work_targets WHERE removed_at IS NULL GROUP BY state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return nil, err
		}
		out[state] = n
	}
	return out, rows.Err()
}

// OpenWorkItemsOnRepository returns the ids of the Work Items pinned to
// repository on forge that are not done or withdrawn, oldest first, at most
// limit, so a removal can name the items its target leaves behind
// (ADR-0193 section 6).
func (s *Store) OpenWorkItemsOnRepository(ctx context.Context, forge, owner, repo string, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text FROM work_items
		WHERE target_forge = $1 AND lower(target_owner) = lower($2) AND lower(target_repo) = lower($3)
		  AND state NOT IN ('done', 'withdrawn') ORDER BY id LIMIT $4`, forge, owner, repo, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
