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

// AttributionError is a crack attribution step Ploeg refused (ADR-0052).
// Code is one of the codes below; Message says why in words.
type AttributionError struct {
	Code    string
	Message string
}

func (e *AttributionError) Error() string { return e.Code + ": " + e.Message }

// Attribution refusal codes.
const (
	AttributionInvalid        = "invalid_request"
	AttributionForbidden      = "forbidden_actor"
	AttributionState          = "invalid_state"
	AttributionLimit          = "crack_limit"
	AttributionDuplicate      = "already_attributed"
	AttributionNotMerged      = "not_merged"
	AttributionAfterBug       = "merged_after_bug"
	AttributionDisputeClosed  = "dispute_closed"
	AttributionConcealUnknown = "concealment_unproven"
)

func refuse(code, message string) error { return &AttributionError{Code: code, Message: message} }

// Limits of the attribution flow (ADR-0052).
const (
	MaxCracksPerBug   = 3
	DisputeWorkdays   = 5
	CandidateWindow   = 365 * 24 * time.Hour
	maxCandidates     = 20
	maxCandidateFiles = 20
	maxCracksListed   = 100
)

// Crack is one attribution of a bug Work Item to the card whose play caused
// it, as the operator API shows it (ADR-0052). State is proposed, confirmed,
// disputed, unlinked or evolved. ConfirmedBy is the proposer and the
// confirmer once confirmed, and empty before. Disputed is true while the
// steward's dispute waits for a referee.
type Crack struct {
	ID            string     `json:"id"`
	Team          string     `json:"team"`
	State         string     `json:"state"`
	Card          CrackItem  `json:"card"`
	Bug           CrackItem  `json:"bug"`
	Play          *int       `json:"play"`
	Severity      *string    `json:"severity"`
	Share         *string    `json:"share"`
	Discovery     *string    `json:"discovery"`
	Steward       *string    `json:"steward"`
	Note          *string    `json:"note"`
	ProposedBy    string     `json:"proposedBy"`
	ProposedAt    time.Time  `json:"proposedAt"`
	ConfirmedBy   []string   `json:"confirmedBy"`
	ConfirmedAt   *time.Time `json:"confirmedAt"`
	DisputeUntil  *time.Time `json:"disputeUntil"`
	Disputed      bool       `json:"disputed"`
	DisputedBy    *string    `json:"disputedBy"`
	DisputedAt    *time.Time `json:"disputedAt"`
	DisputeReason *string    `json:"disputeReason"`
	ResolvedBy    *string    `json:"resolvedBy"`
	ResolvedAt    *time.Time `json:"resolvedAt"`
	Resolution    *string    `json:"resolution"`
	EvolvedBy     *string    `json:"evolvedBy"`
	EvolvedAt     *time.Time `json:"evolvedAt"`
	Mended        *CrackMend `json:"mended"`
}

// CrackItem names a Work Item in an attribution.
type CrackItem struct {
	WorkItemID  string `json:"workItemId"`
	Title       string `json:"title"`
	ExternalRef string `json:"externalRef,omitempty"`
}

// CrackMend is the merged fix of a crack's bug Work Item. ConfirmedAt is set
// once the mend stood its window; ReopenedAt when a new crack on the card
// came within it.
type CrackMend struct {
	At          time.Time  `json:"at"`
	By          string     `json:"by,omitempty"`
	PR          int        `json:"pr"`
	BySteward   bool       `json:"bySteward"`
	ConfirmedAt *time.Time `json:"confirmedAt"`
	ReopenedAt  *time.Time `json:"reopenedAt,omitempty"`
}

