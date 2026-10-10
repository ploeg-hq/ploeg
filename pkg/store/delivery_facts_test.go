package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/gate"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestMigration0024AddsDiffAndCIColumns(t *testing.T) {
	ctx := context.Background()
	var applied bool
	if err := testStore.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = '0024_pull_request_diff_and_ci.sql')`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("migration 0024 was not applied")
	}
	for _, column := range []string{"additions", "deletions", "changed_files", "ci_state", "ci_checks", "ci_head_sha", "ci_captured_at"} {
		var nullable string
		if err := testStore.pool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns
			WHERE table_name = 'pull_requests' AND column_name = $1`, column).Scan(&nullable); err != nil {
			t.Errorf("pull_requests.%s missing: %v", column, err)
			continue
		}
		if nullable != "YES" {
			t.Errorf("pull_requests.%s must be nullable: an unreported fact is NULL, never a default", column)
		}
	}
}

type storedDiffCI struct {
	additions, deletions, changedFiles *int
	ciState, ciHead                    *string
	ciChecks                           []byte
	ciAt                               *time.Time
}

func readDiffCI(t *testing.T, number int) storedDiffCI {
	t.Helper()
	var s storedDiffCI
	if err := testStore.pool.QueryRow(context.Background(), `SELECT additions, deletions, changed_files, ci_state, ci_head_sha,
		ci_checks, ci_captured_at FROM pull_requests WHERE number = $1`, number).
		Scan(&s.additions, &s.deletions, &s.changedFiles, &s.ciState, &s.ciHead, &s.ciChecks, &s.ciAt); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRecordPullRequestFacts_DiffAndCI(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	item, _ := pullRequestItem(t, "agent/vik-1900")
	record := func(f PullRequestFacts) {
		t.Helper()
		f.Forge, f.Repo, f.Number, f.WorkItemID = "forgejo", "webgrip/ploeg", 30, item
		if ok, err := testStore.RecordPullRequestFacts(ctx, f); err != nil || !ok {
			t.Fatalf("recorded = %v, err = %v", ok, err)
		}
	}

	record(PullRequestFacts{State: "open", HeadSHA: "h1"})
	if s := readDiffCI(t, 30); s.additions != nil || s.changedFiles != nil || s.ciState != nil || s.ciChecks != nil {
		t.Fatalf("unreported diff and CI stored as %+v; want NULL", s)
	}

	at := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	record(PullRequestFacts{Additions: intp(214), Deletions: intp(0), ChangedFiles: intp(6),
		CI: &PullRequestCI{State: "failure", HeadSHA: "h1", CapturedAt: at, Checks: []PullRequestCheck{
			{Context: "verify", State: "failure"}, {Context: "odd", State: "warning"}}}})
	s := readDiffCI(t, 30)
	if s.additions == nil || *s.additions != 214 || s.deletions == nil || *s.deletions != 0 || *s.changedFiles != 6 {
		t.Errorf("diff = %v %v %v; want 214, a reported 0, 6", s.additions, s.deletions, s.changedFiles)
	}
	if s.ciState == nil || *s.ciState != "failure" || *s.ciHead != "h1" || !s.ciAt.Equal(at) ||
		string(s.ciChecks) != `[{"state": "failure", "context": "verify"}]` {
		t.Errorf("ci = %v %v %s %v; the warning check must be left out", s.ciState, s.ciHead, s.ciChecks, s.ciAt)
	}

	record(PullRequestFacts{HeadSHA: "h2", Additions: intp(-1), CI: &PullRequestCI{State: "neutral", HeadSHA: "h2"}})
	s = readDiffCI(t, 30)
	if *s.additions != 214 || *s.ciState != "failure" || *s.ciHead != "h1" {
		t.Errorf("a negative count or an unknown CI state erased a known fact: %+v", s)
	}

	record(PullRequestFacts{Additions: intp(220), CI: &PullRequestCI{State: "success", HeadSHA: "h2"}})
	s = readDiffCI(t, 30)
	if *s.additions != 220 || *s.deletions != 0 || *s.ciState != "success" || *s.ciHead != "h2" ||
		string(s.ciChecks) != `[]` || s.ciAt == nil {
		t.Errorf("a newer reading must replace the diff figure it reports and the whole CI: %+v (%s)", s, s.ciChecks)
	}
}

func TestMigration0032AddsStatusTransitionsAndTrackerFacts(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct{ table, column string }{
		{"status_transitions", "gate"}, {"work_items", "tracker_created_at"}, {"work_items", "estimate_seconds"},
	} {
		var nullable string
		if err := testStore.pool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns WHERE table_name = $1 AND column_name = $2`,
			c.table, c.column).Scan(&nullable); err != nil {
			t.Fatalf("%s.%s missing: %v", c.table, c.column, err)
		}
		if nullable != "YES" {
			t.Errorf("%s.%s must be nullable: an unreported fact is NULL", c.table, c.column)
		}
	}
}

type statusRow struct {
	status, gate string
	observed     bool
	at           time.Time
}

func statusRows(t *testing.T, item int64) []statusRow {
	t.Helper()
	rows, err := testStore.pool.Query(context.Background(), `SELECT status, COALESCE(gate, '-'), observed, at FROM status_transitions
		WHERE work_item_id = $1 ORDER BY id`, item)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []statusRow
	for rows.Next() {
		var r statusRow
		if err := rows.Scan(&r.status, &r.gate, &r.observed, &r.at); err != nil {
			t.Fatal(err)
		}
		r.at = r.at.UTC()
		out = append(out, r)
	}
	return out
}

func TestRecordStatusMove_KeepsEveryChangeAndSaysWhoseClockTimedIt(t *testing.T) {
	w := newWorld(t)
	item := w.item("1800", "silver")
	ctx := w.ctx
	status := func(status string, g gate.Gate, at time.Time) bool {
		t.Helper()
		recorded, err := testStore.RecordStatusMove(ctx, StatusMove{Provider: "vikunja", ExternalID: "1800", Status: status, Gate: g, At: at})
		if err != nil {
			t.Fatal(err)
		}
		return recorded
	}
	base := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	if !status("Backlog", "", base) || status(" backlog ", "", base.Add(time.Minute)) {
		t.Fatal("a repeated status, compared without case or space, must be recorded once")
	}
	if !status("Doing", gate.Development, base.Add(time.Hour)) || !status("Refinement", "", time.Time{}) {
		t.Fatal("moves not recorded")
	}
	before := time.Now()
	if !status("Blocked", gate.Development, base) || !status("In test", gate.Test, time.Now().Add(time.Hour)) {
		t.Fatal("moves with an untrusted time not recorded")
	}
	rows := statusRows(t, item)
	if len(rows) != 5 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0] != (statusRow{"Backlog", "-", false, base}) || rows[1] != (statusRow{"Doing", "development", false, base.Add(time.Hour)}) {
		t.Errorf("tracker-timed rows = %+v", rows[:2])
	}
	for _, r := range rows[2:] {
		if !r.observed || r.at.Before(before.Add(-time.Minute)) || r.at.After(time.Now().Add(time.Second)) {
			t.Errorf("%+v: a missing, earlier or future tracker time is replaced by the receive time and marked observed", r)
		}
	}
	if _, err := testStore.RecordStatusMove(ctx, StatusMove{Provider: "vikunja", ExternalID: "nope", Status: "Doing"}); !errors.Is(err, ErrWorkItemNotFound) {
		t.Errorf("unknown ticket: %v", err)
	}
	for _, bad := range []StatusMove{{Status: "  "}, {Status: "Doing", Gate: "production"}} {
		bad.Provider, bad.ExternalID = "vikunja", "1800"
		if _, err := testStore.RecordStatusMove(ctx, bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func TestTrackerFactsAreKeptAtIngestAndOnABoardRead(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	created := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	estimate := int64(7200)
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "clickup", ExternalID: "t1", Team: "silver",
		TrackerCreatedAt: created, EstimateSeconds: &estimate})
	if err != nil {
		t.Fatal(err)
	}
	read := func() (*time.Time, *int64) {
		var at *time.Time
		var est *int64
		if err := testStore.pool.QueryRow(ctx, `SELECT tracker_created_at, estimate_seconds FROM work_items WHERE id = $1`, id).Scan(&at, &est); err != nil {
			t.Fatal(err)
		}
		return at, est
	}
	if at, est := read(); at == nil || !at.Equal(created) || est == nil || *est != 7200 {
		t.Fatalf("ingest kept %v, %v", at, est)
	}
	if _, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "clickup", ExternalID: "t1", Team: "silver"}); err != nil {
		t.Fatal(err)
	}
	if at, est := read(); at == nil || est == nil {
		t.Fatalf("a re-ingest without the facts erased them: %v, %v", at, est)
	}
	if err := testStore.RecordTrackerFacts(ctx, TrackerFacts{Provider: "clickup", ExternalID: "t1", Estimates: false, EstimateSeconds: ptrTo(int64(1))}); err != nil {
		t.Fatal(err)
	}
	if _, est := read(); *est != 7200 {
		t.Fatalf("a tracker that keeps no estimate changed it to %d", *est)
	}
	if err := testStore.RecordTrackerFacts(ctx, TrackerFacts{Provider: "clickup", ExternalID: "t1", Estimates: true}); err != nil {
		t.Fatal(err)
	}
	if at, est := read(); at == nil || est != nil {
		t.Fatalf("a removed estimate must read null and the creation time stay: %v, %v", at, est)
	}
	if err := testStore.RecordTrackerFacts(ctx, TrackerFacts{Provider: "clickup", ExternalID: "nope", Estimates: true}); !errors.Is(err, ErrWorkItemNotFound) {
		t.Fatalf("unknown ticket: %v", err)
	}
}

func ptrTo[T any](v T) *T { return &v }

func TestMigration0031AddsLinesPerFile(t *testing.T) {
	ctx := context.Background()
	for _, column := range []string{"additions", "deletions"} {
		var nullable string
		if err := testStore.pool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns
			WHERE table_name = 'pull_request_files' AND column_name = $1`, column).Scan(&nullable); err != nil || nullable != "YES" {
			t.Errorf("pull_request_files.%s = %q, %v; an uncounted file is NULL, never zero", column, nullable, err)
		}
	}
}

