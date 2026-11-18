package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/spendcap"
	"github.com/ploeg-hq/ploeg/pkg/team"
)

// Audit actions of runtime Teams (ADR-0198 section 9).
const (
	AuditTeamUpdated        = "team.updated"
	AuditTeamDrainStarted   = "team.drain_started"
	AuditTeamDrainCancelled = "team.drain_cancelled"
	AuditTeamDrainExpired   = "team.drain_expired"
	AuditTeamShadowed       = "team.shadowed"
	AuditTeamUnshadowed     = "team.unshadowed"
	AuditTeamInvalid        = "team.invalid"
	AuditTeamRestored       = "team.restored"
)

// MaxDrain is how long a drain lasts before its open Work Items go to a
// person (ADR-0198 section 5).
const MaxDrain = 14 * 24 * time.Hour

// NeedsHumanTeamDrainExpired is the reason a drain that ran out gives the
// Work Items it moves to needs_human.
const NeedsHumanTeamDrainExpired = "team_drain_expired"

// RuntimeTeam is one runtime Team with its manifest.
type RuntimeTeam struct {
	ID            string
	Tenant        string
	Key           string
	Aliases       []string
	Name          string
	Manifest      team.Manifest
	Revision      int64
	State         string
	ShadowedBy    string
	InvalidReason string
	DrainingSince *time.Time
	CreatedBy     string
	UpdatedBy     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	RemovedAt     *time.Time
}

// DrainDeadline is when a draining Team's drain runs out.
func (t RuntimeTeam) DrainDeadline() *time.Time {
	if t.DrainingSince == nil {
		return nil
	}
	at := t.DrainingSince.Add(MaxDrain)
	return &at
}

// TeamError is a runtime Team change the store refused, with the operator
// error code of ADR-0198 section 3.
type TeamError struct {
	Code          string
	Message       string
	ReservedUntil *time.Time
	Headroom      *float64
	Current       *RuntimeTeam
	InUse         *TeamUse
}

func (e *TeamError) Error() string { return e.Code + ": " + e.Message }

// TeamUse is what still refers to a Team that a person asked to remove.
type TeamUse struct {
	Bindings      []int64  `json:"bindings"`
	WorkItems     []int64  `json:"workItems"`
	RefinedByKeys []string `json:"refinementTeamOf,omitempty"`
}

// Refusal codes the store answers.
const (
	TeamExists          = "team_exists"
	TeamKeyTaken        = "key_taken"
	TeamKeyReserved     = "key_reserved"
	TeamConflictsStatic = "conflicts_with_static"
	TeamLimitReached    = "team_limit"
	TeamSpendExceeds    = "spend_exceeds_tenant"
	TeamStaleRevision   = "stale_revision"
	TeamDraining        = "team_draining"
	TeamInUseCode       = "team_in_use"
	TeamUnknown         = "unknown_team"
	TeamTooManyAliases  = "too_many_aliases"
	TeamAliasInUse      = "alias_in_use"
)

// TeamLimits are the bounds a runtime Team change is checked against, in
// the transaction that writes it.
type TeamLimits struct {
	Max         int
	Allowance   spendcap.Cap
	Reservation time.Duration
	Now         time.Time
}

// TeamAudit names who changed a runtime Team.
type TeamAudit struct {
	Attribution Attribution
	Actor       string
}

func (a TeamAudit) actor() string {
	return WorkTargetAudit{Attribution: a.Attribution, Actor: a.Actor}.actor()
}

