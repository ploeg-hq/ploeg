package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestOperatorWorkItemReportsAConflictedPullRequest(t *testing.T) {
	reset(t)
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "operator-conflict", Team: "silver", Title: "PR item",
		Target: &work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "ploeg", BaseBranch: "development"}})
	if err != nil {
		t.Fatal(err)
	}
	shift, err := testStore.OpenShift(ctx, id, "silver", "agent/vik-1598", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.OpenRound(ctx, shift, 0, []store.Role{{Name: "builder", Writes: true, Cap: 1}}); err != nil {
		t.Fatal(err)
	}
	run, err := testStore.ClaimRole(ctx, "silver", "builder", time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	const prURL = "https://forge.example/webgrip/ploeg/pulls/45"
	if _, err := testStore.ReportOutcome(ctx, run.RunToken, store.Report(work.OutcomePROpened, "opened", "", []string{prURL}, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if ok, err := testStore.RecordPullRequestFacts(ctx, store.PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 45,
		WorkItemID: id, State: "open", HeadSHA: "aaa"}); err != nil || !ok {
		t.Fatalf("facts: %v %v", ok, err)
	}
	conflicted := false
	key := store.PullRequestKey{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 45}
	checkedAt := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	for range store.ConflictConfirmPolls {
		if _, ok, err := testStore.RecordMergeCheck(ctx, key, id, store.MergeObservation{Mergeable: &conflicted, HeadSHA: "aaa", BaseBranch: "main"}, checkedAt); err != nil || !ok {
			t.Fatalf("merge check: %v %v", ok, err)
		}
	}

	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	s := &Server{Store: testStore, OperatorConfig: OperatorConfig{Consumers: consumers, Teams: map[string][]string{"silver": {"builder"}}}}
	for _, endpoint := range []string{"work-items", fmt.Sprintf("work-items/%d", id)} {
		raw := operatorSchemaGET(t, s, token, endpoint)
		var body struct {
			Items []struct {
				PullRequest *store.OperatorPullRequest `json:"pullRequest"`
			} `json:"items"`
			Item struct {
				PullRequest *store.OperatorPullRequest `json:"pullRequest"`
			} `json:"item"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		got := body.Item.PullRequest
		if endpoint == "work-items" {
			if len(body.Items) != 1 {
				t.Fatalf("%s: want one item, got %s", endpoint, raw)
			}
			got = body.Items[0].PullRequest
		}
		if got == nil || got.Number == nil || *got.Number != 45 || got.MergeState != "conflicted" || got.HeadSHA != "aaa" ||
			got.BaseBranch == nil || *got.BaseBranch != "main" || got.CheckedAt == nil || !got.CheckedAt.Equal(checkedAt) {
			t.Fatalf("%s: pullRequest = %+v\n%s", endpoint, got, raw)
		}
	}

	d, err := testStore.OperatorItem(ctx, id, []string{"silver"})
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, e := range d.Events {
		if e.Action == "pull_request.conflicted" {
			found++
		}
	}
	if found != 1 {
		t.Errorf("item events hold %d pull_request.conflicted rows, want 1: %+v", found, d.Events)
	}
}
