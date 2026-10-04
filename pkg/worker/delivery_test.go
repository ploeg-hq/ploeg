package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func init() {
	forgeReadBackoff = []time.Duration{0, 0, 0}
}

func findOpenChangeRequestURL(ref harness.RepoRef, token, branch string) (string, error) {
	cr, err := findOpenChangeRequest(ref, token, branch)
	return cr.URL, err
}

const (
	shaBefore = "1111111111111111111111111111111111111111"
	shaAfter  = "2222222222222222222222222222222222222222"
)

func TestForgejoPullRequestFromAForkWithTheSameBranchNameIsNotTheRunsPullRequest(t *testing.T) {
	body := `[{"number":5,"html_url":"https://forge/x/y/pulls/5","head":{"ref":"agent/vik-1","sha":"` + shaBefore + `","repo":{"full_name":"mallory/y"}},"base":{"ref":"main","repo":{"full_name":"x/y"}}},
	          {"number":7,"html_url":"https://forge/x/y/pulls/7","head":{"ref":"agent/vik-1","sha":"` + shaAfter + `","repo":{"full_name":"x/y"}},"base":{"ref":"main","repo":{"full_name":"x/y"}}}]`
	base, _, _ := forgeStub(t, body)
	got, err := findOpenChangeRequest(harness.RepoRef{ForgeURL: base, Owner: "x", Name: "y", BaseBranch: "main"}, "tok", "agent/vik-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Number != 7 || got.URL != "https://forge/x/y/pulls/7" || got.HeadSHA != shaAfter || got.BaseBranch != "main" {
		t.Fatalf("change request = %+v, want pull request 7 from x/y itself with its head commit", got)
	}
}

func TestGitLabMergeRequestFromAForkIsNotTheRunsMergeRequest(t *testing.T) {
	body := `[{"iid":2,"web_url":"https://gl/g/p/-/merge_requests/2","source_branch":"agent/vik-1","target_branch":"main","sha":"` + shaBefore + `","source_project_id":9,"target_project_id":4},
	          {"iid":3,"web_url":"https://gl/g/p/-/merge_requests/3","source_branch":"agent/vik-1","target_branch":"main","sha":"` + shaAfter + `","source_project_id":4,"target_project_id":4}]`
	base, _, _ := forgeStub(t, body)
	got, err := findOpenChangeRequest(harness.RepoRef{Forge: harness.ForgeGitLab, ForgeURL: base, Owner: "g", Name: "p", BaseBranch: "main"}, "tok", "agent/vik-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Number != 3 || got.HeadSHA != shaAfter {
		t.Fatalf("change request = %+v, want merge request 3 from the project itself", got)
	}
}

func TestAForgeReadIsTriedThreeTimesBeforeItIsUnknown(t *testing.T) {
	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if reads.Add(1) < 3 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)
	ref := harness.RepoRef{ForgeURL: srv.URL, Owner: "x", Name: "y"}
	if _, err := readChangeRequest(ref, "tok", "b"); err != nil || reads.Load() != 3 {
		t.Fatalf("err=%v reads=%d, want success on the third read", err, reads.Load())
	}
	reads.Store(-10)
	if _, err := readChangeRequest(ref, "tok", "b"); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("err=%v, want the last read's failure after three tries", err)
	}
}