func (a TeamAudit) detail(extra map[string]any) map[string]any {
	out := map[string]any{}
	if a.Attribution.Consumer != "" {
		out["consumer"] = a.Attribution.Consumer
		out["channel"] = channelOf(a.Attribution)
		out[AuditAttributionKey] = a.Attribution
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func lockTeams(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(`+teamsLockKey+`)`)
	return err
}

const runtimeTeamColumns = `t.id, t.tenant, coalesce(t.name, ''), t.manifest, t.revision, t.state, coalesce(t.shadowed_by, ''),
	coalesce(t.invalid_reason, ''), t.draining_since, t.created_by, t.updated_by, t.created_at, t.updated_at, t.removed_at,
	coalesce((SELECT k.key FROM team_keys k WHERE k.team_id = t.id AND k.kind = 'current'
		ORDER BY k.released_at DESC NULLS FIRST, k.since DESC LIMIT 1), ''),
	coalesce((SELECT array_agg(k.key ORDER BY k.key) FROM team_keys k WHERE k.team_id = t.id AND k.kind = 'alias' AND k.released_at IS NULL), '{}')`

func scanRuntimeTeam(row pgx.Row) (RuntimeTeam, error) {
	var t RuntimeTeam
	var manifest []byte
	err := row.Scan(&t.ID, &t.Tenant, &t.Name, &manifest, &t.Revision, &t.State, &t.ShadowedBy, &t.InvalidReason,
		&t.DrainingSince, &t.CreatedBy, &t.UpdatedBy, &t.CreatedAt, &t.UpdatedAt, &t.RemovedAt, &t.Key, &t.Aliases)
	if err != nil {
		return t, err
	}
	return t, json.Unmarshal(manifest, &t.Manifest)
}

// RuntimeTeams returns the live runtime Teams of every Tenant.
func (s *Store) RuntimeTeams(ctx context.Context) ([]RuntimeTeam, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+runtimeTeamColumns+` FROM teams t
		WHERE t.source = 'runtime' AND t.removed_at IS NULL ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (RuntimeTeam, error) { return scanRuntimeTeam(r) })
}

// RuntimeTeam returns one live runtime Team by id.
func (s *Store) RuntimeTeam(ctx context.Context, id string) (RuntimeTeam, error) {
	t, err := scanRuntimeTeam(s.pool.QueryRow(ctx, `SELECT `+runtimeTeamColumns+` FROM teams t
		WHERE t.id = $1 AND t.source = 'runtime' AND t.removed_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return t, &TeamError{Code: TeamUnknown, Message: "No live runtime Team has id " + id + "."}
	}
	return t, err
}

func runtimeTeamTx(ctx context.Context, tx pgx.Tx, id string) (RuntimeTeam, error) {
	t, err := scanRuntimeTeam(tx.QueryRow(ctx, `SELECT `+runtimeTeamColumns+` FROM teams t
		WHERE t.id = $1 AND t.source = 'runtime' AND t.removed_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return t, &TeamError{Code: TeamUnknown, Message: "No live runtime Team has id " + id + "."}
	}
	return t, err
}

// keyHolder says who holds key in tenant: a live static Team, a live
// runtime Team of the Tenant, or a reservation that ends at reservedUntil.
func keyHolder(ctx context.Context, tx pgx.Tx, tenant, key string, limits TeamLimits) (holderID, holderSource string, reservedBy string, reservedUntil *time.Time, err error) {
	err = tx.QueryRow(ctx, `
		SELECT k.team_id, t.source FROM team_keys k JOIN teams t ON t.id = k.team_id
		WHERE k.released_at IS NULL AND t.removed_at IS NULL AND lower(k.key) = lower($2)
		  AND (k.tenant IS NULL OR k.tenant = $1)
		ORDER BY (k.tenant IS NULL) DESC LIMIT 1`, tenant, key).Scan(&holderID, &holderSource)
	if err == nil {
		return holderID, holderSource, "", nil, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", nil, err
	}
	var released time.Time
	err = tx.QueryRow(ctx, `
		SELECT k.team_id, k.released_at FROM team_keys k
		WHERE k.tenant = $1 AND lower(k.key) = lower($2) AND k.released_at IS NOT NULL AND k.released_at > $3
		ORDER BY k.released_at DESC LIMIT 1`, tenant, key, limits.Now.Add(-limits.Reservation)).Scan(&reservedBy, &released)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", nil, nil
	}
	if err != nil {
		return "", "", "", nil, err
	}
	until := released.Add(limits.Reservation)
	return "", "", reservedBy, &until, nil
}

func checkKeyFree(ctx context.Context, tx pgx.Tx, tenant, key, self string, limits TeamLimits) error {
	holder, source, reservedBy, until, err := keyHolder(ctx, tx, tenant, key, limits)
	if err != nil {
		return err
	}
	switch {
	case holder != "" && source == team.SourceStatic:
		return &TeamError{Code: TeamConflictsStatic, Message: "A static Team in Ploeg's configuration file has key " + key + "; the file wins."}
	case holder != "" && holder != self:
		return &TeamError{Code: TeamKeyTaken, Message: "Key " + key + " is the key or an alias of another Team in this Tenant."}
	case until != nil && reservedBy != self:
		return &TeamError{Code: TeamKeyReserved, ReservedUntil: until,
			Message: fmt.Sprintf("Key %s was released and stays reserved until %s, so an old link cannot reach a new Team.", key, until.UTC().Format(time.RFC3339))}
	}
	return nil
}

func checkSpend(ctx context.Context, tx pgx.Tx, tenant, self string, cap spendcap.Cap, limits TeamLimits) error {
	if !limits.Allowance.Capped() || cap.Period != limits.Allowance.Period {
		zero := 0.0
		return &TeamError{Code: TeamSpendExceeds, Headroom: &zero,
			Message: "This Tenant has no allowance for runtime Teams in that period (teamCreation.tenants.<tenant>.spend)."}
	}
	rows, err := tx.Query(ctx, `SELECT id, manifest FROM teams WHERE source = 'runtime' AND tenant = $1 AND removed_at IS NULL`, tenant)
	if err != nil {
		return err
	}
	used := 0.0
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return err
		}
		var m team.Manifest
		if json.Unmarshal(raw, &m) == nil && id != self && m.Spend.Cap != nil {
			used += m.Spend.Cap.USD
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	headroom := limits.Allowance.USD - used
	if cap.USD > headroom+1e-9 {
		return &TeamError{Code: TeamSpendExceeds, Headroom: &headroom,
			Message: fmt.Sprintf("The Team's cap of %.2f USD per %s is over the %.2f USD the Tenant's allowance has left.", cap.USD, cap.Period, headroom)}
	}
	return nil
}

// SpendHeadroom is what a Tenant's allowance has left for runtime Team
// caps, not counting the Team self.
func (s *Store) SpendHeadroom(ctx context.Context, tenant, self string, allowance spendcap.Cap) (float64, error) {
	var headroom float64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := checkSpend(ctx, tx, tenant, self, spendcap.Cap{USD: 1e18, Period: allowance.Period}, TeamLimits{Allowance: allowance})
		var te *TeamError
		if errors.As(err, &te) && te.Headroom != nil {
			headroom = *te.Headroom
			return nil
		}
		return err
	})
	return headroom, err
}

