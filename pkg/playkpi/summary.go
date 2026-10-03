package playkpi

import (
	"math"
	"time"
)

// PlayFigures are one play's derived figures, as a card summarizes them.
// Plays are passed oldest number first.
type PlayFigures struct {
	State    string
	MergedAt *time.Time
	Timeline *Timeline
	CI       *CI
	Shape    *Shape
}

// Pipeline sums up review and CI across a card's plays (ADR-0058).
// ToFirstFeedback is the first play's, and OpenToMerge the latest merged
// play's. ReviewRounds and Comments add up the plays that know them, and
// FirstPassGreen is the first play's. Median holds the median of each
// figure over the plays that know it. Waiting for a review measures the
// team's response, not the author's work.
type Pipeline struct {
	Plays           int            `json:"plays"`
	ToFirstFeedback *int64         `json:"toFirstFeedbackSeconds"`
	OpenToMerge     *int64         `json:"openToMergeSeconds"`
	ReviewRounds    *int           `json:"reviewRounds"`
	Comments        *int           `json:"comments"`
	FirstPassGreen  *bool          `json:"firstPassGreen"`
	Median          PipelineMedian `json:"median"`
	CI              *PipelineCI    `json:"ci"`
}

// PipelineMedian holds medians over a card's plays, in seconds.
type PipelineMedian struct {
	ToFirstFeedback  *int64 `json:"toFirstFeedbackSeconds"`
	OpenToMerge      *int64 `json:"openToMergeSeconds"`
	ResponseSeconds  *int64 `json:"responseSeconds"`
	LastGreenSeconds *int64 `json:"lastGreenSeconds"`
}

// PipelineCI adds up the CI of every play whose CI was read.
type PipelineCI struct {
	Runs         int      `json:"runs"`
	FailedRuns   int      `json:"failedRuns"`
	Reruns       int      `json:"reruns"`
	Minutes      *float64 `json:"minutes"`
	QueueSeconds *int64   `json:"queueSeconds"`
}

// Summarize computes a card's Pipeline and Shape from its plays. Each is
// nil when no play carries the figures it sums.
func Summarize(plays []PlayFigures) (*Pipeline, *CardShape) {
	return pipeline(plays), cardShape(plays)
}

func pipeline(plays []PlayFigures) *Pipeline {
	var p Pipeline
	var feedback, merge, response, green []int64
	var latest *PlayFigures
	var rounds, comments int
	knownRounds, knownComments := false, false
	first := true
	for i := range plays {
		pf := &plays[i]
		if pf.Timeline == nil && pf.CI == nil {
			continue
		}
		p.Plays++
		if t := pf.Timeline; t != nil {
			if first {
				p.ToFirstFeedback = t.ToFirstFeedback
			}
			rounds += t.ReviewRounds
			knownRounds = true
			if t.Comments != nil {
				comments += *t.Comments
				knownComments = true
			}
			feedback = appendKnown(feedback, t.ToFirstFeedback)
			merge = appendKnown(merge, t.OpenToMerge)
			response = appendKnown(response, t.ResponseSeconds)
		}
		if first && pf.CI != nil {
			p.FirstPassGreen = pf.CI.FirstPassGreen
		}
		first = false
		if c := pf.CI; c != nil {
			if p.CI == nil {
				p.CI = &PipelineCI{}
			}
			p.CI.Runs += c.Runs
			p.CI.FailedRuns += c.FailedRuns
			p.CI.Reruns += c.Reruns
			if c.Minutes != nil {
				m := math.Round((valueOr(p.CI.Minutes)+*c.Minutes)*10) / 10
				p.CI.Minutes = &m
			}
			if c.QueueSeconds != nil {
				q := valueOrInt(p.CI.QueueSeconds) + *c.QueueSeconds
				p.CI.QueueSeconds = &q
			}
			green = appendKnown(green, c.LastGreenSeconds)
		}
		if pf.State == "merged" && (latest == nil || timeOr(pf.MergedAt).Compare(timeOr(latest.MergedAt)) >= 0) {
			latest = pf
		}
	}
	if p.Plays == 0 {
		return nil
	}
	if latest != nil && latest.Timeline != nil {
		p.OpenToMerge = latest.Timeline.OpenToMerge
	}
	if knownRounds {
		p.ReviewRounds = &rounds
	}
	if knownComments {
		p.Comments = &comments
	}
	p.Median = PipelineMedian{ToFirstFeedback: median(feedback), OpenToMerge: median(merge), ResponseSeconds: median(response),
		LastGreenSeconds: median(green)}
	return &p
}

