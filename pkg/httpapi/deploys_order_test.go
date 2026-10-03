package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDeploys_AnEarlierDeployReportedLateCorrectsTheFirstTime(t *testing.T) {
	forge := &compareForge{responses: map[string]string{}}
	s, _ := deployServer(t, forge)
	item, _ := factsItem(t)
	now := time.Now().UTC().Truncate(time.Second)
	mergedPullRequest(t, item, 18, sha("1"), now.Add(-4*time.Hour))
	for _, deployed := range []string{"a", "b", "c"} {
		forge.answer(sha(deployed), sha("1"), `{"total_commits":0,"commits":[]}`)
	}
	forge.answer(sha("9"), sha("1"), `{"total_commits":1,"commits":[{"sha":"1"}]}`)
	report := func(commit string, at time.Time) deployAnswer {
		t.Helper()
		body := deployBody("production", sha(commit))
		body["deployedAt"] = at.Format(time.RFC3339)
		return accepted(t, postDeploy(t, s, "Bearer "+deployToken, body))
	}
	t1, t2, t3 := now.Add(-3*time.Hour), now.Add(-2*time.Hour), now.Add(-time.Hour)

	if a := report("c", t3); a.PullRequests != 1 {
		t.Errorf("newest deploy first marked %d", a.PullRequests)
	}
	if a := report("b", t2); a.PullRequests != 0 {
		t.Errorf("an earlier deploy counted %d new marks; it corrects, it does not add", a.PullRequests)
	}
	if got := firstDeploys(t)["18/production"]; !got.Equal(t2) {
		t.Errorf("after the second report: first deploy %v, want %v", got, t2)
	}
	report("a", t1)
	if got := firstDeploys(t)["18/production"]; !got.Equal(t1) {
		t.Errorf("after the third report: first deploy %v, want the earliest %v", got, t1)
	}
	if got := fmt.Sprint(forge.took()); got != "[a...1 b...1 c...1]" {
		t.Errorf("compared %s", got)
	}

	for _, again := range []struct {
		commit string
		at     time.Time
	}{{"c", t3}, {"a", t1}, {"b", t2}} {
		if a := report(again.commit, again.at); a.PullRequests != 0 {
			t.Errorf("repeat of %s marked %d", again.commit, a.PullRequests)
		}
	}
	if got := forge.took(); len(got) != 0 {
		t.Errorf("repeats compared %v; every pair is compared once", got)
	}
	if a := report("9", now.Add(-210*time.Minute)); a.PullRequests != 0 {
		t.Errorf("an earlier deploy without the change marked %d", a.PullRequests)
	}
	var deployedSHA string
	if err := testPool.QueryRow(context.Background(), `SELECT d.sha FROM pull_request_deployments pd
		JOIN deployments d ON d.id = pd.deployment_id`).Scan(&deployedSHA); err != nil {
		t.Fatal(err)
	}
	if got := firstDeploys(t)["18/production"]; !got.Equal(t1) || deployedSHA != sha("a") {
		t.Errorf("first deploy %v from %s; an earlier deploy that lacks the change moves nothing", got, deployedSHA[:1])
	}
}

type ancestorForge struct {
	mu    sync.Mutex
	down  bool
	calls map[string]int
}

func (f *ancestorForge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	basehead, ok := strings.CutPrefix(r.URL.Path, "/api/v1/repos/webgrip/ploeg/compare/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if f.down {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	f.calls[basehead]++
	_, _ = w.Write([]byte(`{"total_commits":0,"commits":[]}`))
}

func (f *ancestorForge) setDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

func (f *ancestorForge) compared() (pairs, calls int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, n := range f.calls {
		pairs++
		calls += n
	}
	return pairs, calls
}

type deployCheckState struct {
	checked  bool
	attempts int
	due      bool
}

func deployState(t *testing.T, id string) deployCheckState {
	t.Helper()
	var st deployCheckState
	if err := testPool.QueryRow(context.Background(), `SELECT checked_at IS NOT NULL, check_attempts, check_after <= now()
		FROM deployments WHERE id = $1`, id).Scan(&st.checked, &st.attempts, &st.due); err != nil {
		t.Fatal(err)
	}
	return st
}

func makeDue(t *testing.T) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `UPDATE deployments SET check_after = now()`); err != nil {
		t.Fatal(err)
	}
}