const crackSelect = `SELECT c.id, c.team, c.state, c.severity, c.share, c.discovery, c.steward, c.note, c.proposed_by, c.proposed_at,
	c.confirmed_by, c.confirmed_at, c.dispute_until, c.disputed_by, c.disputed_at, c.dispute_reason,
	c.resolved_by, c.resolved_at, c.resolution, c.evolved_by, c.evolved_at,
	c.mend_number, c.mended_at, c.mended_by, c.mend_by_steward, c.mend_confirmed_at, c.mend_reopened_at,
	k.id, left(k.title, 4096), k.provider, k.external_id, p.number,
	b.id, left(b.title, 4096), b.provider, b.external_id
	FROM card_cracks c JOIN work_items k ON k.id = c.card_work_item_id JOIN work_items b ON b.id = c.bug_work_item_id
	LEFT JOIN pull_requests p ON p.id = c.pull_request_id `

func scanCrack(row pgx.Row) (Crack, error) {
	var (
		c                          Crack
		id, cardID, bugID          int64
		steward                    string
		confirmedBy                *string
		mendNumber                 *int
		mendedAt                   *time.Time
		mendedBy                   *string
		mendBySteward              *bool
		mendConfirmed, mendReopen  *time.Time
		cardProvider, cardExternal string
		bugProvider, bugExternal   string
	)
	if err := row.Scan(&id, &c.Team, &c.State, &c.Severity, &c.Share, &c.Discovery, &steward, &c.Note, &c.ProposedBy, &c.ProposedAt,
		&confirmedBy, &c.ConfirmedAt, &c.DisputeUntil, &c.DisputedBy, &c.DisputedAt, &c.DisputeReason,
		&c.ResolvedBy, &c.ResolvedAt, &c.Resolution, &c.EvolvedBy, &c.EvolvedAt,
		&mendNumber, &mendedAt, &mendedBy, &mendBySteward, &mendConfirmed, &mendReopen,
		&cardID, &c.Card.Title, &cardProvider, &cardExternal, &c.Play,
		&bugID, &c.Bug.Title, &bugProvider, &bugExternal); err != nil {
		return Crack{}, err
	}
	c.ID = strconv.FormatInt(id, 10)
	c.Card.WorkItemID, c.Bug.WorkItemID = strconv.FormatInt(cardID, 10), strconv.FormatInt(bugID, 10)
	c.Card.ExternalRef, c.Bug.ExternalRef = itemRef(cardProvider, cardExternal), itemRef(bugProvider, bugExternal)
	if steward != "" {
		c.Steward = &steward
	}
	c.ConfirmedBy = []string{}
	if confirmedBy != nil {
		c.ConfirmedBy = []string{c.ProposedBy, *confirmedBy}
	}
	c.Disputed = c.State == "disputed"
	if mendedAt != nil && mendNumber != nil {
		m := &CrackMend{At: mendedAt.UTC(), PR: *mendNumber, ConfirmedAt: mendConfirmed, ReopenedAt: mendReopen}
		if mendedBy != nil {
			m.By = *mendedBy
		}
		if mendBySteward != nil {
			m.BySteward = *mendBySteward
		}
		c.Mended = m
	}
	return c, nil
}

func itemRef(provider, externalID string) string {
	if provider == "manual" || externalID == "" {
		return ""
	}
	return work.Reference(work.WorkItem{Provider: provider, ExternalID: externalID})
}