// CreateRuntimeTeam stores a validated runtime Team in tenant, idempotently
// by key: the same key with the same body returns the live Team and
// created false; another body is team_exists. Key, limit and spend are
// checked in the transaction that writes, so two concurrent creates cannot
// both fit.
func (s *Store) CreateRuntimeTeam(ctx context.Context, tenant string, m team.Manifest, decision team.Decision, limits TeamLimits, audit TeamAudit, newID func() string) (RuntimeTeam, bool, error) {
	var out RuntimeTeam
	created := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockTeams(ctx, tx); err != nil {
			return err
		}
		holder, source, _, _, err := keyHolder(ctx, tx, tenant, m.Key, limits)
		if err != nil {
			return err
		}
		if holder != "" && source == team.SourceRuntime {
			current, err := runtimeTeamTx(ctx, tx, holder)
			if err != nil {
				return err
			}
			if current.Key == m.Key && team.SameBody(current.Manifest, m) {
				out = current
				return nil
			}
			if current.Key == m.Key {
				return &TeamError{Code: TeamExists, Current: &current, Message: "A runtime Team with key " + m.Key + " exists with another body; change it with PUT and its revision."}
			}
		}
		if err := checkKeyFree(ctx, tx, tenant, m.Key, "", limits); err != nil {
			return err
		}
		var live int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM teams WHERE source = 'runtime' AND tenant = $1 AND removed_at IS NULL`, tenant).Scan(&live); err != nil {
			return err
		}
		if live >= limits.Max {
			return &TeamError{Code: TeamLimitReached, Message: fmt.Sprintf("This Tenant holds %d live runtime Teams, its limit (teamCreation.max).", live)}
		}
		if err := checkSpend(ctx, tx, tenant, "", *m.Spend.Cap, limits); err != nil {
			return err
		}
		raw, err := json.Marshal(m)
		if err != nil {
			return err
		}
		id := newID()
		if _, err := tx.Exec(ctx, `INSERT INTO teams (id, tenant, source, name, manifest, state, shadowed_by, invalid_reason, created_by, updated_by)
			VALUES ($1, $2, 'runtime', $3, $4, $5, nullif($6, ''), nullif($7, ''), $8, $8)`,
			id, tenant, m.Name, raw, decision.State, decision.ShadowedBy, decision.InvalidReason, audit.actor()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO team_keys (team_id, tenant, key, kind) VALUES ($1, $2, $3, 'current')`, id, tenant, m.Key); err != nil {
			return err
		}
		if out, err = runtimeTeamTx(ctx, tx, id); err != nil {
			return err
		}
		created = true
		return auditTeamAs(ctx, tx, audit.actor(), AuditTeamCreated, id, m.Key, audit.detail(map[string]any{"revision": out.Revision, "state": out.State}))
	})
	return out, created, err
}

