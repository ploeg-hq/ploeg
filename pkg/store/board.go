package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/notes"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// ErrNoBoardVersions is returned for a Run whose claim recorded no version
// vector: one claimed before ADR-0124, so nothing can be compared with it.
var ErrNoBoardVersions = errors.New("the Run recorded no board versions")

const (
	maxBriefedItems   = 200
	maxBriefedKeyLen  = 256
	maxGraphCommitLen = 128
	boardTextLimit    = 1500
	boardNoteLimit    = 600
)

var forgeCommit = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// RecordBoardClaim records the half of a Run's version vector ploegd knows
// at the claim: Ploeg's audit sequence and the Work Item's tracker revision
// (ADR-0124). It writes once; a later call returns what the first recorded.
func (s *Store) RecordBoardClaim(ctx context.Context, runToken string) (work.BoardVersions, error) {
	var v work.BoardVersions
	err := s.pool.QueryRow(ctx, `UPDATE agent_runs r SET board_claimed_at = now(),
			board_ploeg_sequence = (SELECT COALESCE(max(id), 0) FROM audit_log),
			board_tracker_revision = w.revision
		FROM work_items w
		WHERE w.id = r.work_item_id AND r.run_token = $1 AND r.board_claimed_at IS NULL
		RETURNING r.board_ploeg_sequence, r.board_tracker_revision`, runToken).Scan(&v.PloegSequence, &v.TrackerRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		var seq *int64
		var rev *string
		err = s.pool.QueryRow(ctx, `SELECT board_ploeg_sequence, board_tracker_revision FROM agent_runs WHERE run_token = $1`, runToken).Scan(&seq, &rev)
		if errors.Is(err, pgx.ErrNoRows) {
			return v, ErrUnknownRun
		}
		if seq != nil {
			v.PloegSequence = *seq
		}
		if rev != nil {
			v.TrackerRevision = *rev
		}
	}
	return v, err
}

