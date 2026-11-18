package store

import (
	"context"
	"encoding/json"
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
)

// StaticTeam is one Team of the configuration file as the boot reconciler
// sees it.
type StaticTeam struct {
	Key     string
	Aliases []string
	Name    string
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
}

// TeamReconciliation says what the boot reconciler changed.
type TeamReconciliation struct {
	Created []string
	Renamed []string
	Removed []string
}

const teamsLockKey = "hashtext('ploeg_teams')"

// ReconcileStaticTeams gives every static Team of the file a row and an id
// (ADR-0198 section 6): a Team found by its key or one of its aliases keeps
// its id; a key never seen gets a new one from newID; a static row whose
// key and aliases left the file is removed. Changes are audited as made by
// the configuration.
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
		claimed := map[string]bool{}
		sorted := slices.Clone(teams)
		slices.SortFunc(sorted, func(a, b StaticTeam) int { return strings.Compare(a.Key, b.Key) })
		type match struct {
			want     StaticTeam
			row      TeamRecord
			found    bool
			returned bool
		}
		plan := make([]match, 0, len(sorted))
		for _, want := range sorted {
			m := match{want: want}
			m.row, m.found = matchStaticTeam(live, want, claimed)
			if !m.found {
				m.row, m.found = matchStaticTeam(removed, StaticTeam{Key: want.Key}, claimed)
				m.returned = m.found
			}
			if m.found {
				claimed[m.row.ID] = true
			}
			plan = append(plan, m)
		}
		for _, row := range live {
			if claimed[row.ID] {
				continue
			}
			if _, err := tx.Exec(ctx, `UPDATE teams SET removed_at = now(), removed_by = $2, updated_at = now(),
				revision = nextval('team_revisions') WHERE id = $1`, row.ID, team.ConfigurationActor); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE team_keys SET released_at = now() WHERE team_id = $1 AND released_at IS NULL`, row.ID); err != nil {
				return err
			}
			if err := auditTeam(ctx, tx, AuditTeamRemoved, row.ID, row.Key, nil); err != nil {
				return err
			}
			out.Removed = append(out.Removed, row.Key)
		}
		for _, m := range plan {
			if !m.found {
				id := newID()
				if err := insertStaticTeam(ctx, tx, id, m.want); err != nil {
					return err
				}
				out.Created = append(out.Created, m.want.Key)
				continue
			}
			if err := syncStaticTeam(ctx, tx, m.row, m.want, m.returned, &out); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

func matchStaticTeam(rows []TeamRecord, want StaticTeam, claimed map[string]bool) (TeamRecord, bool) {
	for _, row := range rows {
		if !claimed[row.ID] && strings.EqualFold(row.Key, want.Key) {
			return row, true
		}
	}
	refs := append([]string{want.Key}, want.Aliases...)
	for _, row := range rows {
		if claimed[row.ID] {
			continue
		}
		held := append([]string{row.Key}, row.Aliases...)
		for _, ref := range refs {
			if slices.ContainsFunc(held, func(h string) bool { return strings.EqualFold(h, ref) }) {
				return row, true
			}
		}
	}
	return TeamRecord{}, false
}

func insertStaticTeam(ctx context.Context, tx pgx.Tx, id string, want StaticTeam) error {
	if _, err := tx.Exec(ctx, `INSERT INTO teams (id, source, name, created_by, updated_by)
		VALUES ($1, 'static', nullif($2, ''), $3, $3)`, id, want.Name, team.ConfigurationActor); err != nil {
		return err
	}
	if err := insertTeamKey(ctx, tx, id, want.Key, "current"); err != nil {
		return err
	}
	for _, alias := range want.Aliases {
		if err := insertTeamKey(ctx, tx, id, alias, "alias"); err != nil {
			return err
		}
	}
	return auditTeam(ctx, tx, AuditTeamCreated, id, want.Key, nil)
}

func insertTeamKey(ctx context.Context, tx pgx.Tx, id, key, kind string) error {
	_, err := tx.Exec(ctx, `INSERT INTO team_keys (team_id, key, kind) VALUES ($1, $2, $3)`, id, key, kind)
	if err != nil {
		return fmt.Errorf("team %s key %q: %w", id, key, err)
	}
	return nil
}

func syncStaticTeam(ctx context.Context, tx pgx.Tx, row TeamRecord, want StaticTeam, returned bool, out *TeamReconciliation) error {
	if returned {
		if _, err := tx.Exec(ctx, `UPDATE teams SET removed_at = NULL, removed_by = NULL, updated_at = now(),
			revision = nextval('team_revisions') WHERE id = $1`, row.ID); err != nil {
			return err
		}
		if err := insertTeamKey(ctx, tx, row.ID, want.Key, "current"); err != nil {
			return err
		}
		for _, alias := range want.Aliases {
			if err := insertTeamKey(ctx, tx, row.ID, alias, "alias"); err != nil {
				return err
			}
		}
		out.Created = append(out.Created, want.Key)
		return auditTeam(ctx, tx, AuditTeamCreated, row.ID, want.Key, map[string]any{"returned": true})
	}
	if row.Key != want.Key {
		if _, err := tx.Exec(ctx, `UPDATE team_keys SET released_at = now() WHERE team_id = $1 AND released_at IS NULL`, row.ID); err != nil {
			return err
		}
		if err := insertTeamKey(ctx, tx, row.ID, want.Key, "current"); err != nil {
			return err
		}
		for _, alias := range want.Aliases {
			if err := insertTeamKey(ctx, tx, row.ID, alias, "alias"); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE teams SET updated_at = now(), revision = nextval('team_revisions') WHERE id = $1`, row.ID); err != nil {
			return err
		}
		out.Renamed = append(out.Renamed, row.Key+"→"+want.Key)
		if err := auditTeam(ctx, tx, AuditTeamRenamed, row.ID, want.Key, map[string]any{"from": row.Key, "to": want.Key}); err != nil {
			return err
		}
	} else if err := syncStaticAliases(ctx, tx, row, want.Aliases); err != nil {
		return err
	}
	if row.Name != want.Name {
		if _, err := tx.Exec(ctx, `UPDATE teams SET name = nullif($2, ''), updated_at = now(),
			revision = nextval('team_revisions') WHERE id = $1`, row.ID, want.Name); err != nil {
			return err
		}
		return auditTeam(ctx, tx, AuditTeamNameChanged, row.ID, want.Key, map[string]any{"from": row.Name, "to": want.Name})
	}
	return nil
}

