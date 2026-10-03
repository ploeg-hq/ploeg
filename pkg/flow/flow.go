package flow

import (
	"math"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/gate"
)

// MaxStatuses is the most statuses a card lists. Time in a status beyond
// it still counts towards the gate and kind totals.
const MaxStatuses = 50

// Entry is one recorded move of a Work Item into a tracker status, oldest
// first. Gate is empty when the status maps to no gate. Observed is true
// when At is when Ploeg saw the move rather than when the tracker says it
// happened.
type Entry struct {
	Status   string
	Gate     gate.Gate
	At       time.Time
	Observed bool
}

// Run is one started Run; Finished is nil while it runs.
type Run struct {
	Started  time.Time
	Finished *time.Time
}

// Deploy is the first deploy of one environment that carried a merge.
type Deploy struct {
	Environment string
	At          time.Time
}

// Restore is one confirmed crack that was mended.
type Restore struct {
	CrackID   string
	Confirmed time.Time
	Mended    time.Time
}

// Facts is everything Compute reads. Every time is UTC or carries its own
// zone; a nil pointer is a fact nobody reported.
type Facts struct {
	// Now ends a running span and the current status.
	Now time.Time
	// Statuses are the recorded status moves, oldest first. Truncated is
	// true when older moves were left out.
	Statuses  []Entry
	Truncated bool
	// TrackerCreated is when the tracker says the ticket was created;
	// FirstSeen is when Ploeg first stored the Work Item.
	TrackerCreated *time.Time
	FirstSeen      time.Time
	// Admitted is when the Work Item was first queued for a team.
	Admitted *time.Time
	Runs     []Run
	// FirstPlay is when the card's first pull request was seen.
	FirstPlay *time.Time
	// Merge is when the card's latest merged pull request merged, and
	// Deploys its first deploy per environment, earliest first.
	Merge   *time.Time
	Deploys []Deploy
	// Release is when the change went live, nil until then.
	Release *time.Time
	// ReleaseEnvironment is the environment whose first deploy releases
	// the change.
	ReleaseEnvironment string
	// Open is false when nothing will be delivered any more: the Work Item
	// was withdrawn, or every pull request closed unmerged.
	Open     bool
	Restores []Restore
	// EstimateSeconds is the tracker's time estimate, nil when it has none
	// or keeps none.
	EstimateSeconds *int64
	Kinds           KindMap
	Calendar        Calendar
}

// Status is the time a Work Item spent in one tracker status, in the order
// it first entered it. Gate is nil when the status maps to no gate.
// Observed is true when any entry into the status was timed by Ploeg
// rather than by the tracker. Current marks the status the ticket is in.
type Status struct {
	Status         string  `json:"status"`
	Gate           *string `json:"gate"`
	Kind           Kind    `json:"kind"`
	Visits         int     `json:"visits"`
	Seconds        int64   `json:"seconds"`
	WorkingSeconds int64   `json:"workingSeconds"`
	Observed       bool    `json:"observed"`
	Current        bool    `json:"current"`
}

// Duration is a length of time in elapsed and in working seconds.
type Duration struct {
	Seconds        int64 `json:"seconds"`
	WorkingSeconds int64 `json:"workingSeconds"`
}

// Span is a measured stretch of the ticket's life. Start names what From
// is (tracker_created, first_seen, first_active or first_run) and End what
// To is (first_active, first_run, merge, release or now). Running is true
// when To is now because the span has not ended.
type Span struct {
	From           time.Time `json:"from"`
	To             time.Time `json:"to"`
	Seconds        int64     `json:"seconds"`
	WorkingSeconds int64     `json:"workingSeconds"`
	Running        bool      `json:"running"`
	Start          string    `json:"start"`
	End            string    `json:"end"`
}

// EnvironmentTime is how long a merge took to reach one environment.
type EnvironmentTime struct {
	Environment    string `json:"environment"`
	Seconds        int64  `json:"seconds"`
	WorkingSeconds int64  `json:"workingSeconds"`
}

