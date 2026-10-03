package store

import (
	"reflect"
	"testing"
	"time"
)

func f64(v float64) *float64 { return &v }

func TestHalfStepRoundsToTheNearestHalfWithTiesUp(t *testing.T) {
	for _, tc := range []struct{ in, want float64 }{
		{8.24, 8}, {8.25, 8.5}, {8.74, 8.5}, {8.75, 9}, {9.0, 9}, {0.2, 1}, {12, 10}, {-3, 1},
		{0.40*10 + 0.25*6 + 0.20*10 + 0.15*10, 9},
		{0.40*10 + 0.25*8.5 + 0.20*7.5 + 0.15*9, 9},
	} {
		if got := halfStep(tc.in); got != tc.want {
			t.Errorf("halfStep(%v) = %v; want %v", tc.in, got, tc.want)
		}
	}
}

func TestComputeGradeFormula(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	ago := func(days int) *time.Time { at := now.Add(-time.Duration(days)*24*time.Hour - time.Hour); return &at }
	one, two := 1, 2
	known := func(f gradeFacts) gradeFacts {
		f.now = now
		if f.costUSD == nil {
			f.costUSD, f.authorizedUSD = f64(1), 2
		}
		if f.defectBounces == nil {
			zero := 0
			f.defectBounces = &zero
		}
		return f
	}
	for _, tc := range []struct {
		name        string
		facts       gradeFacts
		sub         CardSubgrades
		overall     float64
		provisional bool
		label       string
		qualifiers  []string
	}{
		{"not live yet: durability starts at 6", known(gradeFacts{plays: 1, reviewRounds: 1}),
			CardSubgrades{10, 6, 10, 10}, 9, true, "", []string{}},
		{"45 days live: durability 8", known(gradeFacts{liveSince: ago(45), plays: 1, reviewRounds: 1}),
			CardSubgrades{10, 8, 10, 10}, 9, true, "", []string{}},
		{"179 days live is still provisional: capped at 9 and no label", known(gradeFacts{liveSince: ago(179), plays: 1, reviewRounds: 1}),
			CardSubgrades{10, 10, 10, 10}, 9, true, "", []string{}},
		{"180 days live, all tens: black", known(gradeFacts{liveSince: ago(180), plays: 1, reviewRounds: 1}),
			CardSubgrades{10, 10, 10, 10}, 10, false, "black", []string{}},
		{"180 days live, overall 10 without four tens: gold",
			known(gradeFacts{liveSince: ago(180), plays: 1, changeRequests: 1, reviewRounds: 2, reworkRounds: 1}),
			CardSubgrades{10, 10, 10, 9}, 10, false, "gold", []string{}},
		{"over budget by a quarter or less costs 1 and marks OB",
			known(gradeFacts{liveSince: ago(400), plays: 1, reviewRounds: 1, costUSD: f64(2.5), authorizedUSD: 2}),
			CardSubgrades{10, 10, 9, 10}, 10, false, "gold", []string{"OB"}},
		{"over budget by more than a quarter costs 2",
			known(gradeFacts{liveSince: ago(400), plays: 1, reviewRounds: 1, costUSD: f64(2.51), authorizedUSD: 2}),
			CardSubgrades{10, 10, 8, 10}, 9.5, false, "", []string{"OB"}},
		{"within budget costs nothing", known(gradeFacts{plays: 1, reviewRounds: 1, costUSD: f64(2), authorizedUSD: 2}),
			CardSubgrades{10, 6, 10, 10}, 9, true, "", []string{}},
		{"failed runs, extra plays and defect bounces lower delivery; RT",
			known(gradeFacts{liveSince: ago(45), plays: 2, failedRuns: 1, defectBounces: &two, reviewRounds: 1}),
			CardSubgrades{10, 8, 5.5, 10}, 8.5, true, "", []string{"RT"}},
		{"approved and comment-only rounds lower nothing",
			known(gradeFacts{liveSince: ago(400), plays: 1, reviewRounds: 4}),
			CardSubgrades{10, 10, 10, 10}, 10, false, "black", []string{}},
		{"each rework round costs 1, however many reviewers asked in it",
			known(gradeFacts{liveSince: ago(45), plays: 1, changeRequests: 3, reviewRounds: 3, reworkRounds: 2, defectBounces: &one}),
			CardSubgrades{10, 8, 8.5, 8}, 9, true, "", []string{}},
		{"subgrades never fall below 1",
			known(gradeFacts{plays: 12, failedRuns: 9, changeRequests: 15, reviewRounds: 15, reworkRounds: 15}),
			CardSubgrades{10, 6, 1, 1}, 6, true, "", []string{"RT"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := computeGrade(tc.facts)
			if g.Formula != "2026.3" || g.Subgrades != tc.sub || g.Overall != tc.overall || g.Provisional != tc.provisional {
				t.Fatalf("grade = %s %v %+v provisional %v; want %v %+v provisional %v", g.Formula, g.Overall, g.Subgrades, g.Provisional,
					tc.overall, tc.sub, tc.provisional)
			}
			label := ""
			if g.Label != nil {
				label = *g.Label
			}
			if label != tc.label || !reflect.DeepEqual(g.Qualifiers, tc.qualifiers) {
				t.Fatalf("label %q qualifiers %v; want %q %v", label, g.Qualifiers, tc.label, tc.qualifiers)
			}
			if len(g.Inputs.Missing) != 0 {
				t.Fatalf("missing = %v; every input of these cases is known", g.Inputs.Missing)
			}
		})
	}
}

