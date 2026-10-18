package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/notes"
)

// Errors of the note review queue (ADR-0122).
var (
	ErrNoteReviewDecided = errors.New("note review already decided")
	ErrInvalidDecision   = errors.New("decision does not fit this review")
	ErrNoteNotFound      = errors.New("note not found")
	ErrNoteCommandReused = errors.New("command id was used for a different note")
)

// insertNote queues n, which no Run wrote, for the notes graph. The key
// makes a repeat a no-op; it reports whether the note was new.
func insertNote(ctx context.Context, tx pgx.Tx, n notes.Note, workItemID int64, runID *int64) (bool, error) {
	if err := n.Validate(); err != nil {
		return false, err
	}
	root, err := strconv.ParseInt(n.RootWorkItem, 10, 64)
	if err != nil {
		return false, fmt.Errorf("note %s: root Work Item %q: %w", n.Key, n.RootWorkItem, err)
	}
	body, err := json.Marshal(harness.Learning{Type: n.Type, Title: n.Title, Description: n.Description,
		Resource: n.Resource, Tags: n.Tags, Body: n.Body})
	if err != nil {
		return false, err
	}
	var expires *time.Time
	if !n.ExpiresAt.IsZero() {
		expires = &n.ExpiresAt
	}
	suggested := n.SuggestedLevel
	if !suggested.Valid() {
		suggested = n.Level
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO work_item_note_outbox
			(note_key, run_id, work_item_id, root_work_item_id, repository, learning, suggested_scope, written_by,
			 level, client, subject, author_kind, author, trust, source_refs, observed_at, expires_at,
			 supersedes, promoted_from, evidence, approved_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
		ON CONFLICT (note_key) DO NOTHING`,
		n.Key, runID, workItemID, root, n.Repository, body, string(suggested), n.WrittenBy(),
		string(n.Level), n.Client, n.Subject, string(n.AuthorKind), n.Author, string(n.Trust), nonNil(n.SourceRefs),
		n.ObservedAt, expires, n.Supersedes, n.PromotedFrom, nonNil(n.Evidence), n.ApprovedBy)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// outboxNote reads one queued note by key, written or not.
func outboxNote(ctx context.Context, q pgx.Tx, key string) (PendingNote, error) {
	n, err := scanPendingNote(q.QueryRow(ctx, `SELECT `+pendingNoteColumns+` FROM work_item_note_outbox WHERE note_key = $1`, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return n, ErrNoteNotFound
	}
	return n, err
}

// ReviewOutcomeNote records, when a pull request ploegd delivered leaves
// review, a repository-level note saying how (ADR-0122): merged, rejected
// (closed after a reviewer asked for changes) or closed. A merge is also
// evidence: every open repository promotion of the Work Item's notes is
// applied with it. It does nothing for a Work Item without a repository or
// pull request record.
func (s *Store) ReviewOutcomeNote(ctx context.Context, workItemID int64, number int, merged bool, at time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var repository, mergedBy, lastReview, lastReviewer string
	var root int64
	err = tx.QueryRow(ctx, `
		SELECT w.target_forge || '/' || w.target_owner || '/' || w.target_repo, COALESCE(w.root_work_item_id, w.id),
		       COALESCE(p.merged_by, ''),
		       COALESCE((SELECT rv.state FROM pull_request_reviews rv WHERE rv.pull_request_id = p.id
		                 AND rv.state <> 'commented' ORDER BY rv.id DESC LIMIT 1), ''),
		       COALESCE((SELECT rv.reviewer FROM pull_request_reviews rv WHERE rv.pull_request_id = p.id
		                 AND rv.state <> 'commented' ORDER BY rv.id DESC LIMIT 1), '')
		FROM work_items w
		LEFT JOIN pull_requests p ON p.work_item_id = w.id AND p.number = $2
		WHERE w.id = $1 AND w.target_repo <> ''`, workItemID, number).
		Scan(&repository, &root, &mergedBy, &lastReview, &lastReviewer)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	outcome := notes.ReviewOutcome{
		Repository: repository, Number: number, Outcome: notes.OutcomeClosed,
		WorkItem: strconv.FormatInt(workItemID, 10), RootWorkItem: strconv.FormatInt(root, 10), At: at.UTC(),
	}
	switch {
	case merged:
		outcome.Outcome, outcome.By = notes.OutcomeMerged, mergedBy
	case lastReview == "changes_requested":
		outcome.Outcome, outcome.By = notes.OutcomeRejected, lastReviewer
	}
	if _, err := insertNote(ctx, tx, outcome.Note(), workItemID, nil); err != nil {
		return err
	}
	if merged {
		evidence := notes.Evidence{Kind: notes.EvidenceMergedPullRequest, Ref: repository + "#" + strconv.Itoa(number)}
		if err := acceptRepositoryPromotions(ctx, tx, workItemID, evidence, at); err != nil {
			return err
		}
	}
	if err := audit(ctx, tx, "ploegd:knowledge", "note.review_outcome", &workItemID,
		map[string]any{"pullRequest": number, "outcome": string(outcome.Outcome)}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// acceptRepositoryPromotions applies, on evidence, every open repository
// promotion of a Work Item's notes that no open contradiction holds.
func acceptRepositoryPromotions(ctx context.Context, tx pgx.Tx, workItemID int64, evidence notes.Evidence, at time.Time) error {
	rows, err := tx.Query(ctx, `
		SELECT r.id, r.note_key FROM note_reviews r
		WHERE r.kind = 'promotion' AND r.state = 'open' AND r.target_level = 'repository' AND r.work_item_id = $1
		  AND NOT EXISTS (SELECT 1 FROM note_reviews c WHERE c.kind = 'contradiction' AND c.state = 'open'
		                  AND (c.note_key = r.note_key OR r.note_key = ANY (c.conflicts_with)))
		ORDER BY r.id FOR UPDATE OF r`, workItemID)
	if err != nil {
		return err
	}
	type due struct {
		id  int64
		key string
	}
	var promotions []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.key); err != nil {
			rows.Close()
			return err
		}
		promotions = append(promotions, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range promotions {
		original, err := outboxNote(ctx, tx, d.key)
		if err != nil {
			return err
		}
		promoted, err := notes.Promote(original.Note(), notes.LevelRepository, []notes.Evidence{evidence}, "", false, at)
		if err != nil {
			continue
		}
		if _, err := insertNote(ctx, tx, promoted, original.WorkItemID, original.RunID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE note_reviews SET state = 'accepted', decision = 'evidence',
			decided_by = 'ploegd:evidence', evidence = $2, decided_at = now(), applied_at = now() WHERE id = $1`,
			d.id, []string{evidence.String()}); err != nil {
			return err
		}
	}
	return nil
}