// RestoreTime is how long one confirmed crack took to mend, from its
// confirmation; 0 when the mend came first.
type RestoreTime struct {
	CrackID        string    `json:"crackId"`
	ConfirmedAt    time.Time `json:"confirmedAt"`
	MendedAt       time.Time `json:"mendedAt"`
	Seconds        int64     `json:"seconds"`
	WorkingSeconds int64     `json:"workingSeconds"`
}

// Flow is the card's flow figures (ADR-0057). A figure nobody can know yet
// is null, never 0. Waiting and blocked time describe the team's process,
// not the steward.
type Flow struct {
	Statuses                          []Status            `json:"statuses"`
	StatusesSince                     *time.Time          `json:"statusesSince"`
	Truncated                         bool                `json:"truncated"`
	Gates                             map[string]Duration `json:"gates"`
	Kinds                             map[string]Duration `json:"kinds"`
	LeadTime                          *Span               `json:"leadTime"`
	CycleTime                         *Span               `json:"cycleTime"`
	TimeToStart                       *Span               `json:"timeToStart"`
	Efficiency                        *float64            `json:"efficiency"`
	BlockedSeconds                    *int64              `json:"blockedSeconds"`
	BlockedWorkingSeconds             *int64              `json:"blockedWorkingSeconds"`
	Reopens                           *int                `json:"reopens"`
	QueueSeconds                      *int64              `json:"queueSeconds"`
	QueueWorkingSeconds               *int64              `json:"queueWorkingSeconds"`
	AgentSeconds                      *int64              `json:"agentSeconds"`
	Runs                              int                 `json:"runs"`
	FirstRunToFirstPlaySeconds        *int64              `json:"firstRunToFirstPlaySeconds"`
	FirstRunToFirstPlayWorkingSeconds *int64              `json:"firstRunToFirstPlayWorkingSeconds"`
	MergeTo                           map[string]int64    `json:"mergeTo"`
	MergeToWorking                    map[string]int64    `json:"mergeToWorking"`
	EnvironmentsReached               []string            `json:"environmentsReached"`
	TimeToProduction                  *EnvironmentTime    `json:"timeToProduction"`
	Restores                          []RestoreTime       `json:"restores"`
	MeanRestoreSeconds                *float64            `json:"meanRestoreSeconds"`
	MeanRestoreWorkingSeconds         *float64            `json:"meanRestoreWorkingSeconds"`
	EstimateSeconds                   *int64              `json:"estimateSeconds"`
	Calendar                          CalendarInfo        `json:"calendar"`
	NotCollected                      []string            `json:"notCollected"`
}

type interval struct {
	key, status string
	kind        Kind
	gate        gate.Gate
	observed    bool
	from, to    time.Time
	length      Duration
}

