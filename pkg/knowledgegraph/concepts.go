package knowledgegraph

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/notes"
	"github.com/ploeg-hq/ploeg/pkg/okf"
	"github.com/ploeg-hq/ploeg/pkg/omnigraph"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

// Concept scopes (ADR-0087, proposed).
const (
	ScopeTenant     = "tenant"
	ScopeClient     = "client"
	ScopeRepository = "repository"
)

// Knowledge is one Tenant's reviewed knowledge graph, read as its run actor.
// Review is the Tenant's review actor, which alone may change the graph: it
// writes a Client or Tenant promotion a person accepted, and purges a
// Client. Nil when the deployment configures none; those then wait.
type Knowledge struct {
	Client *omnigraph.Client
	Review *omnigraph.Client
	Graph  string
}

// ErrNoReviewActor is a write the knowledge graph needs its review actor
// for, on a deployment that configures none.
var ErrNoReviewActor = errors.New("the knowledge graph's review actor is not configured")

// PutConcept writes p as one reviewed concept, keyed so writing it again
// changes nothing, and returns the commit. The concept keeps where it came
// from and who accepted it; an untrusted note stays marked untrusted in the
// document, so the briefing quotes it.
func (k *Knowledge) PutConcept(ctx context.Context, p store.ConceptPromotion) (string, error) {
	if k.Review == nil {
		return "", ErrNoReviewActor
	}
	owner := ""
	switch p.TargetLevel {
	case notes.LevelClient:
		owner = p.Note.Client
		if owner == "" {
			return "", fmt.Errorf("%w: %s belongs to no Client", notes.ErrNotPromotable, p.Note.Key)
		}
	case notes.LevelTenant:
	default:
		return "", fmt.Errorf("%w: %s is not a knowledge graph level", notes.ErrNotPromotable, p.TargetLevel)
	}
	path := notes.SubjectOf("", p.Note.Title)[1:] + ".md"
	if path == ".md" {
		path = "note.md"
	}
	doc := okf.Format(okf.Concept{
		Path: path, Type: p.Note.Type, Title: p.Note.Title, Description: p.Note.Description,
		Resource: p.Note.Resource, Tags: p.Note.Tags, Timestamp: p.DecidedAt, Body: p.Note.Body,
		Extra: map[string]any{"status": "reviewed", "trust": string(p.Note.Trust), "from-note": p.Note.Key,
			"author": p.Note.WrittenBy(), "accepted-by": p.ApprovedBy},
	})
	return k.Review.Mutate(ctx, k.Graph, "put_concept", map[string]any{
		"slug": ConceptSlug(string(p.TargetLevel), owner, path), "scope": string(p.TargetLevel),
		"client": nilIfEmpty(owner), "repository": nil, "type": p.Note.Type, "title": p.Note.Title,
		"description": nilIfEmpty(p.Note.Description), "resource": nilIfEmpty(p.Note.Resource),
		"tags": nilIfNone(p.Note.Tags), "body": string(doc), "from_note": p.Note.Key,
		"accepted_by": p.ApprovedBy, "accepted_at": p.DecidedAt.UTC().Format(time.RFC3339Nano),
	})
}

// PurgeClient removes every concept of client and of its repositories from
// the graph's main branch and reports how many of the Client's concepts
// remain.
func (k *Knowledge) PurgeClient(ctx context.Context, client string, repositories []string) (int, error) {
	if k.Review == nil {
		return 0, ErrNoReviewActor
	}
	if client == "" {
		return 0, errors.New("a purge names one Client")
	}
	if _, err := k.Review.Mutate(ctx, k.Graph, "delete_client_concepts", map[string]any{"client": client}); err != nil {
		return 0, err
	}
	for _, repository := range repositories {
		if _, err := k.Review.Mutate(ctx, k.Graph, "delete_repository_concepts", map[string]any{"repository": repository}); err != nil {
			return 0, err
		}
	}
	res, err := k.Review.Read(ctx, k.Graph, "client_concept_count", map[string]any{"client": client})
	if err != nil {
		return 0, err
	}
	return count(res, "concepts"), nil
}

// ConceptSlug is a concept's graph key: its scope, the Client or repository
// it belongs to, and its OKF path, so the same path in two repositories is
// two concepts.
func ConceptSlug(scope, owner, path string) string {
	if scope == ScopeTenant {
		return ScopeTenant + "/" + path
	}
	return scope + "/" + owner + "/" + path
}

// Concepts reads every Tenant-scoped concept, the concepts of client when it
// is set, and those of repository when it is set, all at one graph commit:
// when a write lands between the reads they are read again once.
func (k *Knowledge) Concepts(ctx context.Context, client, repository string) ([]harness.KnowledgeConcept, string, error) {
	var items []harness.KnowledgeConcept
	var commit string
	for attempt := 0; attempt < 2; attempt++ {
		items, commit = nil, ""
		consistent := true
		reads := []scopeRead{{ScopeTenant, "", "tenant_concepts", nil}}
		if client != "" {
			reads = append(reads, scopeRead{ScopeClient, client, "client_concepts", map[string]any{"client": client}})
		}
		if repository != "" {
			reads = append(reads, scopeRead{ScopeRepository, repository, "repository_concepts", map[string]any{"repository": repository}})
		}
		for _, r := range reads {
			res, err := k.Client.Read(ctx, k.Graph, r.query, r.params)
			if err != nil {
				return nil, "", err
			}
			if commit != "" && res.Commit != commit {
				consistent = false
			}
			commit = res.Commit
			for _, row := range res.Rows {
				if c, ok := concept(r.scope, r.owner, row); ok {
					items = append(items, c)
				}
			}
		}
		if consistent {
			break
		}
	}
	return items, commit, nil
}

type scopeRead struct {
	scope, owner, query string
	params              map[string]any
}

func concept(scope, owner string, row map[string]any) (harness.KnowledgeConcept, bool) {
	prefix := ConceptSlug(scope, owner, "")
	path, ok := strings.CutPrefix(str(row["slug"]), prefix)
	body := str(row["body"])
	if !ok || body == "" || str(row["scope"]) != scope || okf.ValidPath(path) != nil {
		return harness.KnowledgeConcept{}, false
	}
	return harness.KnowledgeConcept{Scope: scope, Path: path, Document: body}, true
}