func TestComputeGradeMissingInputsCapTheirSubgradeAndWithholdTheLabel(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	live := now.Add(-400 * 24 * time.Hour)
	zero := 0
	for _, tc := range []struct {
		name     string
		facts    gradeFacts
		delivery float64
		overall  float64
		missing  []string
	}{
		{"every input known: a black label", gradeFacts{costUSD: f64(1), authorizedUSD: 2, defectBounces: &zero},
			10, 10, []string{}},
		{"cost not reported", gradeFacts{authorizedUSD: 2, defectBounces: &zero},
			9, 10, []string{"delivery.budgetShare"}},
		{"nothing authorized", gradeFacts{costUSD: f64(1), defectBounces: &zero},
			9, 10, []string{"delivery.budgetShare"}},
		{"the board maps no gates", gradeFacts{costUSD: f64(1), authorizedUSD: 2},
			9, 10, []string{"delivery.defectBounces"}},
		{"both unknown", gradeFacts{},
			9, 10, []string{"delivery.budgetShare", "delivery.defectBounces"}},
		{"a known penalty below the cap still counts", gradeFacts{costUSD: f64(3), authorizedUSD: 2},
			8, 9.5, []string{"delivery.defectBounces"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.facts
			f.now, f.liveSince, f.plays, f.reviewRounds = now, &live, 1, 1
			g := computeGrade(f)
			if g.Subgrades.Delivery != tc.delivery || g.Overall != tc.overall || !reflect.DeepEqual(g.Inputs.Missing, tc.missing) {
				t.Fatalf("delivery %v overall %v missing %v; want %v %v %v", g.Subgrades.Delivery, g.Overall, g.Inputs.Missing,
					tc.delivery, tc.overall, tc.missing)
			}
			if g.Subgrades.Reliability != 10 || g.Subgrades.Durability != 10 || g.Subgrades.Review != 10 {
				t.Fatalf("subgrades = %+v; a missing delivery input caps only delivery", g.Subgrades)
			}
			if complete := len(tc.missing) == 0; (g.Label != nil) != complete {
				t.Fatalf("label = %v with missing %v; only complete evidence earns a label", g.Label, tc.missing)
			}
		})
	}
}

func TestCardGradeCountsReworkRoundsNotReviews(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	review := func(who, state, head string) CardReview {
		return CardReview{Reviewer: who, State: state, HeadSHA: head, ReceivedAt: now}
	}
	for _, tc := range []struct {
		name                              string
		reviews                           []CardReview
		review                            float64
		changeRequests, rounds, reworkRds int
	}{
		{"one approval", []CardReview{review("anna", "approved", "a")}, 10, 0, 1, 0},
		{"comments then approval over three heads",
			[]CardReview{review("anna", "commented", "a"), review("bert", "commented", "b"), review("anna", "approved", "c")}, 10, 0, 3, 0},
		{"two reviewers approving the same head", []CardReview{review("anna", "approved", "a"), review("bert", "approved", "a")}, 10, 0, 1, 0},
		{"two reviewers asking for changes on one head are one rework round",
			[]CardReview{review("anna", "changes_requested", "a"), review("bert", "changes_requested", "a"), review("anna", "approved", "b")}, 9, 2, 2, 1},
		{"changes asked on two heads are two rework rounds",
			[]CardReview{review("anna", "changes_requested", "a"), review("anna", "changes_requested", "b"), review("anna", "approved", "c")}, 8, 2, 3, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &OperatorCard{Plays: []CardPlay{{Number: 7, State: "open", Reviews: tc.reviews}}}
			g := c.grade(nil, 0, false, now)
			if g == nil {
				t.Fatal("grade = nil; a human verdict grades the card")
			}
			in := g.Inputs.Review
			if g.Subgrades.Review != tc.review || in.ChangeRequests != tc.changeRequests || in.ReviewRounds != tc.rounds || in.ReworkRounds != tc.reworkRds {
				t.Fatalf("review %v inputs %+v; want %v changeRequests %d rounds %d rework %d", g.Subgrades.Review, in,
					tc.review, tc.changeRequests, tc.rounds, tc.reworkRds)
			}
		})
	}
}

