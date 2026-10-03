package playkpi

import (
	"math"
	"sort"
	"time"
)

// Job is one stored attempt of a CI job or check.
type Job struct {
	Name          string     `json:"name"`
	Status        string     `json:"status"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	CompletedAt   *time.Time `json:"completedAt,omitempty"`
	QueuedSeconds *int64     `json:"queuedSeconds,omitempty"`
	Attempt       int        `json:"attempt"`
}

// Run is one stored CI run on one head commit. Status is success, failure,
// error, cancelled, skipped, pending or running.
type Run struct {
	ID          string
	SHA         string
	Workflow    string
	Status      string
	CreatedAt   *time.Time
	StartedAt   *time.Time
	CompletedAt *time.Time
	Jobs        []Job
}

// SlowJob is one of a play's slowest CI jobs: its name and its longest
// attempt in seconds.
type SlowJob struct {
	Name    string `json:"name"`
	Seconds int64  `json:"seconds"`
}

// SlowestJobs is how many jobs CI.Slowest lists.
const SlowestJobs = 3

// CI is how a play's CI went (ADR-0058). Runs counts its CI runs across
// every head commit, FailedRuns those that ended in failure or error, and
// Reruns the repeat runs on an unchanged commit: a second run of the same
// workflow on the same commit, or a job's second attempt. LastGreenSeconds
// is the wall time, queue excluded, of the latest successful run on the
// play's head. QueueSeconds adds the time each job attempt waited for a
// runner. TimeToGreenSeconds runs from readiness to the first time every
// workflow of one head commit was green. Minutes adds the duration of every
// job attempt. FirstPassGreen is whether the first run on the head that was
// ready for review succeeded without a rerun, nil while it runs or when it
// was cancelled or skipped.
type CI struct {
	Runs               int       `json:"runs"`
	FailedRuns         int       `json:"failedRuns"`
	Reruns             int       `json:"reruns"`
	LastGreenSeconds   *int64    `json:"lastGreenSeconds"`
	QueueSeconds       *int64    `json:"queueSeconds"`
	TimeToGreenSeconds *int64    `json:"timeToGreenSeconds"`
	Minutes            *float64  `json:"minutes"`
	Slowest            []SlowJob `json:"slowest"`
	FirstPassGreen     *bool     `json:"firstPassGreen"`
	Source             string    `json:"source"`
	Truncated          bool      `json:"truncated"`
	CapturedAt         time.Time `json:"capturedAt"`
}

func deriveCI(p Play, readyAt *time.Time) *CI {
	if p.CICapturedAt == nil {
		return nil
	}
	ci := &CI{Runs: len(p.Runs), Slowest: []SlowJob{}, Source: p.CISource, Truncated: p.CITruncated, CapturedAt: p.CICapturedAt.UTC()}
	runs := append([]Run(nil), p.Runs...)
	sort.SliceStable(runs, func(i, j int) bool { return before(runs[i], runs[j]) })

	groups := map[[2]string]int{}
	var queue, busy int64
	queued, timed := false, false
	slowest := map[string]int64{}
	for _, r := range runs {
		if r.Status == "failure" || r.Status == "error" {
			ci.FailedRuns++
		}
		groups[[2]string{r.SHA, r.Workflow}]++
		attempts := 1
		for _, j := range r.Jobs {
			attempts = max(attempts, j.Attempt)
			if j.QueuedSeconds != nil {
				queue += *j.QueuedSeconds
				queued = true
			}
			if d := seconds(j.StartedAt, j.CompletedAt); d != nil {
				busy += *d
				timed = true
				if longest, ok := slowest[j.Name]; !ok || *d > longest {
					slowest[j.Name] = *d
				}
			}
		}
		ci.Reruns += attempts - 1
	}
	for _, n := range groups {
		ci.Reruns += n - 1
	}
	if queued {
		ci.QueueSeconds = &queue
	}
	if timed {
		m := math.Round(float64(busy)/60*10) / 10
		ci.Minutes = &m
	}
	for name, s := range slowest {
		ci.Slowest = append(ci.Slowest, SlowJob{Name: name, Seconds: s})
	}
	sort.Slice(ci.Slowest, func(i, j int) bool {
		if ci.Slowest[i].Seconds != ci.Slowest[j].Seconds {
			return ci.Slowest[i].Seconds > ci.Slowest[j].Seconds
		}
		return ci.Slowest[i].Name < ci.Slowest[j].Name
	})
	if len(ci.Slowest) > SlowestJobs {
		ci.Slowest = ci.Slowest[:SlowestJobs]
	}

	for i := len(runs) - 1; i >= 0; i-- {
		if r := runs[i]; r.SHA == p.HeadSHA && p.HeadSHA != "" && r.Status == "success" {
			ci.LastGreenSeconds = seconds(r.StartedAt, r.CompletedAt)
			break
		}
	}
	if green := firstGreen(runs); green != nil && readyAt != nil {
		ci.TimeToGreenSeconds = sinceReady(readyAt, green)
	}
	ci.FirstPassGreen = firstPassGreen(p, runs, readyAt)
	return ci
}

func before(a, b Run) bool {
	ta, tb := timeOr(a.CreatedAt), timeOr(b.CreatedAt)
	if !ta.Equal(tb) {
		return ta.Before(tb)
	}
	return a.ID < b.ID
}

func timeOr(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func seconds(from, to *time.Time) *int64 {
	return between(from, to)
}

func firstGreen(runs []Run) *time.Time {
	latest := map[string]map[string]Run{}
	var order []string
	for _, r := range runs {
		if latest[r.SHA] == nil {
			latest[r.SHA] = map[string]Run{}
			order = append(order, r.SHA)
		}
		latest[r.SHA][r.Workflow] = r
	}
	var first *time.Time
	for _, sha := range order {
		var at time.Time
		green := true
		for _, r := range latest[sha] {
			if r.Status != "success" || r.CompletedAt == nil {
				green = false
				break
			}
			if r.CompletedAt.After(at) {
				at = *r.CompletedAt
			}
		}
		if green && (first == nil || at.Before(*first)) {
			first = utc(&at)
		}
	}
	return first
}

func firstPassGreen(p Play, runs []Run, readyAt *time.Time) *bool {
	head := readyHead(p, readyAt)
	if head == "" && len(runs) > 0 {
		head = runs[0].SHA
	}
	for _, r := range runs {
		if r.SHA != head {
			continue
		}
		var green bool
		switch r.Status {
		case "success":
			green = true
			for _, j := range r.Jobs {
				if j.Attempt > 1 {
					green = false
				}
			}
		case "failure", "error":
			green = false
		default:
			return nil
		}
		return &green
	}
	return nil
}

func readyHead(p Play, readyAt *time.Time) string {
	head, first := "", ""
	for _, e := range p.Events {
		if (e.Kind != KindPush && e.Kind != KindForcePush) || e.HeadSHA == "" {
			continue
		}
		if first == "" {
			first = e.HeadSHA
		}
		if readyAt != nil && !e.At.After(*readyAt) {
			head = e.HeadSHA
		}
	}
	if head == "" {
		head = first
	}
	return head
}