// Compute derives the card's flow figures from f. The same facts always
// give the same figures.
func Compute(f Facts) Flow {
	out := Flow{Statuses: []Status{}, Gates: map[string]Duration{}, Kinds: map[string]Duration{}, EnvironmentsReached: []string{},
		Restores: []RestoreTime{}, Runs: len(f.Runs), EstimateSeconds: f.EstimateSeconds, Calendar: f.Calendar.Info(),
		Truncated: f.Truncated, NotCollected: []string{}}
	cal := f.Calendar
	intervals := statusIntervals(f)
	for i := range intervals {
		iv := &intervals[i]
		iv.length = Duration{Seconds: seconds(iv.from, iv.to), WorkingSeconds: cal.WorkingSeconds(iv.from, iv.to)}
	}
	out.Statuses, out.Truncated = statusTable(intervals, out.Truncated)
	if len(intervals) > 0 {
		since := intervals[0].from
		out.StatusesSince = &since
		var blocked, blockedWorking int64
		reopens := 0
		for i, iv := range intervals {
			d := iv.length
			if iv.gate.Known() {
				out.Gates[string(iv.gate)] = add(out.Gates[string(iv.gate)], d)
			}
			out.Kinds[string(iv.kind)] = add(out.Kinds[string(iv.kind)], d)
			if iv.kind == Blocked {
				blocked += d.Seconds
				blockedWorking += d.WorkingSeconds
			}
			if i > 0 && intervals[i-1].kind == Done && iv.kind != Done {
				reopens++
			}
		}
		out.BlockedSeconds, out.BlockedWorkingSeconds, out.Reopens = &blocked, &blockedWorking, &reopens
	} else {
		out.NotCollected = append(out.NotCollected, "statuses")
	}

	start, startSource := leadStart(f)
	cycleStart, cycleSource := cycleStart(f, intervals)
	end, endSource, ended := deliveryEnd(f)

	if ended || f.Open {
		out.LeadTime = span(cal, start, end, startSource, endSource, !ended)
	}
	if cycleStart != nil {
		cycleEnd, cycleEndSource, cycleEnded := end, endSource, ended
		if !ended && f.Merge != nil {
			cycleEnd, cycleEndSource, cycleEnded = *f.Merge, "merge", true
		}
		if cycleEnded || f.Open {
			out.CycleTime = span(cal, *cycleStart, cycleEnd, cycleSource, cycleEndSource, !cycleEnded)
			out.Efficiency = efficiency(intervals, out.CycleTime.From, out.CycleTime.To)
		}
		out.TimeToStart = span(cal, start, *cycleStart, startSource, cycleSource, false)
	} else if f.Open {
		out.TimeToStart = span(cal, start, f.Now, startSource, "now", true)
	}

	firstRun, agent := runFacts(f)
	if firstRun != nil {
		out.AgentSeconds = &agent
		if f.Admitted != nil {
			q, qw := seconds(*f.Admitted, *firstRun), cal.WorkingSeconds(*f.Admitted, *firstRun)
			out.QueueSeconds, out.QueueWorkingSeconds = &q, &qw
		}
		if f.FirstPlay != nil {
			p, pw := seconds(*firstRun, *f.FirstPlay), cal.WorkingSeconds(*firstRun, *f.FirstPlay)
			out.FirstRunToFirstPlaySeconds, out.FirstRunToFirstPlayWorkingSeconds = &p, &pw
		}
	}

	if f.Merge != nil {
		out.MergeTo, out.MergeToWorking = map[string]int64{}, map[string]int64{}
		for _, d := range f.Deploys {
			if _, seen := out.MergeTo[d.Environment]; seen {
				continue
			}
			out.MergeTo[d.Environment] = seconds(*f.Merge, d.At)
			out.MergeToWorking[d.Environment] = cal.WorkingSeconds(*f.Merge, d.At)
			out.EnvironmentsReached = append(out.EnvironmentsReached, d.Environment)
			if d.Environment == f.ReleaseEnvironment {
				out.TimeToProduction = &EnvironmentTime{Environment: d.Environment, Seconds: out.MergeTo[d.Environment],
					WorkingSeconds: out.MergeToWorking[d.Environment]}
			}
		}
	}

	var restoreTotal, restoreWorking float64
	for _, r := range f.Restores {
		rt := RestoreTime{CrackID: r.CrackID, ConfirmedAt: r.Confirmed, MendedAt: r.Mended, Seconds: seconds(r.Confirmed, r.Mended),
			WorkingSeconds: cal.WorkingSeconds(r.Confirmed, r.Mended)}
		restoreTotal += float64(rt.Seconds)
		restoreWorking += float64(rt.WorkingSeconds)
		out.Restores = append(out.Restores, rt)
	}
	if n := float64(len(out.Restores)); n > 0 {
		mean, meanWorking := round1(restoreTotal/n), round1(restoreWorking/n)
		out.MeanRestoreSeconds, out.MeanRestoreWorkingSeconds = &mean, &meanWorking
	}

	if f.EstimateSeconds == nil {
		out.NotCollected = append(out.NotCollected, "estimate")
	}
	if out.Calendar.Holidays == 0 {
		out.NotCollected = append(out.NotCollected, "holidays")
	}
	return out
}