func TestComputeGradeInputsKeepUnknownsUnknown(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	g := computeGrade(gradeFacts{now: now, plays: 1, authorizedUSD: 2, reviewRounds: 1})
	in := g.Inputs
	if in.Delivery.BudgetShare != nil || in.Delivery.DefectBounces != nil || in.Durability.LiveSince != nil || in.Durability.DaysLive != 0 {
		t.Fatalf("inputs = %+v; an unknown cost or unmapped board must stay null", in)
	}
	if in.Durability.Survival != nil || in.Review.CIFirstGreen != nil || in.Review.Findings != nil {
		t.Fatalf("inputs = %+v; inputs without a source are null", in)
	}
	if in.Reliability.CrackWeight == nil || *in.Reliability.CrackWeight != 0 || in.Reliability.Reverted == nil || *in.Reliability.Reverted ||
		in.Durability.Reverts == nil || *in.Durability.Reverts != 0 || in.Durability.Hotfixes == nil || *in.Durability.Hotfixes != 0 {
		t.Fatalf("inputs = %+v; cracks, reverts and hotfixes are collected and zero when none happened", in)
	}
	want := []string{"durability.survival", "review.ciFirstGreen", "review.findings"}
	if !reflect.DeepEqual(in.NotCollected, want) {
		t.Fatalf("notCollected = %v", in.NotCollected)
	}
	if missing := []string{"delivery.budgetShare", "delivery.defectBounces"}; !reflect.DeepEqual(in.Missing, missing) {
		t.Fatalf("missing = %v; want %v", in.Missing, missing)
	}
	share := computeGrade(gradeFacts{now: now, plays: 1, costUSD: f64(0.5), authorizedUSD: 2}).Inputs.Delivery.BudgetShare
	if share == nil || *share != 0.25 {
		t.Fatalf("budgetShare = %v", share)
	}
}

func TestComputeGradeCracksRevertsAndHotfixes(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	ago := func(days int) *time.Time { at := now.Add(-time.Duration(days)*24*time.Hour - time.Hour); return &at }
	zero := 0
	for _, tc := range []struct {
		name       string
		facts      gradeFacts
		sub        CardSubgrades
		overall    float64
		label      string
		qualifiers []string
	}{
		{"a discovered unmended S2 takes 2 off reliability",
			gradeFacts{now: now, liveSince: ago(400), plays: 1, reviewRounds: 1, crackWeight: 2, cracked: true},
			CardSubgrades{8, 10, 10, 10}, 9, "", []string{}},
		{"a mended crack still ends below a never-cracked card",
			gradeFacts{now: now, liveSince: ago(400), plays: 1, reviewRounds: 1, crackWeight: 0.0625, cracked: true},
			CardSubgrades{9.5, 10, 10, 10}, 10, "gold", []string{}},
		{"a history-only crack weighs nothing and caps nothing",
			gradeFacts{now: now, liveSince: ago(400), plays: 1, reviewRounds: 1, defectBounces: &zero},
			CardSubgrades{10, 10, 10, 10}, 10, "black", []string{}},
		{"a revert counts as at least an S2 and lowers durability by 2; RV",
			gradeFacts{now: now, liveSince: ago(400), plays: 1, reviewRounds: 1, reverts: 1},
			CardSubgrades{8, 8, 10, 10}, 8.5, "", []string{"RV"}},
		{"a revert does not add to a heavier crack",
			gradeFacts{now: now, liveSince: ago(400), plays: 1, reviewRounds: 1, reverts: 1, crackWeight: 4, cracked: true},
			CardSubgrades{6, 8, 10, 10}, 8, "", []string{"RV"}},
		{"a hotfix lowers durability by 1; HF before OB and RT",
			gradeFacts{now: now, liveSince: ago(400), plays: 1, reviewRounds: 1, hotfixes: 1, crackWeight: 1, cracked: true,
				costUSD: f64(3), authorizedUSD: 2, failedRuns: 1},
			CardSubgrades{9, 9, 7.5, 10}, 9, "", []string{"HF", "OB", "RT"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.facts.costUSD == nil {
				tc.facts.costUSD, tc.facts.authorizedUSD = f64(1), 2
			}
			tc.facts.defectBounces = &zero
			g := computeGrade(tc.facts)
			label := ""
			if g.Label != nil {
				label = *g.Label
			}
			if g.Subgrades != tc.sub || g.Overall != tc.overall || label != tc.label || !reflect.DeepEqual(g.Qualifiers, tc.qualifiers) {
				t.Fatalf("grade = %v %+v %q %v; want %v %+v %q %v", g.Overall, g.Subgrades, label, g.Qualifiers,
					tc.overall, tc.sub, tc.label, tc.qualifiers)
			}
			if *g.Inputs.Reliability.CrackWeight != tc.facts.crackWeight || *g.Inputs.Durability.Reverts != tc.facts.reverts ||
				*g.Inputs.Reliability.Reverted != (tc.facts.reverts > 0) || *g.Inputs.Durability.Hotfixes != tc.facts.hotfixes {
				t.Fatalf("inputs = %+v %+v", g.Inputs.Reliability, g.Inputs.Durability)
			}
		})
	}
}

