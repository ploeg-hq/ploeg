package httpapi

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/notes"
	"github.com/ploeg-hq/ploeg/pkg/omnigraph"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

// NoteGraph is the Tenant's notes graph as ploegd reaches it with the run
// actor (ADR-0087, ADR-0088, ADR-0122).
type NoteGraph interface {
	ScopeNotes(ctx context.Context, rootWorkItemID int64, repository string) ([]notes.Note, string, error)
	SubjectNotes(ctx context.Context, subject string) ([]notes.Note, string, error)
	Has(ctx context.Context, key string) (bool, error)
	Write(ctx context.Context, note notes.Note, d notes.Decision, basedOn string) (string, error)
}

// ConceptWriter writes the Client and Tenant promotions a person accepted to
// the reviewed knowledge graph, as the Tenant's review actor (ADR-0122).
type ConceptWriter interface {
	PutConcept(ctx context.Context, p store.ConceptPromotion) (string, error)
}

// SweepConceptPromotions writes accepted Client and Tenant promotions to the
// knowledge graph. One the graph refused keeps its error and is tried on the
// next sweep; writing a concept again changes nothing.
func (s *Server) SweepConceptPromotions(ctx context.Context) {
	if s.Concepts == nil {
		return
	}
	due, err := s.Store.AcceptedConceptPromotions(ctx, noteSweepBatch)
	if err != nil {
		s.Log.Error("promotion sweep failed", "err", err)
		return
	}
	for _, p := range due {
		wctx, cancel := context.WithTimeout(ctx, noteWriteTimeout)
		_, err := s.Concepts.PutConcept(wctx, p)
		cancel()
		if err != nil {
			s.Log.Warn("accepted promotion not written to the knowledge graph; the sweep retries", "review", p.ReviewID, "err", err)
			if err := s.Store.RecordPromotionError(context.WithoutCancel(ctx), p.ReviewID, err); err != nil {
				s.Log.Warn("promotion error not recorded", "review", p.ReviewID, "err", err)
			}
			continue
		}
		if err := s.Store.MarkPromotionApplied(ctx, p.ReviewID); err != nil {
			s.Log.Error("promotion written but not marked; the sweep writes it again", "review", p.ReviewID, "err", err)
		}
	}
}

// ConceptGraph is the Tenant's reviewed knowledge graph as ploegd reads it
// with the run actor.
type ConceptGraph interface {
	Concepts(ctx context.Context, client, repository string) ([]harness.KnowledgeConcept, string, error)
}

const (
	maxNotesPerRun   = 10
	notesReadTimeout = 10 * time.Second

	noteSweepBatch    = 20
	noteWriteLease    = 2 * time.Minute
	noteWriteTimeout  = 15 * time.Second
	noteWriteBackoff  = 30 * time.Second
	noteWriteMaxDelay = time.Hour
	// noteWriteRaces bounds how often one sweep re-reads a subject after the
	// graph moved under a conditional write, before the note waits for the
	// next sweep.
	noteWriteRaces = 3
)

// keptLearnings are the learnings that become Work Item Notes: the first
// maxNotesPerRun with the fields a note needs, as the worker keeps them.
func keptLearnings(learnings []harness.Learning) []harness.Learning {
	var kept []harness.Learning
	for _, l := range learnings {
		if len(kept) == maxNotesPerRun {
			break
		}
		if l.Type == "" || l.Title == "" || l.Body == "" {
			continue
		}
		kept = append(kept, l)
	}
	return kept
}

// BriefingNotes reads the notes a running Run in scope may be briefed with:
// its root Work Item's and its repository's, filtered by notes.ForRun for its
// Client, expiry, supersession and retraction, with the open contradictions
// marked, and the graph commit they were read at (ADR-0122). It is the one
// read the briefing assembles notes from.
func (s *Server) BriefingNotes(ctx context.Context, scope store.RunKnowledgeScope) (notes.View, string, error) {
	all, commit, err := s.Notes.ScopeNotes(ctx, scope.RootWorkItemID, scope.Repository)
	if err != nil {
		return notes.View{}, "", err
	}
	keys := make([]string, 0, len(all))
	for _, n := range all {
		keys = append(keys, n.Key)
	}
	contested, err := s.Store.ContestedKeys(ctx, keys)
	if err != nil {
		return notes.View{}, "", err
	}
	view := notes.ForRun(notes.Scope{
		RootWorkItem: strconv.FormatInt(scope.RootWorkItemID, 10),
		Repository:   scope.Repository,
		Client:       scope.Client,
	}, all, contested, time.Now())
	return view, commit, nil
}