func TestRecordPullRequestChange_KeepsLinesPerFile(t *testing.T) {
	w := newWorld(t)
	item := w.item("lines", "silver")
	w.merged(item, 40, 1, "stewart")
	if ok, err := testStore.RecordPullRequestChange(w.ctx, PullRequestChange{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 40,
		Files: []string{"a.go", "b.go"}, Lines: map[string]FileLines{"a.go": {20, 4}}}); err != nil || !ok {
		t.Fatal(ok, err)
	}
	rows, err := testStore.pool.Query(w.ctx, `SELECT f.path, f.additions, f.deletions FROM pull_request_files f
		JOIN pull_requests p ON p.id = f.pull_request_id WHERE p.number = 40 ORDER BY f.path`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var path string
		var a, d *int
		if err := rows.Scan(&path, &a, &d); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s:%v:%v", path, a != nil, d != nil))
		if path == "a.go" && (*a != 20 || *d != 4) {
			t.Errorf("a.go lines = %d/%d", *a, *d)
		}
	}
	if strings.Join(got, ",") != "a.go:true:true,b.go:false:false" {
		t.Errorf("files = %v; a file the forge did not count keeps NULL lines", got)
	}
}

func TestRecordRevertMarksTheRevertedPlaysOnce(t *testing.T) {
	w := newWorld(t)
	feature := w.item("feature", "silver")
	w.merged(feature, 10, 20, "stewart")
	other := w.item("other", "silver")
	w.merged(other, 12, 20, "stewart")

	marked, err := testStore.RecordRevert(w.ctx, Revert{Forge: "forgejo", Repo: "WebGrip/Ploeg", Number: 99, Numbers: []int{10, 99}})
	if err != nil || !reflect.DeepEqual(marked, []int64{feature}) {
		t.Fatalf("marked = %v, %v; want the Work Item of #10", marked, err)
	}
	again, err := testStore.RecordRevert(w.ctx, Revert{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 99, Numbers: []int{10}})
	if err != nil || len(again) != 0 {
		t.Fatalf("a repeated revert marked %v, %v", again, err)
	}
	bySHA, err := testStore.RecordRevert(w.ctx, Revert{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 100,
		SHAs: []string{strings.Repeat("12", 6)}})
	if err != nil || !reflect.DeepEqual(bySHA, []int64{other}) {
		t.Fatalf("revert by commit marked %v, %v", bySHA, err)
	}
	elsewhere, err := testStore.RecordRevert(w.ctx, Revert{Forge: "forgejo", Repo: "webgrip/site", Number: 5, Numbers: []int{10}})
	if err != nil || len(elsewhere) != 0 {
		t.Fatalf("a revert in another repository marked %v, %v", elsewhere, err)
	}

	if got := auditActions(t, feature); !reflect.DeepEqual(got, []string{"pull_request.reverted"}) {
		t.Fatalf("audit = %v", got)
	}
	var reverts int
	if err := testStore.pool.QueryRow(w.ctx, `SELECT count(*) FROM pull_request_reverts`).Scan(&reverts); err != nil || reverts != 2 {
		t.Fatalf("reverts stored = %d, %v", reverts, err)
	}
}

