package store

import (
	"context"
	"encoding/json"
)

// Bounds of one page of the legacy export (ADR-0079).
const (
	LegacyExportLimit        = 200
	DefaultLegacyExportLimit = 50
)

// LegacyExportItem is every row a person or a freeze created about one Work
// Item's card: the cracks where it is the card, its frozen rarity, its card
// comment record and the change shapes of its pull requests. It is kept
// raw for a consumer to import once, and removed with those tables.
type LegacyExportItem struct {
	WorkItemID string            `json:"workItemId"`
	Team       string            `json:"team"`
	Provider   string            `json:"provider"`
	ExternalID string            `json:"externalId"`
	Cracks     []json.RawMessage `json:"cracks"`
	Rarity     json.RawMessage   `json:"rarity"`
	Comment    json.RawMessage   `json:"comment"`
	Shapes     []json.RawMessage `json:"shapes"`
}

// LegacyExportPage is one page of the export, by ascending Work Item id.
// NextAfter is nil on the last page.
type LegacyExportPage struct {
	Items     []LegacyExportItem
	NextAfter *string
}

const legacyRefJSON = `jsonb_build_object('id', x.id::text, 'forge', x.forge, 'owner', x.repo_owner, 'repo', x.repo_name, 'number', x.number)`

const legacyItemQuery = `SELECT i.id::text, i.team, i.provider, i.external_id,
	(SELECT COALESCE(jsonb_agg(jsonb_build_object(
		'id', c.id::text, 'team', c.team, 'state', c.state,
		'cardWorkItemId', c.card_work_item_id::text, 'bugWorkItemId', c.bug_work_item_id::text,
		'bug', (SELECT jsonb_build_object('provider', b.provider, 'externalId', b.external_id) FROM work_items b WHERE b.id = c.bug_work_item_id),
		'pullRequest', (SELECT ` + legacyRefJSON + ` FROM pull_requests x WHERE x.id = c.pull_request_id),
		'severity', c.severity, 'share', c.share, 'discovery', c.discovery, 'steward', c.steward, 'note', c.note,
		'proposedBy', c.proposed_by, 'proposedAt', c.proposed_at, 'confirmedBy', c.confirmed_by, 'confirmedAt', c.confirmed_at,
		'disputeUntil', c.dispute_until, 'disputedBy', c.disputed_by, 'disputedAt', c.disputed_at, 'disputeReason', c.dispute_reason,
		'resolvedBy', c.resolved_by, 'resolvedAt', c.resolved_at, 'resolution', c.resolution,
		'evolvedBy', c.evolved_by, 'evolvedAt', c.evolved_at,
		'mendPullRequest', (SELECT ` + legacyRefJSON + ` FROM pull_requests x WHERE x.id = c.mend_pull_request_id),
		'mendNumber', c.mend_number, 'mendedAt', c.mended_at, 'mendedBy', c.mended_by, 'mendBySteward', c.mend_by_steward,
		'mendConfirmedAt', c.mend_confirmed_at, 'mendReopenedAt', c.mend_reopened_at) ORDER BY c.id), '[]'::jsonb)
		FROM card_cracks c WHERE c.card_work_item_id = i.id),
	(SELECT jsonb_build_object('formula', r.formula, 'revealedTier', r.revealed_tier, 'predictedTier', r.predicted_tier,
		'score', r.score, 'predictedScore', r.predicted_score, 'percentile', r.percentile, 'cohortTarget', r.cohort_target,
		'cohortQuarter', r.cohort_quarter, 'cohortSize', r.cohort_size, 'inputs', r.inputs, 'revealedAt', r.revealed_at,
		'recordedAt', r.recorded_at, 'checkedAt', r.checked_at)
		FROM card_rarity r WHERE r.work_item_id = i.id AND r.revealed_tier IS NOT NULL),
	(SELECT jsonb_build_object('pullRequest', (SELECT ` + legacyRefJSON + ` FROM pull_requests x WHERE x.id = cc.pull_request_id),
		'moment', cc.moment, 'commentId', cc.comment_id, 'image', cc.image, 'publishedAt', cc.published_at, 'checkedAt', cc.checked_at)
		FROM card_comments cc WHERE cc.work_item_id = i.id),
	(SELECT COALESCE(jsonb_agg(jsonb_build_object('pullRequest', ` + legacyRefJSON + `, 'shape', x.shape) ORDER BY x.id), '[]'::jsonb)
		FROM pull_requests x WHERE x.work_item_id = i.id AND x.shape IS NOT NULL)
	FROM work_items i
	WHERE i.id > $1 AND ($2::text[] IS NULL OR i.team = ANY($2))
	  AND (EXISTS (SELECT 1 FROM card_cracks c WHERE c.card_work_item_id = i.id)
	    OR EXISTS (SELECT 1 FROM card_rarity r WHERE r.work_item_id = i.id AND r.revealed_tier IS NOT NULL)
	    OR EXISTS (SELECT 1 FROM card_comments cc WHERE cc.work_item_id = i.id)
	    OR EXISTS (SELECT 1 FROM pull_requests x WHERE x.work_item_id = i.id AND x.shape IS NOT NULL))
	ORDER BY i.id LIMIT $3`

// LegacyExport reads one page of the card rows Ploeg cannot recompute, for
// the Work Items within teams (nil means every team) whose id is above
// after (ADR-0079).
func (s *Store) LegacyExport(ctx context.Context, teams []string, after int64, limit int) (LegacyExportPage, error) {
	if limit <= 0 {
		limit = DefaultLegacyExportLimit
	}
	limit = min(limit, LegacyExportLimit)
	rows, err := s.pool.Query(ctx, legacyItemQuery, after, teams, limit+1)
	if err != nil {
		return LegacyExportPage{}, err
	}
	defer rows.Close()
	page := LegacyExportPage{Items: []LegacyExportItem{}}
	for rows.Next() {
		var item LegacyExportItem
		var cracks, rarity, comment, shapes []byte
		if err := rows.Scan(&item.WorkItemID, &item.Team, &item.Provider, &item.ExternalID, &cracks, &rarity, &comment, &shapes); err != nil {
			return LegacyExportPage{}, err
		}
		if err := json.Unmarshal(cracks, &item.Cracks); err != nil {
			return LegacyExportPage{}, err
		}
		if err := json.Unmarshal(shapes, &item.Shapes); err != nil {
			return LegacyExportPage{}, err
		}
		item.Rarity, item.Comment = legacyNullable(rarity), legacyNullable(comment)
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return LegacyExportPage{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1].WorkItemID
		page.NextAfter = &last
	}
	return page, nil
}

func legacyNullable(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	return json.RawMessage(raw)
}
