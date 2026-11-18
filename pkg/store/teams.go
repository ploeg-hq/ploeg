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

	"github.com/ploeg-hq/ploeg/pkg/team"
)

// Audit actions of Teams (ADR-0198 section 9).
const (
	AuditTeamCreated       = "team.created"
	AuditTeamRenamed       = "team.renamed"
	AuditTeamNameChanged   = "team.name_changed"
	AuditTeamRemoved       = "team.removed"
	AuditTeamRefused       = "team.refused"
	AuditTeamAliasReleased = "team.alias_released"
	// AuditTeamRenameSuspected is written when a static key leaves the
	// file in the boot a new key with the same configuration appears.
	AuditTeamRenameSuspected = "team.rename_suspected"
)

// StaticTeam is one Team of the configuration file as the boot reconciler
// sees it.
type StaticTeam struct {
	Key     string
	Aliases []string
	Name    string
	Digest  string
}

// TeamRecord is one Team row with its live keys.
type TeamRecord struct {
	ID        string
	Tenant    string
	Source    string
	Key       string
	Aliases   []string
	Name      string
	Revision  int64
	State     string
	RemovedAt *time.Time
	Digest    string
	Draining  bool
}

// SuspectedRename is a static key that left the file in the same boot as
// a new key with the same configuration appeared: most likely a rename
// whose old key was not listed under aliases, so the Team lost its id.
type SuspectedRename struct {
	From   string
	FromID string
	To     string
	ToID   string
}

// TeamReconciliation says what the boot reconciler changed.
type TeamReconciliation struct {
	Created          []string
	Renamed          []string
	Removed          []string
	SuspectedRenames []SuspectedRename
}

const teamsLockKey = "hashtext('ploeg_teams')"

type staticTeamMatch struct {
	want     StaticTeam
	row      TeamRecord
	found    bool
	returned bool
}

// ReconcileStaticTeams gives every static Team of the file a row and an id
// (ADR-0198 section 6): a Team found by its key or one of its aliases keeps
// its id; a key never seen gets a new one from newID; a static row whose
// key and aliases left the file is removed. Changes are audited as made by
// the configuration. Every key or alias that leaves a Team is released
// before any is taken, so a key may move between Teams in one boot.
func (s *Store) ReconcileStaticTeams(ctx context.Context, teams []StaticTeam, newID func() string) (TeamReconciliation, error) {
	var out TeamReconciliation
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		out = TeamReconciliation{}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(`+teamsLockKey+`)`); err != nil {
			return err
		}
		live, err := staticTeamRows(ctx, tx, false)
		if err != nil {
			return err
		}
		removed, err := staticTeamRows(ctx, tx, true)
		if err != nil {
			return err
		}
		plan, claimed := planStaticTeams(teams, live, removed)
		var gone []TeamRecord
		for _, row := range live {
			if claimed[row.ID] {
				continue
			}
			if err := removeStaticTeam(ctx, tx, row); err != nil {
				return err
			}
			gone = append(gone, row)
			out.Removed = append(out.Removed, row.Key)
		}
		for _, m := range plan {
			if m.found && !m.returned {
				if err := releaseDepartingKeys(ctx, tx, m.row, m.want); err != nil {
					return err
				}
			}
		}
		took := false
		created := map[string]string{}
		for _, m := range plan {
			if !m.found {
				id := newID()
				if err := insertStaticTeam(ctx, tx, id, m.want); err != nil {
					return err
				}
				out.Created = append(out.Created, m.want.Key)
				created[m.want.Key] = id
				took = true
				continue
			}
			gained, err := syncStaticTeam(ctx, tx, m.row, m.want, m.returned, &out)
			if err != nil {
				return err
			}
			took = took || gained
		}
		if err := flagSuspectedRenames(ctx, tx, plan, gone, created, &out); err != nil {
			return err
		}
		if took {
			if _, err := tx.Exec(ctx, `DELETE FROM team_id_backfills`); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

func planStaticTeams(teams []StaticTeam, live, removed []TeamRecord) ([]staticTeamMatch, map[string]bool) {
	sorted := slices.Clone(teams)
	slices.SortFunc(sorted, func(a, b StaticTeam) int { return strings.Compare(a.Key, b.Key) })
	plan := make([]staticTeamMatch, len(sorted))
	claimed := map[string]bool{}
	claim := func(i int, row TeamRecord, returned bool) {
		plan[i].row, plan[i].found, plan[i].returned = row, true, returned
		claimed[row.ID] = true
	}
	for i, want := range sorted {
		plan[i].want = want
		if row, ok := teamHolding(live, []string{want.Key}, false, claimed); ok {
			claim(i, row, false)
		}
	}
	for i, m := range plan {
		if m.found {
			continue
		}
		if row, ok := teamHolding(live, append([]string{m.want.Key}, m.want.Aliases...), true, claimed); ok {
			claim(i, row, false)
		}
	}
	for i, m := range plan {
		if m.found {
			continue
		}
		if row, ok := teamHolding(removed, []string{m.want.Key}, false, claimed); ok {
			claim(i, row, true)
		}
	}
	return plan, claimed
}

func teamHolding(rows []TeamRecord, refs []string, withAliases bool, claimed map[string]bool) (TeamRecord, bool) {
	for _, row := range rows {
		if claimed[row.ID] {
			continue
		}
		held := []string{row.Key}
		if withAliases {
			held = append(held, row.Aliases...)
		}
		for _, ref := range refs {
			if slices.ContainsFunc(held, func(h string) bool { return strings.EqualFold(h, ref) }) {
				return row, true
			}
		}
	}
	return TeamRecord{}, false
}

func removeStaticTeam(ctx context.Context, tx pgx.Tx, row TeamRecord) error {
	if _, err := tx.Exec(ctx, `UPDATE teams SET removed_at = now(), removed_by = $2, updated_at = now(),
		revision = nextval('team_revisions') WHERE id = $1`, row.ID, team.ConfigurationActor); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE team_keys SET released_at = now() WHERE team_id = $1 AND released_at IS NULL`, row.ID); err != nil {
		return err
	}
	return auditTeam(ctx, tx, AuditTeamRemoved, row.ID, row.Key, nil)
}

