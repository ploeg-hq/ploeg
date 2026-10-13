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

// notesSource and graphSource are the pack source names of the Work Item
// Notes and of the Tenant's reviewed knowledge graph.
const (
	notesSource = "notes"
	graphSource = "graph"
)

// notesFetchAttempts and notesFetchBackoff bound how long a Run waits for its
// notes before it goes ahead without them: knowledge never stops a Run.
const (
	notesFetchAttempts = 3
	notesFetchBackoff  = 2 * time.Second
)

// tenantKnowledge is what the worker read from the Tenant's graphs: the Run's Work
// Item Notes and the reviewed concepts of its scopes.
type tenantKnowledge struct {
	// on is set when ploegd writes Work Item Notes, so the Run's learnings
	// reach later Runs at once.
	on bool
	// source holds the notes as a pack source; nil when there are none.
	source *knowledge.Source
	// provenance is the notes' entry in the Task Spec; nil when ploegd
	// writes no notes.
	provenance *harness.KnowledgeSource
	// concepts holds the reviewed graph concepts as a pack source; nil when
	// there are none.
	concepts []graphConcept
	// conceptsProvenance is the graph's entry in the Task Spec; nil when
	// ploegd serves no graph concepts.
	conceptsProvenance *harness.KnowledgeSource
}

// graphConcept is one reviewed concept with the scope it came from.
type graphConcept struct {
	scope   string
	path    string
	concept okf.Concept
}

// readTenantKnowledge asks ploegd for the notes of the Run's root Work Item
// and the reviewed concepts of its scopes. An older ploegd without the route,
// or one that serves neither, gives none; a graph that does not answer in
// time is recorded as unavailable.
func (w *Worker) readTenantKnowledge(ctx context.Context, runToken string) tenantKnowledge {
	if w.API == nil {
		return tenantKnowledge{}
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
	if err != nil {
		var status *knowledgeHTTPError
		if !errors.As(err, &status) || status.status != 404 {
			w.Log.Warn("tenant knowledge unavailable; the Run goes ahead without it", "err", err)
		}
		return tenantKnowledge{}
	}
	out := tenantKnowledge{}
	switch resp.Notes.Status {
	case harness.KnowledgeOK:
		bundle := okf.Bundle{Name: notesSource, Concepts: noteConcepts(resp.Notes.Items)}
		out.on = true
		out.provenance = &harness.KnowledgeSource{Name: notesSource, Digest: knowledge.Digest(bundle), Commit: resp.Notes.Commit}
		if len(bundle.Concepts) > 0 {
			out.source = &knowledge.Source{Name: notesSource, Bundle: bundle, Unreviewed: true}
		}
		w.Log.Info("read the work item notes", "notes", len(bundle.Concepts), "commit", resp.Notes.Commit)
	case harness.KnowledgeUnavailable:
		w.Log.Warn("work item notes unavailable; the Run goes ahead without them")
		out.on = true
		out.provenance = &harness.KnowledgeSource{Name: notesSource, Status: harness.KnowledgeUnavailable}
	}
	if c := resp.Concepts; c != nil {
		switch c.Status {
		case harness.KnowledgeOK:
			out.concepts = w.graphConcepts(c.Items)
			out.conceptsProvenance = &harness.KnowledgeSource{Name: graphSource, Commit: c.Commit}
			w.Log.Info("read the tenant knowledge", "concepts", len(out.concepts), "commit", c.Commit)
		case harness.KnowledgeUnavailable:
			w.Log.Warn("tenant knowledge unavailable; the Run goes ahead without it")
			out.conceptsProvenance = &harness.KnowledgeSource{Name: graphSource, Status: harness.KnowledgeUnavailable}
		}
	}
	return out
}

// graphConcepts parses each reviewed concept's OKF document under its scope;
// one that does not parse is logged and left out.
func (w *Worker) graphConcepts(items []harness.KnowledgeConcept) []graphConcept {
	var out []graphConcept
	for _, item := range items {
		c, err := okf.Parse(item.Scope+"/"+item.Path, []byte(item.Document))
		if err != nil {
			w.Log.Warn("tenant knowledge concept skipped", "scope", item.Scope, "path", item.Path, "err", err)
			continue
		}
		out = append(out, graphConcept{scope: item.Scope, path: item.Path, concept: c})
	}
	return out
}

// graphSource builds the pack source of the reviewed concepts. A repository
// concept whose path the repository's own knowledge directory also holds is
// left out: the client's merged file wins over the graph's copy (design D8).
func (n tenantKnowledge) graphSource(repoPaths map[string]bool) *knowledge.Source {
	var concepts []okf.Concept
	for _, g := range n.concepts {
		if g.scope == "repository" && repoPaths[g.path] {
			continue
		}
		concepts = append(concepts, g.concept)
	}
	if len(concepts) == 0 {
		return nil
	}
	return &knowledge.Source{Name: graphSource, Bundle: okf.Bundle{Name: graphSource, Concepts: concepts}}
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
