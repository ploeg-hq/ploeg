package knowledgegraph

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/omnigraph"
)

// TestConcepts_AgainstARealServer writes concepts as the review actor and
// reads them as the run actor from an Omnigraph server serving the
// tenant-knowledge bundle, when PLOEG_TEST_OMNIGRAPH_URL,
// PLOEG_TEST_KNOWLEDGE_GRAPH, PLOEG_TEST_OMNIGRAPH_RUN_TOKEN and
// PLOEG_TEST_OMNIGRAPH_REVIEW_TOKEN name one. CI fakes the server instead.
func TestConcepts_AgainstARealServer(t *testing.T) {
	url, graph := os.Getenv("PLOEG_TEST_OMNIGRAPH_URL"), os.Getenv("PLOEG_TEST_KNOWLEDGE_GRAPH")
	run, review := os.Getenv("PLOEG_TEST_OMNIGRAPH_RUN_TOKEN"), os.Getenv("PLOEG_TEST_OMNIGRAPH_REVIEW_TOKEN")
	if url == "" || graph == "" || run == "" || review == "" {
		t.Skip("no Omnigraph server named")
	}
	ctx := context.Background()
	repo := "forgejo/acme/shop-" + time.Now().Format("150405.000000")
	put := func(token, scope, client, repository, path string) error {
		c := &omnigraph.Client{BaseURL: url, Token: token}
		owner := client + repository
		_, err := c.Mutate(ctx, graph, "put_concept", map[string]any{
			"slug": ConceptSlug(scope, owner, path), "scope": scope, "client": nilIfEmpty(client), "repository": nilIfEmpty(repository),
			"type": "Fact", "title": path, "description": nil, "resource": nil, "tags": []string{"t"},
			"body": "---\ntype: Fact\ntitle: " + path + "\n---\nbody\n", "from_note": nil, "accepted_by": "test",
			"accepted_at": "2026-10-13T08:00:00Z",
		})
		return err
	}
	for _, p := range [][3]string{{"repository", repo, "pitfalls/csp.md"}, {"repository", repo + "-other", "pitfalls/secret.md"}} {
		if err := put(review, p[0], "", p[1], p[2]); err != nil {
			t.Fatalf("review actor put %s: %v", p[2], err)
		}
	}
	if err := put(run, "repository", "", repo, "run.md"); err == nil || omnigraph.Retryable(err) {
		t.Errorf("the run actor wrote reviewed knowledge or got a retryable answer: %v", err)
	}
	k := &Knowledge{Client: &omnigraph.Client{BaseURL: url, Token: run}, Graph: graph}
	items, commit, err := k.Concepts(ctx, "", repo)
	if err != nil || commit == "" {
		t.Fatalf("concepts: %v %q", err, commit)
	}
	found := 0
	for _, c := range items {
		if c.Scope == ScopeRepository {
			if c.Path != "pitfalls/csp.md" {
				t.Errorf("another repository's concept was served: %+v", c)
			}
			found++
		}
	}
	if found != 1 {
		t.Errorf("repository concepts %+v", items)
	}
}
