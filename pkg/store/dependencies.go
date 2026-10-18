package store

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// maxDependencies is how many dependencies Ploeg keeps per Work Item.
const maxDependencies = 50

// Dependency is a tracker item a Work Item waits for (ADR-0123). Kind is
// blocked_by or follows. WorkItemID is Ploeg's Work Item for the same
// tracker item, empty when Ploeg has none.
type Dependency struct {
	Provider    string    `json:"provider"`
	ExternalID  string    `json:"externalId"`
	Title       string    `json:"title"`
	Kind        string    `json:"kind"`
	Done        bool      `json:"done"`
	FirstSeenAt time.Time `json:"firstSeenAt"`
	WorkItemID  string    `json:"workItemId,omitempty"`
}

// Dependencies is a Work Item's dependencies as its tracker last reported
// them, and when that was; CheckedAt is nil when the tracker was never read.
type Dependencies struct {
	Items     []Dependency
	CheckedAt *time.Time
}

// Open lists the dependencies that are not done.
func (d Dependencies) Open() []Dependency {
	var out []Dependency
	for _, dep := range d.Items {
		if !dep.Done {
			out = append(out, dep)
		}
	}
	return out
}

// RecordDependencies replaces the stored dependencies of a Work Item with
// what its tracker reported now and marks the tracker read. A dependency
// reported again keeps its firstSeenAt. It reports whether the set of open
// dependencies changed, and audits that change.
func (s *Store) RecordDependencies(ctx context.Context, workItemID int64, provider string, deps []Dependency) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var externalID string
	err = tx.QueryRow(ctx, `SELECT external_id FROM work_items WHERE id = $1 FOR UPDATE`, workItemID).Scan(&externalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrWorkItemNotFound
	}
	if err != nil {
		return false, err
	}
	before, err := openDependencyIDs(ctx, tx, workItemID)
	if err != nil {
		return false, err
	}
	ids, titles, kinds, done := []string{}, []string{}, []string{}, []bool{}
	seen := map[string]bool{}
	for _, d := range deps {
		kind := knownValue(d.Kind, "blocked_by", "follows")
		if d.ExternalID == "" || len(d.ExternalID) > 256 || d.ExternalID == externalID || seen[d.ExternalID] || kind == "" || len(ids) == maxDependencies {
			continue
		}
		seen[d.ExternalID] = true
		ids, titles, kinds, done = append(ids, d.ExternalID), append(titles, truncate(d.Title, 4096)), append(kinds, kind), append(done, d.Done)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM work_item_dependencies WHERE work_item_id = $1 AND NOT (provider = $2 AND external_id = ANY($3))`,
		workItemID, provider, ids); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO work_item_dependencies (work_item_id, provider, external_id, title, kind, done)
		SELECT $1, $2, d.id, d.title, d.kind, d.done FROM unnest($3::text[], $4::text[], $5::text[], $6::bool[]) AS d(id, title, kind, done)
		ON CONFLICT (work_item_id, provider, external_id) DO UPDATE SET title = EXCLUDED.title, kind = EXCLUDED.kind, done = EXCLUDED.done`,
		workItemID, provider, ids, titles, kinds, done); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE work_items SET dependencies_checked_at = now() WHERE id = $1`, workItemID); err != nil {
		return false, err
	}
	after, err := openDependencyIDs(ctx, tx, workItemID)
	if err != nil {
		return false, err
	}
	changed := !slices.Equal(before, after)
	if changed {
		if err := audit(ctx, tx, "tracker:"+provider, "work_item.dependencies_seen", &workItemID, map[string]any{"waiting_for": after}); err != nil {
			return false, err
		}
	}
	return changed, tx.Commit(ctx)
}

func openDependencyIDs(ctx context.Context, tx pgx.Tx, workItemID int64) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT external_id FROM work_item_dependencies WHERE work_item_id = $1 AND NOT done ORDER BY external_id`, workItemID)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if ids == nil {
		ids = []string{}
	}
	return ids, err
}

// WorkItemDependencies reads the stored dependencies of a Work Item.
func (s *Store) WorkItemDependencies(ctx context.Context, workItemID int64) (Dependencies, error) {
	var out Dependencies
	if err := s.pool.QueryRow(ctx, `SELECT dependencies_checked_at FROM work_items WHERE id = $1`, workItemID).Scan(&out.CheckedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, ErrWorkItemNotFound
		}
		return out, err
	}
	rows, err := s.pool.Query(ctx, `SELECT d.provider, d.external_id, d.title, d.kind, d.done, d.first_seen_at, COALESCE(w.id::text, '')
		FROM work_item_dependencies d
		LEFT JOIN work_items w ON w.provider = d.provider AND w.external_id = d.external_id
		WHERE d.work_item_id = $1 ORDER BY d.first_seen_at, d.external_id`, workItemID)
	if err != nil {
		return out, err
	}
	out.Items, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Dependency, error) {
		var d Dependency
		err := r.Scan(&d.Provider, &d.ExternalID, &d.Title, &d.Kind, &d.Done, &d.FirstSeenAt, &d.WorkItemID)
		return d, err
	})
	return out, err
}
