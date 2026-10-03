package store

import (
	"context"
	"encoding/base64"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// CardListLimit is the most cards one page of the card list holds, and
// DefaultCardListLimit the page size when the caller names none (ADR-0054).
const (
	CardListLimit        = 50
	DefaultCardListLimit = 20
	// CardListMembers is the most logins one card list request may name.
	CardListMembers = 20
)

// ErrInvalidCardCursor is returned for a card list cursor Ploeg did not
// issue.
var ErrInvalidCardCursor = errors.New("invalid card list cursor")

// CardListFilter selects the cards of OperatorCards (ADR-0054). Teams is
// the consumer's scope (nil means every team) and Team narrows it. A card
// is listed when its roster names one of Members, compared without case.
// Since keeps the cards whose latest activity is at or after it; Before
// continues after a cursor of an earlier page.
type CardListFilter struct {
	Teams   []string
	Team    string
	Members []string
	Since   *time.Time
	Before  *CardCursor
	Limit   int
}

// CardCursor is a position in the card list: the activity time and Work
// Item of the last card a page examined.
type CardCursor struct {
	At         time.Time
	WorkItemID int64
}

const cardCursorPrefix = "c1."

// String encodes c as the opaque nextBefore of the card list.
func (c CardCursor) String() string {
	raw := strconv.FormatInt(c.At.UnixMicro(), 10) + "." + strconv.FormatInt(c.WorkItemID, 10)
	return cardCursorPrefix + base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// ParseCardCursor decodes a nextBefore that CardCursor.String issued.
func ParseCardCursor(s string) (CardCursor, error) {
	encoded, ok := strings.CutPrefix(s, cardCursorPrefix)
	if !ok || len(encoded) > 64 {
		return CardCursor{}, ErrInvalidCardCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return CardCursor{}, ErrInvalidCardCursor
	}
	at, id, ok := strings.Cut(string(raw), ".")
	if !ok {
		return CardCursor{}, ErrInvalidCardCursor
	}
	micros, err := strconv.ParseInt(at, 10, 64)
	if err != nil {
		return CardCursor{}, ErrInvalidCardCursor
	}
	workItem, err := strconv.ParseInt(id, 10, 64)
	if err != nil || workItem <= 0 {
		return CardCursor{}, ErrInvalidCardCursor
	}
	return CardCursor{At: time.UnixMicro(micros).UTC(), WorkItemID: workItem}, nil
}

// CardPage is one page of the card list, newest activity first. NextBefore
// is nil when no candidate is left; otherwise the next page may still be
// empty.
type CardPage struct {
	Cards      []OperatorCard
	NextBefore *CardCursor
}

type cardCandidate struct {
	id int64
	at time.Time
}

const cardCandidateQuery = `WITH members AS (
		SELECT p.work_item_id AS id FROM pull_requests p WHERE lower(p.merged_by) = ANY($1)
		UNION
		SELECT p.work_item_id FROM pull_request_reviews r JOIN pull_requests p ON p.id = r.pull_request_id
		WHERE lower(r.reviewer) = ANY($1)
		UNION
		SELECT g.work_item_id FROM gate_transitions g
		WHERE lower(g.actor) = ANY($1)
		  AND (SELECT prev.gate FROM gate_transitions prev WHERE prev.work_item_id = g.work_item_id AND prev.id < g.id
		       ORDER BY prev.id DESC LIMIT 1) IN ('test', 'acceptance')
		UNION
		SELECT c.card_work_item_id FROM card_cracks c
		WHERE lower(c.mended_by) = ANY($1) AND c.state IN ('confirmed', 'disputed') AND c.confirmed_at IS NOT NULL
		  AND c.mended_at IS NOT NULL AND c.mend_number IS NOT NULL AND NOT COALESCE(c.mend_by_steward, false)
	), activity AS (
		SELECT i.id, COALESCE(GREATEST(
			(SELECT min(r.started_at) FROM agent_runs r WHERE r.work_item_id = i.id),
			(SELECT max(GREATEST(p.first_seen_at, CASE WHEN p.state = 'merged' THEN p.merged_at END))
			   FROM pull_requests p WHERE p.work_item_id = i.id),
			(SELECT max(pd.first_deployed_at) FROM pull_requests p JOIN pull_request_deployments pd ON pd.pull_request_id = p.id
			   WHERE p.work_item_id = i.id AND p.state = 'merged'
			     AND pd.environment = COALESCE((SELECT e.env FROM unnest($4::text[], $5::text[]) AS e(repo, env)
			                                    WHERE e.repo = lower(p.repo_owner || '/' || p.repo_name) LIMIT 1), $6)),
			(SELECT max(GREATEST(c.confirmed_at, c.mended_at, c.mend_confirmed_at)) FROM card_cracks c
			   WHERE c.card_work_item_id = i.id AND c.state IN ('confirmed', 'disputed') AND c.confirmed_at IS NOT NULL)
		), i.created_at) AS at
		FROM work_items i JOIN members m ON m.id = i.id
		WHERE ($2::text[] IS NULL OR i.team = ANY($2)) AND ($3::text = '' OR i.team = $3)
	)
	SELECT id, at FROM activity
	WHERE ($7::timestamptz IS NULL OR at >= $7)
	  AND ($8::timestamptz IS NULL OR (at, id) < ($8, $9))
	ORDER BY at DESC, id DESC LIMIT $10`

// OperatorCards lists the cards whose roster names one of f.Members,
// newest activity first (ADR-0054). Each card is assembled by OperatorCard
// with opts, so it has the same shape as a single card; Style is left zero
// for the caller to fill. A page examines at most twice f.Limit candidates,
// so it can hold fewer than f.Limit cards while NextBefore is set.
func (s *Store) OperatorCards(ctx context.Context, f CardListFilter, opts CardOptions) (CardPage, error) {
	page := CardPage{Cards: []OperatorCard{}}
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultCardListLimit
	}
	limit = min(limit, CardListLimit)
	logins := cardListLogins(f.Members, opts.Bots)
	if len(logins) == 0 {
		return page, nil
	}
	repos, envs := releaseEnvironmentColumns(opts.ReleaseEnvironments)
	var beforeAt *time.Time
	var beforeID int64
	if f.Before != nil {
		at := f.Before.At
		beforeAt, beforeID = &at, f.Before.WorkItemID
	}
	examine := 2 * limit
	rows, err := s.pool.Query(ctx, cardCandidateQuery, logins, f.Teams, f.Team, repos, envs, work.DefaultReleaseEnvironment,
		f.Since, beforeAt, beforeID, examine)
	if err != nil {
		return CardPage{}, err
	}
	var candidates []cardCandidate
	for rows.Next() {
		var c cardCandidate
		if err := rows.Scan(&c.id, &c.at); err != nil {
			rows.Close()
			return CardPage{}, err
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return CardPage{}, err
	}

	wanted := make(map[string]bool, len(logins))
	for _, l := range logins {
		wanted[l] = true
	}
	last := -1
	for i, c := range candidates {
		if len(page.Cards) == limit {
			break
		}
		last = i
		card, err := s.OperatorCard(ctx, c.id, f.Teams, opts)
		if errors.Is(err, ErrOperatorNotFound) {
			continue
		}
		if err != nil {
			return CardPage{}, err
		}
		if card.namesAny(wanted) {
			page.Cards = append(page.Cards, card)
		}
	}
	if last >= 0 && (last < len(candidates)-1 || len(candidates) == examine) {
		page.NextBefore = &CardCursor{At: candidates[last].at.UTC(), WorkItemID: candidates[last].id}
	}
	return page, nil
}

func (c OperatorCard) namesAny(logins map[string]bool) bool {
	for _, p := range c.Roster {
		if logins[strings.ToLower(p.Name)] {
			return true
		}
	}
	return c.Steward != nil && logins[strings.ToLower(c.Steward.Name)]
}

func cardListLogins(members, bots []string) []string {
	bot := make(map[string]bool, len(bots))
	for _, b := range bots {
		bot[strings.ToLower(b)] = true
	}
	seen := map[string]bool{}
	out := []string{}
	for _, m := range members {
		l := strings.ToLower(strings.TrimSpace(m))
		if l == "" || bot[l] || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

func releaseEnvironmentColumns(environments map[string]string) ([]string, []string) {
	keys := make([]string, 0, len(environments))
	for key, env := range environments {
		if env != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	repos, envs := make([]string, 0, len(keys)), make([]string, 0, len(keys))
	for _, key := range keys {
		repos, envs = append(repos, strings.ToLower(key)), append(envs, environments[key])
	}
	return repos, envs
}
