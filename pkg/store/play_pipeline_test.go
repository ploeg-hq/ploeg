package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/playkpi"
	"github.com/ploeg-hq/ploeg/pkg/rarity"
)

var pipelineKey = PullRequestKey{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 90}

func TestMigration0033AddsPipelineFacts(t *testing.T) {
	ctx := context.Background()
	for _, column := range []string{"opened_at", "author", "draft", "commits", "force_pushes", "kpis", "shape"} {
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

func pipelineWorld(t *testing.T) (*crackWorld, int64, time.Time) {
	t.Helper()
	w := newCrackWorld(t)
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

func TestRecordPullRequestPipeline_DerivesTheTimelineAndCIOnTheCard(t *testing.T) {
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
			Jobs: []playkpi.Job{{Name: "test", Status: "failure", StartedAt: atp(2), CompletedAt: atp(10), Attempt: 1}}},
		{Key: "2", HeadSHA: "b", Workflow: "ci.yml", Status: "success", CreatedAt: atp(75), StartedAt: atp(76), CompletedAt: atp(80),
			Jobs: []playkpi.Job{{Name: "test", Status: "success", StartedAt: atp(76), CompletedAt: atp(80), Attempt: 1, QueuedSeconds: q64(60)}}},
		{Key: "bad", HeadSHA: "b", Status: "exploded"},
	}}
	if ok, err := testStore.RecordPullRequestFacts(w.ctx, PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 90,
		Review: &PullRequestReview{Reviewer: "anna", State: "changes_requested", HeadSHA: "a"}}); err != nil || !ok {
		t.Fatal(ok, err)
	}
	opts := CardOptions{Bots: []string{"ploeg-bot"}, Now: w.now, Rarity: &RarityOptions{}}
	before := w.card(item, opts)
	if ok, err := testStore.RecordPullRequestPipeline(w.ctx, pipelineKey, activity, ci, w.now, []string{"ploeg-bot"}); err != nil || !ok {
		t.Fatalf("record pipeline: %v %v", ok, err)
	}
	card := w.card(item, opts)
	if before.Grade == nil || !reflect.DeepEqual(before.Grade, card.Grade) || !reflect.DeepEqual(before.Rarity, card.Rarity) {
		t.Errorf("grade %+v -> %+v, rarity %+v -> %+v; pipeline figures never move the grade or rarity", before.Grade, card.Grade,
			before.Rarity, card.Rarity)
	}
	p := card.Plays[0]
	tl := p.Timeline
	if tl == nil || tl.ReadyAt == nil || !tl.ReadyAt.Equal(opened) || tl.ToFirstFeedback == nil || *tl.ToFirstFeedback != 1800 {
		t.Fatalf("timeline = %+v", tl)
	}
	if *tl.Comments != 1 || *tl.Commits != 4 || *tl.ForcePushes != 1 || *tl.ResponseSeconds != 1800 || *tl.CodingSeconds != 7200 {
		t.Errorf("timeline counts = %+v", tl)
	}
	if p.CITiming == nil || p.CITiming.Runs != 2 || p.CITiming.FailedRuns != 1 || *p.CITiming.LastGreenSeconds != 240 ||
		*p.CITiming.QueueSeconds != 60 || *p.CITiming.FirstPassGreen {
		t.Errorf("ci timing = %+v", p.CITiming)
	}
	if card.Pipeline == nil || card.Pipeline.Plays != 1 || *card.Pipeline.ToFirstFeedback != 1800 || card.Pipeline.CI.Runs != 2 {
		t.Errorf("pipeline = %+v", card.Pipeline)
	}
	var events int
	if err := testStore.pool.QueryRow(w.ctx, `SELECT count(*) FROM pull_request_events`).Scan(&events); err != nil || events != 5 {
		t.Errorf("events stored = %d, %v; an unknown kind is dropped", events, err)
	}

	if ok, err := testStore.RecordPullRequestFacts(w.ctx, PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 90,
		Review: &PullRequestReview{Reviewer: "bob", State: "approved", HeadSHA: "b"}}); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := testStore.RefreshPullRequestKPIs(w.ctx, pipelineKey, w.now, []string{"ploeg-bot"}); err != nil || !ok {
		t.Fatal(ok, err)
	}
	card = w.card(item, CardOptions{Bots: []string{"ploeg-bot"}})
	if tl := card.Plays[0].Timeline; tl.FirstApprovalAt == nil || tl.Reviewers != 2 || *tl.Comments != 1 || card.Plays[0].CITiming == nil {
		t.Errorf("after a webhook review the timeline = %+v; the refresh keeps the stored activity and CI", tl)
	}
	if ok, _ := testStore.RecordPullRequestPipeline(w.ctx, PullRequestKey{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 999}, activity, ci,
		w.now, nil); ok {
		t.Error("a pull request Ploeg never recorded was given a pipeline")
	}
}

