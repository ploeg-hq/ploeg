package flow

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/gate"
)

func utc(day, hour, minute int) time.Time {
	return time.Date(2026, 10, day, hour, minute, 0, 0, time.UTC)
}

func ptr[T any](v T) *T { return &v }

func utcCalendar(t *testing.T) Calendar {
	t.Helper()
	c, err := NewCalendar(Hours{Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func releasedFacts(t *testing.T) Facts {
	return Facts{
		Now:            utc(7, 9, 0),
		TrackerCreated: ptr(utc(2, 9, 0)),
		FirstSeen:      utc(5, 8, 0),
		Admitted:       ptr(utc(5, 8, 0)),
		Statuses: []Entry{
			{Status: "Backlog", At: utc(5, 8, 0), Observed: true},
			{Status: "Doing", Gate: gate.Development, At: utc(5, 9, 0)},
			{Status: "Blocked", Gate: gate.Development, At: utc(5, 11, 0)},
			{Status: "Doing", Gate: gate.Development, At: utc(5, 12, 0)},
			{Status: "Ready for test", Gate: gate.Test, At: utc(5, 13, 0)},
			{Status: "In test", Gate: gate.Test, At: utc(5, 14, 0)},
			{Status: "Done", Gate: gate.Done, At: utc(5, 15, 0)},
			{Status: "Doing", Gate: gate.Development, At: utc(6, 9, 0)},
			{Status: "Done", Gate: gate.Done, At: utc(6, 10, 0), Observed: true},
		},
		Runs:               []Run{{Started: utc(5, 9, 30), Finished: ptr(utc(5, 10, 30))}, {Started: utc(6, 9, 10), Finished: ptr(utc(6, 9, 40))}},
		FirstPlay:          ptr(utc(5, 10, 30)),
		Merge:              ptr(utc(6, 9, 50)),
		Deploys:            []Deploy{{Environment: "test", At: utc(6, 10, 0)}, {Environment: "production", At: utc(6, 12, 0)}},
		Release:            ptr(utc(6, 12, 0)),
		ReleaseEnvironment: "production",
		Open:               true,
		Restores: []Restore{{CrackID: "7", Confirmed: utc(6, 13, 0), Mended: utc(6, 16, 0)},
			{CrackID: "8", Confirmed: utc(6, 14, 0), Mended: utc(6, 11, 0)}},
		Calendar: utcCalendar(t),
	}
}

func TestComputeAReleasedCard(t *testing.T) {
	f := Compute(releasedFacts(t))
	type row struct {
		status, gate, kind string
		visits             int
		seconds, working   int64
		observed, current  bool
	}
	var got []row
	for _, s := range f.Statuses {
		g := "-"
		if s.Gate != nil {
			g = *s.Gate
		}
		got = append(got, row{s.Status, g, string(s.Kind), s.Visits, s.Seconds, s.WorkingSeconds, s.Observed, s.Current})
	}
	want := []row{
		{"Backlog", "-", "waiting", 1, 3600, 0, true, false},
		{"Doing", "development", "active", 3, 14400, 14400, false, false},
		{"Blocked", "development", "blocked", 1, 3600, 3600, false, false},
		{"Ready for test", "test", "waiting", 1, 3600, 3600, false, false},
		{"In test", "test", "active", 1, 3600, 3600, false, false},
		{"Done", "done", "done", 2, 147600, 32400, true, true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("statuses =\n%v\nwant\n%v", got, want)
	}
	if f.StatusesSince == nil || !f.StatusesSince.Equal(utc(5, 8, 0)) || f.Truncated {
		t.Fatalf("since %v, truncated %v", f.StatusesSince, f.Truncated)
	}
	wantGates := map[string]Duration{"development": {18000, 18000}, "test": {7200, 7200}, "done": {147600, 32400}}
	if !reflect.DeepEqual(f.Gates, wantGates) {
		t.Fatalf("gates = %v", f.Gates)
	}
	wantKinds := map[string]Duration{"active": {18000, 18000}, "waiting": {7200, 3600}, "blocked": {3600, 3600}, "done": {147600, 32400}}
	if !reflect.DeepEqual(f.Kinds, wantKinds) {
		t.Fatalf("kinds = %v", f.Kinds)
	}
	if *f.BlockedSeconds != 3600 || *f.BlockedWorkingSeconds != 3600 || *f.Reopens != 1 {
		t.Fatalf("blocked %d/%d, reopens %d", *f.BlockedSeconds, *f.BlockedWorkingSeconds, *f.Reopens)
	}
	checkSpan(t, "leadTime", f.LeadTime, Span{From: utc(2, 9, 0), To: utc(6, 12, 0), Seconds: 99 * 3600, WorkingSeconds: 19 * 3600,
		Start: "tracker_created", End: "release"})
	checkSpan(t, "cycleTime", f.CycleTime, Span{From: utc(5, 9, 0), To: utc(6, 12, 0), Seconds: 27 * 3600, WorkingSeconds: 11 * 3600,
		Start: "first_active", End: "release"})
	checkSpan(t, "timeToStart", f.TimeToStart, Span{From: utc(2, 9, 0), To: utc(5, 9, 0), Seconds: 72 * 3600, WorkingSeconds: 8 * 3600,
		Start: "tracker_created", End: "first_active"})
	if f.Efficiency == nil || *f.Efficiency != 0.714 {
		t.Fatalf("efficiency = %v; want 18000 active of 25200 active, waiting and blocked seconds in the cycle", f.Efficiency)
	}
	if *f.QueueSeconds != 5400 || *f.QueueWorkingSeconds != 1800 || *f.AgentSeconds != 5400 || f.Runs != 2 ||
		*f.FirstRunToFirstPlaySeconds != 3600 || *f.FirstRunToFirstPlayWorkingSeconds != 3600 {
		t.Fatalf("queue %d/%d, agent %d, runs %d, first play %d/%d", *f.QueueSeconds, *f.QueueWorkingSeconds, *f.AgentSeconds, f.Runs,
			*f.FirstRunToFirstPlaySeconds, *f.FirstRunToFirstPlayWorkingSeconds)
	}
	if !reflect.DeepEqual(f.MergeTo, map[string]int64{"test": 600, "production": 7800}) ||
		!reflect.DeepEqual(f.MergeToWorking, map[string]int64{"test": 600, "production": 7800}) ||
		fmt.Sprint(f.EnvironmentsReached) != "[test production]" ||
		f.TimeToProduction == nil || *f.TimeToProduction != (EnvironmentTime{Environment: "production", Seconds: 7800, WorkingSeconds: 7800}) {
		t.Fatalf("mergeTo %v / %v, reached %v, time to production %+v", f.MergeTo, f.MergeToWorking, f.EnvironmentsReached, f.TimeToProduction)
	}
	if len(f.Restores) != 2 || f.Restores[0].Seconds != 10800 || f.Restores[0].WorkingSeconds != 10800 || f.Restores[1].Seconds != 0 ||
		*f.MeanRestoreSeconds != 5400 || *f.MeanRestoreWorkingSeconds != 5400 {
		t.Fatalf("restores %+v, mean %v", f.Restores, *f.MeanRestoreSeconds)
	}
	if fmt.Sprint(f.NotCollected) != "[estimate holidays]" || f.EstimateSeconds != nil {
		t.Fatalf("notCollected = %v", f.NotCollected)
	}
}

func checkSpan(t *testing.T, name string, got *Span, want Span) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s is null, want %+v", name, want)
	}
	if !got.From.Equal(want.From) || !got.To.Equal(want.To) || got.Seconds != want.Seconds || got.WorkingSeconds != want.WorkingSeconds ||
		got.Running != want.Running || got.Start != want.Start || got.End != want.End {
		t.Fatalf("%s = %+v, want %+v", name, *got, want)
	}
}

func TestComputeAnOpenCardRunsToNow(t *testing.T) {
	f := Compute(Facts{
		Now:       utc(6, 12, 0),
		FirstSeen: utc(5, 8, 0),
		Admitted:  ptr(utc(5, 8, 0)),
		Statuses:  []Entry{{Status: "To do", At: utc(5, 8, 0)}, {Status: "In progress", At: utc(5, 10, 0)}},
		Runs:      []Run{{Started: utc(6, 11, 0)}},
		Open:      true,
		Calendar:  utcCalendar(t),
	})
	checkSpan(t, "leadTime", f.LeadTime, Span{From: utc(5, 8, 0), To: utc(6, 12, 0), Seconds: 28 * 3600, WorkingSeconds: 11 * 3600,
		Running: true, Start: "first_seen", End: "now"})
	checkSpan(t, "cycleTime", f.CycleTime, Span{From: utc(5, 10, 0), To: utc(6, 12, 0), Seconds: 26 * 3600, WorkingSeconds: 10 * 3600,
		Running: true, Start: "first_active", End: "now"})
	if *f.AgentSeconds != 3600 || *f.Efficiency != 1 || f.MergeTo != nil || f.TimeToProduction != nil || f.FirstRunToFirstPlaySeconds != nil {
		t.Fatalf("agent %d, efficiency %v, mergeTo %v, production %v, first play %v", *f.AgentSeconds, *f.Efficiency, f.MergeTo,
			f.TimeToProduction, f.FirstRunToFirstPlaySeconds)
	}
	if !f.Statuses[1].Current || f.Statuses[1].Kind != Active || f.Statuses[0].Kind != Waiting {
		t.Fatalf("statuses = %+v", f.Statuses)
	}
}

func TestComputeAMergedCardWithoutReleaseEndsItsCycleAtTheMerge(t *testing.T) {
	f := Compute(Facts{
		Now: utc(7, 12, 0), FirstSeen: utc(5, 8, 0), TrackerCreated: ptr(utc(5, 7, 0)),
		Runs:  []Run{{Started: utc(5, 9, 0), Finished: ptr(utc(5, 9, 30))}},
		Merge: ptr(utc(5, 15, 0)), Deploys: []Deploy{{Environment: "test", At: utc(5, 16, 0)}}, ReleaseEnvironment: "production",
		Open: true, EstimateSeconds: ptr(int64(7200)), Calendar: utcCalendar(t),
	})
	checkSpan(t, "cycleTime", f.CycleTime, Span{From: utc(5, 9, 0), To: utc(5, 15, 0), Seconds: 6 * 3600, WorkingSeconds: 6 * 3600,
		Start: "first_run", End: "merge"})
	if f.LeadTime == nil || !f.LeadTime.Running || f.LeadTime.End != "now" || f.TimeToProduction != nil ||
		fmt.Sprint(f.EnvironmentsReached) != "[test]" || f.MergeTo["test"] != 3600 {
		t.Fatalf("lead %+v, production %+v, reached %v, mergeTo %v", f.LeadTime, f.TimeToProduction, f.EnvironmentsReached, f.MergeTo)
	}
	if len(f.Statuses) != 0 || f.StatusesSince != nil || f.BlockedSeconds != nil || f.Reopens != nil || f.Efficiency != nil {
		t.Fatalf("without recorded statuses nothing status-based is known: %+v", f)
	}
	if fmt.Sprint(f.NotCollected) != "[statuses holidays]" || *f.EstimateSeconds != 7200 {
		t.Fatalf("notCollected = %v, estimate %v", f.NotCollected, f.EstimateSeconds)
	}
}

func TestComputeAClosedCardHasNoLeadOrCycleTime(t *testing.T) {
	f := Compute(Facts{
		Now: utc(7, 12, 0), FirstSeen: utc(5, 8, 0),
		Statuses: []Entry{{Status: "Doing", Gate: gate.Development, At: utc(5, 9, 0)}},
		Runs:     []Run{{Started: utc(5, 10, 0), Finished: ptr(utc(5, 11, 0))}},
		Calendar: utcCalendar(t),
	})
	if f.LeadTime != nil || f.CycleTime != nil || f.Efficiency != nil || f.QueueSeconds != nil {
		t.Fatalf("a card that will deliver nothing has lead %+v, cycle %+v, efficiency %v, queue %v", f.LeadTime, f.CycleTime, f.Efficiency, f.QueueSeconds)
	}
	checkSpan(t, "timeToStart", f.TimeToStart, Span{From: utc(5, 8, 0), To: utc(5, 9, 0), Seconds: 3600, WorkingSeconds: 0,
		Start: "first_seen", End: "first_active"})
}

func TestComputeAnUnstartedCardWaitsToStart(t *testing.T) {
	f := Compute(Facts{Now: utc(5, 12, 0), FirstSeen: utc(5, 8, 0), Open: true, Calendar: utcCalendar(t),
		Statuses: []Entry{{Status: "Backlog", At: utc(5, 8, 0)}}})
	checkSpan(t, "timeToStart", f.TimeToStart, Span{From: utc(5, 8, 0), To: utc(5, 12, 0), Seconds: 4 * 3600, WorkingSeconds: 3 * 3600,
		Running: true, Start: "first_seen", End: "now"})
	if f.CycleTime != nil || f.AgentSeconds != nil || f.Runs != 0 {
		t.Fatalf("cycle %+v, agent %v", f.CycleTime, f.AgentSeconds)
	}
}

func TestComputeCapsTheStatusTable(t *testing.T) {
	var entries []Entry
	for i := 0; i < MaxStatuses+10; i++ {
		entries = append(entries, Entry{Status: fmt.Sprintf("Column %d", i), At: utc(5, 0, i)})
	}
	f := Compute(Facts{Now: utc(5, 2, 0), FirstSeen: utc(5, 0, 0), Statuses: entries, Calendar: utcCalendar(t)})
	if len(f.Statuses) != MaxStatuses || !f.Truncated {
		t.Fatalf("%d statuses listed, truncated %v", len(f.Statuses), f.Truncated)
	}
	if got := f.Kinds["waiting"].Seconds; got != 2*3600 {
		t.Fatalf("time beyond the cap must still count towards the kinds: %d", got)
	}
}

func TestComputeMergesRepeatedStatusesAndKeepsTheFirstSpelling(t *testing.T) {
	f := Compute(Facts{Now: utc(5, 12, 0), FirstSeen: utc(5, 8, 0), Calendar: utcCalendar(t), Statuses: []Entry{
		{Status: "Doing", Gate: gate.Development, At: utc(5, 9, 0)},
		{Status: "doing", Gate: gate.Development, At: utc(5, 10, 0), Observed: true},
	}})
	if len(f.Statuses) != 1 || f.Statuses[0].Status != "Doing" || f.Statuses[0].Visits != 1 || f.Statuses[0].Seconds != 3*3600 || !f.Statuses[0].Observed {
		t.Fatalf("statuses = %+v", f.Statuses)
	}
}

func TestFlowEncodesUnknownsAsNull(t *testing.T) {
	raw, err := json.Marshal(Compute(Facts{Now: utc(5, 12, 0), FirstSeen: utc(5, 8, 0), Calendar: DefaultCalendar()}))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"leadTime", "cycleTime", "efficiency", "blockedSeconds", "reopens", "queueSeconds", "agentSeconds",
		"firstRunToFirstPlaySeconds", "mergeTo", "timeToProduction", "meanRestoreSeconds", "estimateSeconds", "statusesSince"} {
		if v, ok := m[key]; !ok || v != nil {
			t.Errorf("%s = %v; an unknown figure is null, never 0", key, v)
		}
	}
}

func BenchmarkComputeALongBusyCard(b *testing.B) {
	var entries []Entry
	start := time.Date(2024, 10, 1, 9, 0, 0, 0, time.UTC)
	names := []string{"Backlog", "Doing", "Blocked", "Ready for test", "In test", "UAT", "Done"}
	for i := 0; i < 500; i++ {
		entries = append(entries, Entry{Status: names[i%len(names)], At: start.Add(time.Duration(i) * 35 * time.Hour)})
	}
	facts := Facts{Now: start.Add(2 * 365 * 24 * time.Hour), FirstSeen: start, TrackerCreated: ptr(start.Add(-24 * time.Hour)),
		Statuses: entries, Runs: []Run{{Started: start.Add(time.Hour), Finished: ptr(start.Add(2 * time.Hour))}}, Open: true,
		Calendar: DefaultCalendar()}
	b.ReportAllocs()
	for b.Loop() {
		Compute(facts)
	}
}