// Cracks lists the attributions in which Work Item id is the bug or the
// card, oldest first, within teams (nil means every team).
func (s *Store) Cracks(ctx context.Context, id int64, teams []string) ([]Crack, error) {
	var team string
	err := s.pool.QueryRow(ctx, `SELECT team FROM work_items WHERE id = $1 AND ($2::text[] IS NULL OR team = ANY($2))`, id, teams).Scan(&team)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrOperatorNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, crackSelect+`WHERE (c.bug_work_item_id = $1 OR c.card_work_item_id = $1)
		AND ($2::text[] IS NULL OR c.team = ANY($2)) ORDER BY c.id LIMIT $3`, id, teams, maxCracksListed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Crack{}
	for rows.Next() {
		c, err := scanCrack(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) crack(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id int64) (Crack, error) {
	c, err := scanCrack(q.QueryRow(ctx, crackSelect+`WHERE c.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Crack{}, ErrOperatorNotFound
	}
	return c, err
}

// CrackCandidates are the earlier merged plays a bug Work Item's fix may
// have been caused by (ADR-0052): plays of other Work Items of the bug's
// team, in a repository the fix touched, merged within CandidateWindow
// before the bug Work Item was created, that touched a path the fix touched.
// They are ranked by how many paths they share with the fix, then newest
// first. Ploeg only proposes them; a crack needs people.
type CrackCandidates struct {
	Bug               CrackItem        `json:"bug"`
	FixFiles          int              `json:"fixFiles"`
	FixFilesTruncated bool             `json:"fixFilesTruncated"`
	Since             time.Time        `json:"since"`
	Until             time.Time        `json:"until"`
	Candidates        []CrackCandidate `json:"candidates"`
}

// CrackCandidate is one earlier play that shares paths with the fix. Share
// is SharedFiles over the fix's file count. Attribution is the state of an
// existing attribution of this card to the bug, if any.
type CrackCandidate struct {
	Card        CrackItem `json:"card"`
	Play        int       `json:"play"`
	Repo        string    `json:"repo"`
	MergedAt    time.Time `json:"mergedAt"`
	MergedBy    string    `json:"mergedBy,omitempty"`
	SharedFiles int       `json:"sharedFiles"`
	Share       float64   `json:"share"`
	Files       []string  `json:"files"`
	Reverted    bool      `json:"reverted"`
	Attribution *string   `json:"attribution"`
}

// CrackCandidates lists the candidates for bug Work Item id within teams.
func (s *Store) CrackCandidates(ctx context.Context, id int64, teams []string) (CrackCandidates, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return CrackCandidates{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var out CrackCandidates
	var provider, external, team string
	var created time.Time
	err = tx.QueryRow(ctx, `SELECT id::text, left(title, 4096), provider, external_id, team, created_at FROM work_items
		WHERE id = $1 AND ($2::text[] IS NULL OR team = ANY($2))`, id, teams).
		Scan(&out.Bug.WorkItemID, &out.Bug.Title, &provider, &external, &team, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return CrackCandidates{}, ErrOperatorNotFound
	}
	if err != nil {
		return CrackCandidates{}, err
	}
	out.Bug.ExternalRef = itemRef(provider, external)
	out.Until = created.UTC()
	out.Since = out.Until.Add(-CandidateWindow)
	out.Candidates = []CrackCandidate{}
	if err := tx.QueryRow(ctx, `SELECT count(DISTINCT f.path), COALESCE(bool_or(p.files_truncated), false)
		FROM pull_requests p LEFT JOIN pull_request_files f ON f.pull_request_id = p.id WHERE p.work_item_id = $1`, id).
		Scan(&out.FixFiles, &out.FixFilesTruncated); err != nil {
		return CrackCandidates{}, err
	}
	if out.FixFiles == 0 {
		return out, tx.Commit(ctx)
	}
	rows, err := tx.Query(ctx, `WITH fix AS (
			SELECT DISTINCT p.forge, lower(p.repo_owner) AS owner, lower(p.repo_name) AS name, f.path
			FROM pull_requests p JOIN pull_request_files f ON f.pull_request_id = p.id WHERE p.work_item_id = $1
		), hits AS (
			SELECT p.id, p.work_item_id, p.number, p.repo_owner || '/' || p.repo_name AS repo, p.merged_at, COALESCE(p.merged_by, '') AS merged_by,
				array_agg(DISTINCT f.path ORDER BY f.path) AS paths
			FROM pull_requests p
			JOIN work_items i ON i.id = p.work_item_id
			JOIN pull_request_files f ON f.pull_request_id = p.id
			JOIN fix ON fix.forge = p.forge AND fix.owner = lower(p.repo_owner) AND fix.name = lower(p.repo_name) AND fix.path = f.path
			WHERE p.state = 'merged' AND p.merged_at IS NOT NULL AND p.merged_at <= $2 AND p.merged_at > $3
			  AND p.work_item_id <> $1 AND i.team = $4
			GROUP BY p.id
		)
		SELECT h.work_item_id, left(i.title, 4096), i.provider, i.external_id, h.number, h.repo, h.merged_at, h.merged_by,
			cardinality(h.paths), h.paths[1:$5],
			EXISTS (SELECT 1 FROM pull_request_reverts r WHERE r.pull_request_id = h.id),
			(SELECT c.state FROM card_cracks c WHERE c.card_work_item_id = h.work_item_id AND c.bug_work_item_id = $1)
		FROM hits h JOIN work_items i ON i.id = h.work_item_id
		ORDER BY cardinality(h.paths) DESC, h.merged_at DESC, h.id DESC LIMIT $6`,
		id, out.Until, out.Since, team, maxCandidateFiles, maxCandidates)
	if err != nil {
		return CrackCandidates{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var c CrackCandidate
		var cardID int64
		var cardProvider, cardExternal string
		if err := rows.Scan(&cardID, &c.Card.Title, &cardProvider, &cardExternal, &c.Play, &c.Repo, &c.MergedAt, &c.MergedBy,
			&c.SharedFiles, &c.Files, &c.Reverted, &c.Attribution); err != nil {
			return CrackCandidates{}, err
		}
		c.Card.WorkItemID = strconv.FormatInt(cardID, 10)
		c.Card.ExternalRef = itemRef(cardProvider, cardExternal)
		c.MergedAt = c.MergedAt.UTC()
		c.Share = float64(c.SharedFiles) / float64(out.FixFiles)
		if c.Files == nil {
			c.Files = []string{}
		}
		out.Candidates = append(out.Candidates, c)
	}
	if err := rows.Err(); err != nil {
		return CrackCandidates{}, err
	}
	return out, tx.Commit(ctx)
}

// Actor is the person behind an attribution step and how the audit log
// names them. Person is compared, ignoring case, with the steward and the
// other people in the flow; Audit is the audit log actor.
type Actor struct {
	Person string
	Audit  string
}

// CrackProposal is the fixer naming the card whose play caused the bug
// Work Item. Play zero means the card's latest play merged before the bug
// Work Item was created. Discovery is discovered or concealed and empty
// means discovered; it is self whatever was asked when the fixer is the
// card's steward.
type CrackProposal struct {
	Bug       int64
	Card      int64
	Play      int
	Severity  string
	Share     string
	Discovery string
	Note      string
	By        Actor
	Teams     []string
	Bots      []string
}

var severities = map[string]int{"S1": 1, "S2": 2, "S3": 3, "S4": 4}

func validSeverity(s string) bool { _, ok := severities[s]; return ok }

func validShare(s string) bool { return s == "primary" || s == "contributing" }

func severityFloor(s string, reverted bool) string {
	if reverted && severities[s] > 2 {
		return "S2"
	}
	return s
}

func samePerson(a, b string) bool { return a != "" && strings.EqualFold(a, b) }

// ProposeCrack records p as a proposed crack and returns it.
func (s *Store) ProposeCrack(ctx context.Context, p CrackProposal) (Crack, error) {
	if !validSeverity(p.Severity) || !validShare(p.Share) || p.Card == p.Bug || p.Play < 0 || len(p.Note) > 2000 ||
		(p.Discovery != "" && p.Discovery != "discovered" && p.Discovery != "concealed") || p.By.Person == "" {
		return Crack{}, refuse(AttributionInvalid, "A proposal names a card, a severity S1 to S4, a share primary or contributing and at most a 2000-character note.")
	}
	card, err := s.OperatorCard(ctx, p.Card, p.Teams, CardOptions{Bots: p.Bots})
	if err != nil {
		return Crack{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Crack{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var bugTeam string
	var bugCreated time.Time
	err = tx.QueryRow(ctx, `SELECT team, created_at FROM work_items WHERE id = $1 AND ($2::text[] IS NULL OR team = ANY($2)) FOR UPDATE`,
		p.Bug, p.Teams).Scan(&bugTeam, &bugCreated)
	if errors.Is(err, pgx.ErrNoRows) {
		return Crack{}, ErrOperatorNotFound
	}
	if err != nil {
		return Crack{}, err
	}
	if card.Team != bugTeam {
		return Crack{}, ErrOperatorNotFound
	}
	var play *CardPlay
	for i := range card.Plays {
		cp := &card.Plays[i]
		if cp.State != "merged" || cp.MergedAt == nil || (p.Play != 0 && cp.Number != p.Play) {
			continue
		}
		if p.Play == 0 && cp.MergedAt.After(bugCreated) {
			continue
		}
		if play == nil || cp.MergedAt.After(*play.MergedAt) {
			play = cp
		}
	}
	if play == nil {
		return Crack{}, refuse(AttributionNotMerged, "The card has no merged play to attribute the bug to.")
	}
	if play.MergedAt.After(bugCreated) {
		return Crack{}, refuse(AttributionAfterBug, "The play merged after the bug Work Item was created, so it cannot have caused it.")
	}
	steward := ""
	if card.Steward != nil {
		steward = card.Steward.Name
	}
	discovery := p.Discovery
	if discovery == "" {
		discovery = "discovered"
	}
	if samePerson(p.By.Person, steward) {
		discovery = "self"
	}
	if discovery == "concealed" {
		var fixedBySteward bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pull_requests WHERE work_item_id = $1 AND state = 'merged'
			AND $2 <> '' AND lower(COALESCE(merged_by, '')) = lower($2))`, p.Bug, steward).Scan(&fixedBySteward); err != nil {
			return Crack{}, err
		}
		if !fixedBySteward {
			return Crack{}, refuse(AttributionConcealUnknown, "Concealed needs the card's steward to have merged the bug's fix.")
		}
	}
	var open int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM card_cracks WHERE bug_work_item_id = $1
		AND state IN ('proposed', 'confirmed', 'disputed')`, p.Bug).Scan(&open); err != nil {
		return Crack{}, err
	}
	if open >= MaxCracksPerBug {
		return Crack{}, refuse(AttributionLimit, "A bug cracks at most three cards; more than that is a systemic bug, not a card's.")
	}
	var playID int64
	if err := tx.QueryRow(ctx, `SELECT id FROM pull_requests WHERE work_item_id = $1 AND number = $2 AND state = 'merged'
		ORDER BY merged_at DESC NULLS LAST, id DESC LIMIT 1`, p.Card, play.Number).Scan(&playID); err != nil {
		return Crack{}, err
	}
	reverted := card.Grade != nil && card.Grade.Inputs.Reliability.Reverted != nil && *card.Grade.Inputs.Reliability.Reverted
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO card_cracks (team, card_work_item_id, pull_request_id, bug_work_item_id, state,
			severity, share, discovery, steward, note, proposed_by)
		VALUES ($1, $2, $3, $4, 'proposed', $5, $6, $7, left($8, 256), NULLIF($9, ''), left($10, 256))
		ON CONFLICT (card_work_item_id, bug_work_item_id) DO NOTHING RETURNING id`,
		bugTeam, p.Card, playID, p.Bug, severityFloor(p.Severity, reverted), p.Share, discovery, steward, p.Note, p.By.Person).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Crack{}, refuse(AttributionDuplicate, "This card is already attributed to this bug.")
	}
	if err != nil {
		return Crack{}, err
	}
	cardID := p.Card
	if err := audit(ctx, tx, p.By.Audit, "card.crack_proposed", &cardID, map[string]any{
		"crackId": id, "bugWorkItemId": p.Bug, "pr": play.Number, "severity": severityFloor(p.Severity, reverted),
		"share": p.Share, "discovery": discovery}); err != nil {
		return Crack{}, err
	}
	if _, err := syncMends(ctx, tx, p.Bug, p.By.Audit); err != nil {
		return Crack{}, err
	}
	c, err := s.crack(ctx, tx, id)
	if err != nil {
		return Crack{}, err
	}
	return c, tx.Commit(ctx)
}

