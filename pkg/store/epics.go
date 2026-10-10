package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
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