func releaseDepartingKeys(ctx context.Context, tx pgx.Tx, row TeamRecord, want StaticTeam) error {
	if row.Key != want.Key {
		_, err := tx.Exec(ctx, `UPDATE team_keys SET released_at = now() WHERE team_id = $1 AND released_at IS NULL`, row.ID)
		return err
	}
	for _, held := range row.Aliases {
		if slices.ContainsFunc(want.Aliases, func(a string) bool { return strings.EqualFold(a, held) }) {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE team_keys SET released_at = now()
			WHERE team_id = $1 AND kind = 'alias' AND lower(key) = lower($2) AND released_at IS NULL`, row.ID, held); err != nil {
			return err
		}
		if err := auditTeam(ctx, tx, AuditTeamAliasReleased, row.ID, row.Key, map[string]any{"alias": held}); err != nil {
			return err
		}
	}
	return nil
}

func insertStaticTeam(ctx context.Context, tx pgx.Tx, id string, want StaticTeam) error {
	if _, err := tx.Exec(ctx, `INSERT INTO teams (id, source, name, config_digest, created_by, updated_by)
		VALUES ($1, 'static', nullif($2, ''), nullif($3, ''), $4, $4)`, id, want.Name, want.Digest, team.ConfigurationActor); err != nil {
		return err
	}
	if err := insertTeamKeys(ctx, tx, id, want.Key, want.Aliases); err != nil {
		return err
	}
	return auditTeam(ctx, tx, AuditTeamCreated, id, want.Key, nil)
}

func insertTeamKeys(ctx context.Context, tx pgx.Tx, id, key string, aliases []string) error {
	if err := insertTeamKey(ctx, tx, id, key, "current"); err != nil {
		return err
	}
	for _, alias := range aliases {
		if err := insertTeamKey(ctx, tx, id, alias, "alias"); err != nil {
			return err
		}
	}
	return nil
}

func insertTeamKey(ctx context.Context, tx pgx.Tx, id, key, kind string) error {
	_, err := tx.Exec(ctx, `INSERT INTO team_keys (team_id, key, kind) VALUES ($1, $2, $3)`, id, key, kind)
	if err != nil {
		return fmt.Errorf("team %s key %q: %w", id, key, err)
	}
	return nil
}

// syncStaticTeam brings a found Team's keys, name and digest to the file's
// and says whether the Team took a key it did not hold.
func syncStaticTeam(ctx context.Context, tx pgx.Tx, row TeamRecord, want StaticTeam, returned bool, out *TeamReconciliation) (bool, error) {
	if row.Digest != want.Digest {
		if _, err := tx.Exec(ctx, `UPDATE teams SET config_digest = nullif($2, '') WHERE id = $1`, row.ID, want.Digest); err != nil {
			return false, err
		}
	}
	if returned {
		if _, err := tx.Exec(ctx, `UPDATE teams SET removed_at = NULL, removed_by = NULL, updated_at = now(),
			revision = nextval('team_revisions') WHERE id = $1`, row.ID); err != nil {
			return false, err
		}
		if err := insertTeamKeys(ctx, tx, row.ID, want.Key, want.Aliases); err != nil {
			return false, err
		}
		out.Created = append(out.Created, want.Key)
		return true, auditTeam(ctx, tx, AuditTeamCreated, row.ID, want.Key, map[string]any{"returned": true})
	}
	gained := false
	if row.Key != want.Key {
		if err := insertTeamKeys(ctx, tx, row.ID, want.Key, want.Aliases); err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `UPDATE teams SET updated_at = now(), revision = nextval('team_revisions') WHERE id = $1`, row.ID); err != nil {
			return false, err
		}
		out.Renamed = append(out.Renamed, row.Key+"→"+want.Key)
		if err := auditTeam(ctx, tx, AuditTeamRenamed, row.ID, want.Key, map[string]any{"from": row.Key, "to": want.Key}); err != nil {
			return false, err
		}
		gained = true
	} else {
		for _, alias := range want.Aliases {
			if slices.ContainsFunc(row.Aliases, func(h string) bool { return strings.EqualFold(h, alias) }) {
				continue
			}
			if err := insertTeamKey(ctx, tx, row.ID, alias, "alias"); err != nil {
				return false, err
			}
			gained = true
		}
	}
	if row.Name != want.Name {
		if _, err := tx.Exec(ctx, `UPDATE teams SET name = nullif($2, ''), updated_at = now(),
			revision = nextval('team_revisions') WHERE id = $1`, row.ID, want.Name); err != nil {
			return false, err
		}
		return gained, auditTeam(ctx, tx, AuditTeamNameChanged, row.ID, want.Key, map[string]any{"from": row.Name, "to": want.Name})
	}
	return gained, nil
}

// flagSuspectedRenames pairs each Team removed in this boot with a Team
// created in it from the same configuration, and audits each pair as
// team.rename_suspected.
func flagSuspectedRenames(ctx context.Context, tx pgx.Tx, plan []staticTeamMatch, gone []TeamRecord, created map[string]string, out *TeamReconciliation) error {
	paired := map[string]bool{}
	for _, m := range plan {
		id, ok := created[m.want.Key]
		if !ok || m.want.Digest == "" {
			continue
		}
		for _, row := range gone {
			if paired[row.ID] || row.Digest != m.want.Digest {
				continue
			}
			paired[row.ID] = true
			out.SuspectedRenames = append(out.SuspectedRenames, SuspectedRename{From: row.Key, FromID: row.ID, To: m.want.Key, ToID: id})
			if err := auditTeam(ctx, tx, AuditTeamRenameSuspected, id, m.want.Key, map[string]any{"from": row.Key, "from_team_id": row.ID}); err != nil {
				return err
			}
			break
		}
	}
	return nil
}

func staticTeamRows(ctx context.Context, tx pgx.Tx, removed bool) ([]TeamRecord, error) {
	rows, err := tx.Query(ctx, `
		SELECT t.id, coalesce(t.name, ''), t.revision, t.state, t.removed_at, coalesce(t.config_digest, ''),
			coalesce((SELECT k.key FROM team_keys k WHERE k.team_id = t.id AND k.kind = 'current'
				AND (k.released_at IS NULL OR $1) ORDER BY k.since DESC, k.released_at DESC NULLS FIRST LIMIT 1), ''),
			coalesce((SELECT array_agg(k.key ORDER BY k.key) FROM team_keys k WHERE k.team_id = t.id AND k.kind = 'alias'
				AND k.released_at IS NULL), '{}')
		FROM teams t
		WHERE t.source = 'static' AND (t.removed_at IS NOT NULL) = $1
		ORDER BY t.removed_at DESC NULLS FIRST, t.created_at`, removed)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (TeamRecord, error) {
		rec := TeamRecord{Source: team.SourceStatic}
		err := r.Scan(&rec.ID, &rec.Name, &rec.Revision, &rec.State, &rec.RemovedAt, &rec.Digest, &rec.Key, &rec.Aliases)
		return rec, err
	})
}

// LiveTeams returns every Team that is not removed, with its current key
// and live aliases.
func (s *Store) LiveTeams(ctx context.Context) ([]TeamRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.id, coalesce(t.tenant, ''), t.source, coalesce(t.name, ''), t.revision, t.state,
			coalesce((SELECT k.key FROM team_keys k WHERE k.team_id = t.id AND k.kind = 'current' AND k.released_at IS NULL), ''),
			coalesce((SELECT array_agg(k.key ORDER BY k.key) FROM team_keys k WHERE k.team_id = t.id AND k.kind = 'alias'
				AND k.released_at IS NULL), '{}'), t.draining_since IS NOT NULL
		FROM teams t WHERE t.removed_at IS NULL ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (TeamRecord, error) {
		var rec TeamRecord
		err := r.Scan(&rec.ID, &rec.Tenant, &rec.Source, &rec.Name, &rec.Revision, &rec.State, &rec.Key, &rec.Aliases, &rec.Draining)
		return rec, err
	})
}

// AuditTeamRefusal records a refused Team change with its error code, so
// repeated probing is visible (ADR-0198 section 9).
func (s *Store) AuditTeamRefusal(ctx context.Context, a Attribution, id, key, code string, extra map[string]any) error {
	detail := map[string]any{"code": code}
	if a.Consumer != "" {
		detail["consumer"] = a.Consumer
		detail["channel"] = channelOf(a)
		detail[AuditAttributionKey] = a
	}
	for k, v := range extra {
		detail[k] = v
	}
	actor := WorkTargetAudit{Attribution: a}.actor()
	return auditTeamAs(ctx, s.pool, actor, AuditTeamRefused, id, key, detail)
}

func auditTeam(ctx context.Context, db execer, action, id, key string, extra map[string]any) error {
	return auditTeamAs(ctx, db, team.ConfigurationActor, action, id, key, extra)
}

func auditTeamAs(ctx context.Context, db execer, actor, action, id, key string, extra map[string]any) error {
	detail := map[string]any{"team_id": id, "team": key}
	for k, v := range extra {
		detail[k] = v
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `INSERT INTO audit_log (actor, action, detail) VALUES ($1, $2, $3)`, actor, action, raw)
	return err
}

// teamTables are the tables that name a Team, with the column that orders
// a batched backfill and the expression of a row's tenant.
var teamTables = []struct{ table, key, tenant string }{
	{"work_items", "id", "NULL"},
	{"shifts", "id", "NULL"},
	{"agent_runs", "id", "NULL"},
	{"leases", "work_item_id", "NULL"},
	{"chats", "run_id", "NULL"},
	{"work_item_premise_checks", "work_item_id", "NULL"},
	{"tracker_bindings", "id", "x.tenant"},
}

const teamBackfillLockKey = "hashtext('ploeg_team_id_backfill')"

// ErrTeamBackfillElsewhere says another ploegd holds the backfill.
var ErrTeamBackfillElsewhere = errors.New("another ploegd is backfilling Team ids")

// BackfillTeamIDs sets team_id from the static key in team on every row
// that has none, batch rows per statement so a large table is never locked
// whole (ADR-0198 section 6). One ploegd backfills at a time: the others
// get ErrTeamBackfillElsewhere. Each batch locks its rows in key order and
// skips rows live dispatch holds. A table whose pass skipped none is marked
// complete and not scanned again until reconciliation gives a key a Team.
// It returns the rows it set.
func (s *Store) BackfillTeamIDs(ctx context.Context, batch int) (total int64, err error) {
	if batch <= 0 {
		batch = 10000
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	var held bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(`+teamBackfillLockKey+`)`).Scan(&held); err != nil {
		conn.Release()
		return 0, err
	}
	if !held {
		conn.Release()
		return 0, ErrTeamBackfillElsewhere
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if _, unlockErr := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(`+teamBackfillLockKey+`)`); unlockErr != nil {
			_ = conn.Hijack().Close(unlockCtx)
			return
		}
		conn.Release()
	}()
	for _, t := range teamTables {
		var done bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM team_id_backfills WHERE table_name = $1)`, t.table).Scan(&done); err != nil {
			return total, err
		}
		if done {
			continue
		}
		set, skipped, err := backfillTeamTable(ctx, conn.Conn(), t.table, t.key, t.tenant, batch)
		total += set
		if err != nil {
			return total, fmt.Errorf("backfill %s: %w", t.table, err)
		}
		if skipped {
			continue
		}
		if _, err := conn.Exec(ctx, `INSERT INTO team_id_backfills (table_name) VALUES ($1) ON CONFLICT (table_name) DO NOTHING`, t.table); err != nil {
			return total, err
		}
	}
	return total, nil
}