// handleRunKnowledge serves a running Run the notes of its own root Work
// Item and repository and the reviewed concepts of its Tenant, Client and
// repository. The scope comes from the Run's record, never from the request,
// and an unreachable graph answers unavailable instead of failing: knowledge
// never stops a Run (ADR-0087, ADR-0122).
func (s *Server) handleRunKnowledge(w http.ResponseWriter, r *http.Request) {
	scope, err := s.Store.KnowledgeScopeFor(r.Context(), r.PathValue("token"))
	if err != nil {
		runError(w, err)
		return
	}
	resp := harness.KnowledgeResponse{Notes: harness.KnowledgeNotes{Status: harness.KnowledgeOff, Items: []harness.WorkItemNote{}}}
	if s.Notes != nil {
		ctx, cancel := context.WithTimeout(r.Context(), notesReadTimeout)
		view, commit, err := s.BriefingNotes(ctx, scope)
		cancel()
		if err != nil {
			s.Log.Warn("work item notes unavailable; the Run goes ahead without them", "root_work_item", scope.RootWorkItemID, "err", err)
			resp.Notes.Status = harness.KnowledgeUnavailable
		} else {
			items := make([]harness.WorkItemNote, 0, len(view.Notes))
			for _, n := range view.Notes {
				items = append(items, n.Wire())
			}
			resp.Notes = harness.KnowledgeNotes{Status: harness.KnowledgeOK, Commit: commit, Items: items}
		}
	}
	if s.Knowledge != nil {
		ctx, cancel := context.WithTimeout(r.Context(), notesReadTimeout)
		concepts, commit, err := s.Knowledge.Concepts(ctx, scope.Client, scope.Repository)
		cancel()
		if err != nil {
			s.Log.Warn("tenant knowledge unavailable; the Run goes ahead without it", "repository", scope.Repository, "err", err)
			resp.Concepts = &harness.KnowledgeConcepts{Status: harness.KnowledgeUnavailable, Items: []harness.KnowledgeConcept{}}
		} else {
			if concepts == nil {
				concepts = []harness.KnowledgeConcept{}
			}
			resp.Concepts = &harness.KnowledgeConcepts{Status: harness.KnowledgeOK, Commit: commit, Items: concepts}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// SweepNoteWrites writes due outbox rows to the notes graph. A row whose
// earlier attempt may have reached the graph is read back first, so a lost
// answer never writes a note twice; a failed write waits and is tried again,
// and the outcome that queued it is never touched.
func (s *Server) SweepNoteWrites(ctx context.Context) {
	if s.Notes == nil {
		return
	}
	due, err := s.Store.ClaimNoteWrites(ctx, noteSweepBatch, noteWriteLease)
	if err != nil {
		s.Log.Error("work item note sweep failed", "err", err)
		return
	}
	for _, note := range due {
		if ctx.Err() != nil {
			return
		}
		s.writeNote(ctx, note)
	}
}

func (s *Server) writeNote(ctx context.Context, pending store.PendingNote) {
	ctx, cancel := context.WithTimeout(ctx, noteWriteTimeout)
	defer cancel()
	log := s.Log.With("note", pending.Key, "work_item", pending.WorkItemID)
	if pending.Attempts > 0 {
		has, err := s.Notes.Has(ctx, pending.Key)
		if err != nil {
			s.deferNote(ctx, log, pending, err)
			return
		}
		if has {
			if err := s.Store.MarkNoteWritten(ctx, pending.ID, ""); err != nil {
				log.Error("work item note found in the graph but not marked written; the sweep retries", "err", err)
			}
			return
		}
	}
	note := pending.Note()
	if err := note.Validate(); err != nil {
		s.deferNote(ctx, log, pending, err)
		return
	}
	var commit string
	var decision notes.Decision
	err := omnigraph.ErrPreconditionFailed
	for race := 0; race < noteWriteRaces && errors.Is(err, omnigraph.ErrPreconditionFailed); race++ {
		var existing []notes.Note
		var basedOn string
		existing, basedOn, err = s.Notes.SubjectNotes(ctx, note.Subject)
		if err != nil {
			break
		}
		decision = noteDecision(note, existing, pending.RunStartedAt)
		commit, err = s.Notes.Write(ctx, note, decision, basedOn)
	}
	if err != nil {
		s.deferNote(ctx, log, pending, err)
		return
	}
	if err := s.Store.FlagContradiction(ctx, note.Key, decision.Contradicts); err != nil {
		log.Error("work item note disagrees with another but the flag was not recorded", "contradicts", decision.Contradicts, "err", err)
	}
	if len(decision.Contradicts) > 0 {
		log.Info("work item note disagrees with current notes on its subject; a person decides", "contradicts", decision.Contradicts)
	}
	if err := s.Store.MarkNoteWritten(ctx, pending.ID, commit); err != nil {
		log.Error("work item note written but not marked; the sweep reads it back", "err", err)
	}
}

// noteDecision is what note does to the notes on its subject. A retraction
// replaces exactly the note a person decided against.
func noteDecision(note notes.Note, existing []notes.Note, briefedAt time.Time) notes.Decision {
	if note.Type == notes.TypeRetraction {
		return notes.Decision{Supersedes: note.Supersedes}
	}
	return notes.Reconcile(note, existing, briefedAt)
}

func (s *Server) deferNote(ctx context.Context, log interface{ Warn(string, ...any) }, note store.PendingNote, cause error) {
	log.Warn("work item note not written; the sweep retries", "attempt", note.Attempts+1, "err", cause)
	if err := s.Store.DeferNoteWrite(context.WithoutCancel(ctx), note.ID, cause, noteWriteBackoff, noteWriteMaxDelay); err != nil {
		log.Warn("work item note retry not recorded; its lease expires instead", "err", err)
	}
}

// maxLiveNotesPerRun bounds what one Run can send while it works: the
// agent's learnings plus the worker's stop note.
const (
	maxLiveNotesPerRun = maxNotesPerRun + 1
	liveNotesBodyLimit = 64 << 10
)

var liveNoteKey = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// handleRunNotes stores the notes a running Run sends while it works, so a
// Run that is stopped later keeps what it found (ADR-0088, amended
// 2026-10-17). A deployment without a notes graph answers 404, and the
// worker then keeps the learnings for its outcome.
func (s *Server) handleRunNotes(w http.ResponseWriter, r *http.Request) {
	if s.Notes == nil {
		http.NotFound(w, r)
		return
	}
	var req harness.RunNotesRequest
	if err := decodeBounded(w, r, liveNotesBodyLimit, &req); err != nil {
		if refuseOversizedBody(w, err) {
			return
		}
		http.Error(w, "notes must be a JSON runNotesRequest", http.StatusBadRequest)
		return
	}
	if len(req.Notes) == 0 || len(req.Notes) > maxLiveNotesPerRun {
		http.Error(w, "send between 1 and 11 notes", http.StatusBadRequest)
		return
	}
	notes := make([]store.LiveNote, 0, len(req.Notes))
	for _, n := range req.Notes {
		if !liveNoteKey.MatchString(n.Key) || len(keptLearnings([]harness.Learning{n.Learning})) == 0 {
			http.Error(w, "each note needs a key and a learning with a type, title and body", http.StatusBadRequest)
			return
		}
		notes = append(notes, store.LiveNote{Key: n.Key, ByWorker: n.ByWorker, Learning: n.Learning})
	}
	queued, err := s.Store.QueueLiveNotes(r.Context(), r.PathValue("token"), notes)
	if err != nil {
		runError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, harness.RunNotesResponse{Queued: queued})
}