// ReplaceRuntimeTeam replaces a runtime Team's manifest at revision
// ifMatch. A changed key is a rename: the old key becomes an alias.
func (s *Store) ReplaceRuntimeTeam(ctx context.Context, id string, ifMatch int64, m team.Manifest, decision team.Decision, limits TeamLimits, audit TeamAudit) (RuntimeTeam, error) {
	var out RuntimeTeam
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockTeams(ctx, tx); err != nil {
			return err
		}
		current, err := runtimeTeamTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Revision != ifMatch {
			return &TeamError{Code: TeamStaleRevision, Current: &current, Message: "The Team changed since the revision If-Match names."}
		}
		if current.DrainingSince != nil {
			return &TeamError{Code: TeamDraining, Current: &current, Message: "The Team is draining; cancel the drain before changing it."}
		}
		renamed := current.Key != m.Key
		if renamed {
			if err := checkKeyFree(ctx, tx, current.Tenant, m.Key, id, limits); err != nil {
				return err
			}
			becomesAlias := !slices.ContainsFunc(current.Aliases, func(a string) bool { return strings.EqualFold(a, m.Key) })
			if becomesAlias && len(current.Aliases) >= team.MaxAliases {
				return &TeamError{Code: TeamTooManyAliases, Current: &current, Message: fmt.Sprintf("The Team already holds %d aliases; release one before renaming it.", team.MaxAliases)}
			}
			if _, err := tx.Exec(ctx, `UPDATE team_keys SET released_at = now()
				WHERE team_id = $1 AND released_at IS NULL AND (kind = 'current' OR lower(key) = lower($2))`, id, m.Key); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO team_keys (team_id, tenant, key, kind) VALUES ($1, $2, $3, 'current'), ($1, $2, $4, 'alias')`,
				id, current.Tenant, m.Key, current.Key); err != nil {
				return err
			}
		}
		if err := checkSpend(ctx, tx, current.Tenant, id, *m.Spend.Cap, limits); err != nil {
			return err
		}
		raw, err := json.Marshal(m)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE teams SET manifest = $2, name = $3, state = $4, shadowed_by = nullif($5, ''),
			invalid_reason = nullif($6, ''), updated_by = $7, updated_at = now(), revision = nextval('team_revisions') WHERE id = $1`,
			id, raw, m.Name, decision.State, decision.ShadowedBy, decision.InvalidReason, audit.actor()); err != nil {
			return err
		}
		if out, err = runtimeTeamTx(ctx, tx, id); err != nil {
			return err
		}
		if renamed {
			if err := auditTeamAs(ctx, tx, audit.actor(), AuditTeamRenamed, id, m.Key, audit.detail(map[string]any{"from": current.Key, "to": m.Key})); err != nil {
				return err
			}
		}
		return auditTeamAs(ctx, tx, audit.actor(), AuditTeamUpdated, id, m.Key, audit.detail(map[string]any{"fromRevision": current.Revision, "revision": out.Revision}))
	})
	return out, err
}

