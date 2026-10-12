package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/knowledge"
	"github.com/ploeg-hq/ploeg/pkg/okf"
)

// notesSource is the pack source name of the Work Item Notes.
const notesSource = "notes"

// notesFetchAttempts and notesFetchBackoff bound how long a Run waits for its
// notes before it goes ahead without them: knowledge never stops a Run.
const (
	notesFetchAttempts = 3
	notesFetchBackoff  = 2 * time.Second
)

// runNotes is what the worker learned about the Run's Work Item Notes.
type runNotes struct {
	// on is set when ploegd writes Work Item Notes, so the Run's learnings
	// reach later Runs at once.
	on bool
	// source holds the notes as a pack source; nil when there are none.
	source *knowledge.Source
	// provenance is the notes' entry in the Task Spec; nil when ploegd
	// writes no notes.
	provenance *harness.KnowledgeSource
}

// workItemNotes asks ploegd for the notes of the Run's root Work Item. An
// older ploegd without the route, or one that writes no notes, gives none;
// a graph that does not answer in time is recorded as unavailable.
func (w *Worker) workItemNotes(ctx context.Context, runToken string) runNotes {
	if w.API == nil {
		return runNotes{}
	}
	var resp harness.KnowledgeResponse
	var err error
	for attempt := 1; attempt <= notesFetchAttempts; attempt++ {
		resp, err = w.API.Knowledge(ctx, runToken)
		var status *knowledgeHTTPError
		if err == nil || (errors.As(err, &status) && status.status < 500 && status.status != 429) {
			break
		}
		if attempt < notesFetchAttempts {
			select {
			case <-ctx.Done():
				attempt = notesFetchAttempts
			case <-time.After(notesFetchBackoff):
			}
		}
	}
	switch {
	case err != nil:
		var status *knowledgeHTTPError
		if errors.As(err, &status) && status.status == 404 {
			return runNotes{}
		}
		w.Log.Warn("work item notes unavailable; the Run goes ahead without them", "err", err)
		return runNotes{}
	case resp.Notes.Status == harness.KnowledgeOff:
		return runNotes{}
	case resp.Notes.Status != harness.KnowledgeOK:
		w.Log.Warn("work item notes unavailable; the Run goes ahead without them")
		return runNotes{on: true, provenance: &harness.KnowledgeSource{Name: notesSource, Status: harness.KnowledgeUnavailable}}
	}
	bundle := okf.Bundle{Name: notesSource, Concepts: noteConcepts(resp.Notes.Items)}
	out := runNotes{on: true, provenance: &harness.KnowledgeSource{Name: notesSource, Digest: knowledge.Digest(bundle), Commit: resp.Notes.Commit}}
	if len(bundle.Concepts) > 0 {
		out.source = &knowledge.Source{Name: notesSource, Bundle: bundle, Unreviewed: true}
	}
	w.Log.Info("read the work item notes", "notes", len(bundle.Concepts), "commit", resp.Notes.Commit)
	return out
}

// noteConcepts turns notes into OKF concepts under their own slugs, marked as
// unreviewed and naming the Run that wrote each.
func noteConcepts(notes []harness.WorkItemNote) []okf.Concept {
	out := make([]okf.Concept, 0, len(notes))
	used := map[string]bool{}
	for _, n := range notes {
		if strings.TrimSpace(n.Title) == "" || strings.TrimSpace(n.Body) == "" {
			continue
		}
		slug := strings.Trim(learningSlugUnsafe.ReplaceAllString(strings.ToLower(n.Title), "-"), "-")
		if len(slug) > 60 {
			slug = strings.Trim(slug[:60], "-")
		}
		if slug == "" {
			slug = "note"
		}
		for base, i := slug, 2; used[slug]; i++ {
			slug = fmt.Sprintf("%s-%d", base, i)
		}
		used[slug] = true
		extra := map[string]any{"status": "unreviewed", "note": n.Key}
		if n.WrittenBy != "" {
			extra["written-by"] = n.WrittenBy
		}
		if n.WorkItem != "" {
			extra["work-item"] = n.WorkItem
		}
		out = append(out, okf.Concept{
			Path:        slug + ".md",
			Type:        n.Type,
			Title:       n.Title,
			Description: n.Description,
			Resource:    n.Resource,
			Tags:        n.Tags,
			Timestamp:   n.WrittenAt,
			Body:        n.Body,
			Extra:       extra,
		})
	}
	return out
}
