package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// EpicRef is a tracker item's parent, named by its tracker id (ADR-0053).
type EpicRef struct {
	ExternalID string
	Title      string
}

const maxEpicRefs = 20

// RecordEpics stores the parents the tracker reported for the Work Item of
// a tracker item: a new parent is first seen now, a parent no longer
// reported is removed, and a removed parent reported again is first seen
// anew. It reports whether anything changed, and returns
// ErrWorkItemNotFound when Ploeg has no Work Item for the tracker item.
func (s *Store) RecordEpics(ctx context.Context, provider, externalID string, parents []EpicRef) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var id int64
	err = tx.QueryRow(ctx, `SELECT id FROM work_items WHERE provider = $1 AND external_id = $2 FOR UPDATE`, provider, externalID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrWorkItemNotFound
	}
	if err != nil {
		return false, err
	}
	ids, titles := []string{}, []string{}
	seen := map[string]bool{}
	for _, p := range parents {
		if p.ExternalID == "" || p.ExternalID == externalID || seen[p.ExternalID] || len(ids) == maxEpicRefs {
			continue
		}
		seen[p.ExternalID] = true
		ids, titles = append(ids, truncate(p.ExternalID, 256)), append(titles, truncate(p.Title, 4096))
	}
	added, err := tx.Exec(ctx, `INSERT INTO work_item_epics (work_item_id, provider, epic_external_id, epic_title)
		SELECT $1, $2, e.id, e.title FROM unnest($3::text[], $4::text[]) AS e(id, title)
		ON CONFLICT (work_item_id, provider, epic_external_id) DO UPDATE SET
			epic_title = CASE WHEN EXCLUDED.epic_title = '' THEN work_item_epics.epic_title ELSE EXCLUDED.epic_title END,
			last_seen_at = now(),
			first_seen_at = CASE WHEN work_item_epics.removed_at IS NULL THEN work_item_epics.first_seen_at ELSE now() END,
			removed_at = NULL
		WHERE work_item_epics.removed_at IS NOT NULL OR work_item_epics.epic_title IS DISTINCT FROM
			(CASE WHEN EXCLUDED.epic_title = '' THEN work_item_epics.epic_title ELSE EXCLUDED.epic_title END)`,
		id, provider, ids, titles)
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE work_item_epics SET last_seen_at = now()
		WHERE work_item_id = $1 AND provider = $2 AND epic_external_id = ANY($3) AND removed_at IS NULL`, id, provider, ids); err != nil {
		return false, err
	}
	removed, err := tx.Exec(ctx, `UPDATE work_item_epics SET removed_at = now()
		WHERE work_item_id = $1 AND removed_at IS NULL AND NOT (provider = $2 AND epic_external_id = ANY($3))`, id, provider, ids)
	if err != nil {
		return false, err
	}
	changed := added.RowsAffected() > 0 || removed.RowsAffected() > 0
	if changed {
		if err := audit(ctx, tx, "webhook:"+provider, "work_item.epics_seen", &id, map[string]any{"epics": ids}); err != nil {
			return false, err
		}
	}
	return changed, tx.Commit(ctx)
}

// CardSet places the card in its epic's set (ADR-0053). Role is epic on the
// epic's own card and child on a member's. Epic.WorkItemID is nil when the
// epic is not a Work Item of the card's team. Position counts from 1 in the
// order the members started, and is nil on the epic card. Children are
// listed on the epic card only. Complete is true when every member merged,
// has been live SettledDays or more and has no crack without a confirmed
// mend.
type CardSet struct {
	Role     string         `json:"role"`
	Epic     CardSetEpic    `json:"epic"`
	Position *int           `json:"position"`
	Size     int            `json:"size"`
	Children []CardSetChild `json:"children,omitempty"`
	Complete bool           `json:"complete"`
}

// CardSetEpic names the epic of a set.
type CardSetEpic struct {
	WorkItemID *string `json:"workItemId"`
	Ref        string  `json:"ref,omitempty"`
	Title      string  `json:"title"`
}

// CardSetChild is one member of a set as the epic card lists it. State is
// the member card's state; Settled is true once it merged and has been live
// SettledDays; Cracked is true while it has a crack without a confirmed mend.
type CardSetChild struct {
	WorkItemID string `json:"workItemId"`
	Title      string `json:"title"`
	State      string `json:"state"`
	Settled    bool   `json:"settled"`
	Cracked    bool   `json:"cracked"`
}

// SettledDays is how long a merged member must be live before its set can
// complete.
const SettledDays = 30

const maxSetMembers = 100

type setMember struct {
	id         int64
	title      string
	itemState  string
	firstSeen  time.Time
	firstShift *time.Time
}

func (m setMember) counts() bool { return m.firstShift == nil || !m.firstSeen.After(*m.firstShift) }

func (c *OperatorCard) loadSet(ctx context.Context, tx pgx.Tx, id int64, provider, externalID string, opts CardOptions) error {
	if provider == "" || externalID == "" || provider == "manual" {
		return nil
	}
	epicID, epicTitle, role := externalID, c.Title, "epic"
	members, err := setMembers(ctx, tx, provider, epicID, c.Team)
	if err != nil {
		return err
	}
	if len(members) == 0 {
		role = "child"
		rows, err := tx.Query(ctx, `SELECT e.epic_external_id, e.epic_title, e.first_seen_at,
				(SELECT min(s.opened_at) FROM shifts s WHERE s.work_item_id = e.work_item_id)
			FROM work_item_epics e WHERE e.work_item_id = $1 AND e.removed_at IS NULL
			ORDER BY e.first_seen_at, e.epic_external_id LIMIT $2`, id, maxEpicRefs)
		if err != nil {
			return err
		}
		found := false
		for rows.Next() {
			var m setMember
			var ref, title string
			if err := rows.Scan(&ref, &title, &m.firstSeen, &m.firstShift); err != nil {
				rows.Close()
				return err
			}
			if !found && m.counts() {
				epicID, epicTitle, found = ref, title, true
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if !found {
			return nil
		}
		if members, err = setMembers(ctx, tx, provider, epicID, c.Team); err != nil {
			return err
		}
	}
	if len(members) == 0 {
		return nil
	}
	set := &CardSet{Role: role, Size: len(members), Epic: CardSetEpic{Title: epicTitle,
		Ref: work.Reference(work.WorkItem{Provider: provider, ExternalID: epicID})}}
	if role == "epic" {
		self := strconv.FormatInt(id, 10)
		set.Epic.WorkItemID = &self
	} else {
		var epicItem int64
		var title string
		err := tx.QueryRow(ctx, `SELECT id, left(title, 4096) FROM work_items WHERE provider = $1 AND external_id = $2 AND team = $3`,
			provider, epicID, c.Team).Scan(&epicItem, &title)
		switch {
		case err == nil:
			ref := strconv.FormatInt(epicItem, 10)
			set.Epic.WorkItemID = &ref
			if title != "" {
				set.Epic.Title = title
			}
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
	}
	children, err := setChildren(ctx, tx, members, opts.ReleaseEnvironments, cardNow(opts))
	if err != nil {
		return err
	}
	set.Complete = true
	for i, child := range children {
		set.Complete = set.Complete && child.Settled && !child.Cracked
		if role == "child" && members[i].id == id {
			position := i + 1
			set.Position = &position
		}
	}
	if role == "child" && set.Position == nil {
		return nil
	}
	if role == "epic" {
		set.Children = children
	}
	c.Set = set
	return nil
}

func setMembers(ctx context.Context, tx pgx.Tx, provider, epicID, team string) ([]setMember, error) {
	rows, err := tx.Query(ctx, `SELECT i.id, left(i.title, 4096), i.state, e.first_seen_at,
			(SELECT min(s.opened_at) FROM shifts s WHERE s.work_item_id = i.id) AS first_shift
		FROM work_item_epics e JOIN work_items i ON i.id = e.work_item_id
		WHERE e.provider = $1 AND e.epic_external_id = $2 AND e.removed_at IS NULL AND i.team = $3
		ORDER BY first_shift NULLS LAST, i.id LIMIT $4`, provider, epicID, team, maxSetMembers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []setMember
	for rows.Next() {
		var m setMember
		if err := rows.Scan(&m.id, &m.title, &m.itemState, &m.firstSeen, &m.firstShift); err != nil {
			return nil, err
		}
		if m.counts() {
			out = append(out, m)
		}
	}
	return out, rows.Err()
}

type setPlay struct {
	id           int64
	forge, repo  string
	state        string
	mergedAt     *time.Time
	firstDeploys map[string]time.Time
}

func setChildren(ctx context.Context, tx pgx.Tx, members []setMember, environments map[string]string, now time.Time) ([]CardSetChild, error) {
	ids := make([]int64, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.id)
	}
	plays := map[int64][]*setPlay{}
	byPR := map[int64]*setPlay{}
	rows, err := tx.Query(ctx, `SELECT work_item_id, id, forge, repo_owner || '/' || repo_name, COALESCE(state, ''), merged_at
		FROM pull_requests WHERE work_item_id = ANY($1) ORDER BY number, id LIMIT 2000`, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var item int64
		p := &setPlay{firstDeploys: map[string]time.Time{}}
		if err := rows.Scan(&item, &p.id, &p.forge, &p.repo, &p.state, &p.mergedAt); err != nil {
			rows.Close()
			return nil, err
		}
		plays[item] = append(plays[item], p)
		byPR[p.id] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	prs := make([]int64, 0, len(byPR))
	for id := range byPR {
		prs = append(prs, id)
	}
	deploys, err := tx.Query(ctx, `SELECT pull_request_id, environment, first_deployed_at FROM pull_request_deployments
		WHERE pull_request_id = ANY($1) LIMIT 5000`, prs)
	if err != nil {
		return nil, err
	}
	for deploys.Next() {
		var pr int64
		var env string
		var at time.Time
		if err := deploys.Scan(&pr, &env, &at); err != nil {
			deploys.Close()
			return nil, err
		}
		byPR[pr].firstDeploys[env] = at
	}
	deploys.Close()
	if err := deploys.Err(); err != nil {
		return nil, err
	}
	cracked := map[int64]bool{}
	crackRows, err := tx.Query(ctx, `SELECT DISTINCT card_work_item_id FROM card_cracks WHERE card_work_item_id = ANY($1)
		AND state IN ('confirmed', 'disputed') AND mend_confirmed_at IS NULL`, ids)
	if err != nil {
		return nil, err
	}
	for crackRows.Next() {
		var id int64
		if err := crackRows.Scan(&id); err != nil {
			crackRows.Close()
			return nil, err
		}
		cracked[id] = true
	}
	crackRows.Close()
	if err := crackRows.Err(); err != nil {
		return nil, err
	}

	reported := map[string]bool{}
	out := make([]CardSetChild, 0, len(members))
	for _, m := range members {
		child := CardSetChild{WorkItemID: strconv.FormatInt(m.id, 10), Title: m.title, Cracked: cracked[m.id]}
		states := make([]string, 0, len(plays[m.id]))
		var latest *setPlay
		for _, p := range plays[m.id] {
			states = append(states, p.state)
			if p.state == "merged" && (latest == nil || timeOf(p.mergedAt).Compare(timeOf(latest.mergedAt)) >= 0) {
				latest = p
			}
		}
		child.State = cardState(m.itemState, states)
		if child.State == "merged" && latest != nil {
			live, err := setLiveSince(ctx, tx, latest, environments, reported)
			if err != nil {
				return nil, err
			}
			child.Settled = live != nil && now.Sub(*live) >= SettledDays*24*time.Hour
		}
		out = append(out, child)
	}
	return out, nil
}

func timeOf(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func setLiveSince(ctx context.Context, tx pgx.Tx, p *setPlay, environments map[string]string, reported map[string]bool) (*time.Time, error) {
	environment := work.DefaultReleaseEnvironment
	if configured, ok := environments[strings.ToLower(p.repo)]; ok && configured != "" {
		environment = configured
	}
	if at, ok := p.firstDeploys[environment]; ok {
		return &at, nil
	}
	owner, name, ok := splitRepo(p.repo)
	if !ok {
		return nil, nil
	}
	key := p.forge + "|" + strings.ToLower(p.repo) + "|" + environment
	known, seen := reported[key]
	if !seen {
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deployments
			WHERE forge = $1 AND repo_owner = lower($2) AND repo_name = lower($3) AND environment = $4)`,
			p.forge, owner, name, environment).Scan(&known); err != nil {
			return nil, err
		}
		reported[key] = known
	}
	if known || p.mergedAt == nil {
		return nil, nil
	}
	return p.mergedAt, nil
}