func TestAnAgentCannotClaimDelivery(t *testing.T) {
	claim := func(o work.Outcome) harness.OutcomeReport {
		return harness.OutcomeReport{
			Outcome: o, Summary: "opened PR #123",
			Links:        []string{"https://forge/other/repo/pulls/123"},
			Checkpoint:   &work.Checkpoint{Phase: "pr_opened", Branch: "elsewhere", PRURL: "https://forge/other/repo/pulls/123"},
			Delivery:     &harness.Delivery{Repository: "other/repo", Branch: "elsewhere", Observed: harness.DeliveryOpened, Number: 123},
			Verification: &harness.Verification{Result: harness.VerificationPassed},
			Problem:      "the bug", Solution: "the fix",
		}
	}
	for _, tc := range []struct {
		name      string
		report    harness.OutcomeReport
		prURL     string
		prExisted bool
		want      work.Outcome
		wantLinks []string
	}{
		{"pr_opened with no pull request on the forge", claim(work.OutcomePROpened), "", false, work.OutcomeNoChangeNeeded, nil},
		{"pr_updated with no pull request on the forge", claim(work.OutcomePRUpdated), "", false, work.OutcomeNoChangeNeeded, nil},
		{"pr_opened while the forge shows the earlier pull request", claim(work.OutcomePROpened), "https://forge/o/r/pulls/9", true, work.OutcomePRUpdated, []string{"https://forge/o/r/pulls/9"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveOutcome("acp", tc.report, nil, nil, tc.prURL, tc.prExisted, "t", "agent/vik-1", nil, true, true)
			if got.Outcome != tc.want {
				t.Fatalf("outcome = %q, want %q", got.Outcome, tc.want)
			}
			if strings.Join(got.Links, ",") != strings.Join(tc.wantLinks, ",") {
				t.Errorf("links = %v, want %v: an agent's link never reaches the report", got.Links, tc.wantLinks)
			}
			if got.Checkpoint != nil && (got.Checkpoint.Branch != "agent/vik-1" || strings.Contains(got.Checkpoint.PRURL, "123")) {
				t.Errorf("checkpoint = %+v: the agent's checkpoint survived", got.Checkpoint)
			}
			if got.Delivery != nil || got.Verification != nil {
				t.Errorf("delivery=%+v verification=%+v survived resolveOutcome", got.Delivery, got.Verification)
			}
			if got.Problem != "the bug" || got.Solution != "the fix" {
				t.Errorf("problem/solution = %q/%q, want the writer's account kept", got.Problem, got.Solution)
			}
		})
	}

	reader := harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "reviewed", Findings: "- nit",
		Verdict: harness.VerdictRequestChanges, Links: []string{"https://forge/other/repo/pulls/123"}}
	got := resolveOutcome("acp", reader, nil, nil, "https://forge/o/r/pulls/9", true, "t", "agent/vik-1", nil, true, false)
	if got.Verdict != harness.VerdictRequestChanges || got.Findings != "- nit" {
		t.Errorf("reader report = %+v, want its findings and verdict kept", got)
	}
	if len(got.Links) != 1 || got.Links[0] != "https://forge/o/r/pulls/9" {
		t.Errorf("reader links = %v, want only the pull request the worker read", got.Links)
	}
}

func TestDeliveryIsWhatTheTwoForgeReadsShowed(t *testing.T) {
	ref := harness.RepoRef{Forge: "forgejo-main", Owner: "o", Name: "r"}
	pr := func(n int, head string) changeRequest {
		return changeRequest{URL: "https://forge/o/r/pulls/" + map[int]string{3: "3", 4: "4"}[n], Number: n, BaseBranch: "main", HeadSHA: head}
	}
	for _, tc := range []struct {
		name          string
		before, after changeRequest
		err           error
		want          harness.Delivery
	}{
		{"new pull request", changeRequest{}, pr(3, shaAfter), nil,
			harness.Delivery{Observed: harness.DeliveryOpened, Number: 3, URL: "https://forge/o/r/pulls/3", Base: "main", Head: shaAfter}},
		{"pushed to the open pull request", pr(3, shaBefore), pr(3, shaAfter), nil,
			harness.Delivery{Observed: harness.DeliveryUpdated, Number: 3, URL: "https://forge/o/r/pulls/3", Base: "main", Head: shaAfter, HeadBefore: shaBefore}},
		{"open pull request with the same head", pr(3, shaBefore), pr(3, shaBefore), nil,
			harness.Delivery{Observed: harness.DeliveryNone, Number: 3, URL: "https://forge/o/r/pulls/3", Base: "main", Head: shaBefore, HeadBefore: shaBefore}},
		{"pull request replaced during the Run", pr(3, shaBefore), pr(4, shaAfter), nil,
			harness.Delivery{Observed: harness.DeliveryOpened, Number: 4, URL: "https://forge/o/r/pulls/4", Base: "main", Head: shaAfter}},
		{"no pull request", changeRequest{}, changeRequest{}, nil, harness.Delivery{Observed: harness.DeliveryNone}},
		{"a failed read", pr(3, shaBefore), changeRequest{}, errTooManyOpenPullRequests,
			harness.Delivery{Observed: harness.DeliveryUnknown, Reason: errTooManyOpenPullRequests.Error()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.want.Forge, tc.want.Repository, tc.want.Branch = "forgejo-main", "o/r", "agent/vik-1"
			if got := observedDelivery(ref, "agent/vik-1", tc.before, tc.after, tc.err); *got != tc.want {
				t.Fatalf("delivery = %+v\nwant       %+v", *got, tc.want)
			}
		})
	}
}

const dropBoxClaim = `printf '%s' '{"outcome":"pr_opened","summary":"opened PR #123","links":["https://forge.example/other/repo/pulls/123"],"checkpoint":{"phase":"pr_opened","branch":"elsewhere","prUrl":"https://forge.example/other/repo/pulls/123"},"failureReason":"budget","problem":"the bug","solution":"the fix"}' > "$PLOEG_OUTCOME_FILE"`