func appendKnown(values []int64, v *int64) []int64 {
	if v == nil {
		return values
	}
	return append(values, *v)
}

func valueOr(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func valueOrInt(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

// CardShape adds up the Shape of a card's merged plays (ADR-0058). Plays
// counts the merged plays that were measured and Complete says every merged
// play was. Counts and lines add up; a count any measured play does not know
// is nil. Files adds each play's files, so a file two plays touched counts
// twice. MaxDepth is the deepest play's, Hotspots merges the plays' files
// and Languages their lines.
type CardShape struct {
	Plays        int         `json:"plays"`
	Complete     bool        `json:"complete"`
	Complexity   *Complexity `json:"complexity"`
	Files        int         `json:"files"`
	CountedLines *int64      `json:"countedLines"`
	TestLines    *int64      `json:"testLines"`
	TestRatio    *float64    `json:"testRatio"`
	DocsTouched  int         `json:"docsTouched"`
	Languages    []Language  `json:"languages"`
	Truncated    bool        `json:"truncated"`
}

func cardShape(plays []PlayFigures) *CardShape {
	s := CardShape{Complete: true, Languages: []Language{}}
	var lines, tests int64
	linesKnown, testsKnown, complexityKnown := true, true, true
	var complexity Complexity
	hotspots := map[string]int{}
	languages := map[string]int64{}
	merged := 0
	for _, pf := range plays {
		if pf.State != "merged" {
			continue
		}
		merged++
		if pf.Shape == nil {
			s.Complete = false
			continue
		}
		sh := pf.Shape
		s.Plays++
		s.Files += sh.Files
		s.DocsTouched += sh.DocsTouched
		s.Truncated = s.Truncated || sh.Truncated
		if sh.CountedLines == nil {
			linesKnown = false
		} else {
			lines += *sh.CountedLines
		}
		if sh.TestLines == nil {
			testsKnown = false
		} else {
			tests += *sh.TestLines
		}
		for _, l := range sh.Languages {
			languages[l.Name] += l.Lines
		}
		if sh.Complexity == nil {
			complexityKnown = false
			continue
		}
		complexity.Added += sh.Complexity.Added
		complexity.Removed += sh.Complexity.Removed
		complexity.MaxDepth = max(complexity.MaxDepth, sh.Complexity.MaxDepth)
		for _, h := range sh.Complexity.Hotspots {
			hotspots[h.Path] += h.Added
		}
	}
	if merged == 0 || s.Plays == 0 {
		return nil
	}
	if linesKnown {
		s.CountedLines = &lines
	}
	if testsKnown && linesKnown {
		s.TestLines = &tests
		if rest := lines - tests; rest > 0 {
			ratio := math.Round(float64(tests)/float64(rest)*1000) / 1000
			s.TestRatio = &ratio
		}
	}
	s.Languages = topLanguages(languages, ShownLanguages)
	if complexityKnown {
		complexity.Method = ComplexityMethod
		complexity.Net = complexity.Added - complexity.Removed
		complexity.Hotspots = []Hotspot{}
		for p, added := range hotspots {
			complexity.Hotspots = append(complexity.Hotspots, Hotspot{Path: p, Added: added})
		}
		sortHotspots(complexity.Hotspots)
		if len(complexity.Hotspots) > ShownHotspots {
			complexity.Hotspots = complexity.Hotspots[:ShownHotspots]
		}
		s.Complexity = &complexity
	}
	return &s
}
