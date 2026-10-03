package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/forgejo"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const factsBranch = "agent/vik-1800"

func factsItem(t *testing.T) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "1800", Team: "silver", Title: "facts",
		Target: &work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "ploeg", BaseBranch: "development"}})
	if err != nil {
		t.Fatal(err)
	}
	shift, err := testStore.OpenShift(ctx, id, "silver", factsBranch, 0)
	if err != nil {
		t.Fatal(err)
	}
	return id, shift
}

func factsReview(state, reviewer, branch string) map[string]any {
	return map[string]any{
		"repository":   map[string]any{"full_name": "webgrip/ploeg"},
		"sender":       map[string]any{"login": reviewer},
		"pull_request": map[string]any{"number": 18, "head": map[string]any{"ref": branch, "sha": "head-" + reviewer}},
		"review":       map[string]any{"type": "pull_request_review_" + state, "content": "review text"},
	}
}

func factsMerge() map[string]any {
	return map[string]any{
		"action":     "closed",
		"repository": map[string]any{"full_name": "webgrip/ploeg"},
		"sender":     map[string]any{"login": "ryan"},
		"pull_request": map[string]any{
			"number": 18, "merged": true,
			"head":             map[string]any{"ref": factsBranch, "sha": "head-final"},
			"merge_commit_sha": "merge-sha",
			"merged_at":        "2026-10-01T09:30:00Z",
			"closed_at":        "2026-10-01T09:30:00Z",
			"merged_by":        map[string]any{"login": "ryan"},
		},
	}
}

// Regression for ADR-0045: the audit row for a submitted review kept only
// repo, pull request and branch, so who reviewed and what they decided was
// lost. Both now survive, in the audit row and as a review fact.
func TestForgeWebhook_ReviewVerdictAndReviewerAreKept(t *testing.T) {
	h := forgeServer(t, "shh")
	factsItem(t)
	if code := forgePost(t, h, "shh", "review-1", factsReview("approved", "anna", factsBranch)); code != http.StatusAccepted {
		t.Fatalf("webhook returned %d", code)
	}

	var actor, review, head *string
	if err := testPool.QueryRow(context.Background(), `SELECT detail->>'actor', detail->>'review', detail->>'head_sha'
		FROM audit_log WHERE action = 'forge.review_submitted'`).Scan(&actor, &review, &head); err != nil {
		t.Fatal(err)
	}
	if actor == nil || *actor != "anna" || review == nil || *review != "approved" || head == nil || *head != "head-anna" {
		t.Errorf("audit detail actor=%v review=%v head=%v; want anna, approved, head-anna", actor, review, head)
	}

	var reviewer, state, reviewHead string
	if err := testPool.QueryRow(context.Background(), `SELECT reviewer, state, head_sha FROM pull_request_reviews`).
		Scan(&reviewer, &state, &reviewHead); err != nil {
		t.Fatalf("review fact: %v", err)
	}
	if reviewer != "anna" || state != "approved" || reviewHead != "head-anna" {
		t.Errorf("review fact = %s %s %s", reviewer, state, reviewHead)
	}
}

func TestForgeWebhook_MergeFactsAreStored(t *testing.T) {
	h := forgeServer(t, "shh")
	item, shift := factsItem(t)
	if code := forgePost(t, h, "shh", "merge-1", factsMerge()); code != http.StatusAccepted {
		t.Fatalf("webhook returned %d", code)
	}
	var workItem, shiftID int64
	var state, head, mergeCommit, mergedBy string
	var mergedAt time.Time
	if err := testPool.QueryRow(context.Background(), `SELECT work_item_id, shift_id, state, head_sha, merge_commit_sha,
		merged_by, merged_at FROM pull_requests WHERE forge = 'forgejo' AND repo_owner = 'webgrip' AND repo_name = 'ploeg' AND number = 18`).
		Scan(&workItem, &shiftID, &state, &head, &mergeCommit, &mergedBy, &mergedAt); err != nil {
		t.Fatalf("merge facts: %v", err)
	}
	if workItem != item || shiftID != shift || state != "merged" || head != "head-final" || mergeCommit != "merge-sha" ||
		mergedBy != "ryan" || !mergedAt.Equal(time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)) {
		t.Errorf("merge facts = item %d shift %d %s %s %s %s %s", workItem, shiftID, state, head, mergeCommit, mergedBy, mergedAt)
	}
	var auditMergedBy, auditCommit *string
	if err := testPool.QueryRow(context.Background(), `SELECT detail->>'merged_by', detail->>'merge_commit_sha'
		FROM audit_log WHERE action = 'forge.pr_merged'`).Scan(&auditMergedBy, &auditCommit); err != nil {
		t.Fatal(err)
	}
	if auditMergedBy == nil || *auditMergedBy != "ryan" || auditCommit == nil || *auditCommit != "merge-sha" {
		t.Errorf("audit merge detail = %v %v", auditMergedBy, auditCommit)
	}
}

