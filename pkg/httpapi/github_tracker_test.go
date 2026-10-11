package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/githubissues"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func githubIssueHook(t *testing.T, h http.Handler, delivery, action, labelName, state string) int {
	t.Helper()
	body := fmt.Sprintf(`{"action":%q,"issue":{"number":42,"title":"Add a health endpoint","body":"Expose /healthz.","state":%q,
		"html_url":"https://github.com/WebGrip/App/issues/42","updated_at":"2026-10-11T09:30:00Z","created_at":"2026-10-09T08:00:00Z",
		"labels":[{"name":"ploeg:bronze"}]},"label":{"name":%q},"repository":{"full_name":"WebGrip/App"},"sender":{"login":"maintainer"}}`,
		action, state, labelName)
	r := httptest.NewRequest(http.MethodPost, "/webhooks/tracker/github", strings.NewReader(body))
	mac := hmac.New(sha256.New, []byte(testTrackerSecret))
	mac.Write([]byte(body))
	r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	r.Header.Set("X-GitHub-Event", "issues")
	r.Header.Set("X-GitHub-Delivery", delivery)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

func githubServer(t *testing.T) http.Handler {
	t.Helper()
	targets, err := target.New(target.Table{
		Targets: map[string]work.Target{"app": appTarget},
		Rules:   []target.Rule{{Scope: "webgrip:app", Default: "app"}},
	}, "forgejo")
	if err != nil {
		t.Fatal(err)
	}
	gh := &githubissues.Provider{Secret: testTrackerSecret, Repositories: map[string]bool{"webgrip:app": true},
		Log: slog.New(slog.DiscardHandler)}
	s := &Server{Store: testStore, LeaseTTL: time.Minute, Log: slog.New(slog.DiscardHandler),
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true}, Targets: targets,
		Trackers: map[string]provider.TrackerProvider{"github": gh}}
	return s.Handler()
}

func githubItem(t *testing.T) (state, team string, target work.Target, n int) {
	t.Helper()
	ctx := context.Background()
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM work_items WHERE provider='github' AND external_id='webgrip:app:42'`).Scan(&n); err != nil || n == 0 {
		return "", "", work.Target{}, n
	}
	if err := testPool.QueryRow(ctx, `SELECT state, team, target_forge, target_owner, target_repo, target_base_branch
		FROM work_items WHERE provider='github' AND external_id='webgrip:app:42'`).
		Scan(&state, &team, &target.Forge, &target.Owner, &target.Repo, &target.BaseBranch); err != nil {
		t.Fatal(err)
	}
	return state, team, target, n
}

func resetDeliveries(t *testing.T) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `DELETE FROM forge_deliveries`); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubTeamLabelQueuesARoutedWorkItem(t *testing.T) {
	reset(t)
	resetDeliveries(t)
	h := githubServer(t)

	if code := githubIssueHook(t, h, "d-1", "labeled", "ploeg:bronze", "open"); code != http.StatusAccepted {
		t.Fatalf("labeled: HTTP %d", code)
	}
	state, team, got, n := githubItem(t)
	if n != 1 || state != string(work.StateQueued) || team != "bronze" || got != appTarget {
		t.Fatalf("work item n=%d state=%q team=%q target=%+v; want one queued bronze item routed to the repository's target", n, state, team, got)
	}
}

func TestGitHubRedeliveryIsSkippedAndANewDeliveryIsApplied(t *testing.T) {
	reset(t)
	resetDeliveries(t)
	h := githubServer(t)

	githubIssueHook(t, h, "d-assign", "labeled", "ploeg:bronze", "open")
	if code := githubIssueHook(t, h, "d-unassign", "unlabeled", "ploeg:bronze", "open"); code != http.StatusAccepted {
		t.Fatalf("unlabeled: HTTP %d", code)
	}
	if state, _, _, _ := githubItem(t); state != string(work.StateWithdrawn) {
		t.Fatalf("state after removing the team label = %q, want withdrawn", state)
	}

	if code := githubIssueHook(t, h, "d-assign", "labeled", "ploeg:bronze", "open"); code != http.StatusAccepted {
		t.Fatalf("redelivery: HTTP %d", code)
	}
	if state, _, _, _ := githubItem(t); state != string(work.StateWithdrawn) {
		t.Fatalf("a redelivered assignment was applied again: state %q", state)
	}

	githubIssueHook(t, h, "d-assign-again", "labeled", "ploeg:bronze", "open")
	if state, _, _, n := githubItem(t); n != 1 || state != string(work.StateQueued) {
		t.Fatalf("a new assignment delivery: n=%d state=%q, want the same Work Item queued again", n, state)
	}
}

func TestClosingAGitHubIssueWithdrawsUnstartedWork(t *testing.T) {
	reset(t)
	resetDeliveries(t)
	h := githubServer(t)

	githubIssueHook(t, h, "c-1", "labeled", "ploeg:bronze", "open")
	githubIssueHook(t, h, "c-2", "closed", "", "closed")
	if state, _, _, _ := githubItem(t); state != string(work.StateWithdrawn) {
		t.Fatalf("state after closing the issue = %q, want withdrawn", state)
	}
	githubIssueHook(t, h, "c-3", "labeled", "ploeg:bronze", "closed")
	if state, _, _, _ := githubItem(t); state != string(work.StateWithdrawn) {
		t.Fatalf("labelling a closed issue dispatched it: state %q", state)
	}
}