func backfillTeamTable(ctx context.Context, conn *pgx.Conn, table, key, tenant string, batch int) (int64, bool, error) {
	var total int64
	skipped := false
	after := int64(-1 << 63)
	for {
		var last *int64
		var set, missing int64
		err := conn.QueryRow(ctx, fmt.Sprintf(`
			WITH chunk AS (
				SELECT %[2]s AS k FROM %[1]s WHERE %[2]s > $1 ORDER BY %[2]s LIMIT $2
			), wanted AS (
				SELECT x.%[2]s AS k FROM %[1]s x JOIN chunk ON x.%[2]s = chunk.k
				WHERE x.team_id IS NULL AND ploeg_static_team_id(x.team, %[3]s) IS NOT NULL
			), locked AS (
				SELECT x.%[2]s AS k FROM %[1]s x
				WHERE x.%[2]s IN (SELECT k FROM wanted)
				ORDER BY x.%[2]s FOR UPDATE SKIP LOCKED
			), filled AS (
				UPDATE %[1]s x SET team_id = ploeg_static_team_id(x.team, %[3]s)
				FROM locked WHERE x.%[2]s = locked.k AND x.team_id IS NULL
				RETURNING 1
			)
			SELECT (SELECT max(k) FROM chunk), (SELECT count(*) FROM filled),
				(SELECT count(*) FROM wanted) - (SELECT count(*) FROM locked)`, table, key, tenant), after, batch).Scan(&last, &set, &missing)
		if err != nil {
			return total, skipped, err
		}
		total += set
		skipped = skipped || missing > 0
		if last == nil {
			return total, skipped, nil
		}
		after = *last
	}
}

// Directory returns the Teams of records as the Team directory holds them.
func Directory(records []TeamRecord) []team.Team {
	out := make([]team.Team, 0, len(records))
	for _, r := range records {
		t := team.Team{ID: r.ID, Key: r.Key, Aliases: slices.Clone(r.Aliases), Source: r.Source, State: r.State, Name: r.Name, Revision: r.Revision, Draining: r.Draining}
		if r.Source == team.SourceRuntime {
			t.NameSource = team.NameSource{Source: team.NameFromOperatorAPI}
		}
		out = append(out, t)
	}
	return out
}