// FlagContradiction opens the review a person decides when note key, just
// written, disagrees with current notes on its subject. A repeat for the
// same note changes nothing.
func (s *Store) FlagContradiction(ctx context.Context, key string, conflicts []string) error {
	if len(conflicts) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO note_reviews (kind, note_key, work_item_id, repository, client, subject, conflicts_with)
		SELECT 'contradiction', note_key, work_item_id, repository, client, subject, $2
		FROM work_item_note_outbox WHERE note_key = $1
		ON CONFLICT DO NOTHING`, key, conflicts)
	return err
}

// ContestedKeys reports which of keys an open contradiction holds, either
// side.
func (s *Store) ContestedKeys(ctx context.Context, keys []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT k FROM unnest($1::text[]) k
		WHERE EXISTS (SELECT 1 FROM note_reviews c WHERE c.kind = 'contradiction' AND c.state = 'open'
		              AND (c.note_key = k OR k = ANY (c.conflicts_with)))`, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

// NoteReview is one promotion or contradiction a person decides.
type NoteReview struct {
	ID            int64      `json:"id,string"`
	Kind          string     `json:"kind"`
	NoteKey       string     `json:"noteKey"`
	WorkItemID    int64      `json:"workItemId,string"`
	Team          string     `json:"team"`
	Repository    string     `json:"repository"`
	Subject       string     `json:"subject"`
	TargetLevel   string     `json:"targetLevel,omitempty"`
	ConflictsWith []string   `json:"conflictsWith"`
	Evidence      []string   `json:"evidence"`
	State         string     `json:"state"`
	Decision      string     `json:"decision,omitempty"`
	DecidedBy     string     `json:"decidedBy,omitempty"`
	Reason        string     `json:"reason,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	DecidedAt     *time.Time `json:"decidedAt"`
	Applied       bool       `json:"applied"`
	// Title and AuthorKind describe the note under review. The title is the
	// agent's own text when a Run wrote it.
	Title      string `json:"title"`
	AuthorKind string `json:"authorKind"`
	Trust      string `json:"trust"`
}

const noteReviewColumns = `r.id, r.kind, r.note_key, r.work_item_id, w.team, r.repository, r.subject, r.target_level,
	r.conflicts_with, r.evidence, r.state, r.decision, r.decided_by, r.reason, r.created_at, r.decided_at,
	r.applied_at IS NOT NULL, COALESCE(o.learning->>'title', ''), COALESCE(o.author_kind, ''), COALESCE(o.trust, '')`

func scanNoteReview(row pgx.Row) (NoteReview, error) {
	var r NoteReview
	err := row.Scan(&r.ID, &r.Kind, &r.NoteKey, &r.WorkItemID, &r.Team, &r.Repository, &r.Subject, &r.TargetLevel,
		&r.ConflictsWith, &r.Evidence, &r.State, &r.Decision, &r.DecidedBy, &r.Reason, &r.CreatedAt, &r.DecidedAt,
		&r.Applied, &r.Title, &r.AuthorKind, &r.Trust)
	return r, err
}

// NoteReviews lists the reviews of Work Items in teams (nil: every Team),
// newest first: the open ones, or every one when all is set.
func (s *Store) NoteReviews(ctx context.Context, teams []string, all bool, limit int) ([]NoteReview, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+noteReviewColumns+`
		FROM note_reviews r JOIN work_items w ON w.id = r.work_item_id
		LEFT JOIN work_item_note_outbox o ON o.note_key = r.note_key
		WHERE ($1::text[] IS NULL OR w.team = ANY ($1)) AND ($2 OR r.state = 'open')
		ORDER BY r.id DESC LIMIT $3`, teams, all, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NoteReview{}
	for rows.Next() {
		r, err := scanNoteReview(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Note review decisions.
const (
	DecisionAccept    = "accept"
	DecisionReject    = "reject"
	DecisionKeepNewer = "keep_newer"
	DecisionKeepOlder = "keep_older"
	DecisionKeepBoth  = "keep_both"
)

// DecideNoteReview records a person's decision on review id, in teams.
//
// Accepting a repository promotion queues the promoted note at once;
// accepting a Client or Tenant promotion waits for the review actor to write
// the concept to the knowledge graph. A rejected promotion is kept, so the
// same suggestion is not raised again. A contradiction is resolved by
// keeping the newer note, the older notes or both; a note that is not kept
// gets a retraction the person wrote, which takes it out of every briefing
// and leaves it in history.
func (s *Store) DecideNoteReview(ctx context.Context, id int64, decision, person, reason string, teams []string) (NoteReview, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return NoteReview{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	r, err := scanNoteReview(tx.QueryRow(ctx, `
		SELECT `+noteReviewColumns+`
		FROM note_reviews r JOIN work_items w ON w.id = r.work_item_id
		LEFT JOIN work_item_note_outbox o ON o.note_key = r.note_key
		WHERE r.id = $1 FOR UPDATE OF r`, id))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !teamAllowed(teams, r.Team)) {
		return NoteReview{}, ErrOperatorNotFound
	}
	if err != nil {
		return NoteReview{}, err
	}
	if r.State != "open" {
		return r, ErrNoteReviewDecided
	}
	now := time.Now().UTC()
	state, applied := "resolved", false
	switch {
	case r.Kind == "promotion" && decision == DecisionReject:
		state = "rejected"
	case r.Kind == "promotion" && decision == DecisionAccept:
		state = "accepted"
		original, err := outboxNote(ctx, tx, r.NoteKey)
		if err != nil {
			return r, err
		}
		contested, err := contestedIn(ctx, tx, r.NoteKey)
		if err != nil {
			return r, err
		}
		promoted, err := notes.Promote(original.Note(), notes.Level(r.TargetLevel), nil, person, contested, now)
		if err != nil {
			return r, fmt.Errorf("%w: %v", ErrInvalidDecision, err)
		}
		if promoted.Level.InNotesGraph() {
			if _, err := insertNote(ctx, tx, promoted, original.WorkItemID, original.RunID); err != nil {
				return r, err
			}
			applied = true
		}
	case r.Kind == "contradiction" && (decision == DecisionKeepNewer || decision == DecisionKeepOlder || decision == DecisionKeepBoth):
		var retract []string
		switch decision {
		case DecisionKeepNewer:
			retract = r.ConflictsWith
		case DecisionKeepOlder:
			retract = []string{r.NoteKey}
		}
		for _, key := range retract {
			if err := retractNote(ctx, tx, id, key, person, reason, now); err != nil {
				return r, err
			}
		}
	default:
		return r, ErrInvalidDecision
	}
	var appliedAt *time.Time
	if applied {
		appliedAt = &now
	}
	if _, err := tx.Exec(ctx, `UPDATE note_reviews SET state = $2, decision = $3, decided_by = $4, reason = $5,
		decided_at = $6, applied_at = COALESCE($7, applied_at) WHERE id = $1`,
		id, state, decision, person, reason, now, appliedAt); err != nil {
		return r, err
	}
	if err := audit(ctx, tx, person, "note.review_decided", &r.WorkItemID,
		map[string]any{"review": id, "kind": r.Kind, "decision": decision, "note": r.NoteKey, "reason": reason}); err != nil {
		return r, err
	}
	if err := tx.Commit(ctx); err != nil {
		return r, err
	}
	r.State, r.Decision, r.DecidedBy, r.Reason, r.DecidedAt, r.Applied = state, decision, person, reason, &now, r.Applied || applied
	return r, nil
}

func contestedIn(ctx context.Context, tx pgx.Tx, key string) (bool, error) {
	var contested bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM note_reviews c WHERE c.kind = 'contradiction'
		AND c.state = 'open' AND (c.note_key = $1 OR $1 = ANY (c.conflicts_with)))`, key).Scan(&contested)
	return contested, err
}

