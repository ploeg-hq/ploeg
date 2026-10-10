package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/playkpi"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestWorkItemFacts_KeepsPerFileIndentationAndReadsLiveUsage(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "facts-store", Team: "silver", Title: "facts",
		Target: &work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "ploeg", BaseBranch: "development"}})
	if err != nil {
		t.Fatal(err)
	}
	key := PullRequestKey{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 77}
	if ok, err := testStore.RecordPullRequestFacts(ctx, PullRequestFacts{Forge: key.Forge, Repo: key.Repo, Number: key.Number, WorkItemID: id,
		State: "open", HeadSHA: strings.Repeat("a", 40)}); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := testStore.RecordPullRequestChange(ctx, PullRequestChange{Forge: key.Forge, Repo: key.Repo, Number: key.Number,
		Files: []string{"pkg/a.go", "docs/a.md"}, Lines: map[string]FileLines{"pkg/a.go": {2, 0}, "docs/a.md": {1, 0}}}); err != nil || !ok {
		t.Fatal(ok, err)
	}
	diff := "diff --git a/pkg/a.go b/pkg/a.go\n--- a/pkg/a.go\n+++ b/pkg/a.go\n@@ -1 +1,2 @@\n+\tif x {\n+\t\ty()\n"
	if ok, err := testStore.RecordPullRequestShape(ctx, key, ShapeInput{Size: defaultMatcher, Paths: playkpi.DefaultMatcher, At: time.Now(),
		Diff: []byte(diff)}); err != nil || !ok {
		t.Fatal(ok, err)
	}
	token := strings.Repeat("ab", 24)
	if _, err := testStore.pool.Exec(ctx, `INSERT INTO agent_runs (work_item_id, team, role, round, writes, state, started_at, run_token)
		VALUES ($1, 'silver', 'builder', 1, true, 'running', now(), $2)`, id, token); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	facts, err := testStore.WorkItemFacts(ctx, id, []string{"silver"}, FactsOptions{Now: now, Bots: []string{"Bot", "bot"},
		Live: func(_ context.Context, runToken string) (LiveUsage, error) {
			if runToken != token {
				return LiveUsage{}, errors.New("wrong run")
			}
			return LiveUsage{CostUSD: 0.25, InputTokens: 100, OutputTokens: 20}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.LiveUsage) != 1 || facts.LiveUsage[0].CostUSD != 0.25 || !facts.LiveUsage[0].ObservedAt.Equal(now) {
		t.Errorf("live usage = %+v", facts.LiveUsage)
	}
	if len(facts.BotLogins) != 1 || facts.BotLogins[0] != "bot" {
		t.Errorf("bot logins = %v", facts.BotLogins)
	}
	var plays []struct {
		Files []struct {
			Path        string `json:"path"`
			Indentation *struct {
				Method   string `json:"method"`
				Unit     int    `json:"unit"`
				Added    int    `json:"added"`
				Removed  int    `json:"removed"`
				MaxDepth int    `json:"maxDepth"`
			} `json:"indentation"`
		} `json:"files"`
	}
	raw, _ := json.Marshal(facts.PullRequests)
	if err := json.Unmarshal(raw, &plays); err != nil {
		t.Fatal(err)
	}
	if len(plays) != 1 || len(plays[0].Files) != 2 {
		t.Fatalf("plays = %s", raw)
	}
	doc, code := plays[0].Files[0], plays[0].Files[1]
	if doc.Path != "docs/a.md" || doc.Indentation != nil {
		t.Errorf("a file the diff did not show = %+v", doc)
	}
	if code.Path != "pkg/a.go" || code.Indentation == nil || code.Indentation.Method != "indentation/2026.1" ||
		code.Indentation.Added != 3 || code.Indentation.MaxDepth != 2 || code.Indentation.Removed != 0 {
		t.Errorf("measured file = %+v", code.Indentation)
	}
	if _, err := testStore.WorkItemFacts(ctx, id, []string{"gold"}, FactsOptions{}); !errors.Is(err, ErrOperatorNotFound) {
		t.Errorf("out of scope = %v", err)
	}
}