// RenameRuntimeTeam changes only a runtime Team's display name.
func (s *Store) RenameRuntimeTeam(ctx context.Context, id string, ifMatch int64, name string, audit TeamAudit) (RuntimeTeam, error) {
	var out RuntimeTeam
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockTeams(ctx, tx); err != nil {
			return err
		}
		current, err := runtimeTeamTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Revision != ifMatch {
			return &TeamError{Code: TeamStaleRevision, Current: &current, Message: "The Team changed since the revision If-Match names."}
		}
		m := current.Manifest
		m.Name = name
		raw, err := json.Marshal(m)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE teams SET manifest = $2, name = $3, updated_by = $4, updated_at = now(),
			revision = nextval('team_revisions') WHERE id = $1`, id, raw, name, audit.actor()); err != nil {
			return err
		}
		if out, err = runtimeTeamTx(ctx, tx, id); err != nil {
			return err
		}
		return auditTeamAs(ctx, tx, audit.actor(), AuditTeamNameChanged, id, current.Key, audit.detail(map[string]any{"from": current.Name, "to": name}))
	})
	return out, err
}

func teamUse(ctx context.Context, tx pgx.Tx, id string) (TeamUse, error) {
	use := TeamUse{Bindings: []int64{}, WorkItems: []int64{}}
	rows, err := tx.Query(ctx, `SELECT id FROM tracker_bindings WHERE team_id = $1 AND removed_at IS NULL ORDER BY id LIMIT 50`, id)
	if err != nil {
		return use, err
	}
	if use.Bindings, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil {
		return use, err
	}
	rows, err = tx.Query(ctx, `SELECT id FROM work_items WHERE team_id = $1 AND state NOT IN ('done', 'withdrawn') ORDER BY id LIMIT 50`, id)
	if err != nil {
		return use, err
	}
	use.WorkItems, err = pgx.CollectRows(rows, pgx.RowTo[int64])
	return use, err
}

func removeRuntimeTeamTx(ctx context.Context, tx pgx.Tx, t RuntimeTeam, audit TeamAudit, extra map[string]any) error {
	if _, err := tx.Exec(ctx, `UPDATE teams SET removed_at = now(), removed_by = $2, updated_at = now(),
		revision = nextval('team_revisions') WHERE id = $1`, t.ID, audit.actor()); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE team_keys SET released_at = now() WHERE team_id = $1 AND released_at IS NULL`, t.ID); err != nil {
		return err
	}
	return auditTeamAs(ctx, tx, audit.actor(), AuditTeamRemoved, t.ID, t.Key, audit.detail(extra))
}