func TestForgeWebhook_PullRequestOnNoPloegBranchStoresNoFacts(t *testing.T) {
	h := forgeServer(t, "shh")
	factsItem(t)
	if code := forgePost(t, h, "shh", "review-x", factsReview("approved", "anna", "feature/human")); code != http.StatusAccepted {
		t.Fatalf("webhook returned %d", code)
	}
	var n int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM pull_requests`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("pull_requests rows = %d for a human branch, want 0", n)
	}
	if forgeAuditCount(t) != 1 {
		t.Error("the event itself must still be audited")
	}
}

func TestOperatorWorkItemReportsMergeFactsAndReviews(t *testing.T) {
	reset(t)
	ctx := context.Background()
	item, shift := factsItem(t)
	if _, err := testStore.OpenRound(ctx, shift, 0, []store.Role{{Name: "builder", Writes: true, Cap: 1}}); err != nil {
		t.Fatal(err)
	}
	run, err := testStore.ClaimRole(ctx, "silver", "builder", time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ReportOutcome(ctx, run.RunToken, store.Report(work.OutcomePROpened, "opened", "",
		[]string{"https://forge.example/webgrip/ploeg/pulls/18"}, nil, nil)); err != nil {
		t.Fatal(err)
	}
	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	s := &Server{
		Store: testStore, Log: slog.New(slog.DiscardHandler),
		Forges:         map[string]provider.ForgeProvider{"forgejo": &forgejo.Provider{Secret: "shh", Log: slog.New(slog.DiscardHandler)}},
		OperatorConfig: OperatorConfig{Consumers: consumers, Teams: map[string][]string{"silver": {"builder"}}},
	}

	before := operatorSchemaGET(t, s, token, fmt.Sprintf("work-items/%d", item))
	var empty struct {
		Item struct {
			PullRequest map[string]json.RawMessage `json:"pullRequest"`
		} `json:"item"`
	}
	if err := json.Unmarshal(before, &empty); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"mergedAt", "mergedBy", "headSha", "mergeCommitSha"} {
		if _, ok := empty.Item.PullRequest[key]; ok {
			t.Errorf("pullRequest.%s present before the forge reported it: %s", key, before)
		}
	}
	if string(empty.Item.PullRequest["reviews"]) != "[]" {
		t.Errorf("reviews = %s, want []", empty.Item.PullRequest["reviews"])
	}

	h := s.Handler()
	for delivery, body := range map[string]map[string]any{
		"r-1": factsReview("rejected", "anna", factsBranch),
		"r-2": factsReview("approved", "bert", factsBranch),
	} {
		if code := forgePost(t, h, "shh", delivery, body); code != http.StatusAccepted {
			t.Fatalf("%s returned %d", delivery, code)
		}
	}
	if code := forgePost(t, h, "shh", "m-1", factsMerge()); code != http.StatusAccepted {
		t.Fatalf("merge returned %d", code)
	}

	raw := operatorSchemaGET(t, s, token, fmt.Sprintf("work-items/%d", item))
	var body struct {
		Item struct {
			PullRequest *store.OperatorPullRequest `json:"pullRequest"`
		} `json:"item"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	pr := body.Item.PullRequest
	if pr == nil || pr.MergedAt == nil || !pr.MergedAt.Equal(time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)) ||
		pr.MergedBy != "ryan" || pr.HeadSHA != "head-final" || pr.MergeCommitSHA != "merge-sha" {
		t.Fatalf("pullRequest = %+v\n%s", pr, raw)
	}
	if len(pr.Reviews) != 2 {
		t.Fatalf("reviews = %+v, want two", pr.Reviews)
	}
	got := map[string]string{}
	for _, r := range pr.Reviews {
		got[r.Reviewer] = r.State
		if r.ReceivedAt.IsZero() || r.HeadSHA != "head-"+r.Reviewer {
			t.Errorf("review = %+v", r)
		}
	}
	if got["anna"] != "changes_requested" || got["bert"] != "approved" {
		t.Errorf("reviews by reviewer = %v", got)
	}
}

