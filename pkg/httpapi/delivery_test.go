package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

var (
	deliveryHead   = strings.Repeat("a", 40)
	deliveryBefore = strings.Repeat("b", 40)
)

func deliveryWriter(t *testing.T, externalID string) (token, branch string) {
	t.Helper()
	ctx := context.Background()
	reset(t)
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{
		Provider: "vikunja", ExternalID: externalID, Team: "bronze", Title: "t",
		Target: &work.Target{Forge: "home", Owner: "o", Repo: "r", BaseBranch: "main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	branch = "agent/vik-" + externalID
	shiftID, err := testStore.OpenShift(ctx, id, "bronze", branch, 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []store.Role{{Name: "builder", Writes: true, Cap: 1}}); err != nil {
		t.Fatal(err)
	}
	run, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	return run.RunToken, branch
}

func postRun(t *testing.T, token, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	apiServer(t, plan.Plans{}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/runs/"+token+"/"+path, strings.NewReader(string(raw))))
	return rec
}

type storedDelivery struct {
	outcome, source, stuckReason string
	links                        []string
	delivery                     *harness.Delivery
}

func readStoredDelivery(t *testing.T, token string) storedDelivery {
	t.Helper()
	var s storedDelivery
	var raw []byte
	var source *string
	if err := testPool.QueryRow(context.Background(), `
		SELECT outcome, stuck_reason, links, delivery, delivery_source FROM agent_runs WHERE run_token = $1`, token).
		Scan(&s.outcome, &s.stuckReason, &s.links, &raw, &source); err != nil {
		t.Fatal(err)
	}
	if source != nil {
		s.source = *source
	}
	if raw != nil {
		s.delivery = new(harness.Delivery)
		if err := json.Unmarshal(raw, s.delivery); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// VIK-1732 (ADR-0059): a direct outcome call that claims pr_opened with a link
// to another repository's pull request does not record a delivery, and the
// link is not kept. Before this change the Run was stored as pr_opened with
// that link, which made the Shift ready for review and pointed publication at
// pull request 123 of the Work Item's repository.
func TestOutcomeClaimingAForeignPullRequestIsNotADelivery(t *testing.T) {
	token, _ := deliveryWriter(t, "1732")
	rec := postRun(t, token, "outcome", map[string]any{
		"outcome": "pr_opened", "summary": "opened it",
		"links": []string{"https://forgejo/other/repo/pulls/123"},
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("outcome = %d %s", rec.Code, rec.Body)
	}
	got := readStoredDelivery(t, token)
	if got.outcome != string(work.OutcomeStuck) || len(got.links) != 0 || got.source != store.DeliverySourceLegacy {
		t.Fatalf("stored %+v, want stuck with no links from an older-worker report", got)
	}
}

func TestOutcomeDeliveryIsBoundToTheRun(t *testing.T) {
	good := func(branch string) map[string]any {
		return map[string]any{"forge": "home", "repository": "o/r", "branch": branch, "observed": "opened",
			"number": 7, "url": "https://forgejo/o/r/pulls/7", "base": "main", "head": deliveryHead}
	}
	for name, tc := range map[string]struct {
		outcome      string
		delivery     func(branch string) map[string]any
		links        []string
		wantOutcome  work.Outcome
		wantSource   string
		wantObserved harness.DeliveryObservation
		wantLinks    int
	}{
		"observed delivery": {
			outcome: "pr_opened", delivery: good,
			links:       []string{"https://forgejo/o/r/pulls/7", "https://forgejo/other/repo/pulls/123"},
			wantOutcome: work.OutcomePROpened, wantSource: store.DeliverySourceWorker, wantObserved: harness.DeliveryOpened, wantLinks: 1,
		},
		"another repository": {
			outcome: "pr_opened",
			delivery: func(branch string) map[string]any {
				d := good(branch)
				d["repository"], d["url"], d["number"] = "other/repo", "https://forgejo/other/repo/pulls/123", 123
				return d
			},
			links:       []string{"https://forgejo/other/repo/pulls/123"},
			wantOutcome: work.OutcomeStuck, wantSource: store.DeliverySourceMismatch, wantObserved: harness.DeliveryUnknown,
		},
		"another branch": {
			outcome:     "pr_opened",
			delivery:    func(string) map[string]any { return good("main") },
			wantOutcome: work.OutcomeStuck, wantSource: store.DeliverySourceMismatch, wantObserved: harness.DeliveryUnknown,
		},
		"url of another pull request": {
			outcome: "pr_opened",
			delivery: func(branch string) map[string]any {
				d := good(branch)
				d["url"] = "https://forgejo/o/r/pulls/8"
				return d
			},
			wantOutcome: work.OutcomeStuck, wantSource: store.DeliverySourceMismatch, wantObserved: harness.DeliveryUnknown,
		},
		"failed forge read": {
			outcome: "pr_opened",
			delivery: func(branch string) map[string]any {
				return map[string]any{"forge": "home", "repository": "o/r", "branch": branch, "observed": "unknown", "reason": "503"}
			},
			wantOutcome: work.OutcomeStuck, wantSource: store.DeliverySourceWorker, wantObserved: harness.DeliveryUnknown,
		},
		"failed forge read is not no change": {
			outcome: "no_change_needed",
			delivery: func(branch string) map[string]any {
				return map[string]any{"forge": "home", "repository": "o/r", "branch": branch, "observed": "unknown", "reason": "timeout"}
			},
			wantOutcome: work.OutcomeStuck, wantSource: store.DeliverySourceWorker, wantObserved: harness.DeliveryUnknown,
		},
		"head did not move": {
			outcome: "pr_updated",
			delivery: func(branch string) map[string]any {
				d := good(branch)
				d["observed"], d["headBefore"] = "none", deliveryHead
				return d
			},
			links:       []string{"https://forgejo/o/r/pulls/7"},
			wantOutcome: work.OutcomeNoChangeNeeded, wantSource: store.DeliverySourceWorker, wantObserved: harness.DeliveryNone, wantLinks: 1,
		},
		"head moved": {
			outcome: "pr_updated",
			delivery: func(branch string) map[string]any {
				d := good(branch)
				d["observed"], d["headBefore"] = "updated", deliveryBefore
				return d
			},
			wantOutcome: work.OutcomePRUpdated, wantSource: store.DeliverySourceWorker, wantObserved: harness.DeliveryUpdated,
		},
	} {
		t.Run(name, func(t *testing.T) {
			token, branch := deliveryWriter(t, "1733")
			body := map[string]any{"outcome": tc.outcome, "summary": "s", "delivery": tc.delivery(branch)}
			if tc.links != nil {
				body["links"] = tc.links
			}
			if rec := postRun(t, token, "outcome", body); rec.Code != http.StatusNoContent {
				t.Fatalf("outcome = %d %s", rec.Code, rec.Body)
			}
			got := readStoredDelivery(t, token)
			if got.outcome != string(tc.wantOutcome) || got.source != tc.wantSource || got.delivery == nil ||
				got.delivery.Observed != tc.wantObserved || len(got.links) != tc.wantLinks {
				t.Fatalf("stored outcome=%s source=%s delivery=%+v links=%v, want %s/%s/%s with %d links",
					got.outcome, got.source, got.delivery, got.links, tc.wantOutcome, tc.wantSource, tc.wantObserved, tc.wantLinks)
			}
			if got.outcome == string(work.OutcomeStuck) && got.stuckReason == "" {
				t.Error("a stuck Run carries no reason")
			}
		})
	}
}

func TestOutcomeRefusesAnUnknownDeliveryObservation(t *testing.T) {
	token, branch := deliveryWriter(t, "1734")
	rec := postRun(t, token, "outcome", map[string]any{"outcome": "pr_opened", "summary": "s",
		"delivery": map[string]any{"repository": "o/r", "branch": branch, "observed": "merged"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("outcome = %d, want 400", rec.Code)
	}
}

func TestCheckpointMustNameTheRunsBranchAndRepository(t *testing.T) {
	token, branch := deliveryWriter(t, "1735")
	for name, tc := range map[string]struct {
		cp   map[string]any
		want int
	}{
		"another branch":       {map[string]any{"phase": "pr_opened", "branch": "main"}, http.StatusBadRequest},
		"foreign pull request": {map[string]any{"phase": "pr_opened", "branch": branch, "prUrl": "https://forgejo/other/repo/pulls/1"}, http.StatusBadRequest},
		"the Run's own":        {map[string]any{"phase": "pr_opened", "branch": branch, "prUrl": "https://forgejo/o/r/pulls/1"}, http.StatusNoContent},
	} {
		if rec := postRun(t, token, "checkpoint", tc.cp); rec.Code != tc.want {
			t.Errorf("%s: checkpoint = %d %s, want %d", name, rec.Code, rec.Body, tc.want)
		}
	}
	var n int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM checkpoints`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("checkpoints = %d, want only the Run's own", n)
	}
}