// RemoveRuntimeTeam removes a runtime Team that nothing refers to; refined
// lists the Teams that name it as refinementTeam. A Team already removed is
// no error.
func (s *Store) RemoveRuntimeTeam(ctx context.Context, id string, refined []string, audit TeamAudit) (bool, error) {
	removed := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockTeams(ctx, tx); err != nil {
			return err
		}
		current, err := runtimeTeamTx(ctx, tx, id)
		var te *TeamError
		if errors.As(err, &te) && te.Code == TeamUnknown {
			return nil
		}
		if err != nil {
			return err
		}
		use, err := teamUse(ctx, tx, id)
		if err != nil {
			return err
		}
		use.RefinedByKeys = refined
		if len(use.Bindings)+len(use.WorkItems)+len(refined) > 0 {
			return &TeamError{Code: TeamInUseCode, InUse: &use, Current: &current, Message: "Bindings, open Work Items or another Team's refinementTeam still refer to this Team."}
		}
		removed = true
		return removeRuntimeTeamTx(ctx, tx, current, audit, nil)
	})
	return removed, err
}

// DrainRuntimeTeam starts a drain: no live binding may name the Team; new
// admissions are refused from now and the Team is removed when its last
// open Work Item ends, or after MaxDrain.
func (s *Store) DrainRuntimeTeam(ctx context.Context, id string, refined []string, audit TeamAudit) (RuntimeTeam, error) {
	var out RuntimeTeam
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockTeams(ctx, tx); err != nil {
			return err
		}
		current, err := runtimeTeamTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.DrainingSince != nil {
			out = current
			return nil
		}
		use, err := teamUse(ctx, tx, id)
		if err != nil {
			return err
		}
		if len(use.Bindings) > 0 || len(refined) > 0 {
			use.RefinedByKeys = refined
			use.WorkItems = []int64{}
			return &TeamError{Code: TeamInUseCode, InUse: &use, Current: &current, Message: "A drain needs that no live binding and no other Team's refinementTeam names the Team."}
		}
		if len(use.WorkItems) == 0 {
			out = current
			return removeRuntimeTeamTx(ctx, tx, current, audit, map[string]any{"drained": true})
		}
		if _, err := tx.Exec(ctx, `UPDATE teams SET draining_since = now(), updated_by = $2, updated_at = now(),
			revision = nextval('team_revisions') WHERE id = $1`, id, audit.actor()); err != nil {
			return err
		}
		if out, err = runtimeTeamTx(ctx, tx, id); err != nil {
			return err
		}
		return auditTeamAs(ctx, tx, audit.actor(), AuditTeamDrainStarted, id, current.Key, audit.detail(map[string]any{"drainDeadline": out.DrainDeadline()}))
	})
	return out, err
}

