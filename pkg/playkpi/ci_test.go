package playkpi

import (
	"reflect"
	"testing"
)

func q(n int64) *int64 { return &n }

func valueOrNilF(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func job(name, status string, start, end int, attempt int, queued *int64) Job {
	return Job{Name: name, Status: status, StartedAt: atp(start), CompletedAt: atp(end), Attempt: attempt, QueuedSeconds: queued}
}

func TestCI_CountsRunsFailuresRerunsQueueAndSlowest(t *testing.T) {
	p := Play{OpenedAt: atp(0), HeadSHA: "b", CICapturedAt: atp(200), CISource: "actions", ActivityCapturedAt: atp(200),
		Events: []Event{{Kind: KindPush, At: at(0), HeadSHA: "a"}, {Kind: KindPush, At: at(30), HeadSHA: "b"}},
		Runs: []Run{
			{ID: "1", SHA: "a", Workflow: "ci.yml", Status: "failure", CreatedAt: atp(0), StartedAt: atp(2), CompletedAt: atp(10),
				Jobs: []Job{job("test", "failure", 2, 10, 1, q(120)), job("lint", "success", 2, 4, 1, q(60))}},
			{ID: "2", SHA: "b", Workflow: "ci.yml", Status: "failure", CreatedAt: atp(30), StartedAt: atp(31), CompletedAt: atp(40),
				Jobs: []Job{job("test", "failure", 31, 40, 1, q(60))}},
			{ID: "3", SHA: "b", Workflow: "ci.yml", Status: "success", CreatedAt: atp(50), StartedAt: atp(51), CompletedAt: atp(63),
				Jobs: []Job{job("test", "failure", 51, 55, 1, nil), job("test", "success", 56, 63, 2, q(30)), job("build", "success", 51, 61, 1, nil)}},
		}}
	_, ci := Derive(p, notBot)
	if ci == nil {
		t.Fatal("no CI")
	}
	if ci.Runs != 3 || ci.FailedRuns != 2 || ci.Reruns != 2 {
		t.Errorf("runs = %d, failed = %d, reruns = %d; a second run and a second attempt on b are reruns", ci.Runs, ci.FailedRuns, ci.Reruns)
	}
	seconds64(t, "queueSeconds", ci.QueueSeconds, 270)
	seconds64(t, "lastGreenSeconds", ci.LastGreenSeconds, 12*60)
	seconds64(t, "timeToGreenSeconds", ci.TimeToGreenSeconds, 63*60)
	if ci.Minutes == nil || *ci.Minutes != 40 {
		t.Errorf("minutes = %v; want 8+2+9+4+7+10", valueOrNilF(ci.Minutes))
	}
	want := []SlowJob{{"build", 600}, {"test", 540}, {"lint", 120}}
	if !reflect.DeepEqual(ci.Slowest, want) {
		t.Errorf("slowest = %+v; want %+v", ci.Slowest, want)
	}
	if ci.FirstPassGreen == nil || *ci.FirstPassGreen {
		t.Errorf("firstPassGreen = %v; the first run on the ready head failed", ci.FirstPassGreen)
	}
}

func TestCI_FirstPassGreenNeedsNoRerun(t *testing.T) {
	base := Play{OpenedAt: atp(0), HeadSHA: "a", CICapturedAt: atp(100), ActivityCapturedAt: atp(100),
		Events: []Event{{Kind: KindPush, At: at(0), HeadSHA: "a"}}}
	green := base
	green.Runs = []Run{{ID: "1", SHA: "a", Status: "success", CreatedAt: atp(1), StartedAt: atp(1), CompletedAt: atp(5),
		Jobs: []Job{job("test", "success", 1, 5, 1, nil)}}}
	if _, ci := Derive(green, notBot); ci.FirstPassGreen == nil || !*ci.FirstPassGreen || *ci.TimeToGreenSeconds != 300 {
		t.Errorf("ci = %+v; a clean first run is a first-pass green", ci)
	}
	rerun := base
	rerun.Runs = []Run{{ID: "1", SHA: "a", Status: "success", CreatedAt: atp(1), StartedAt: atp(3), CompletedAt: atp(6),
		Jobs: []Job{job("test", "failure", 1, 2, 1, nil), job("test", "success", 3, 6, 2, nil)}}}
	if _, ci := Derive(rerun, notBot); ci.FirstPassGreen == nil || *ci.FirstPassGreen || ci.Reruns != 1 {
		t.Errorf("ci = %+v; a run that needed a rerun is not first-pass green", ci)
	}
	running := base
	running.Runs = []Run{{ID: "1", SHA: "a", Status: "running", CreatedAt: atp(1)}}
	if _, ci := Derive(running, notBot); ci.FirstPassGreen != nil || ci.TimeToGreenSeconds != nil || ci.LastGreenSeconds != nil {
		t.Errorf("ci = %+v; a running first run says nothing yet", ci)
	}
}

func TestCI_GreenNeedsEveryWorkflowOfAHead(t *testing.T) {
	p := Play{OpenedAt: atp(0), HeadSHA: "b", CICapturedAt: atp(100), ActivityCapturedAt: atp(100),
		Runs: []Run{
			{ID: "1", SHA: "a", Workflow: "ci.yml", Status: "success", CreatedAt: atp(1), StartedAt: atp(1), CompletedAt: atp(5)},
			{ID: "2", SHA: "a", Workflow: "lint.yml", Status: "failure", CreatedAt: atp(1), StartedAt: atp(1), CompletedAt: atp(3)},
			{ID: "3", SHA: "b", Workflow: "ci.yml", Status: "success", CreatedAt: atp(10), StartedAt: atp(10), CompletedAt: atp(14)},
			{ID: "4", SHA: "b", Workflow: "lint.yml", Status: "success", CreatedAt: atp(10), StartedAt: atp(10), CompletedAt: atp(20)},
		}}
	_, ci := Derive(p, notBot)
	seconds64(t, "timeToGreenSeconds", ci.TimeToGreenSeconds, 20*60)
	if ci.Reruns != 0 || ci.QueueSeconds != nil || ci.Minutes != nil || len(ci.Slowest) != 0 {
		t.Errorf("ci = %+v; two workflows on one commit are not reruns, and runs without jobs time nothing", ci)
	}
}

func TestCI_NoRunsIsAKnownZero(t *testing.T) {
	_, ci := Derive(Play{OpenedAt: atp(0), CICapturedAt: atp(1), CISource: "statuses"}, notBot)
	if ci == nil || ci.Runs != 0 || ci.FailedRuns != 0 || ci.FirstPassGreen != nil || ci.Slowest == nil || ci.Source != "statuses" {
		t.Errorf("ci = %+v", ci)
	}
}