func retractNote(ctx context.Context, tx pgx.Tx, review int64, key, person, reason string, now time.Time) error {
	loser, err := outboxNote(ctx, tx, key)
	if errors.Is(err, ErrNoteNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	l := loser.Note()
	body := "A person decided this note no longer holds."
	if reason = strings.TrimSpace(reason); reason != "" {
		body += " Their reason: " + reason
	}
	_, err = insertNote(ctx, tx, notes.Note{
		Key:          "retraction/" + strconv.FormatInt(review, 10) + "/" + key,
		RootWorkItem: l.RootWorkItem, WorkItem: l.WorkItem, Repository: l.Repository, Client: l.Client,
		Type: notes.TypeRetraction, Title: "Retracted: " + l.Title, Body: body,
		SuggestedLevel: l.Level, Level: l.Level, Subject: l.Subject,
		AuthorKind: notes.AuthorPerson, Author: person, Trust: notes.Trusted,
		SourceRefs: []string{"note-review:" + strconv.FormatInt(review, 10), "note:" + key},
		ObservedAt: now, WrittenAt: now, Supersedes: key, ApprovedBy: person,
	}, loser.WorkItemID, nil)
	return err
}

// PersonNote is a note a person writes through an operator consumer.
type PersonNote struct {
	Consumer   string
	CommandID  string
	Person     string
	WorkItemID int64
	Level      notes.Level
	Learning   harness.Learning
}

// QueuePersonNote stores a person's note on a Work Item in teams (ADR-0122).
// It is trusted and attributed to the person; at repository level the
// person's own words are their approval. A repeated command answers the
// first note's key and replayed true; the same command with other content
// answers ErrCommandConflict.
func (s *Store) QueuePersonNote(ctx context.Context, p PersonNote, teams []string) (string, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var team, repository string
	var root int64
	err = tx.QueryRow(ctx, `SELECT team, COALESCE(root_work_item_id, id),
		CASE WHEN target_repo = '' THEN '' ELSE target_forge || '/' || target_owner || '/' || target_repo END
		FROM work_items WHERE id = $1`, p.WorkItemID).Scan(&team, &root, &repository)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !teamAllowed(teams, team)) {
		return "", false, ErrOperatorNotFound
	}
	if err != nil {
		return "", false, err
	}
	key := "person/" + p.Consumer + "/" + p.CommandID
	now := time.Now().UTC()
	l := p.Learning
	n := notes.Note{
		Key: key, RootWorkItem: strconv.FormatInt(root, 10), WorkItem: strconv.FormatInt(p.WorkItemID, 10),
		Repository: repository, Type: l.Type, Title: l.Title, Description: l.Description, Resource: l.Resource,
		Tags: l.Tags, Body: l.Body, SuggestedLevel: p.Level, Level: p.Level, Subject: noteSubject(l),
		AuthorKind: notes.AuthorPerson, Author: p.Person, Trust: notes.Trusted,
		SourceRefs: []string{"person:" + p.Person, "work-item:" + strconv.FormatInt(p.WorkItemID, 10)},
		ObservedAt: now, WrittenAt: now, Supersedes: clipText(l.Supersedes, 300),
	}
	if l.ExpiresAt != nil {
		n.ExpiresAt = l.ExpiresAt.UTC()
	}
	if p.Level == notes.LevelRepository {
		n.ApprovedBy = p.Person
	}
	fresh, err := insertNote(ctx, tx, n, p.WorkItemID, nil)
	if err != nil {
		return "", false, err
	}
	if !fresh {
		existing, err := outboxNote(ctx, tx, key)
		if err != nil {
			return "", false, err
		}
		if existing.Author != p.Person || existing.Learning.Body != l.Body || existing.Learning.Title != l.Title || existing.Level != string(p.Level) {
			return "", false, ErrNoteCommandReused
		}
		return key, true, nil
	}
	if err := audit(ctx, tx, "operator:"+p.Consumer+":"+p.Person, "note.person_written", &p.WorkItemID,
		map[string]any{"note": key, "level": string(p.Level)}); err != nil {
		return "", false, err
	}
	return key, false, tx.Commit(ctx)
}