func TestRecordPullRequestPipeline_UnknownForcePushesStayNull(t *testing.T) {
	w, item, _ := pipelineWorld(t)
	if _, err := testStore.RecordPullRequestPipeline(w.ctx, pipelineKey, &PullRequestActivity{Commits: 2}, nil, w.now, nil); err != nil {
		t.Fatal(err)
	}
	tl := w.card(item, CardOptions{}).Plays[0].Timeline
	if tl == nil || tl.ForcePushes != nil || tl.Commits == nil || *tl.Commits != 2 || w.card(item, CardOptions{}).Plays[0].CITiming != nil {
		t.Fatalf("timeline = %+v; a forge that reports no force pushes leaves them null", tl)
	}
}

func TestPullRequestCaptureDue(t *testing.T) {
	w, _, _ := pipelineWorld(t)
	due, err := testStore.PullRequestCaptureDue(w.ctx, pipelineKey, w.now)
	if err != nil || !due {
		t.Fatalf("due = %v, %v; never read is due", due, err)
	}
	if _, err := testStore.RecordPullRequestPipeline(w.ctx, pipelineKey, &PullRequestActivity{}, nil, w.now, nil); err != nil {
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

func TestRecordPullRequestShape_MeasuresTheRecordedFiles(t *testing.T) {
	w, item, _ := pipelineWorld(t)
	shapeIn := ShapeInput{Size: defaultMatcher, Paths: playkpi.DefaultMatcher, At: w.now,
		Diff: []byte("diff --git a/pkg/a.go b/pkg/a.go\n--- a/pkg/a.go\n+++ b/pkg/a.go\n@@ -1 +1,2 @@\n+\tif x {\n+\t\ty()\n")}
	if ok, err := testStore.RecordPullRequestShape(w.ctx, pipelineKey, shapeIn); err != nil || ok {
		t.Fatalf("shape before files = %v, %v; it needs the recorded files", ok, err)
	}
	if ok, err := testStore.RecordPullRequestChange(w.ctx, PullRequestChange{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 90,
		Files: []string{"pkg/a.go", "pkg/a_test.go", "go.sum", "docs/a.md"},
		Lines: map[string]FileLines{"pkg/a.go": {80, 20}, "pkg/a_test.go": {40, 0}, "go.sum": {9, 1}, "docs/a.md": {10, 0}}}); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := testStore.RecordPullRequestShape(w.ctx, pipelineKey, shapeIn); err != nil || !ok {
		t.Fatalf("shape = %v, %v", ok, err)
	}
	if ok, err := testStore.RecordPullRequestFacts(w.ctx, PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 90,
		State: "merged", MergedAt: &w.now, MergedBy: "anna"}); err != nil || !ok {
		t.Fatal(ok, err)
	}
	card := w.card(item, CardOptions{})
	s := card.Plays[0].Shape
	if s == nil || s.Files != 3 || *s.CountedLines != 150 || *s.TestLines != 40 || *s.TestRatio != 0.364 || s.DocsTouched != 1 ||
		s.Complexity == nil || s.Complexity.Added != 3 {
		t.Fatalf("shape = %+v", s)
	}
	if card.Shape == nil || card.Shape.Plays != 1 || !card.Shape.Complete || *card.Shape.CountedLines != 150 {
		t.Errorf("card shape = %+v", card.Shape)
	}
	var stored string
	if err := testStore.pool.QueryRow(w.ctx, `SELECT shape::text FROM pull_requests WHERE number = 90`).Scan(&stored); err != nil ||
		len(stored) == 0 || containsAny(stored, "y()", "if x") {
		t.Errorf("stored shape = %s, %v; the diff's code is never stored", stored, err)
	}
	custom, err := playkpi.Rules{TestPaths: []string{}, DocPaths: []string{"pkg/**"}}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	sizeRules, err := rarity.Rules{SizeExclude: []string{}}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.RecordPullRequestShape(w.ctx, pipelineKey, ShapeInput{Size: sizeRules, Paths: custom, At: w.now}); err != nil {
		t.Fatal(err)
	}
	s = w.card(item, CardOptions{}).Plays[0].Shape
	if s.Files != 4 || *s.TestLines != 0 || s.DocsTouched != 2 || s.Complexity != nil {
		t.Errorf("shape with the Work Target's rules = %+v", s)
	}
}

func q64(n int64) *int64 { return &n }

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
	}
	return false
}