// CrackDecision is a person acting on one proposed or confirmed crack.
// Severity and Share may be changed by the confirmer; Reason is the
// steward's dispute; Resolution is the referee's upheld or unlinked.
// Referees names, per team, the only people who may resolve that team's
// disputes; a team absent or empty there lets anyone uninvolved resolve.
type CrackDecision struct {
	Crack      int64
	By         Actor
	Teams      []string
	Severity   string
	Share      string
	Reason     string
	Resolution string
	Note       string
	Referees   map[string][]string
	Now        time.Time
}

type crackRow struct {
	id, card, bug     int64
	team              string
	state, steward    string
	proposedBy        string
	confirmedBy       string
	disputedBy        string
	severity          string
	share             string
	disputeUntil      *time.Time
	revertedPlayCount int
	resolution        string
}

func lockCrack(ctx context.Context, tx pgx.Tx, id int64, teams []string) (crackRow, error) {
	var r crackRow
	err := tx.QueryRow(ctx, `SELECT c.id, c.card_work_item_id, c.bug_work_item_id, c.team, c.state, c.steward, c.proposed_by,
			COALESCE(c.confirmed_by, ''), COALESCE(c.disputed_by, ''), COALESCE(c.severity, ''), COALESCE(c.share, ''), c.dispute_until, COALESCE(c.resolution, ''),
			(SELECT count(*) FROM pull_request_reverts r JOIN pull_requests p ON p.id = r.pull_request_id WHERE p.work_item_id = c.card_work_item_id)
		FROM card_cracks c WHERE c.id = $1 AND ($2::text[] IS NULL OR c.team = ANY($2)) FOR UPDATE OF c`, id, teams).
		Scan(&r.id, &r.card, &r.bug, &r.team, &r.state, &r.steward, &r.proposedBy, &r.confirmedBy, &r.disputedBy, &r.severity, &r.share,
			&r.disputeUntil, &r.resolution, &r.revertedPlayCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return crackRow{}, ErrOperatorNotFound
	}
	return r, err
}