// ConceptPromotion is an accepted Client or Tenant promotion the review actor
// still has to write to the knowledge graph.
type ConceptPromotion struct {
	ReviewID    int64
	TargetLevel notes.Level
	ApprovedBy  string
	Evidence    []string
	DecidedAt   time.Time
	Note        notes.Note
}

// AcceptedConceptPromotions lists, oldest first, at most limit accepted
// Client or Tenant promotions not yet applied.
func (s *Store) AcceptedConceptPromotions(ctx context.Context, limit int) ([]ConceptPromotion, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, target_level, decided_by, evidence, decided_at, note_key FROM note_reviews
		WHERE kind = 'promotion' AND state = 'accepted' AND applied_at IS NULL AND target_level IN ('client', 'tenant')
		ORDER BY id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	type row struct {
		ConceptPromotion
		key string
	}
	var due []row
	for rows.Next() {
		var r row
		var level string
		var decided *time.Time
		if err := rows.Scan(&r.ReviewID, &level, &r.ApprovedBy, &r.Evidence, &decided, &r.key); err != nil {
			rows.Close()
			return nil, err
		}
		r.TargetLevel = notes.Level(level)
		if decided != nil {
			r.DecidedAt = decided.UTC()
		}
		due = append(due, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []ConceptPromotion
	for _, r := range due {
		n, err := scanPendingNote(s.pool.QueryRow(ctx, `SELECT `+pendingNoteColumns+` FROM work_item_note_outbox WHERE note_key = $1`, r.key))
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		r.Note = n.Note()
		out = append(out, r.ConceptPromotion)
	}
	return out, nil
}

// MarkPromotionApplied records that the knowledge graph holds review id's
// concept.
func (s *Store) MarkPromotionApplied(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE note_reviews SET applied_at = now(), last_error = '' WHERE id = $1 AND applied_at IS NULL`, id)
	return err
}

// RecordPromotionError keeps why review id's concept was not written yet.
func (s *Store) RecordPromotionError(ctx context.Context, id int64, cause error) error {
	_, err := s.pool.Exec(ctx, `UPDATE note_reviews SET last_error = $2 WHERE id = $1`, id, clipText(cause.Error(), 1000))
	return err
}

// PurgeClientNotes removes everything Ploeg's store keeps of client's notes:
// the outbox rows, their text included, and their reviews. The graphs are
// purged separately (ADR-0122). It refuses an empty Client.
func (s *Store) PurgeClientNotes(ctx context.Context, client, actor string) (int64, error) {
	if strings.TrimSpace(client) == "" {
		return 0, errors.New("a purge names one Client")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `DELETE FROM note_reviews WHERE client = $1`, client); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM work_item_note_outbox WHERE client = $1`, client)
	if err != nil {
		return 0, err
	}
	if err := audit(ctx, tx, actor, "note.client_purged", nil, map[string]any{"client": client, "notes": tag.RowsAffected()}); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), tx.Commit(ctx)
}
