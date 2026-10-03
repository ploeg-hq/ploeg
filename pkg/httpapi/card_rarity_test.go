package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/rarity"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func rarityItem(t *testing.T, externalID string, number int, files ...string) int64 {
	t.Helper()
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: externalID, Team: "silver", Title: "rarity " + externalID,
		Target: &work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "ploeg", BaseBranch: "development"}})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-24 * time.Hour).UTC().Truncate(time.Second)
	if ok, err := testStore.RecordPullRequestFacts(ctx, store.PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg", Number: number,
		WorkItemID: id, State: "merged", MergedAt: &at, MergedBy: "anna", HeadSHA: fmt.Sprintf("%040d", number),
		Additions: rarityInt(40), Deletions: rarityInt(2)}); err != nil || !ok {
		t.Fatalf("record play: %v %v", ok, err)
	}
	lines := map[string]store.FileLines{}
	for _, f := range files {
		lines[f] = store.FileLines{Additions: 20, Deletions: 1}
	}
	if ok, err := testStore.RecordPullRequestChange(ctx, store.PullRequestChange{Forge: "forgejo", Repo: "webgrip/ploeg", Number: number,
		Files: files, Lines: lines}); err != nil || !ok {
		t.Fatalf("record files: %v %v", ok, err)
	}
	return id
}

func TestOperatorCard_RevealedRarityMatchesTheSchema(t *testing.T) {
	s, token := cardServer(t, &fakeForgejo{})
	matcher, err := rarity.Rules{AttentionPaths: []string{"pkg/worker/**"}}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	s.OperatorConfig.RarityMatchers = map[string]rarity.Matcher{"webgrip/ploeg": matcher}
	item := rarityItem(t, "1900", 70, "pkg/worker/run.go", "docs/index.md")

	raw := operatorSchemaGET(t, s, token, fmt.Sprintf("work-items/%d/card", item))
	var body struct {
		Card store.OperatorCard `json:"card"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	r := body.Card.Rarity
	if r == nil || r.Revealed == nil || r.Predicted == nil || r.Tier != *r.Revealed || r.RevealedAt == nil || r.Cohort == nil ||
		r.Cohort.Target != "webgrip/ploeg" || r.Inputs.Sensitive.Files == nil || *r.Inputs.Sensitive.Files != 1 ||
		r.Inputs.Sensitive.Paths[0] != "pkg/worker/run.go" {
		t.Fatalf("rarity = %s; an attention path is sensitive ground", raw)
	}
	if !strings.Contains(string(raw), `"rarity":{"formula":"2026.1"`) {
		t.Errorf("card = %s", raw)
	}
	var stored string
	if err := testPool.QueryRow(context.Background(), `SELECT revealed_tier FROM card_rarity WHERE work_item_id = $1`, item).Scan(&stored); err != nil ||
		stored != *r.Revealed {
		t.Errorf("stored tier = %q, %v; reading a released card freezes its tier", stored, err)
	}
}

func TestSweepCardRarity_RevealsUnreadCards(t *testing.T) {
	s, _ := cardServer(t, &fakeForgejo{})
	item := rarityItem(t, "1901", 71, "a.go")
	s.SweepCardRarity(context.Background())
	var tier *string
	var checked time.Time
	if err := testPool.QueryRow(context.Background(), `SELECT revealed_tier, checked_at FROM card_rarity WHERE work_item_id = $1`, item).
		Scan(&tier, &checked); err != nil || tier == nil {
		t.Fatalf("tier = %v, %v; the sweep reveals a released card nobody read", tier, err)
	}
}

func rarityInt(n int) *int { return &n }