func TestAWriterThatClaimsAPullRequestItNeverOpenedDeliversNothing(t *testing.T) {
	forge := newWriterForge(t, false, pullsNone)
	report := runWriterAgainst(t, forge, acpAgentDuringPrompt(t, editToolCall+"\n      "+dropBoxClaim))
	if report.Outcome != work.OutcomeNoChangeNeeded {
		t.Fatalf("report = %+v, want no_change_needed: the forge shows no pull request", report)
	}
	if len(report.Links) != 0 || report.Checkpoint != nil || report.FailureReason != "" {
		t.Errorf("links=%v checkpoint=%+v failure=%q: the agent's delivery claims reached the report", report.Links, report.Checkpoint, report.FailureReason)
	}
	if report.Problem != "the bug" || report.Solution != "the fix" {
		t.Errorf("problem/solution = %q/%q, want the writer's account kept", report.Problem, report.Solution)
	}
	if report.Delivery == nil || report.Delivery.Observed != harness.DeliveryNone || report.Delivery.Repository != "webgrip/example" ||
		report.Delivery.Branch != writerBranch || report.Delivery.Number != 0 {
		t.Errorf("delivery = %+v, want none on webgrip/example %s", report.Delivery, writerBranch)
	}
}

func TestAWriterThatOpenedAPullRequestReportsWhatTheForgeShowed(t *testing.T) {
	forge := newWriterForge(t, false, pullsForPushedBranch)
	report := runWriterAgainst(t, forge, acpAgentDuringPrompt(t, dropBoxClaim+`
      printf 'func main() {}\n' >> main.go
      git checkout -q -b `+writerBranch+` >&2 && git commit -qam 'add main' >&2 && git push -q origin `+writerBranch+` >&2`))
	if report.Outcome != work.OutcomePROpened {
		t.Fatalf("report = %+v, want pr_opened", report)
	}
	d := report.Delivery
	if d == nil || d.Observed != harness.DeliveryOpened || d.Number != 3 || d.Head != forge.branchHead() || d.Base != "development" ||
		!strings.HasSuffix(d.URL, "/webgrip/example/pulls/3") {
		t.Fatalf("delivery = %+v, want opened #3 at %s", d, forge.branchHead())
	}
	if len(report.Links) != 1 || report.Links[0] != d.URL {
		t.Errorf("links = %v, want only the pull request the worker read", report.Links)
	}
}

func TestAWriterThatPushedToItsPullRequestReportsTheMovedHead(t *testing.T) {
	forge := newWriterForge(t, true, pullsForPushedBranch)
	before := forge.branchHead()
	report := runWriterAgainst(t, forge, acpAgentDuringPrompt(t, editToolCall+`
      git fetch -q origin `+writerBranch+` >&2 && git checkout -q -b `+writerBranch+` FETCH_HEAD >&2
      printf 'func main() {}\n' >> main.go && git commit -qam 'fix it' >&2 && git push -q origin `+writerBranch+` >&2`))
	d := report.Delivery
	if report.Outcome != work.OutcomePRUpdated || d == nil || d.Observed != harness.DeliveryUpdated ||
		d.HeadBefore != before || d.Head != forge.branchHead() || d.Head == before {
		t.Fatalf("report = %+v delivery = %+v, want pr_updated from %s to the pushed head", report, d, before)
	}
}

func TestAWriterWhoseForgeCannotBeReadBeforeTheRunNeverStarts(t *testing.T) {
	forge := newWriterForge(t, false, pullsFailAlways)
	adapter := &editingAdapter{edit: func(string) {}, run: func(context.Context) error { return nil }}
	ran := false
	adapter.edit = func(string) { ran = true }
	report := runWriterAgainst(t, forge, adapter)
	if ran {
		t.Fatal("the harness ran although the worker could not tell whether a pull request already existed")
	}
	if report.Outcome != work.OutcomeFailed || report.FailureReason != string(work.FailureInfraNode) ||
		!strings.Contains(report.Summary, "could not read the forge before the Run") {
		t.Fatalf("report = %+v, want failed/infra_node naming the unreadable forge", report)
	}
	if report.Delivery == nil || report.Delivery.Observed != harness.DeliveryUnknown || !strings.Contains(report.Delivery.Reason, "HTTP 500") {
		t.Errorf("delivery = %+v, want unknown with the forge's error", report.Delivery)
	}
}

func TestAWriterWhoseForgeCannotBeReadAfterTheRunIsUnknown(t *testing.T) {
	forge := newWriterForge(t, false, pullsFailAfterFirstRead)
	report := runWriterAgainst(t, forge, acpAgentDuringPrompt(t, dropBoxClaim))
	if report.Outcome != work.OutcomeStuck || report.Delivery == nil || report.Delivery.Observed != harness.DeliveryUnknown {
		t.Fatalf("report = %+v delivery = %+v, want stuck with an unknown delivery", report, report.Delivery)
	}
}
