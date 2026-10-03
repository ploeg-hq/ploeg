package rarity

import (
	"math"
	"testing"
	"time"
)

func ip(n int) *int     { return &n }
func lp(n int64) *int64 { return &n }

func TestScoreFormula2026_1(t *testing.T) {
	for _, tc := range []struct {
		name      string
		facts     Facts
		predicted bool
		want      float64
	}{
		{"nothing known scores 0", Facts{}, false, 0},
		{"one module, half novel, 50 lines", Facts{Modules: ip(1), Repos: ip(1), SensitiveFiles: ip(0), Files: ip(10), NovelFiles: ip(5), CountedLines: lp(50)}, false, 22.9},
		{"three modules, a migration, mostly novel, 400 lines", Facts{Modules: ip(3), Repos: ip(1), SensitiveFiles: ip(1), Files: ip(10), NovelFiles: ip(8), CountedLines: lp(400)}, false, 56.9},
		{"two repositories add three to reach", Facts{Modules: ip(6), Repos: ip(2), SensitiveFiles: ip(4), Files: ip(10), NovelFiles: ip(9), CountedLines: lp(1500)}, false, 86.9},
		{"every component saturates at 100", Facts{Modules: ip(40), Repos: ip(1), SensitiveFiles: ip(30), Files: ip(3), NovelFiles: ip(3), CountedLines: lp(90000)}, false, 100},
		{"repositories alone give reach", Facts{Repos: ip(3)}, false, 23.5},
		{"zero counted lines and no novel file add nothing", Facts{Modules: ip(2), Repos: ip(1), SensitiveFiles: ip(0), Files: ip(4), NovelFiles: ip(0), CountedLines: lp(0)}, false, 8.4},
		{"a set adds 10 to a predicted score", Facts{Repos: ip(1), Set: true}, true, 10},
		{"a set never moves a revealed score", Facts{Repos: ip(1), Set: true}, false, 0},
		{"a predicted score is capped at 100", Facts{Modules: ip(40), Repos: ip(1), SensitiveFiles: ip(30), Files: ip(3), NovelFiles: ip(3), CountedLines: lp(90000), Set: true}, true, 100},
		{"no files means no novelty", Facts{Files: ip(0), NovelFiles: ip(0)}, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, c := Score(tc.facts, tc.predicted)
			if math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("score = %v (%+v); want %v", got, c, tc.want)
			}
			for _, part := range []float64{c.Reach, c.Sensitive, c.Novelty, c.Size} {
				if part < 0 || part > 1 {
					t.Fatalf("component %v outside 0..1: %+v", part, c)
				}
			}
		})
	}
}

func TestScoreWeightsSumToOne(t *testing.T) {
	if sum := WeightReach + WeightSensitive + WeightNovelty + WeightSize; math.Abs(sum-1) > 1e-9 {
		t.Fatalf("weights sum to %v", sum)
	}
}

func TestTierUsesFixedThresholdsBelowMinCohort(t *testing.T) {
	cohort := make([]float64, MinCohort-2)
	for _, tc := range []struct {
		score float64
		want  string
	}{
		{100, Legendary}, {85, Legendary}, {84.9, Epic}, {70, Epic}, {69.9, Rare}, {55, Rare}, {54.9, Uncommon}, {35, Uncommon}, {34.9, Common}, {0, Common},
	} {
		tier, percentile, size := Tier(tc.score, cohort)
		if tier != tc.want || percentile != nil || size != MinCohort-1 {
			t.Errorf("Tier(%v) in a cohort of %d = %s %v size %d; want %s, no percentile", tc.score, size, tier, percentile, size, tc.want)
		}
	}
}

func TestTierUsesPercentilesFromMinCohort(t *testing.T) {
	cohort := make([]float64, 99)
	for i := range cohort {
		cohort[i] = float64(i)
	}
	for _, tc := range []struct {
		score      float64
		tier       string
		percentile float64
	}{
		{1000, Legendary, 100},
		{98, Epic, 99},
		{94.5, Epic, 96},
		{94, Rare, 95},
		{84.5, Rare, 86},
		{84, Uncommon, 85},
		{59.5, Uncommon, 61},
		{59, Common, 60},
		{-1, Common, 1},
	} {
		tier, percentile, size := Tier(tc.score, cohort)
		if tier != tc.tier || percentile == nil || *percentile != tc.percentile || size != 100 {
			t.Errorf("Tier(%v) = %s %v size %d; want %s at %v of 100", tc.score, tier, percentile, size, tc.tier, tc.percentile)
		}
	}
	tier, percentile, size := Tier(0, make([]float64, MinCohort-1))
	if size != MinCohort || percentile == nil || *percentile != round1(100.0/MinCohort) || tier != Common {
		t.Errorf("a card tied with its whole cohort of %d = %s %v; a tie never lifts a card", size, tier, percentile)
	}
	tier, _, _ = Tier(50, append(make([]float64, MinCohort-2), 10))
	if tier != Legendary {
		t.Errorf("the top card of the smallest percentile cohort = %s; want legendary", tier)
	}
}

func TestQuarter(t *testing.T) {
	for at, want := range map[time.Time]string{
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC):                           "2026Q1",
		time.Date(2026, 3, 31, 23, 59, 0, 0, time.UTC):                        "2026Q1",
		time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC):                           "2026Q2",
		time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC):                          "2026Q4",
		time.Date(2027, 1, 1, 0, 30, 0, 0, time.FixedZone("CET", 3600)):       "2026Q4",
		time.Date(2026, 12, 31, 23, 30, 0, 0, time.FixedZone("UTC-2", -7200)): "2027Q1",
	} {
		if got := Quarter(at); got != want {
			t.Errorf("Quarter(%v) = %s; want %s", at, got, want)
		}
	}
}