// recordBriefing stores what a worker reported its Run's briefing was read
// at and held, once. A value outside its bounds is left out, never stored.
func recordBriefing(ctx context.Context, tx pgx.Tx, runToken string, b *work.BoardBriefing) error {
	if b == nil {
		return nil
	}
	forge := b.ForgeCommit
	if !forgeCommit.MatchString(forge) {
		forge = ""
	}
	sections, err := json.Marshal(boundedSections(b.Sections))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE agent_runs SET board_briefed_at = now(),
			board_forge_commit = NULLIF($2, ''), board_notes_commit = NULLIF($3, ''), board_concepts_commit = NULLIF($4, ''),
			briefed_notes = $5, briefed_concepts = $6, briefing_sections = $7
		WHERE run_token = $1 AND board_claimed_at IS NOT NULL AND board_briefed_at IS NULL`,
		runToken, forge, graphCommit(b.NotesCommit), graphCommit(b.ConceptsCommit),
		boundedKeys(b.Notes), boundedKeys(b.Concepts), sections)
	return err
}

func graphCommit(c string) string {
	if len(c) > maxGraphCommitLen {
		return ""
	}
	return c
}

func boundedKeys(keys []string) []string {
	out := make([]string, 0, min(len(keys), maxBriefedItems))
	for _, k := range keys {
		if len(out) == maxBriefedItems {
			break
		}
		if k != "" && len(k) <= maxBriefedKeyLen {
			out = append(out, k)
		}
	}
	return out
}

func boundedSections(sections []work.BriefingSection) []work.BriefingSection {
	out := make([]work.BriefingSection, 0, len(sections))
	for _, s := range sections {
		if len(out) == 8 {
			break
		}
		switch s.Name {
		case work.SectionFindings, work.SectionWorkItem, work.SectionRepository, work.SectionClient, work.SectionTenant,
			work.SectionReviewFindings, work.SectionQuestions, work.SectionActiveWork:
			out = append(out, s)
		}
	}
	return out
}

// boardRun is a running Run's recorded board versions and its Work Item as
// ploegd holds it now.
type boardRun struct {
	id, workItemID, shiftID, rootID int64
	round                           int
	claimedAt                       time.Time
	since                           work.BoardVersions
	briefedNotes                    []string
	revision, title, description    string
	repository                      string
}

func (s *Store) loadBoardRun(ctx context.Context, runToken string) (boardRun, error) {
	var r boardRun
	var claimed *time.Time
	var seq *int64
	err := s.pool.QueryRow(ctx, `SELECT r.id, r.work_item_id, COALESCE(r.shift_id, 0), r.round, r.board_claimed_at, r.board_ploeg_sequence,
			COALESCE(r.board_tracker_revision, ''), COALESCE(r.board_forge_commit, ''), COALESCE(r.board_notes_commit, ''),
			COALESCE(r.board_concepts_commit, ''), COALESCE(r.briefed_notes, '{}'),
			COALESCE(w.root_work_item_id, w.id), w.revision, w.title, w.description,
			CASE WHEN w.target_repo = '' THEN '' ELSE w.target_forge || '/' || w.target_owner || '/' || w.target_repo END
		FROM agent_runs r JOIN work_items w ON w.id = r.work_item_id
		WHERE r.run_token = $1 AND r.state = 'running'`, runToken).Scan(&r.id, &r.workItemID, &r.shiftID, &r.round, &claimed, &seq,
		&r.since.TrackerRevision, &r.since.ForgeCommit, &r.since.NotesCommit, &r.since.ConceptsCommit, &r.briefedNotes,
		&r.rootID, &r.revision, &r.title, &r.description, &r.repository)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrUnknownRun
	}
	if err != nil {
		return r, err
	}
	if claimed == nil || seq == nil {
		return r, ErrNoBoardVersions
	}
	r.claimedAt, r.since.PloegSequence = *claimed, *seq
	return r, nil
}

// quietActions are audit actions that tell a running agent nothing about
// its work: its own progress, the gateway's bookkeeping and Lease upkeep.
// Outcomes are left out too, so Runs of one Round never observe each other
// (ADR-0010); a person's note is served with its text instead.
const quietActions = `a.action NOT LIKE 'checkpoint.%' AND a.action NOT LIKE 'llm.%' AND a.action NOT LIKE 'lease.%'
	AND a.action NOT LIKE 'outcome.%' AND a.action NOT IN ('run.claimed', '` + AuditOperatorNoteAdded + `', '` + AuditOperatorNoteConsumed + `')`

// BoardChanges answers what changed on the board since the running Run's
// briefing (ADR-0124): Ploeg's own events for the Work Item, instructions
// people left, notes on its root Work Item or repository that it may read
// (notes.ForRun), answers people gave to its Work Item's questions,
// contradictions flagged on notes it was briefed with, the tracker task's
// revision, the knowledge graph commit (conceptsCommit is the commit ploegd
// read now, "" when unknown) and the forge head the worker read. Notes of the
// Run's own Round are left out, so its siblings stay unobserved. A catch-up
// with changes, and every before_finish, is recorded on the Run.
func (s *Store) BoardChanges(ctx context.Context, runToken string, req work.BoardChangesRequest, conceptsCommit string) (work.BoardChangesResponse, error) {
	run, err := s.loadBoardRun(ctx, runToken)
	if err != nil {
		return work.BoardChangesResponse{}, err
	}
	resp := work.BoardChangesResponse{Since: run.since, Current: run.since, Changes: []work.BoardChange{}}
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(max(id), 0) FROM audit_log`).Scan(&resp.Current.PloegSequence); err != nil {
		return resp, err
	}
	var changes []work.BoardChange
	events, err := s.boardEvents(ctx, run)
	if err != nil {
		return resp, err
	}
	changes = append(changes, events...)
	notes, err := s.boardOperatorNotes(ctx, run)
	if err != nil {
		return resp, err
	}
	changes = append(changes, notes...)
	for _, read := range []func(context.Context, boardRun) ([]work.BoardChange, error){
		s.boardWorkItemNotes, s.boardQuestionAnswers, s.boardContestedNotes,
	} {
		more, err := read(ctx, run)
		if err != nil {
			return resp, err
		}
		changes = append(changes, more...)
	}
	now := time.Now().UTC()
	resp.Current.TrackerRevision = run.revision
	if run.revision != run.since.TrackerRevision {
		changes = append(changes, work.BoardChange{Source: work.SourceTracker, Kind: "work_item_changed", At: now,
			Cite:      "tracker revision " + run.revision,
			Summary:   fmt.Sprintf("The tracker task changed since your briefing (revision %q, now %q). Its title and description now read as quoted below; check whether your work still matches it.", run.since.TrackerRevision, run.revision),
			Untrusted: clipRunes("Title: "+run.title+"\n\n"+run.description, boardTextLimit)})
	}
	if conceptsCommit != "" {
		resp.Current.ConceptsCommit = conceptsCommit
		if run.since.ConceptsCommit != "" && conceptsCommit != run.since.ConceptsCommit {
			changes = append(changes, work.BoardChange{Source: work.SourceGraph, Kind: "knowledge_changed", At: now,
				Cite:    "graph commit " + conceptsCommit,
				Summary: "The reviewed knowledge of this repository, Client or Tenant changed since your briefing. The next Run is briefed from it; nothing is required of you unless you rely on a knowledge document."})
		}
	}
	if forgeCommit.MatchString(req.ForgeHead) {
		resp.Current.ForgeCommit = req.ForgeHead
		if req.ForgeMovedByOthers && req.ForgeHead != run.since.ForgeCommit {
			changes = append(changes, work.BoardChange{Source: work.SourceForge, Kind: "branch_moved", At: now,
				Cite:    "commit " + req.ForgeHead,
				Summary: "The branch head is now " + req.ForgeHead + ", which holds commits this Run did not make. Fetch it and read what changed before you push or judge."})
		}
	}
	sort.SliceStable(changes, func(i, j int) bool { return changes[i].At.After(changes[j].At) })
	if len(changes) > work.MaxBoardChanges {
		resp.Omitted = len(changes) - work.MaxBoardChanges
		changes = changes[:work.MaxBoardChanges]
	}
	resp.Changes = append(resp.Changes, changes...)
	if err := s.recordCatchUp(ctx, run.id, req, resp); err != nil {
		return resp, err
	}
	return resp, nil
}

