package store

import (
	"context"
	"testing"
	"time"
)

var pipelineKey = PullRequestKey{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 90}

func TestMigration0033AddsPipelineFacts(t *testing.T) {
	ctx := context.Background()
	for _, column := range []string{"opened_at", "author", "draft", "commits", "force_pushes"} {
		var nullable string
		if err := testStore.pool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns
			WHERE table_name = 'pull_requests' AND column_name = $1`, column).Scan(&nullable); err != nil || nullable != "YES" {
			t.Errorf("pull_requests.%s = %q, %v; an unreported fact is NULL", column, nullable, err)
		}
	}
	for _, table := range []string{"pull_request_events", "pull_request_ci_runs"} {
		var exists bool
		if err := testStore.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`, table).
			Scan(&exists); err != nil || !exists {
			t.Errorf("table %s missing: %v", table, err)
		}
	}
	var body int
	if err := testStore.pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'pull_request_events' AND column_name IN ('body', 'text', 'description')`).Scan(&body); err != nil || body != 0 {
		t.Errorf("pull_request_events keeps text: %d, %v", body, err)
	}
}

func pipelineWorld(t *testing.T) (*world, int64, time.Time) {
	t.Helper()
	w := newWorld(t)
	item := w.item("pipeline", "silver")
	opened := w.now.Add(-10 * time.Hour)
	draft := false
	if ok, err := testStore.RecordPullRequestFacts(w.ctx, PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 90,
		WorkItemID: item, State: "open", HeadSHA: "b", OpenedAt: &opened, Author: "ploeg-bot", Draft: &draft,
		Additions: intp(120), Deletions: intp(30)}); err != nil || !ok {
		t.Fatalf("record play: %v %v", ok, err)
	}
	return w, item, opened
}

func TestRecordPullRequestPipeline_KeepsTheForgesActivityAndCIRuns(t *testing.T) {
	w, item, opened := pipelineWorld(t)
	at := func(minutes int) time.Time { return opened.Add(time.Duration(minutes) * time.Minute) }
	atp := func(minutes int) *time.Time { t := at(minutes); return &t }
	first := at(-120)
	activity := &PullRequestActivity{Commits: 4, FirstCommitAt: &first, ForcePushesKnown: true, Events: []PullRequestEvent{
		{Kind: "push", Actor: "ploeg-bot", At: at(0), HeadSHA: "a"},
		{Kind: "comment", Actor: "ploeg-bot", At: at(1)},
		{Kind: "comment", Actor: "anna", At: at(30)},
		{Kind: "review", Actor: "anna", At: at(45), State: "changes_requested", HeadSHA: "a"},
		{Kind: "force_push", Actor: "ploeg-bot", At: at(75), HeadSHA: "b"},
		{Kind: "unknown", Actor: "x", At: at(76)},
	}}
	ci := &PullRequestCIRuns{Source: "actions", Runs: []PullRequestCIRun{
		{Key: "1", HeadSHA: "a", Workflow: "ci.yml", Status: "failure", CreatedAt: atp(0), StartedAt: atp(2), CompletedAt: atp(10),
			Jobs: []CIJob{{Name: "test", Status: "failure", StartedAt: atp(2), CompletedAt: atp(10), Attempt: 1}}},
		{Key: "2", HeadSHA: "b", Workflow: "ci.yml", Status: "success", CreatedAt: atp(75), StartedAt: atp(76), CompletedAt: atp(80),
			Jobs: []CIJob{{Name: "test", Status: "success", StartedAt: atp(76), CompletedAt: atp(80), Attempt: 0, QueuedSeconds: q64(-5)},
				{Name: "", Status: "success"}, {Name: "lint", Status: "exploded"}}},
		{Key: "bad", HeadSHA: "b", Status: "exploded"},
	}}
	if ok, err := testStore.RecordPullRequestPipeline(w.ctx, pipelineKey, activity, ci, w.now); err != nil || !ok {
		t.Fatalf("record pipeline: %v %v", ok, err)
	}
	p := w.play(item)
	if len(p.Events) != 5 || p.Events[0].Kind != "push" || p.Events[4].Kind != "force_push" {
		t.Errorf("events = %+v; an unknown kind is dropped", p.Events)
	}
	if p.Commits == nil || *p.Commits != 4 || p.ForcePushes == nil || *p.ForcePushes != 1 {
		t.Errorf("commits %v force pushes %v", p.Commits, p.ForcePushes)
	}
	if len(p.CIRuns) != 2 || p.CIRuns[0].Status != "failure" || len(p.CIRuns[1].Jobs) != 1 {
		t.Fatalf("ci runs = %+v; an unknown status and a nameless job are dropped", p.CIRuns)
	}
	if job := p.CIRuns[1].Jobs[0]; job.Attempt != 1 || job.QueuedSeconds != nil {
		t.Errorf("job = %+v; the attempt counts from 1 and a negative queue time is unknown", job)
	}

	if ok, err := testStore.RecordPullRequestPipeline(w.ctx, pipelineKey, nil, nil, w.now); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if p := w.play(item); len(p.Events) != 5 || len(p.CIRuns) != 2 {
		t.Errorf("after a nil read: %d events, %d ci runs; a nil read keeps what is recorded", len(p.Events), len(p.CIRuns))
	}
	if ok, _ := testStore.RecordPullRequestPipeline(w.ctx, PullRequestKey{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 999}, activity, ci,
		w.now); ok {
		t.Error("a pull request Ploeg never recorded was given a pipeline")
	}
}

func TestRecordPullRequestPipeline_UnknownForcePushesStayNull(t *testing.T) {
	w, item, _ := pipelineWorld(t)
	if _, err := testStore.RecordPullRequestPipeline(w.ctx, pipelineKey, &PullRequestActivity{Commits: 2}, nil, w.now); err != nil {
		t.Fatal(err)
	}
	p := w.play(item)
	if p.ForcePushes != nil || p.Commits == nil || *p.Commits != 2 {
		t.Fatalf("play = %+v; a forge that reports no force pushes leaves them null", p)
	}
}

func TestPullRequestCaptureDue(t *testing.T) {
	w, _, _ := pipelineWorld(t)
	due, err := testStore.PullRequestCaptureDue(w.ctx, pipelineKey, w.now)
	if err != nil || !due {
		t.Fatalf("due = %v, %v; never read is due", due, err)
	}
	if _, err := testStore.RecordPullRequestPipeline(w.ctx, pipelineKey, &PullRequestActivity{}, nil, w.now); err != nil {
		t.Fatal(err)
	}
	if due, _ := testStore.PullRequestCaptureDue(w.ctx, pipelineKey, w.now.Add(-time.Minute)); due {
		t.Error("read a minute ago is not due again")
	}
	if due, _ := testStore.PullRequestCaptureDue(w.ctx, pipelineKey, w.now.Add(time.Minute)); !due {
		t.Error("read before the window is due")
	}
	if due, _ := testStore.PullRequestCaptureDue(w.ctx, PullRequestKey{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 5}, w.now); due {
		t.Error("an unrecorded pull request is never due")
	}
}

func TestRecordPullRequestIndentation_MeasuresTheRecordedFilesAndKeepsNoCode(t *testing.T) {
	w, item, _ := pipelineWorld(t)
	diff := []byte("diff --git a/pkg/a.go b/pkg/a.go\n--- a/pkg/a.go\n+++ b/pkg/a.go\n@@ -1 +1,2 @@\n+\tif x {\n+\t\ty()\n")
	if ok, err := testStore.RecordPullRequestIndentation(w.ctx, pipelineKey, diff); err != nil || ok {
		t.Fatalf("indentation before files = %v, %v; it needs the recorded files", ok, err)
	}
	if ok, err := testStore.RecordPullRequestChange(w.ctx, PullRequestChange{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 90,
		Files: []string{"pkg/a.go", "docs/a.md"}, Lines: map[string]FileLines{"pkg/a.go": {2, 0}, "docs/a.md": {10, 0}}}); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := testStore.RecordPullRequestIndentation(w.ctx, pipelineKey, diff); err != nil || !ok {
		t.Fatalf("indentation = %v, %v", ok, err)
	}
	files := w.play(item).Files
	if len(files) != 2 || files[0].Indentation != nil || files[1].Indentation == nil || files[1].Indentation.Added != 3 {
		t.Fatalf("files = %+v; only the file the diff shows is measured", files)
	}
	var stored int
	if err := testStore.pool.QueryRow(w.ctx, `SELECT count(*) FROM pull_request_files f JOIN pull_requests p ON p.id = f.pull_request_id
		WHERE p.number = 90 AND f::text LIKE '%y()%'`).Scan(&stored); err != nil || stored != 0 {
		t.Errorf("rows with code = %d, %v; the diff's code is never stored", stored, err)
	}
	if ok, _ := testStore.RecordPullRequestIndentation(w.ctx, PullRequestKey{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 999}, diff); ok {
		t.Error("a pull request Ploeg never recorded was measured")
	}
}

func TestMigration0044DropsTheRunCardTablesAndColumns(t *testing.T) {
	ctx := context.Background()
	for _, table := range []string{"card_cracks", "card_rarity", "card_comments"} {
		var exists bool
		if err := testStore.pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil || exists {
			t.Errorf("table %s exists = %v (%v); the Run card left Ploeg", table, exists, err)
		}
	}
	for _, column := range []string{"kpis", "kpis_computed_at", "shape"} {
		var n int
		if err := testStore.pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
			WHERE table_name = 'pull_requests' AND column_name = $1`, column).Scan(&n); err != nil || n != 0 {
			t.Errorf("pull_requests.%s still exists (%v)", column, err)
		}
	}
}

func q64(n int64) *int64 { return &n }
