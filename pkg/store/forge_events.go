package store

import (
	"context"
	"strings"
	"time"
)

// LatestForgeEvents returns when Ploeg last recorded a forge webhook event
// for each repository since the given time, keyed by ForgeEventKey. A
// repository whose forge sent nothing in that window is absent.
func (s *Store) LatestForgeEvents(ctx context.Context, since time.Time) (map[string]time.Time, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT substr(actor, length('webhook:') + 1), lower(detail->>'repo'), max(at)
		FROM audit_log
		WHERE action LIKE 'forge.%' AND actor LIKE 'webhook:%' AND at >= $1
			AND COALESCE(detail->>'repo', '') <> ''
		GROUP BY 1, 2`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var forge, repo string
		var at time.Time
		if err := rows.Scan(&forge, &repo, &at); err != nil {
			return nil, err
		}
		out[ForgeEventKey(forge, repo)] = at.UTC()
	}
	return out, rows.Err()
}

// ForgeEventKey names one repository on one forge in LatestForgeEvents.
func ForgeEventKey(forge, repo string) string {
	return forge + "\x00" + strings.ToLower(repo)
}
