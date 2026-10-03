package playkpi

import (
	"reflect"
	"testing"
)

func i64(n int64) *int64 { return &n }

func f64(n float64) *float64 { return &n }

func TestSummarize_PipelineTakesFirstAndLastPlaysAndAddsTheRest(t *testing.T) {
	no, yes := false, true
	plays := []PlayFigures{
		{State: "closed",
			Timeline: &Timeline{ToFirstFeedback: i64(600), OpenToMerge: nil, ReviewRounds: 2, Comments: intp(3), ResponseSeconds: i64(100)},
			CI:       &CI{Runs: 3, FailedRuns: 2, Reruns: 1, Minutes: f64(12.5), QueueSeconds: i64(60), FirstPassGreen: &no}},
		{State: "merged", MergedAt: atp(500),
			Timeline: &Timeline{ToFirstFeedback: i64(1200), OpenToMerge: i64(7200), ReviewRounds: 1, Comments: nil, ResponseSeconds: i64(300)},
			CI:       &CI{Runs: 1, LastGreenSeconds: i64(240), Minutes: f64(4.2), FirstPassGreen: &yes}},
		{State: "open"},
	}
	p, _ := Summarize(plays)
	if p == nil || p.Plays != 2 || *p.ToFirstFeedback != 600 || *p.OpenToMerge != 7200 || *p.ReviewRounds != 3 || *p.Comments != 3 {
		t.Fatalf("pipeline = %+v", p)
	}
	if p.FirstPassGreen == nil || *p.FirstPassGreen {
		t.Errorf("firstPassGreen = %v; the first play's first run failed", p.FirstPassGreen)
	}
	if p.CI == nil || p.CI.Runs != 4 || p.CI.FailedRuns != 2 || p.CI.Reruns != 1 || *p.CI.Minutes != 16.7 || *p.CI.QueueSeconds != 60 {
		t.Errorf("ci = %+v", p.CI)
	}
	if *p.Median.ToFirstFeedback != 900 || *p.Median.OpenToMerge != 7200 || *p.Median.ResponseSeconds != 200 || *p.Median.LastGreenSeconds != 240 {
		t.Errorf("median = %+v", p.Median)
	}
	if p, s := Summarize([]PlayFigures{{State: "open"}}); p != nil || s != nil {
		t.Errorf("a play without figures sums to nothing: %+v %+v", p, s)
	}
}

func TestSummarize_CardShapeAddsMergedPlays(t *testing.T) {
	plays := []PlayFigures{
		{State: "merged", Shape: &Shape{Files: 3, CountedLines: i64(100), TestLines: i64(20), DocsTouched: 1,
			Languages:  []Language{{"Go", 80}, {"Markdown", 20}},
			Complexity: &Complexity{Added: 10, Removed: 4, MaxDepth: 3, Hotspots: []Hotspot{{"a.go", 6}, {"b.go", 4}}}}},
		{State: "closed", Shape: &Shape{Files: 99}},
		{State: "merged", Shape: &Shape{Files: 2, CountedLines: i64(50), TestLines: i64(30), Truncated: true,
			Languages:  []Language{{"Go", 40}, {"YAML", 10}, {"Shell", 5}, {"SQL", 1}},
			Complexity: &Complexity{Added: 5, Removed: 9, MaxDepth: 5, Hotspots: []Hotspot{{"b.go", 5}, {"c.go", 1}}}}},
	}
	_, s := Summarize(plays)
	if s == nil || s.Plays != 2 || !s.Complete || s.Files != 5 || *s.CountedLines != 150 || *s.TestLines != 50 || *s.TestRatio != 0.5 ||
		s.DocsTouched != 1 || !s.Truncated {
		t.Fatalf("shape = %+v", s)
	}
	c := s.Complexity
	if c.Added != 15 || c.Removed != 13 || c.Net != 2 || c.MaxDepth != 5 || c.Method != ComplexityMethod ||
		!reflect.DeepEqual(c.Hotspots, []Hotspot{{"b.go", 9}, {"a.go", 6}, {"c.go", 1}}) {
		t.Errorf("complexity = %+v", c)
	}
	if !reflect.DeepEqual(s.Languages, []Language{{"Go", 120}, {"Markdown", 20}, {"YAML", 10}}) {
		t.Errorf("languages = %+v", s.Languages)
	}
	partial := append(plays, PlayFigures{State: "merged"})
	if _, s := Summarize(partial); s.Complete || s.Plays != 2 {
		t.Errorf("a merged play without a shape makes the sum incomplete: %+v", s)
	}
	unknown := []PlayFigures{{State: "merged", Shape: &Shape{Files: 1}}, plays[0]}
	if _, s := Summarize(unknown); s.CountedLines != nil || s.TestLines != nil || s.Complexity != nil {
		t.Errorf("a play without lines or a diff leaves the sums unknown: %+v", s)
	}
}