func statusIntervals(f Facts) []interval {
	var out []interval
	for i, e := range f.Statuses {
		key := StatusKey(e.Status)
		if key == "" {
			continue
		}
		to := f.Now
		if i+1 < len(f.Statuses) {
			to = f.Statuses[i+1].At
		}
		if to.Before(e.At) {
			to = e.At
		}
		if n := len(out); n > 0 && out[n-1].key == key {
			out[n-1].to = to
			out[n-1].observed = out[n-1].observed || e.Observed
			continue
		}
		out = append(out, interval{key: key, status: e.Status, kind: f.Kinds.Kind(e.Status, e.Gate), gate: e.Gate,
			observed: e.Observed, from: e.At, to: to})
	}
	return out
}

func statusTable(intervals []interval, truncated bool) ([]Status, bool) {
	table := []Status{}
	index := map[string]int{}
	for i, iv := range intervals {
		at, ok := index[iv.key]
		if !ok {
			if len(table) == MaxStatuses {
				truncated = true
				continue
			}
			at = len(table)
			index[iv.key] = at
			table = append(table, Status{Status: iv.status})
		}
		s := &table[at]
		s.Visits++
		s.Kind = iv.kind
		s.Gate = nil
		if iv.gate.Known() {
			g := string(iv.gate)
			s.Gate = &g
		}
		s.Seconds += iv.length.Seconds
		s.WorkingSeconds += iv.length.WorkingSeconds
		s.Observed = s.Observed || iv.observed
		s.Current = i == len(intervals)-1
	}
	return table, truncated
}

func leadStart(f Facts) (time.Time, string) {
	if f.TrackerCreated != nil {
		return *f.TrackerCreated, "tracker_created"
	}
	return f.FirstSeen, "first_seen"
}

func cycleStart(f Facts, intervals []interval) (*time.Time, string) {
	var at *time.Time
	source := ""
	for _, iv := range intervals {
		if iv.kind == Active {
			from := iv.from
			at, source = &from, "first_active"
			break
		}
	}
	for _, r := range f.Runs {
		if at == nil || r.Started.Before(*at) {
			started := r.Started
			at, source = &started, "first_run"
		}
	}
	return at, source
}

func deliveryEnd(f Facts) (time.Time, string, bool) {
	if f.Release != nil {
		return *f.Release, "release", true
	}
	return f.Now, "now", false
}

func runFacts(f Facts) (*time.Time, int64) {
	var first *time.Time
	var total int64
	for _, r := range f.Runs {
		if first == nil || r.Started.Before(*first) {
			started := r.Started
			first = &started
		}
		end := f.Now
		if r.Finished != nil {
			end = *r.Finished
		}
		total += seconds(r.Started, end)
	}
	return first, total
}

func efficiency(intervals []interval, from, to time.Time) *float64 {
	var active, other int64
	for _, iv := range intervals {
		a, b := iv.from, iv.to
		if a.Before(from) {
			a = from
		}
		if b.After(to) {
			b = to
		}
		s := seconds(a, b)
		switch iv.kind {
		case Active:
			active += s
		case Waiting, Blocked:
			other += s
		}
	}
	if active+other == 0 {
		return nil
	}
	e := math.Round(float64(active)/float64(active+other)*1000) / 1000
	return &e
}

func span(cal Calendar, from, to time.Time, start, end string, running bool) *Span {
	if to.Before(from) {
		to = from
	}
	return &Span{From: from.UTC(), To: to.UTC(), Seconds: seconds(from, to), WorkingSeconds: cal.WorkingSeconds(from, to),
		Running: running, Start: start, End: end}
}

func seconds(from, to time.Time) int64 {
	if !to.After(from) {
		return 0
	}
	return int64(to.Sub(from) / time.Second)
}

func add(a, b Duration) Duration {
	return Duration{Seconds: a.Seconds + b.Seconds, WorkingSeconds: a.WorkingSeconds + b.WorkingSeconds}
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
