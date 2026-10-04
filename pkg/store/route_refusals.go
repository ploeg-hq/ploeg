package store

import (
	"context"
	"errors"
	"time"
)

// RouteRefusalUnclassified is the code of a refusal recorded before refusals
// carried one.
const RouteRefusalUnclassified = "unclassified"

// RouteRefusal is a tracker task that routing refused (ADR-0038). It is not a
// Work Item: none exists for it.
type RouteRefusal struct {
	Provider      string    `json:"provider"`
	ExternalID    string    `json:"externalId"`
	ExternalScope string    `json:"externalScope"`
	Team          string    `json:"team"`
	Title         string    `json:"title"`
	Labels        []string  `json:"labels"`
	Code          string    `json:"code"`
	Reason        string    `json:"reason"`
	AllowedLabels []string  `json:"allowedLabels"`
	RefusedAt     time.Time `json:"refusedAt"`
}

const recentRouteRefusalsQuery = `
	WITH latest AS (
		SELECT DISTINCT ON (a.actor, a.detail->>'external_id')
			a.id, a.at, substr(a.actor, length('webhook:') + 1) AS provider, a.detail
		FROM audit_log a
		WHERE a.action = 'work_item.route_refused' AND a.work_item_id IS NULL AND a.at >= $2
			AND a.actor LIKE 'webhook:%' AND COALESCE(a.detail->>'external_id', '') <> ''
		ORDER BY a.actor, a.detail->>'external_id', a.id DESC
	)
	SELECT jsonb_build_object(
		'provider', l.provider,
		'externalId', l.detail->>'external_id',
		'externalScope', COALESCE(l.detail->>'external_scope', ''),
		'team', COALESCE(l.detail->>'team', ''),
		'title', left(COALESCE(l.detail->>'title', ''), 4096),
		'labels', CASE WHEN jsonb_typeof(l.detail->'labels') = 'array' THEN l.detail->'labels' ELSE '[]'::jsonb END,
		'code', CASE WHEN jsonb_typeof(l.detail->'code') = 'string' AND l.detail->>'code' <> '' THEN l.detail->>'code' ELSE '` + RouteRefusalUnclassified + `' END,
		'reason', left(COALESCE(l.detail->>'reason', ''), 4096),
		'allowedLabels', CASE WHEN jsonb_typeof(l.detail->'allowed_labels') = 'array' THEN l.detail->'allowed_labels' ELSE '[]'::jsonb END,
		'refusedAt', l.at)
	FROM latest l
	WHERE ($1::text[] IS NULL OR l.detail->>'team' = ANY($1))
		AND NOT EXISTS (
			SELECT 1 FROM work_items i JOIN audit_log q ON q.work_item_id = i.id
			WHERE i.provider = l.provider AND i.external_id = l.detail->>'external_id'
				AND q.action IN ('work_item.queued', 'work_item.refreshed') AND q.id > l.id)
	ORDER BY l.id DESC
	LIMIT $3`

// RecentRouteRefusals returns the newest refusal of each tracker task refused
// at or after since, newest first, for the given teams (nil reads every
// team). A task queued or refreshed after its newest refusal is left out.
func (s *Store) RecentRouteRefusals(ctx context.Context, teams []string, since time.Time, limit int) ([]RouteRefusal, error) {
	if limit < 1 || limit > 200 {
		return nil, errors.New("route refusal limit must be between 1 and 200")
	}
	rows, err := s.pool.Query(ctx, recentRouteRefusalsQuery, teams, since, limit)
	if err != nil {
		return nil, err
	}
	refusals, err := operatorDecodeRows[RouteRefusal](rows)
	if err != nil {
		return nil, err
	}
	for i := range refusals {
		refusals[i].RefusedAt = refusals[i].RefusedAt.UTC()
	}
	return refusals, nil
}
