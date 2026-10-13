package knowledgegraph

import (
	"context"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/okf"
	"github.com/ploeg-hq/ploeg/pkg/omnigraph"
)

// Concept scopes (ADR-0087, proposed).
const (
	ScopeTenant     = "tenant"
	ScopeClient     = "client"
	ScopeRepository = "repository"
)

// Knowledge is one Tenant's reviewed knowledge graph, read as its run actor.
type Knowledge struct {
	Client *omnigraph.Client
	Graph  string
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
