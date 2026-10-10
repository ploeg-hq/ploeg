package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func commit(c string) string { return strings.Repeat(c, 40) }

func deploy(t *testing.T, environment, sha string, at time.Time) RecordedDeployment {
	t.Helper()
	rec, err := testStore.RecordDeployment(context.Background(), Deployment{Forge: "forgejo", Owner: "WebGrip", Name: "Ploeg",
		Environment: environment, SHA: sha, DeployedAt: at, URL: "https://ci.example/runs/" + sha[:4], Source: "ci"})
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestMigration0025AddsDeployments(t *testing.T) {
	ctx := context.Background()
	var applied bool
	if err := testStore.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = '0025_deployments.sql')`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("migration 0025 was not applied")
	}
	for _, table := range []string{"deployments", "pull_request_deployments"} {
		var exists bool
		if err := testStore.pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil || !exists {
			t.Errorf("table %s: exists %v, err %v", table, exists, err)
		}
	}
	if _, err := testStore.pool.Exec(ctx, `INSERT INTO deployments (forge, repo_owner, repo_name, environment, sha, deployed_at)
		VALUES ('forgejo', 'o', 'r', 'Production', $1, now())`, commit("a")); err == nil {
		t.Error("an uppercase environment must be refused by the table")
	}
}

func TestRecordDeployment_IsIdempotentPerRepositoryEnvironmentAndSHA(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	first := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	a := deploy(t, "production", commit("a"), first)
	if !a.Created || a.ID == 0 || !a.DeployedAt.Equal(first) {
		t.Fatalf("first = %+v", a)
	}
	again, err := testStore.RecordDeployment(ctx, Deployment{Forge: "forgejo", Owner: "webgrip", Name: "ploeg",
		Environment: "production", SHA: commit("a"), DeployedAt: first.Add(time.Hour), Source: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if again.Created || again.ID != a.ID || !again.DeployedAt.Equal(first) {
		t.Errorf("repeat = %+v; want the first report's id and time", again)
	}
	other := deploy(t, "test", commit("a"), first)
	if !other.Created || other.ID == a.ID {
		t.Errorf("another environment = %+v; want its own row", other)
	}
	var source, url string
	if err := testStore.pool.QueryRow(ctx, `SELECT source, url FROM deployments WHERE id = $1`, a.ID).Scan(&source, &url); err != nil {
		t.Fatal(err)
	}
	if source != "ci" || url != "https://ci.example/runs/aaaa" {
		t.Errorf("stored source %q url %q; a repeat never rewrites the first report", source, url)
	}
	var audits int
	var detail string
	if err := testStore.pool.QueryRow(ctx, `SELECT count(*), min(detail::text) FROM audit_log
		WHERE action = 'deploy.recorded' AND actor = 'deploy:ci' AND work_item_id IS NULL`).Scan(&audits, &detail); err != nil {
		t.Fatal(err)
	}
	if audits != 2 || !strings.Contains(detail, `"repo": "webgrip/ploeg"`) {
		t.Errorf("audit rows = %d (%s); want one per recorded deploy, none for the repeat", audits, detail)
	}
}

type deployFixture struct {
	t    *testing.T
	item int64
	base time.Time
}

func newDeployFixture(t *testing.T) *deployFixture {
	t.Helper()
	resetTables(t)
	item, _ := pullRequestItem(t, "agent/vik-2000")
	return &deployFixture{t: t, item: item, base: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
}

func (f *deployFixture) merged(number int, mergeCommit string, minutes int) int64 {
	f.t.Helper()
	at := f.base.Add(time.Duration(minutes) * time.Minute)
	if ok, err := testStore.RecordPullRequestFacts(context.Background(), PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg",
		Number: number, WorkItemID: f.item, State: "merged", MergeCommitSHA: mergeCommit, MergedAt: &at}); err != nil || !ok {
		f.t.Fatalf("recorded = %v, err = %v", ok, err)
	}
	var id int64
	if err := testStore.pool.QueryRow(context.Background(), `SELECT id FROM pull_requests WHERE number = $1`, number).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func candidateNumbers(t *testing.T, deploymentID int64, limit int) []int {
	t.Helper()
	candidates, err := testStore.DeployCandidates(context.Background(), deploymentID, 10*time.Minute, limit)
	if err != nil {
		t.Fatal(err)
	}
	out := []int{}
	for _, c := range candidates {
		if c.Repo != "webgrip/ploeg" {
			t.Errorf("candidate repo = %q; the forge needs the reported path", c.Repo)
		}
		out = append(out, c.Number)
	}
	return out
}

func TestDeployCandidates_OnlyMergedUnmarkedPullRequestsMergedBeforeTheDeploy(t *testing.T) {
	f := newDeployFixture(t)
	ctx := context.Background()
	f.merged(1, commit("1"), 0)
	f.merged(2, commit("2"), 30)
	f.merged(3, commit("3"), 125)
	if ok, err := testStore.RecordPullRequestFacts(ctx, PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 4,
		WorkItemID: f.item, State: "open", HeadSHA: commit("4")}); err != nil || !ok {
		t.Fatal(err)
	}
	if ok, err := testStore.RecordPullRequestFacts(ctx, PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 5,
		WorkItemID: f.item, State: "merged"}); err != nil || !ok {
		t.Fatal(err)
	}
	if ok, err := testStore.RecordPullRequestFacts(ctx, PullRequestFacts{Forge: "gitlab", Repo: "webgrip/ploeg", Number: 6,
		WorkItemID: f.item, State: "merged", MergeCommitSHA: commit("6")}); err != nil || !ok {
		t.Fatal(err)
	}

	d := deploy(t, "production", commit("d"), f.base.Add(time.Hour+55*time.Minute))
	if got := fmt.Sprint(candidateNumbers(t, d.ID, 50)); got != "[3 2 1]" {
		t.Errorf("candidates = %s; want merged ones with a merge commit on this forge, newest first, within the skew", got)
	}
	if got := fmt.Sprint(candidateNumbers(t, d.ID, 2)); got != "[3 2]" {
		t.Errorf("bounded batch = %s", got)
	}
	early := deploy(t, "production", commit("e"), f.base.Add(15*time.Minute))
	if got := fmt.Sprint(candidateNumbers(t, early.ID, 50)); got != "[1]" {
		t.Errorf("candidates of an earlier deploy = %s; a change merged after the deploy cannot be in it", got)
	}

	pr2, err := testStore.DeployCandidates(ctx, d.ID, 10*time.Minute, 50)
	if err != nil {
		t.Fatal(err)
	}
	if newly, err := testStore.MarkDeployed(ctx, pr2[1].PullRequestID, d.ID); err != nil || !newly {
		t.Fatalf("mark = %v, %v", newly, err)
	}
	if got := fmt.Sprint(candidateNumbers(t, d.ID, 50)); got != "[3 1]" {
		t.Errorf("after marking 2 = %s", got)
	}
	acceptance := deploy(t, "acceptance", commit("d"), f.base.Add(2*time.Hour))
	if got := fmt.Sprint(candidateNumbers(t, acceptance.ID, 50)); got != "[3 2 1]" {
		t.Errorf("another environment = %s; marks are per environment", got)
	}
}

func TestMarkDeployed_KeepsTheFirstTimePerEnvironment(t *testing.T) {
	f := newDeployFixture(t)
	ctx := context.Background()
	pr := f.merged(1, commit("1"), 0)
	second := deploy(t, "production", commit("b"), f.base.Add(2*time.Hour))
	third := deploy(t, "production", commit("c"), f.base.Add(3*time.Hour))
	first := deploy(t, "production", commit("a"), f.base.Add(time.Hour))

	firstAt := func() (time.Time, int64) {
		t.Helper()
		var at time.Time
		var deployment int64
		if err := testStore.pool.QueryRow(ctx, `SELECT first_deployed_at, deployment_id FROM pull_request_deployments
			WHERE pull_request_id = $1 AND environment = 'production'`, pr).Scan(&at, &deployment); err != nil {
			t.Fatal(err)
		}
		return at, deployment
	}
	if newly, err := testStore.MarkDeployed(ctx, pr, second.ID); err != nil || !newly {
		t.Fatalf("first mark = %v, %v", newly, err)
	}
	if newly, err := testStore.MarkDeployed(ctx, pr, third.ID); err != nil || newly {
		t.Errorf("a later deploy: newly = %v, err = %v", newly, err)
	}
	if at, id := firstAt(); !at.Equal(second.DeployedAt) || id != second.ID {
		t.Errorf("after a later deploy: %v from %d; want the first time kept", at, id)
	}
	if newly, err := testStore.MarkDeployed(ctx, pr, first.ID); err != nil || newly {
		t.Errorf("an earlier deploy reported late: newly = %v, err = %v; it is not a new mark", newly, err)
	}
	if at, id := firstAt(); !at.Equal(first.DeployedAt) || id != first.ID {
		t.Errorf("after an earlier deploy: %v from %d; want the earliest time", at, id)
	}
}