func decisionNow(d CrackDecision) time.Time {
	if d.Now.IsZero() {
		return time.Now().UTC()
	}
	return d.Now.UTC()
}

// AddWorkdays returns t moved n working days (Monday to Friday, in UTC)
// later.
func AddWorkdays(t time.Time, n int) time.Time {
	t = t.UTC()
	for n > 0 {
		t = t.Add(24 * time.Hour)
		if wd := t.Weekday(); wd != time.Saturday && wd != time.Sunday {
			n--
		}
	}
	return t
}

func (s *Store) decide(ctx context.Context, d CrackDecision, apply func(tx pgx.Tx, r crackRow, now time.Time) (string, map[string]any, error)) (Crack, error) {
	if d.By.Person == "" {
		return Crack{}, refuse(AttributionInvalid, "An attribution step needs the person who takes it.")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Crack{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	r, err := lockCrack(ctx, tx, d.Crack, d.Teams)
	if err != nil {
		return Crack{}, err
	}
	now := decisionNow(d)
	action, detail, err := apply(tx, r, now)
	if err != nil {
		return Crack{}, err
	}
	detail["crackId"] = r.id
	detail["bugWorkItemId"] = r.bug
	card := r.card
	if err := audit(ctx, tx, d.By.Audit, action, &card, detail); err != nil {
		return Crack{}, err
	}
	c, err := s.crack(ctx, tx, r.id)
	if err != nil {
		return Crack{}, err
	}
	return c, tx.Commit(ctx)
}

// ConfirmCrack confirms a proposed crack. The confirmer is neither the
// card's steward nor the proposer; they may correct the severity and the
// share. A reverted card's crack is at least S2.
func (s *Store) ConfirmCrack(ctx context.Context, d CrackDecision) (Crack, error) {
	if (d.Severity != "" && !validSeverity(d.Severity)) || (d.Share != "" && !validShare(d.Share)) || len(d.Note) > 2000 {
		return Crack{}, refuse(AttributionInvalid, "Severity is S1 to S4 and share is primary or contributing.")
	}
	return s.decide(ctx, d, func(tx pgx.Tx, r crackRow, now time.Time) (string, map[string]any, error) {
		if r.state != "proposed" {
			return "", nil, refuse(AttributionState, "Only a proposed crack can be confirmed.")
		}
		if samePerson(d.By.Person, r.steward) || samePerson(d.By.Person, r.proposedBy) {
			return "", nil, refuse(AttributionForbidden, "The second person is neither the card's steward nor the proposer.")
		}
		severity, share := r.severity, r.share
		if d.Severity != "" {
			severity = d.Severity
		}
		if d.Share != "" {
			share = d.Share
		}
		severity = severityFloor(severity, r.revertedPlayCount > 0)
		until := AddWorkdays(now, DisputeWorkdays)
		if _, err := tx.Exec(ctx, `UPDATE card_cracks SET state = 'confirmed', confirmed_by = left($2, 256), confirmed_at = $3,
			dispute_until = $4, severity = $5, share = $6 WHERE id = $1`, r.id, d.By.Person, now, until, severity, share); err != nil {
			return "", nil, err
		}
		return "card.crack_confirmed", map[string]any{"severity": severity, "share": share}, nil
	})
}

// DisputeCrack records the card's steward disputing a confirmed crack within
// DisputeWorkdays working days of its confirmation. The crack keeps counting
// until a referee unlinks it.
func (s *Store) DisputeCrack(ctx context.Context, d CrackDecision) (Crack, error) {
	if strings.TrimSpace(d.Reason) == "" || len(d.Reason) > 2000 {
		return Crack{}, refuse(AttributionInvalid, "A dispute gives a reason of at most 2000 characters.")
	}
	return s.decide(ctx, d, func(tx pgx.Tx, r crackRow, now time.Time) (string, map[string]any, error) {
		if r.state != "confirmed" {
			return "", nil, refuse(AttributionState, "Only a confirmed crack can be disputed.")
		}
		if r.resolution != "" {
			return "", nil, refuse(AttributionState, "A referee already decided this crack; the decision is final.")
		}
		if !samePerson(d.By.Person, r.steward) {
			return "", nil, refuse(AttributionForbidden, "Only the card's steward disputes a crack.")
		}
		if r.disputeUntil != nil && now.After(*r.disputeUntil) {
			return "", nil, refuse(AttributionDisputeClosed, "The five working days to dispute this crack have passed.")
		}
		if _, err := tx.Exec(ctx, `UPDATE card_cracks SET state = 'disputed', disputed_by = left($2, 256), disputed_at = $3,
			dispute_reason = $4 WHERE id = $1`, r.id, d.By.Person, now, d.Reason); err != nil {
			return "", nil, err
		}
		return "card.crack_disputed", map[string]any{"reason": truncate(d.Reason, 4096)}, nil
	})
}

// ResolveCrack records a referee's decision on a disputed crack: upheld
// confirms it again, unlinked removes it from the card and keeps it as
// history. A referee is on the team's Referees when it names any, and is never
// the steward, the proposer, the confirmer or the disputer.
func (s *Store) ResolveCrack(ctx context.Context, d CrackDecision) (Crack, error) {
	if (d.Resolution != "upheld" && d.Resolution != "unlinked") || len(d.Note) > 2000 {
		return Crack{}, refuse(AttributionInvalid, "A resolution is upheld or unlinked.")
	}
	return s.decide(ctx, d, func(tx pgx.Tx, r crackRow, now time.Time) (string, map[string]any, error) {
		if r.state != "disputed" {
			return "", nil, refuse(AttributionState, "Only a disputed crack is resolved.")
		}
		for _, involved := range []string{r.steward, r.proposedBy, r.confirmedBy, r.disputedBy} {
			if samePerson(d.By.Person, involved) {
				return "", nil, refuse(AttributionForbidden, "A referee took no part in the crack.")
			}
		}
		if referees := d.Referees[r.team]; len(referees) > 0 {
			listed := false
			for _, ref := range referees {
				listed = listed || samePerson(d.By.Person, ref)
			}
			if !listed {
				return "", nil, refuse(AttributionForbidden, "This team names its referees, and this person is not one.")
			}
		}
		state := "confirmed"
		if d.Resolution == "unlinked" {
			state = "unlinked"
		}
		if _, err := tx.Exec(ctx, `UPDATE card_cracks SET state = $2, resolved_by = left($3, 256), resolved_at = $4, resolution = $5
			WHERE id = $1`, r.id, state, d.By.Person, now, d.Resolution); err != nil {
			return "", nil, err
		}
		detail := map[string]any{"resolution": d.Resolution}
		if d.Note != "" {
			detail["reason"] = truncate(d.Note, 4096)
		}
		return "card.crack_resolved", detail, nil
	})
}

// EvolvedMark says the bug Work Item is no defect of the card: the
// requirement changed. The card gets evolved and no crack.
type EvolvedMark struct {
	Bug   int64
	Card  int64
	Note  string
	By    Actor
	Teams []string
	Bots  []string
}

// MarkEvolved records m. The person is not the card's steward. A proposed
// attribution of the card to the bug becomes evolved; a confirmed, disputed
// or unlinked one is refused.
func (s *Store) MarkEvolved(ctx context.Context, m EvolvedMark) (Crack, error) {
	if m.Card == m.Bug || len(m.Note) > 2000 || m.By.Person == "" {
		return Crack{}, refuse(AttributionInvalid, "Name a card other than the bug and at most a 2000-character note.")
	}
	card, err := s.OperatorCard(ctx, m.Card, m.Teams, CardOptions{Bots: m.Bots})
	if err != nil {
		return Crack{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Crack{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var bugTeam string
	err = tx.QueryRow(ctx, `SELECT team FROM work_items WHERE id = $1 AND ($2::text[] IS NULL OR team = ANY($2)) FOR UPDATE`,
		m.Bug, m.Teams).Scan(&bugTeam)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && bugTeam != card.Team) {
		return Crack{}, ErrOperatorNotFound
	}
	if err != nil {
		return Crack{}, err
	}
	steward := ""
	if card.Steward != nil {
		steward = card.Steward.Name
	}
	var id int64
	var state, rowSteward string
	err = tx.QueryRow(ctx, `SELECT id, state, steward FROM card_cracks WHERE card_work_item_id = $1 AND bug_work_item_id = $2 FOR UPDATE`,
		m.Card, m.Bug).Scan(&id, &state, &rowSteward)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if samePerson(m.By.Person, steward) {
			return Crack{}, refuse(AttributionForbidden, "The card's steward does not decide that their own card evolved.")
		}
		if err := tx.QueryRow(ctx, `INSERT INTO card_cracks (team, card_work_item_id, bug_work_item_id, state, steward, note,
				proposed_by, evolved_by, evolved_at)
			VALUES ($1, $2, $3, 'evolved', left($4, 256), NULLIF($5, ''), left($6, 256), left($6, 256), now()) RETURNING id`,
			bugTeam, m.Card, m.Bug, steward, m.Note, m.By.Person).Scan(&id); err != nil {
			return Crack{}, err
		}
	case err != nil:
		return Crack{}, err
	case state != "proposed":
		return Crack{}, refuse(AttributionState, "Only a proposed attribution can be changed to evolved.")
	case samePerson(m.By.Person, rowSteward):
		return Crack{}, refuse(AttributionForbidden, "The card's steward does not decide that their own card evolved.")
	default:
		if _, err := tx.Exec(ctx, `UPDATE card_cracks SET state = 'evolved', evolved_by = left($2, 256), evolved_at = now(),
			note = COALESCE(NULLIF($3, ''), note) WHERE id = $1`, id, m.By.Person, m.Note); err != nil {
			return Crack{}, err
		}
	}
	cardID := m.Card
	if err := audit(ctx, tx, m.By.Audit, "card.crack_evolved", &cardID, map[string]any{"crackId": id, "bugWorkItemId": m.Bug}); err != nil {
		return Crack{}, err
	}
	c, err := s.crack(ctx, tx, id)
	if err != nil {
		return Crack{}, err
	}
	return c, tx.Commit(ctx)
}
