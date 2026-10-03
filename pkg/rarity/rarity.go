// Package rarity computes how exceptional a Run card's change was: a
// challenge score from facts the steward does not control after the fact,
// and the tier that score earns in its cohort (ADR-0056).
package rarity

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Formula is the version of the score, the tier cut-offs and the fixed
// thresholds. Any change to an input, weight, saturation point, cut-off,
// threshold or default path list changes it.
const Formula = "2026.1"

// Tiers, from most to least common.
const (
	Common    = "common"
	Uncommon  = "uncommon"
	Rare      = "rare"
	Epic      = "epic"
	Legendary = "legendary"
)

// Weights and saturation points of formula 2026.1. Each component runs from
// 0 to 1; the score is 100 times their weighted sum, plus SetBonus on a
// predicted score of a card in an epic's set, capped at 100.
const (
	WeightReach     = 0.30
	WeightSensitive = 0.25
	WeightNovelty   = 0.20
	WeightSize      = 0.25

	// ReachSaturation is the reach (modules, plus RepoReach for every
	// repository beyond the first) at which the reach component reaches 1.
	ReachSaturation = 12
	// RepoReach is what each repository beyond the first adds to reach.
	RepoReach = 3
	// SensitiveSaturation is the number of sensitive files at which the
	// sensitive component reaches 1.
	SensitiveSaturation = 8
	// SizeSaturation is the number of counted lines at which the size
	// component reaches 1.
	SizeSaturation = 2000
	// SetBonus is added to a predicted score when the card is in an epic's
	// set.
	SetBonus = 10.0
)

// NoveltyWindow is how far back an earlier merged play must be to make a
// file it touched no longer novel.
const NoveltyWindow = 180 * 24 * time.Hour

// MinCohort is the number of revealed cards, the card included, a cohort
// needs before tiers come from percentiles instead of fixed thresholds.
const MinCohort = 30

// Fixed thresholds of formula 2026.1: the least score of each tier while
// the cohort is smaller than MinCohort.
const (
	ThresholdLegendary = 85.0
	ThresholdEpic      = 70.0
	ThresholdRare      = 55.0
	ThresholdUncommon  = 35.0
)

// Percentile cut-offs of formula 2026.1: a card's percentile must be above
// the cut-off for the tier. They make the top 1 % legendary, the next 4 %
// epic, the next 10 % rare and the next 25 % uncommon.
const (
	CutLegendary = 99.0
	CutEpic      = 95.0
	CutRare      = 85.0
	CutUncommon  = 60.0
)

// NotCollected names the challenge inputs the proposal lists that have no
// source yet; they never move a score.
var NotCollected = []string{"complexity", "estimate"}

// Facts are what a score is computed from. A nil fact is unknown and adds
// nothing.
type Facts struct {
	Modules        *int
	Repos          *int
	SensitiveFiles *int
	Files          *int
	NovelFiles     *int
	CountedLines   *int64
	// Set is whether the card is in an epic's set; it counts on a
	// predicted score only.
	Set bool
}

// Components are the four parts of a score, each from 0 to 1.
type Components struct {
	Reach, Sensitive, Novelty, Size float64
}

// Score computes the score of f, from 0 to 100 rounded to one decimal.
// predicted adds SetBonus when f.Set.
func Score(f Facts, predicted bool) (float64, Components) {
	var c Components
	if f.Modules != nil || f.Repos != nil {
		reach := 1
		if f.Modules != nil && *f.Modules > 1 {
			reach = *f.Modules
		}
		if f.Repos != nil && *f.Repos > 1 {
			reach += RepoReach * (*f.Repos - 1)
		}
		c.Reach = saturate(math.Log(float64(reach)) / math.Log(ReachSaturation))
	}
	if f.SensitiveFiles != nil {
		c.Sensitive = saturate(math.Log1p(float64(*f.SensitiveFiles)) / math.Log1p(SensitiveSaturation))
	}
	if f.Files != nil && f.NovelFiles != nil && *f.Files > 0 {
		c.Novelty = saturate(float64(*f.NovelFiles) / float64(*f.Files))
	}
	if f.CountedLines != nil {
		c.Size = saturate(math.Log1p(float64(*f.CountedLines)) / math.Log1p(SizeSaturation))
	}
	score := 100 * (WeightReach*c.Reach + WeightSensitive*c.Sensitive + WeightNovelty*c.Novelty + WeightSize*c.Size)
	if predicted && f.Set {
		score += SetBonus
	}
	return round1(math.Min(100, score)), c
}

func saturate(x float64) float64 {
	if math.IsNaN(x) || x < 0 {
		return 0
	}
	return math.Min(1, x)
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }

// Tier places score in a cohort of revealed scores that does not include
// the card itself. While the cohort with the card is smaller than
// MinCohort, the fixed thresholds decide and the percentile is nil.
// Otherwise the percentile is 100 times one plus the number of cohort
// scores strictly below score, over the cohort size with the card; a tie
// never lifts a card.
func Tier(score float64, cohort []float64) (string, *float64, int) {
	size := len(cohort) + 1
	if size < MinCohort {
		return thresholdTier(score), nil, size
	}
	below := 0
	for _, s := range cohort {
		if s < score {
			below++
		}
	}
	p := round1(100 * float64(below+1) / float64(size))
	return percentileTier(100 * float64(below+1) / float64(size)), &p, size
}

func thresholdTier(score float64) string {
	switch {
	case score >= ThresholdLegendary:
		return Legendary
	case score >= ThresholdEpic:
		return Epic
	case score >= ThresholdRare:
		return Rare
	case score >= ThresholdUncommon:
		return Uncommon
	default:
		return Common
	}
}

func percentileTier(p float64) string {
	switch {
	case p > CutLegendary:
		return Legendary
	case p > CutEpic:
		return Epic
	case p > CutRare:
		return Rare
	case p > CutUncommon:
		return Uncommon
	default:
		return Common
	}
}

// Quarter is the calendar quarter of t in UTC, as "2026Q4".
func Quarter(t time.Time) string {
	t = t.UTC()
	return fmt.Sprintf("%dQ%d", t.Year(), (int(t.Month())-1)/3+1)
}

// Valid reports whether tier is one of the five tiers.
func Valid(tier string) bool {
	switch tier {
	case Common, Uncommon, Rare, Epic, Legendary:
		return true
	}
	return false
}

// SortedUnique returns the distinct values of in, sorted.
func SortedUnique(in []string) []string {
	set := map[string]bool{}
	for _, v := range in {
		set[v] = true
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