// UndrainRuntimeTeam cancels a drain.
func (s *Store) UndrainRuntimeTeam(ctx context.Context, id string, audit TeamAudit) (RuntimeTeam, error) {
	var out RuntimeTeam
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockTeams(ctx, tx); err != nil {
			return err
		}
		current, err := runtimeTeamTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.DrainingSince == nil {
			out = current
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE teams SET draining_since = NULL, updated_by = $2, updated_at = now(),
			revision = nextval('team_revisions') WHERE id = $1`, id, audit.actor()); err != nil {
			return err
		}
		if out, err = runtimeTeamTx(ctx, tx, id); err != nil {
			return err
		}
		return auditTeamAs(ctx, tx, audit.actor(), AuditTeamDrainCancelled, id, current.Key, audit.detail(nil))
	})
	return out, err
}

// FinishDrains removes every draining Team whose open Work Items ended, and
// at a drain's deadline moves its open Work Items to needs_human with
// team_drain_expired and removes it (ADR-0198 section 5).
func (s *Store) FinishDrains(ctx context.Context, now time.Time) (removed []string, err error) {
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		removed = nil
		if err := lockTeams(ctx, tx); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+runtimeTeamColumns+` FROM teams t
			WHERE t.source = 'runtime' AND t.removed_at IS NULL AND t.draining_since IS NOT NULL`)
		if err != nil {
			return err
		}
		draining, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (RuntimeTeam, error) { return scanRuntimeTeam(r) })
		if err != nil {
			return err
		}
		sweep := TeamAudit{Actor: "ploegd"}
		for _, t := range draining {
			use, err := teamUse(ctx, tx, t.ID)
			if err != nil {
				return err
			}
			if len(use.WorkItems) > 0 && now.Before(*t.DrainDeadline()) {
				continue
			}
			if len(use.WorkItems) > 0 {
				moved, err := expireDrain(ctx, tx, t)
				if err != nil {
					return err
				}
				if err := auditTeamAs(ctx, tx, sweep.actor(), AuditTeamDrainExpired, t.ID, t.Key, map[string]any{"workItems": moved}); err != nil {
					return err
				}
			}
			if err := removeRuntimeTeamTx(ctx, tx, t, sweep, map[string]any{"drained": true}); err != nil {
				return err
			}
			removed = append(removed, t.ID)
		}
		return nil
	})
	return removed, err
}

func expireDrain(ctx context.Context, tx pgx.Tx, t RuntimeTeam) ([]int64, error) {
	rows, err := tx.Query(ctx, `
		UPDATE work_items SET state = 'needs_human', updated_at = now()
		WHERE team_id = $1 AND state NOT IN ('done', 'withdrawn', 'needs_human') RETURNING id`, t.ID)
	if err != nil {
		return nil, err
	}
	moved, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_runs SET state = 'finished', finished_at = now(), summary = $2
		WHERE team_id = $1 AND state = 'pending'`, t.ID, "cancelled: the Team's drain ran out ("+NeedsHumanTeamDrainExpired+")"); err != nil {
		return nil, err
	}
	for _, id := range moved {
		if err := audit(ctx, tx, "team:"+t.Key, "work_item.needs_human", &id, map[string]any{
			"reason": NeedsHumanTeamDrainExpired, "team_id": t.ID,
			"message": "The Team's drain ran out after 14 days; a person decides what happens to this Work Item.",
		}); err != nil {
			return nil, err
		}
	}
	return moved, nil
}

