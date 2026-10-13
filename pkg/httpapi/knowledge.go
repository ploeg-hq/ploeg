package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

// NoteGraph is the Tenant's notes graph as ploegd reaches it with the run
// actor (ADR-0087, ADR-0088, both proposed).
type NoteGraph interface {
	RootNotes(ctx context.Context, rootWorkItemID int64) ([]harness.WorkItemNote, string, error)
	Has(ctx context.Context, key string) (bool, error)
	Write(ctx context.Context, note store.PendingNote) (string, error)
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

// handleRunKnowledge serves a running Run the Work Item Notes of its own root
// Work Item and the reviewed concepts of its Tenant, Client and repository.
// The scope comes from the Run's record, never from the request, and an
// unreachable graph answers unavailable instead of failing: knowledge never
// stops a Run (ADR-0087, proposed).
func (s *Server) handleRunKnowledge(w http.ResponseWriter, r *http.Request) {
	scope, err := s.Store.KnowledgeScopeFor(r.Context(), r.PathValue("token"))
	if err != nil {
		runError(w, err)
		return
	}
	resp := harness.KnowledgeResponse{Notes: harness.KnowledgeNotes{Status: harness.KnowledgeOff, Items: []harness.WorkItemNote{}}}
	if s.Notes != nil {
		ctx, cancel := context.WithTimeout(r.Context(), notesReadTimeout)
		notes, commit, err := s.Notes.RootNotes(ctx, scope.RootWorkItemID)
		cancel()
		if err != nil {
			s.Log.Warn("work item notes unavailable; the Run goes ahead without them", "root_work_item", scope.RootWorkItemID, "err", err)
			resp.Notes.Status = harness.KnowledgeUnavailable
		} else {
			resp.Notes = harness.KnowledgeNotes{Status: harness.KnowledgeOK, Commit: commit, Items: notes}
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

func (s *Server) writeNote(ctx context.Context, note store.PendingNote) {
	ctx, cancel := context.WithTimeout(ctx, noteWriteTimeout)
	defer cancel()
	log := s.Log.With("note", note.Key, "work_item", note.WorkItemID)
	if note.Attempts > 0 {
		has, err := s.Notes.Has(ctx, note.Key)
		if err != nil {
			s.deferNote(ctx, log, note, err)
			return
		}
		if has {
			if err := s.Store.MarkNoteWritten(ctx, note.ID, ""); err != nil {
				log.Error("work item note found in the graph but not marked written; the sweep retries", "err", err)
			}
			return
		}
	}
	commit, err := s.Notes.Write(ctx, note)
	if err != nil {
		s.deferNote(ctx, log, note, err)
		return
	}
	if err := s.Store.MarkNoteWritten(ctx, note.ID, commit); err != nil {
		log.Error("work item note written but not marked; the sweep reads it back", "err", err)
	}
}

func (s *Server) deferNote(ctx context.Context, log interface{ Warn(string, ...any) }, note store.PendingNote, cause error) {
	log.Warn("work item note not written; the sweep retries", "attempt", note.Attempts+1, "err", cause)
	if err := s.Store.DeferNoteWrite(context.WithoutCancel(ctx), note.ID, cause, noteWriteBackoff, noteWriteMaxDelay); err != nil {
		log.Warn("work item note retry not recorded; its lease expires instead", "err", err)
	}
}