func TestDeploys_TheSweepChecksWhatOneReportCouldNot(t *testing.T) {
	forge := &ancestorForge{calls: map[string]int{}}
	s, _ := deployServer(t, forge)
	item, _ := factsItem(t)
	now := time.Now().UTC().Truncate(time.Second)
	for i := range 120 {
		mergedPullRequest(t, item, 100+i, fmt.Sprintf("%040x", i+1), now.Add(-time.Duration(200-i)*time.Minute))
	}
	a := accepted(t, postDeploy(t, s, "Bearer "+deployToken, deployBody("production", sha("d"))))
	if a.PullRequests != deployCandidateLimit {
		t.Errorf("the report marked %d; one pass checks %d", a.PullRequests, deployCandidateLimit)
	}
	if st := deployState(t, a.DeployID); st.checked || !st.due || st.attempts != 0 {
		t.Errorf("after a full batch: %+v; want pending and due at once", st)
	}
	ctx := context.Background()
	s.SweepDeployChecks(ctx)
	if got := len(firstDeploys(t)); got != 100 {
		t.Errorf("after one sweep %d marked", got)
	}
	makeDue(t)
	s.SweepDeployChecks(ctx)
	if got := len(firstDeploys(t)); got != 120 {
		t.Errorf("after two sweeps %d marked; want all 120 without another deploy", got)
	}
	if st := deployState(t, a.DeployID); !st.checked {
		t.Errorf("after the last batch: %+v; want checked", st)
	}
	makeDue(t)
	s.SweepDeployChecks(ctx)
	if pairs, calls := forge.compared(); pairs != 120 || calls != 120 {
		t.Errorf("compared %d pairs in %d calls; want each pull request once", pairs, calls)
	}
}

func TestDeploys_AForgeOutageDuringTheCheckIsRetriedByTheSweep(t *testing.T) {
	forge := &ancestorForge{calls: map[string]int{}, down: true}
	s, _ := deployServer(t, forge)
	item, _ := factsItem(t)
	now := time.Now().UTC().Truncate(time.Second)
	mergedPullRequest(t, item, 18, sha("1"), now.Add(-2*time.Hour))
	mergedPullRequest(t, item, 19, sha("2"), now.Add(-time.Hour))
	a := accepted(t, postDeploy(t, s, "Bearer "+deployToken, deployBody("production", sha("d"))))
	if a.PullRequests != 0 {
		t.Errorf("marked %d while the forge was down", a.PullRequests)
	}
	if st := deployState(t, a.DeployID); st.checked || st.due || st.attempts != 1 {
		t.Errorf("after a failed pass: %+v; want pending, paused, one attempt", st)
	}
	ctx := context.Background()
	forge.setDown(false)
	s.SweepDeployChecks(ctx)
	if pairs, _ := forge.compared(); pairs != 0 {
		t.Errorf("the sweep compared %d pairs before the pause ended", pairs)
	}
	makeDue(t)
	s.SweepDeployChecks(ctx)
	if got := firstDeploys(t); len(got) != 2 {
		t.Errorf("after the forge recovered: %v; want both marked", got)
	}
	if st := deployState(t, a.DeployID); !st.checked {
		t.Errorf("after recovery: %+v; want checked", st)
	}
}

func TestDeploys_TheSweepGivesUpAfterRepeatedForgeFailures(t *testing.T) {
	forge := &ancestorForge{calls: map[string]int{}, down: true}
	s, _ := deployServer(t, forge)
	item, _ := factsItem(t)
	mergedPullRequest(t, item, 18, sha("1"), time.Now().Add(-time.Hour))
	a := accepted(t, postDeploy(t, s, "Bearer "+deployToken, deployBody("production", sha("d"))))
	for range deployCheckMaxAttempts - 1 {
		if st := deployState(t, a.DeployID); st.checked {
			t.Fatalf("given up early: %+v", st)
		}
		makeDue(t)
		s.SweepDeployChecks(context.Background())
	}
	if st := deployState(t, a.DeployID); !st.checked || st.attempts != deployCheckMaxAttempts {
		t.Errorf("after %d failed passes: %+v; want given up", deployCheckMaxAttempts, st)
	}
}