func TestOperatorRunReportsHarnessUsage(t *testing.T) {
	reset(t)
	s := createdServer(nil)
	consumers, token := operatorTestConsumers(t, []string{"bronze"}, false)
	s.OperatorConfig.Consumers = consumers
	h := s.Handler()
	n := func(v int64) *int64 { return &v }
	cost := 0.55

	ingestSource(t, "7101", "bronze")
	rich := claimLegacy(t, h, "bronze")
	if rec := postOutcome(t, h, rich.RunToken, harness.OutcomeReport{
		Outcome: work.OutcomeNoChangeNeeded, Summary: "done",
		Usage: &harness.Usage{
			InputTokens: 1200, OutputTokens: 9876, CostUSD: 0.58, SessionID: "sess",
			CacheReadInputTokens: n(812004), CacheCreationInputTokens: n(45210), Turns: n(37),
			DurationMs: n(412345), APIDurationMs: n(301200), ToolCalls: n(4),
			ToolCallsByKind: map[string]int64{"edit": 1, "read": 3}, PeakContextTokens: n(150000), ContextWindowTokens: n(200000),
			ModelUsage: map[string]harness.ModelUsage{"claude-sonnet-5": {InputTokens: n(1100), OutputTokens: n(9500), CostUSD: &cost}},
		},
	}); rec.Code != http.StatusNoContent {
		t.Fatalf("outcome: %d %s", rec.Code, rec.Body)
	}
	raw := operatorSchemaGET(t, s, token, fmt.Sprintf("runs/%d", runID(t, rich.RunToken)))
	var body struct {
		Run store.OperatorRun `json:"run"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	u := body.Run.Usage
	if u == nil {
		t.Fatalf("usage = nil: %s", raw)
	}
	for name, pair := range map[string][2]*int64{
		"cacheReadInputTokens": {u.CacheReadInputTokens, n(812004)}, "cacheCreationInputTokens": {u.CacheCreationInputTokens, n(45210)},
		"turns": {u.Turns, n(37)}, "durationMs": {u.DurationMs, n(412345)}, "apiDurationMs": {u.APIDurationMs, n(301200)},
		"toolCalls": {u.ToolCalls, n(4)}, "peakContextTokens": {u.PeakContextTokens, n(150000)},
		"contextWindowTokens": {u.ContextWindowTokens, n(200000)},
	} {
		if pair[0] == nil || *pair[0] != *pair[1] {
			t.Errorf("%s = %v, want %d", name, pair[0], *pair[1])
		}
	}
	if u.ToolCallsByKind["read"] != 3 || u.ToolCallsByKind["edit"] != 1 {
		t.Errorf("toolCallsByKind = %v", u.ToolCallsByKind)
	}
	m := u.ModelUsage["claude-sonnet-5"]
	if m.InputTokens == nil || *m.InputTokens != 1100 || m.CostUSD == nil || *m.CostUSD != 0.55 || m.CacheReadInputTokens != nil {
		t.Errorf("modelUsage = %+v", u.ModelUsage)
	}

	ingestSource(t, "7102", "bronze")
	plain := claimLegacy(t, h, "bronze")
	if rec := postOutcome(t, h, plain.RunToken, harness.OutcomeReport{
		Outcome: work.OutcomeNoChangeNeeded, Summary: "done", Usage: &harness.Usage{InputTokens: 10, CostUSD: 0.01},
	}); rec.Code != http.StatusNoContent {
		t.Fatalf("outcome: %d %s", rec.Code, rec.Body)
	}
	raw = operatorSchemaGET(t, s, token, fmt.Sprintf("runs/%d", runID(t, plain.RunToken)))
	for _, key := range []string{"cacheReadInputTokens", "turns", "toolCalls", "toolCallsByKind", "peakContextTokens", "modelUsage", "durationMs"} {
		if strings.Contains(string(raw), `"`+key+`"`) {
			t.Errorf("run usage carries %q although the harness never reported it: %s", key, raw)
		}
	}
}