func syncStaticAliases(ctx context.Context, tx pgx.Tx, row TeamRecord, aliases []string) error {
	for _, held := range row.Aliases {
		if slices.ContainsFunc(aliases, func(a string) bool { return strings.EqualFold(a, held) }) {
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
	for _, alias := range aliases {
		if slices.ContainsFunc(row.Aliases, func(h string) bool { return strings.EqualFold(h, alias) }) {
			continue
		}
		if err := insertTeamKey(ctx, tx, row.ID, alias, "alias"); err != nil {
			return err
		}
	}
	return nil
}

func staticTeamRows(ctx context.Context, tx pgx.Tx, removed bool) ([]TeamRecord, error) {
	rows, err := tx.Query(ctx, `
		SELECT t.id, coalesce(t.name, ''), t.revision, t.state, t.removed_at,
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
		err := r.Scan(&rec.ID, &rec.Name, &rec.Revision, &rec.State, &rec.RemovedAt, &rec.Key, &rec.Aliases)
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
				AND k.released_at IS NULL), '{}')
		FROM teams t WHERE t.removed_at IS NULL ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (TeamRecord, error) {
		var rec TeamRecord
		err := r.Scan(&rec.ID, &rec.Tenant, &rec.Source, &rec.Name, &rec.Revision, &rec.State, &rec.Key, &rec.Aliases)
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
// a batched backfill.
var teamTables = []struct{ table, key string }{
	{"work_items", "id"},
	{"shifts", "id"},
	{"agent_runs", "id"},
	{"leases", "work_item_id"},
	{"chats", "run_id"},
	{"work_item_premise_checks", "work_item_id"},
	{"tracker_bindings", "id"},
}

// BackfillTeamIDs sets team_id from the static key in team on every row
// that has none, batch rows per transaction so a large table is never
// locked whole (ADR-0198 section 6). It returns the rows it set.
func (s *Store) BackfillTeamIDs(ctx context.Context, batch int) (int64, error) {
	if batch <= 0 {
		batch = 10000
	}
	var total int64
	for _, t := range teamTables {
		after := int64(-1 << 63)
		for {
			var last *int64
			var set int64
			err := s.pool.QueryRow(ctx, fmt.Sprintf(`
				WITH chunk AS (
					SELECT %[2]s AS k FROM %[1]s WHERE %[2]s > $1 ORDER BY %[2]s LIMIT $2
				), filled AS (
					UPDATE %[1]s x SET team_id = ploeg_static_team_id(x.team)
					FROM chunk WHERE x.%[2]s = chunk.k AND x.team_id IS NULL
						AND ploeg_static_team_id(x.team) IS NOT NULL
					RETURNING 1
				)
				SELECT (SELECT max(k) FROM chunk), (SELECT count(*) FROM filled)`, t.table, t.key), after, batch).Scan(&last, &set)
			if err != nil {
				return total, fmt.Errorf("backfill %s: %w", t.table, err)
			}
			total += set
			if last == nil {
				break
			}
			after = *last
		}
	}
	return total, nil
}

// Directory returns the Teams of records as the Team directory holds them.
func Directory(records []TeamRecord) []team.Team {
	out := make([]team.Team, 0, len(records))
	for _, r := range records {
		t := team.Team{ID: r.ID, Key: r.Key, Aliases: slices.Clone(r.Aliases), Source: r.Source, State: r.State, Name: r.Name, Revision: r.Revision}
		if r.Source == team.SourceRuntime {
			t.NameSource = team.NameSource{Source: team.NameFromOperatorAPI}
		}
		out = append(out, t)
	}
	return out
}