func (s *Store) boardEvents(ctx context.Context, run boardRun) ([]work.BoardChange, error) {
	rows, err := s.pool.Query(ctx, `SELECT a.id, a.at, a.action FROM audit_log a
		WHERE a.work_item_id = $1 AND a.id > $2 AND `+quietActions+`
		ORDER BY a.id DESC LIMIT $3`, run.workItemID, run.since.PloegSequence, work.MaxBoardChanges+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []work.BoardChange
	for rows.Next() {
		var id int64
		var at time.Time
		var action string
		if err := rows.Scan(&id, &at, &action); err != nil {
			return nil, err
		}
		out = append(out, work.BoardChange{Source: work.SourcePloeg, Kind: action, At: at.UTC(),
			Cite: "Ploeg event " + strconv.FormatInt(id, 10), Summary: eventSummary(action)})
	}
	return out, rows.Err()
}

func eventSummary(action string) string {
	switch action {
	case "work_item.withdrawn":
		return "The Work Item was withdrawn. Stop, push nothing more and report what you have."
	case "review.changes_requested":
		return "A reviewer requested changes on the pull request. Read the review before you push or judge."
	case "pull_request.conflicted":
		return "The pull request now conflicts with its base branch."
	case "pull_request.reverted":
		return "The pull request was reverted."
	case "work_item.refreshed":
		return "Ploeg re-read the tracker task."
	case "work_item.requeued":
		return "A person restarted the Work Item."
	}
	return "Ploeg recorded " + action + " for this Work Item."
}

func (s *Store) boardOperatorNotes(ctx context.Context, run boardRun) ([]work.BoardChange, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, actor, text, created_at FROM operator_notes
		WHERE work_item_id = $1 AND consumed_at IS NULL AND created_at > $2
		ORDER BY id DESC LIMIT $3`, run.workItemID, run.claimedAt, work.MaxBoardChanges+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []work.BoardChange
	for rows.Next() {
		var id int64
		var actor, text string
		var at time.Time
		if err := rows.Scan(&id, &actor, &text, &at); err != nil {
			return nil, err
		}
		out = append(out, work.BoardChange{Source: work.SourcePloeg, Kind: "operator_note", At: at.UTC(),
			Cite:      "operator note " + strconv.FormatInt(id, 10),
			Summary:   "A person (" + actor + ") left an instruction for this Work Item after your briefing. The next Run is given it too.",
			Untrusted: clipRunes(text, boardTextLimit)})
	}
	return out, rows.Err()
}

// boardWorkItemNotes is every note queued since the claim that the Run may
// read, through the same filter a briefing uses (notes.ForRun, ADR-0122):
// notes of its root Work Item and of its repository, written by people,
// Ploeg or Runs outside its own Round, current and not already briefed.
func (s *Store) boardWorkItemNotes(ctx context.Context, run boardRun) ([]work.BoardChange, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+pendingNoteColumns+` FROM work_item_note_outbox o
		WHERE o.created_at > $2 AND NOT (o.note_key = ANY($3::text[]))
			AND (o.root_work_item_id = $1 OR (o.level = 'repository' AND o.repository <> '' AND o.repository = $8))
			AND (o.run_id IS NULL OR (o.run_id <> $4
				AND NOT ($5 <> 0 AND o.run_id IN (SELECT id FROM agent_runs WHERE shift_id = $5 AND round = $6))))
		ORDER BY o.id DESC LIMIT $7`, run.rootID, run.claimedAt, run.briefedNotes, run.id, run.shiftID, run.round,
		4*work.MaxBoardChanges, run.repository)
	if err != nil {
		return nil, err
	}
	var all []notes.Note
	for rows.Next() {
		p, err := scanPendingNote(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, p.Note())
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(all))
	for _, n := range all {
		keys = append(keys, n.Key)
	}
	contested, err := s.ContestedKeys(ctx, keys)
	if err != nil {
		return nil, err
	}
	view := notes.ForRun(notes.Scope{RootWorkItem: strconv.FormatInt(run.rootID, 10), Repository: run.repository}, all, contested, time.Now())
	out := make([]work.BoardChange, 0, len(view.Notes))
	for _, n := range view.Notes {
		kind := "work_item_note"
		if n.AuthorKind == notes.AuthorPerson {
			kind = "person_note"
		}
		text := n.Title
		if n.Description != "" {
			text += "\n" + n.Description
		}
		out = append(out, work.BoardChange{Source: work.SourceGraph, Kind: kind, At: n.WrittenAt.UTC(),
			Cite:      "note " + n.Key,
			Summary:   "A note was written after your briefing. " + notes.Provenance(n),
			Untrusted: clipRunes(text+"\n\n"+n.Body, boardNoteLimit)})
	}
	return out, nil
}

// boardQuestionAnswers is every answer a person gave since the claim to a
// question a Run of this Work Item asked (ADR-0121).
func (s *Store) boardQuestionAnswers(ctx context.Context, run boardRun) ([]work.BoardChange, error) {
	rows, err := s.pool.Query(ctx, `SELECT question_id, actor, answer, created_at FROM question_answers
		WHERE work_item_id = $1 AND created_at > $2 ORDER BY id DESC LIMIT $3`, run.workItemID, run.claimedAt, work.MaxBoardChanges+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []work.BoardChange
	for rows.Next() {
		var id, actor, answer string
		var at time.Time
		if err := rows.Scan(&id, &actor, &answer, &at); err != nil {
			return nil, err
		}
		out = append(out, work.BoardChange{Source: work.SourcePloeg, Kind: "question_answered", At: at.UTC(),
			Cite:      "question " + id,
			Summary:   "A person (" + notes.Inline(actor, 64) + ") answered question " + id + ". The answer is the person's decision.",
			Untrusted: clipRunes(answer, boardTextLimit)})
	}
	return out, rows.Err()
}

// boardContestedNotes is every contradiction flag raised since the claim on
// a note the Run was briefed with (ADR-0122): a person has not decided yet
// which note holds.
func (s *Store) boardContestedNotes(ctx context.Context, run boardRun) ([]work.BoardChange, error) {
	if len(run.briefedNotes) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT id, note_key, conflicts_with, created_at FROM note_reviews
		WHERE kind = 'contradiction' AND state = 'open' AND created_at > $2
			AND (note_key = ANY($1::text[]) OR conflicts_with && $1::text[])
		ORDER BY id DESC LIMIT $3`, run.briefedNotes, run.claimedAt, work.MaxBoardChanges+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []work.BoardChange
	for rows.Next() {
		var id int64
		var key string
		var conflicts []string
		var at time.Time
		if err := rows.Scan(&id, &key, &conflicts, &at); err != nil {
			return nil, err
		}
		out = append(out, work.BoardChange{Source: work.SourceGraph, Kind: "note_contested", At: at.UTC(),
			Cite: "note review " + strconv.FormatInt(id, 10),
			Summary: fmt.Sprintf("Note %s now disagrees with %s, and a person has not decided which holds. Rely on neither without checking the code.",
				notes.Inline(key, 120), notes.Inline(strings.Join(conflicts, ", "), 300))})
	}
	return out, rows.Err()
}

func clipRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func changesDigest(changes []work.BoardChange) string {
	h := sha256.New()
	for _, c := range changes {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00", c.Source, c.Kind, c.Cite)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))[:16]
}

// recordCatchUp keeps a catch-up on the Run: every before_finish, and a
// refresh only when it found changes the last record did not hold.
func (s *Store) recordCatchUp(ctx context.Context, runID int64, req work.BoardChangesRequest, resp work.BoardChangesResponse) error {
	digest := changesDigest(resp.Changes)
	if req.Checkpoint != work.CatchUpBeforeFinish {
		if len(resp.Changes) == 0 {
			return nil
		}
		var last string
		err := s.pool.QueryRow(ctx, `SELECT digest FROM run_board_catch_ups WHERE run_id = $1 ORDER BY id DESC LIMIT 1`, runID).Scan(&last)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if last == digest {
			return nil
		}
	}
	versions, err := json.Marshal(resp.Current)
	if err != nil {
		return err
	}
	changes, err := json.Marshal(resp.Changes)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO run_board_catch_ups (run_id, checkpoint, delivery, versions, changes, digest)
		VALUES ($1, $2, $3, $4, $5, $6)`, runID, req.Checkpoint, req.Delivery, versions, changes, digest)
	return err
}

// CatchUpBrief is what changed on the board while an earlier Round's Run
// worked, which that Run could not be told.
type CatchUpBrief struct {
	Role    string
	Round   int
	Changes []work.BoardChange
}

// PriorRoundCatchUps returns, for each acting Run of the Shift's earlier
// Rounds, the last before_finish catch-up that was left for the next Round
// and holds changes. until bounds when it was recorded; zero is now.
func (s *Store) PriorRoundCatchUps(ctx context.Context, shiftID int64, round int, until time.Time) ([]CatchUpBrief, error) {
	if until.IsZero() {
		until = time.Now().Add(time.Hour)
	}
	rows, err := s.pool.Query(ctx, `SELECT role, round, changes FROM (
			SELECT DISTINCT ON (r.id) r.id, r.role, r.round, c.changes FROM run_board_catch_ups c JOIN agent_runs r ON r.id = c.run_id
			WHERE r.shift_id = $1 AND r.round < $2 AND NOT r.shadow AND c.checkpoint = 'before_finish' AND c.delivery = 'next_round'
				AND c.at <= $3
			ORDER BY r.id, c.id DESC) last
		WHERE jsonb_array_length(last.changes) > 0
		ORDER BY round, id`, shiftID, round, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CatchUpBrief
	for rows.Next() {
		var b CatchUpBrief
		var changes []byte
		if err := rows.Scan(&b.Role, &b.Round, &changes); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(changes, &b.Changes); err != nil {
			return nil, fmt.Errorf("decode catch-up: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
