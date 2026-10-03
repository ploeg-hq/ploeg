package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/flow"
	"github.com/ploeg-hq/ploeg/pkg/gate"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

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

func (f *cardFixture) status(status string, g gate.Gate, at time.Time) bool {
	f.t.Helper()
	recorded, err := testStore.RecordStatusMove(context.Background(), StatusMove{Provider: "vikunja", ExternalID: f.externalID(),
		Status: status, Gate: g, At: at})
	if err != nil {
		f.t.Fatal(err)
	}
	return recorded
}

func TestRecordStatusMove_KeepsEveryChangeAndSaysWhoseClockTimedIt(t *testing.T) {
	f := newCardFixture(t, "1800")
	ctx := context.Background()
	base := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	if !f.status("Backlog", "", base) || f.status(" backlog ", "", base.Add(time.Minute)) {
		t.Fatal("a repeated status, compared without case or space, must be recorded once")
	}
	if !f.status("Doing", gate.Development, base.Add(time.Hour)) || !f.status("Refinement", "", time.Time{}) {
		t.Fatal("moves not recorded")
	}
	before := time.Now()
	if !f.status("Blocked", gate.Development, base) || !f.status("In test", gate.Test, time.Now().Add(time.Hour)) {
		t.Fatal("moves with an untrusted time not recorded")
	}
	rows := statusRows(t, f.item)
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

func utcCalendar(t *testing.T) flow.Calendar {
	t.Helper()
	c, err := flow.NewCalendar(flow.Hours{Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestOperatorCard_FlowFromStoredFacts(t *testing.T) {
	f := newCardFixture(t, "1801")
	ctx := context.Background()
	f.base = time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	day := func(d, h, m int) time.Time { return time.Date(2026, 9, d+16, h, m, 0, 0, time.UTC) }
	if _, err := testStore.pool.Exec(ctx, `UPDATE work_items SET created_at = $2, tracker_created_at = $3, external_scope = '10' WHERE id = $1`,
		f.item, day(5, 8, 0), day(2, 9, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE audit_log SET at = $2 WHERE work_item_id = $1 AND action = 'work_item.queued'`, f.item, day(5, 8, 0)); err != nil {
		t.Fatal(err)
	}
	for _, m := range []struct {
		status string
		gate   gate.Gate
		at     time.Time
	}{
		{"Backlog", "", day(5, 8, 0)}, {"Doing", gate.Development, day(5, 9, 0)}, {"Blocked", gate.Development, day(5, 11, 0)},
		{"Doing", gate.Development, day(5, 12, 0)}, {"UAT", gate.Acceptance, day(5, 13, 0)}, {"Done", gate.Done, day(5, 15, 0)},
		{"Doing", gate.Development, day(6, 9, 0)}, {"Done", gate.Done, day(6, 10, 0)},
	} {
		f.status(m.status, m.gate, m.at)
	}
	f.openShift("agent/vik-1801")
	f.run(fixtureRun{role: "builder", writes: true, startMin: 30, durationMin: 60, outcome: "pr_opened",
		links: []string{"https://forge.example/webgrip/ploeg/pulls/80"}})
	f.run(fixtureRun{role: "reviewer", startMin: 24*60 + 10, durationMin: 30, outcome: "approved"})
	f.pr(PullRequestFacts{Number: 80, State: "merged", MergedAt: ptrTo(day(6, 9, 50)), MergedBy: "ryan", MergeCommitSHA: commit("8")})
	var pr int64
	if err := testStore.pool.QueryRow(ctx, `SELECT id FROM pull_requests WHERE number = 80`).Scan(&pr); err != nil {
		t.Fatal(err)
	}
	for env, sha := range map[string]string{"test": "a", "production": "b"} {
		at := day(6, 10, 0)
		if env == "production" {
			at = day(6, 12, 0)
		}
		d := deploy(t, env, commit(sha), at)
		if _, err := testStore.MarkDeployed(ctx, pr, d.ID); err != nil {
			t.Fatal(err)
		}
	}
	bug, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "1802", Team: "silver"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.pool.Exec(ctx, `INSERT INTO card_cracks (team, card_work_item_id, bug_work_item_id, state, severity, share, discovery,
		proposed_by, proposed_at, confirmed_by, confirmed_at, mend_number, mended_at, mended_by, mend_by_steward)
		VALUES ('silver', $1, $2, 'confirmed', 'S3', 'primary', 'discovered', 'anna', $3, 'bram', $3, 81, $4, 'ryan', true)`,
		f.item, bug, day(6, 13, 0), day(6, 16, 0)); err != nil {
		t.Fatal(err)
	}

	kinds, err := flow.NewKindMap(flow.Kinds{Active: []string{"UAT"}})
	if err != nil {
		t.Fatal(err)
	}
	opts := CardOptions{Now: day(7, 9, 0), Flow: &FlowOptions{Kinds: flow.Boards{"vikunja": {"10": kinds}},
		Calendars: map[string]flow.Calendar{"silver": utcCalendar(t)}}}
	card, err := testStore.OperatorCard(ctx, f.item, []string{"silver"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	fl := card.Flow
	if fl == nil {
		t.Fatal("no flow with FlowOptions")
	}
	var statuses []string
	for _, s := range fl.Statuses {
		statuses = append(statuses, fmt.Sprintf("%s/%s/%d/%d", s.Status, s.Kind, s.Visits, s.Seconds))
	}
	if fmt.Sprint(statuses) != "[Backlog/waiting/1/3600 Doing/active/3/14400 Blocked/blocked/1/3600 UAT/active/1/7200 Done/done/2/147600]" {
		t.Fatalf("statuses = %v (UAT is active by the board's statusKinds)", statuses)
	}
	if fl.LeadTime == nil || fl.LeadTime.Start != "tracker_created" || fl.LeadTime.End != "release" || fl.LeadTime.Seconds != 99*3600 ||
		fl.LeadTime.WorkingSeconds != 19*3600 {
		t.Fatalf("lead time = %+v", fl.LeadTime)
	}
	if fl.CycleTime == nil || fl.CycleTime.Start != "first_active" || !fl.CycleTime.From.Equal(day(5, 9, 0)) || fl.CycleTime.Seconds != 27*3600 {
		t.Fatalf("cycle time = %+v", fl.CycleTime)
	}
	if *fl.Efficiency != 0.857 || *fl.Reopens != 1 || *fl.BlockedSeconds != 3600 {
		t.Fatalf("efficiency %v, reopens %d, blocked %d", *fl.Efficiency, *fl.Reopens, *fl.BlockedSeconds)
	}
	if *fl.QueueSeconds != 5400 || *fl.AgentSeconds != 5400 || fl.Runs != 2 || *fl.FirstRunToFirstPlaySeconds != 3600 {
		t.Fatalf("queue %d, agent %d, runs %d, first play %d", *fl.QueueSeconds, *fl.AgentSeconds, fl.Runs, *fl.FirstRunToFirstPlaySeconds)
	}
	if !reflect.DeepEqual(fl.MergeTo, map[string]int64{"test": 600, "production": 7800}) || fl.TimeToProduction == nil ||
		fl.TimeToProduction.Seconds != 7800 || fmt.Sprint(fl.EnvironmentsReached) != "[test production]" {
		t.Fatalf("mergeTo %v, production %+v, reached %v", fl.MergeTo, fl.TimeToProduction, fl.EnvironmentsReached)
	}
	if len(fl.Restores) != 1 || fl.Restores[0].Seconds != 10800 || *fl.MeanRestoreSeconds != 10800 {
		t.Fatalf("restores %+v", fl.Restores)
	}
	if fl.Calendar.Timezone != "UTC" || fmt.Sprint(fl.NotCollected) != "[estimate holidays]" {
		t.Fatalf("calendar %+v, notCollected %v", fl.Calendar, fl.NotCollected)
	}

	plain, err := testStore.OperatorCard(ctx, f.item, []string{"silver"}, CardOptions{Now: day(7, 9, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if plain.Flow != nil {
		t.Fatal("a card read without FlowOptions carries no flow")
	}
	if !reflect.DeepEqual(plain.Grade, card.Grade) || !reflect.DeepEqual(plain.Rarity, card.Rarity) {
		t.Fatalf("flow figures must never move the grade or the rarity:\n%+v\n%+v", plain.Grade, card.Grade)
	}
}

func TestOperatorCard_FlowReadsTheNewestStatusMovesWhenThereAreTooMany(t *testing.T) {
	f := newCardFixture(t, "1803")
	ctx := context.Background()
	base := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	if _, err := testStore.pool.Exec(ctx, `INSERT INTO status_transitions (work_item_id, status, at, observed)
		SELECT $1, CASE WHEN n % 2 = 0 THEN 'Doing' ELSE 'Waiting' END, $2::timestamptz + n * interval '1 minute', false
		FROM generate_series(0, $3) AS n`, f.item, base, cardTransitionLimit); err != nil {
		t.Fatal(err)
	}
	card, err := testStore.OperatorCard(ctx, f.item, nil, CardOptions{Now: base.Add(24 * time.Hour), Flow: &FlowOptions{}})
	if err != nil {
		t.Fatal(err)
	}
	if !card.Flow.Truncated || card.Flow.StatusesSince == nil || !card.Flow.StatusesSince.Equal(base.Add(time.Minute)) {
		t.Fatalf("truncated %v since %v; the oldest move beyond %d is left out", card.Flow.Truncated, card.Flow.StatusesSince, cardTransitionLimit)
	}
	st := card.Flow.Statuses
	if len(st) != 2 || st[0].Status != "Waiting" || st[0].Current || st[1].Status != "Doing" || !st[1].Current || st[1].Visits != 250 {
		t.Fatalf("statuses = %+v", st)
	}
	if card.Flow.Calendar.Timezone != "Europe/Amsterdam" {
		t.Fatalf("a team without workingHours counts in the default calendar, got %+v", card.Flow.Calendar)
	}
}