func TestCrackWeightAppliesSeverityShareDiscoveryWarrantyAndMend(t *testing.T) {
	confirmed := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	steward := &CardCrackMend{BySteward: true, ConfirmedAt: &confirmed}
	other := &CardCrackMend{ConfirmedAt: &confirmed}
	pending := &CardCrackMend{BySteward: true}
	for _, tc := range []struct {
		name                       string
		severity, share, discovery string
		primaries                  int
		warranty                   float64
		mend                       *CardCrackMend
		want                       float64
	}{
		{"S1 primary discovered, full warranty, unmended", "S1", "primary", "discovered", 1, 1, nil, 4},
		{"S2 self-reported", "S2", "primary", "self", 1, 1, nil, 1},
		{"S3 concealed", "S3", "primary", "concealed", 1, 1, nil, 1.5},
		{"S4 cosmetic", "S4", "primary", "discovered", 1, 1, nil, 0.25},
		{"two necessary primary causes split the blame", "S2", "primary", "discovered", 2, 1, nil, 1},
		{"a contributing cause carries a quarter", "S1", "contributing", "discovered", 3, 1, nil, 1},
		{"half warranty halves it", "S1", "primary", "discovered", 1, 0.5, nil, 2},
		{"history weighs nothing", "S1", "primary", "discovered", 1, 0, nil, 0},
		{"a confirmed mend by the steward halves it", "S2", "primary", "discovered", 1, 1, steward, 1},
		{"a confirmed mend by someone else restores a quarter", "S2", "primary", "discovered", 1, 1, other, 1.5},
		{"an unconfirmed mend restores nothing yet", "S2", "primary", "discovered", 1, 1, pending, 2},
		{"self-reported S3 mended by the steward", "S3", "primary", "self", 1, 1, steward, 0.25},
	} {
		if got := crackWeight(tc.severity, tc.share, tc.discovery, tc.primaries, tc.warranty, tc.mend); got != tc.want {
			t.Errorf("%s: weight = %v; want %v", tc.name, got, tc.want)
		}
	}
}

func TestWarrantyByCardAgeWhenTheBugWasRaised(t *testing.T) {
	live := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return live.Add(time.Duration(n) * 24 * time.Hour) }
	for _, tc := range []struct {
		liveSince *time.Time
		bug       time.Time
		label     string
		factor    float64
	}{
		{nil, day(500), "full", 1},
		{&live, day(-3), "full", 1},
		{&live, day(180), "full", 1},
		{&live, day(181), "half", 0.5},
		{&live, day(365), "half", 0.5},
		{&live, day(366), "history", 0},
	} {
		label, factor := warranty(tc.liveSince, tc.bug)
		if label != tc.label || factor != tc.factor {
			t.Errorf("warranty(%v, %v) = %s %v; want %s %v", tc.liveSince, tc.bug, label, factor, tc.label, tc.factor)
		}
	}
}

func TestAddWorkdaysSkipsWeekends(t *testing.T) {
	friday := time.Date(2026, 10, 2, 15, 0, 0, 0, time.UTC)
	if got := AddWorkdays(friday, 5); !got.Equal(time.Date(2026, 10, 9, 15, 0, 0, 0, time.UTC)) {
		t.Fatalf("five working days after Friday = %v; want the next Friday", got)
	}
	saturday := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	if got := AddWorkdays(saturday, 1); !got.Equal(time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("one working day after Saturday = %v; want Monday", got)
	}
}