func TestRecordPullRequestChangeKeepsABoundedFileList(t *testing.T) {
	w := newWorld(t)
	item := w.item("feature", "silver")
	w.merged(item, 10, 1, "stewart")
	files := make([]string, 0, MaxStoredFiles+1)
	for i := 0; i <= MaxStoredFiles; i++ {
		files = append(files, fmt.Sprintf("f%03d.go", i))
	}
	ok, err := testStore.RecordPullRequestChange(w.ctx, PullRequestChange{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 10,
		Files: append(files, "f000.go", ""), Labels: []string{"hotfix"}})
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	var stored int
	var truncated bool
	var labels []string
	if err := testStore.pool.QueryRow(w.ctx, `SELECT (SELECT count(*) FROM pull_request_files f WHERE f.pull_request_id = p.id),
		p.files_truncated, p.labels FROM pull_requests p WHERE p.number = 10`).Scan(&stored, &truncated, &labels); err != nil {
		t.Fatal(err)
	}
	if stored != MaxStoredFiles || !truncated || !reflect.DeepEqual(labels, []string{"hotfix"}) {
		t.Fatalf("stored %d files, truncated %v, labels %v", stored, truncated, labels)
	}
	ok, err = testStore.RecordPullRequestChange(w.ctx, PullRequestChange{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 77, Files: []string{"a"}})
	if err != nil || ok {
		t.Fatalf("a pull request Ploeg never recorded stored files: %v %v", ok, err)
	}
	if has, err := testStore.HasMergedPlays(w.ctx, "forgejo", "WebGrip/ploeg"); err != nil || !has {
		t.Fatalf("HasMergedPlays = %v, %v", has, err)
	}
	if has, _ := testStore.HasMergedPlays(w.ctx, "forgejo", "webgrip/site"); has {
		t.Fatal("a repository without plays has merged plays")
	}
}

func auditActions(t *testing.T, item int64) []string {
	t.Helper()
	rows, err := testStore.pool.Query(context.Background(), `SELECT action FROM audit_log WHERE work_item_id = $1 AND action LIKE 'pull_request.%' ORDER BY id`, item)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func (w *world) merged(item int64, number int, daysAgo int, by string, files ...string) {
	w.t.Helper()
	at := w.now.Add(-time.Duration(daysAgo) * 24 * time.Hour)
	if ok, err := testStore.RecordPullRequestFacts(w.ctx, PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg", Number: number,
		WorkItemID: item, State: "merged", MergedAt: &at, MergedBy: by, HeadSHA: fmt.Sprintf("%040d", number),
		MergeCommitSHA: strings.Repeat(fmt.Sprintf("%02d", number%100), 20)}); err != nil || !ok {
		w.t.Fatalf("record play %d: %v %v", number, ok, err)
	}
	if len(files) > 0 {
		if ok, err := testStore.RecordPullRequestChange(w.ctx, PullRequestChange{Forge: "forgejo", Repo: "webgrip/ploeg", Number: number,
			Files: files}); err != nil || !ok {
			w.t.Fatalf("record files of %d: %v %v", number, ok, err)
		}
	}
}