// ReleaseTeamAlias releases one alias of a runtime Team; it stays reserved
// for the reservation period. refined says whether a static reference still
// names it.
func (s *Store) ReleaseTeamAlias(ctx context.Context, id, alias string, inUse bool, audit TeamAudit) (RuntimeTeam, error) {
	var out RuntimeTeam
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockTeams(ctx, tx); err != nil {
			return err
		}
		current, err := runtimeTeamTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if inUse {
			return &TeamError{Code: TeamAliasInUse, Current: &current, Message: "A static reference still names alias " + alias + "."}
		}
		tag, err := tx.Exec(ctx, `UPDATE team_keys SET released_at = now()
			WHERE team_id = $1 AND kind = 'alias' AND lower(key) = lower($2) AND released_at IS NULL`, id, alias)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			out = current
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE teams SET updated_at = now(), revision = nextval('team_revisions') WHERE id = $1`, id); err != nil {
			return err
		}
		if out, err = runtimeTeamTx(ctx, tx, id); err != nil {
			return err
		}
		return auditTeamAs(ctx, tx, audit.actor(), AuditTeamAliasReleased, id, current.Key, audit.detail(map[string]any{"alias": alias}))
	})
	return out, err
}

// ApplyTeamDecision sets a runtime Team's state and audits the transition;
// it reports whether the state changed. A static key equal to one of the
// Team's aliases releases that alias.
func (s *Store) ApplyTeamDecision(ctx context.Context, id string, d team.Decision, staticKeys map[string]string) (bool, error) {
	changed := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockTeams(ctx, tx); err != nil {
			return err
		}
		current, err := runtimeTeamTx(ctx, tx, id)
		if err != nil {
			return err
		}
		for _, alias := range current.Aliases {
			if _, static := staticKeys[strings.ToLower(alias)]; !static {
				continue
			}
			if _, err := tx.Exec(ctx, `UPDATE team_keys SET released_at = now()
				WHERE team_id = $1 AND kind = 'alias' AND lower(key) = lower($2) AND released_at IS NULL`, id, alias); err != nil {
				return err
			}
			if err := auditTeam(ctx, tx, AuditTeamAliasReleased, id, current.Key, map[string]any{"alias": alias, "reason": "static_key"}); err != nil {
				return err
			}
		}
		if current.State == d.State && current.ShadowedBy == d.ShadowedBy && current.InvalidReason == d.InvalidReason {
			return nil
		}
		changed = true
		if _, err := tx.Exec(ctx, `UPDATE teams SET state = $2, shadowed_by = nullif($3, ''), invalid_reason = nullif($4, ''),
			updated_at = now(), revision = nextval('team_revisions') WHERE id = $1`, id, d.State, d.ShadowedBy, d.InvalidReason); err != nil {
			return err
		}
		action := AuditTeamRestored
		switch {
		case d.State == team.StateShadowed:
			action = AuditTeamShadowed
		case d.State == team.StateInvalid:
			action = AuditTeamInvalid
		case current.State == team.StateShadowed:
			action = AuditTeamUnshadowed
		}
		return auditTeam(ctx, tx, action, id, current.Key, map[string]any{"from": current.State, "to": d.State,
			"shadowedBy": d.ShadowedBy, "invalidReason": d.InvalidReason})
	})
	return changed, err
}

// ReservedKey reports until when a released key stays reserved in tenant;
// nil when it is not.
func (s *Store) ReservedKey(ctx context.Context, tenant, key string, limits TeamLimits) (*time.Time, error) {
	var until *time.Time
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		holder, _, _, reserved, err := keyHolder(ctx, tx, tenant, key, limits)
		if holder == "" {
			until = reserved
		}
		return err
	})
	return until, err
}

// PoolRun is the next Run a runtime Team pool may claim: its Team key and
// Role.
type PoolRun struct {
	TeamID string
	Team   string
	Role   string
}

// NextPoolRun selects the oldest pending Run of an active runtime Team of
// tenant on harness that is below its maxRunning (ADR-0198 section 7). It
// never selects a static Team's Run.
func (s *Store) NextPoolRun(ctx context.Context, tenant, harness string) (PoolRun, error) {
	var out PoolRun
	err := s.pool.QueryRow(ctx, poolClaimableQuery+`
		ORDER BY r.id LIMIT 1`, tenant, harness).Scan(&out.TeamID, &out.Team, &out.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNoWork
	}
	return out, err
}

// PoolScalerQuery is the count the chart's pool ScaledJob scales on: the
// Runs a pool claim could select, before maxRunning.
const PoolScalerQuery = `SELECT COUNT(*) FROM agent_runs r JOIN teams t ON t.id = r.team_id
WHERE t.source = 'runtime' AND t.state = 'active' AND t.removed_at IS NULL AND t.tenant = $1
  AND r.state = 'pending' AND r.harness_requested = $2 AND r.carrier IS NULL`

const poolClaimableQuery = `
	SELECT r.team_id, r.team, r.role FROM agent_runs r JOIN teams t ON t.id = r.team_id
	WHERE t.source = 'runtime' AND t.state = 'active' AND t.removed_at IS NULL AND t.tenant = $1
	  AND r.state = 'pending' AND r.harness_requested = $2 AND r.carrier IS NULL
	  AND (coalesce((t.manifest->>'maxRunning')::int, 0) = 0 OR (
		SELECT count(*) FROM agent_runs x WHERE x.team_id = t.id AND x.state = 'running')
		< (t.manifest->>'maxRunning')::int)`

// RuntimeTeamHasKey reports whether key is a live key of a runtime Team.
func (s *Store) RuntimeTeamHasKey(ctx context.Context, key string) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM team_keys k JOIN teams t ON t.id = k.team_id
		WHERE t.source = 'runtime' AND t.removed_at IS NULL AND k.released_at IS NULL AND lower(k.key) = lower($1)`, key).Scan(&n)
	return n > 0, err
}
