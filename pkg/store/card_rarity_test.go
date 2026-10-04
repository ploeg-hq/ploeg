package store

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/rarity"
)

type rarityWorld struct {
	*crackWorld
	runs int
}

func newRarityWorld(t *testing.T) *rarityWorld {
	return &rarityWorld{crackWorld: newCrackWorld(t)}
}

func (w *rarityWorld) mint(item int64) {
	w.t.Helper()
	w.runs++
	if _, err := testStore.pool.Exec(w.ctx, `INSERT INTO agent_runs (work_item_id, team, run_token, state, started_at, role, round, writes, authorized, links)
		VALUES ($1, 'silver', $2, 'running', now() - interval '1 hour', 'builder', 0, true, 1, '{}')`,
		item, fmt.Sprintf("rarity-run-%d-%d", item, w.runs)); err != nil {
		w.t.Fatal(err)
	}
}

type rarityFileLines struct {
	path      string
	additions int
	deletions int
}

func (w *rarityWorld) play(item int64, number int, state string, daysAgo int, additions, deletions int, files ...rarityFileLines) time.Time {
	w.t.Helper()
	at := w.now.Add(-time.Duration(daysAgo) * 24 * time.Hour)
	facts := PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg", Number: number, WorkItemID: item, State: state,
		HeadSHA: fmt.Sprintf("%040d", number), Additions: intp(additions), Deletions: intp(deletions)}
	if state == "merged" {
		facts.MergedAt, facts.MergedBy = &at, "stewart"
		facts.MergeCommitSHA = strings.Repeat(fmt.Sprintf("%02d", number%100), 20)
	}
	if ok, err := testStore.RecordPullRequestFacts(w.ctx, facts); err != nil || !ok {
		w.t.Fatalf("record play %d: %v %v", number, ok, err)
	}
	if len(files) > 0 {
		change := PullRequestChange{Forge: "forgejo", Repo: "webgrip/ploeg", Number: number, Lines: map[string]FileLines{}}
		for _, f := range files {
			change.Files = append(change.Files, f.path)
			if f.additions >= 0 {
				change.Lines[f.path] = FileLines{Additions: f.additions, Deletions: f.deletions}
			}
		}
		if ok, err := testStore.RecordPullRequestChange(w.ctx, change); err != nil || !ok {
			w.t.Fatalf("record files of %d: %v %v", number, ok, err)
		}
	}
	return at
}

func (w *rarityWorld) rarityCard(id int64, opts CardOptions) OperatorCard {
	w.t.Helper()
	if opts.Rarity == nil {
		opts.Rarity = &RarityOptions{}
	}
	return w.card(id, opts)
}

func (w *rarityWorld) cohort(quarter string, scores ...float64) {
	w.t.Helper()
	for i, score := range scores {
		item := w.item(fmt.Sprintf("cohort-%s-%d", quarter, i), "silver")
		if _, err := testStore.pool.Exec(w.ctx, `INSERT INTO card_rarity (work_item_id, checked_at, formula, revealed_tier, predicted_tier,
				score, predicted_score, percentile, cohort_target, cohort_quarter, cohort_size, inputs, revealed_at, recorded_at)
			VALUES ($1, now(), $2, 'common', 'common', $3, 0, NULL, 'webgrip/ploeg', $4, 1, '{}', now(), now())`,
			item, rarity.Formula, score, quarter); err != nil {
			w.t.Fatal(err)
		}
	}
}

func near(a *float64, b float64) bool { return a != nil && math.Abs(*a-b) < 1e-9 }

func TestMigration0031AddsLinesAndCardRarity(t *testing.T) {
	ctx := context.Background()
	for _, column := range []string{"additions", "deletions"} {
		var nullable string
		if err := testStore.pool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns
			WHERE table_name = 'pull_request_files' AND column_name = $1`, column).Scan(&nullable); err != nil || nullable != "YES" {
			t.Errorf("pull_request_files.%s = %q, %v; an uncounted file is NULL, never zero", column, nullable, err)
		}
	}
	w := newRarityWorld(t)
	item := w.item("bad-row", "silver")
	if _, err := testStore.pool.Exec(ctx, `INSERT INTO card_rarity (work_item_id, checked_at, revealed_tier) VALUES ($1, now(), 'rare')`, item); err == nil {
		t.Error("a revealed tier without its score and cohort was stored")
	}
}

func TestRecordPullRequestChange_KeepsLinesPerFile(t *testing.T) {
	w := newRarityWorld(t)
	item := w.item("lines", "silver")
	w.play(item, 40, "merged", 1, 30, 4, rarityFileLines{"a.go", 20, 4}, rarityFileLines{"b.go", -1, 0})
	rows, err := testStore.pool.Query(w.ctx, `SELECT f.path, f.additions, f.deletions FROM pull_request_files f
		JOIN pull_requests p ON p.id = f.pull_request_id WHERE p.number = 40 ORDER BY f.path`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var path string
		var a, d *int
		if err := rows.Scan(&path, &a, &d); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s:%v:%v", path, a != nil, d != nil))
		if path == "a.go" && (*a != 20 || *d != 4) {
			t.Errorf("a.go lines = %d/%d", *a, *d)
		}
	}
	if strings.Join(got, ",") != "a.go:true:true,b.go:false:false" {
		t.Errorf("files = %v; a file the forge did not count keeps NULL lines", got)
	}
}

func TestCardRarity_AbsentUntilMintedAndWithoutOptions(t *testing.T) {
	w := newRarityWorld(t)
	item := w.item("draft", "silver")
	if r := w.rarityCard(item, CardOptions{}).Rarity; r != nil {
		t.Fatalf("a card without a Run has rarity %+v", r)
	}
	w.mint(item)
	if r := w.card(item, CardOptions{}).Rarity; r != nil {
		t.Fatalf("a read without rarity options computed %+v", r)
	}
	r := w.rarityCard(item, CardOptions{}).Rarity
	if r == nil || r.Formula != "2026.1" || r.Predicted == nil || *r.Predicted != "common" || r.Revealed != nil || r.Tier != "common" ||
		!near(r.Score, 0) || r.Percentile != nil || r.RevealedAt != nil {
		t.Fatalf("minted rarity = %+v", r)
	}
	if r.Cohort == nil || *r.Cohort != (CardRarityCohort{Target: "webgrip/ploeg", Quarter: rarity.Quarter(time.Now()), Size: 1}) {
		t.Errorf("cohort = %+v", r.Cohort)
	}
	in := r.Inputs
	if in.Reach.Modules != nil || in.Reach.Repos == nil || *in.Reach.Repos != 1 || in.Sensitive.Files != nil || len(in.Sensitive.Paths) != 0 ||
		in.Novelty.Share != nil || in.Novelty.Files != nil || in.Size.CountedLines != nil || in.Set == nil || *in.Set || in.Truncated ||
		strings.Join(in.NotCollected, ",") != "complexity,estimate" {
		t.Errorf("inputs = %+v; a fact nobody reported stays absent", in)
	}
}

func TestCardRarity_PredictedFromAnOpenPlay(t *testing.T) {
	w := newRarityWorld(t)
	item := w.item("open", "silver")
	w.mint(item)
	w.play(item, 41, "open", 0, 200, 40)
	r := w.rarityCard(item, CardOptions{}).Rarity
	if r == nil || r.Revealed != nil || r.Tier != "common" || !near(r.Score, 18) || r.Inputs.Size.CountedLines == nil ||
		*r.Inputs.Size.CountedLines != 240 || r.Inputs.Reach.Modules != nil {
		t.Fatalf("rarity = %+v; an open play's diff size predicts, uncounted", r)
	}
}

func TestCardRarity_PredictedUsesEarlierPlaysFiles(t *testing.T) {
	w := newRarityWorld(t)
	item := w.item("retry", "silver")
	w.mint(item)
	w.play(item, 42, "merged", 2, 525, 502,
		rarityFileLines{"services/engine/pkg/store/migrations/0001.sql", 5, 0}, rarityFileLines{"apps/console/src/a.ts", 20, 2},
		rarityFileLines{"package-lock.json", 500, 500})
	w.play(item, 43, "open", 0, 30, 10)
	card := w.rarityCard(item, CardOptions{})
	r := card.Rarity
	if card.State != "in_review" || r == nil || r.Revealed != nil {
		t.Fatalf("state %s rarity %+v; a card with an open play is not revealed", card.State, r)
	}
	in := r.Inputs
	if !near(r.Score, 50.1) || r.Tier != "uncommon" || *in.Reach.Modules != 2 || *in.Sensitive.Files != 1 || *in.Novelty.Files != 2 ||
		in.Sensitive.Paths[0] != "services/engine/pkg/store/migrations/0001.sql" || !near(in.Novelty.Share, 1) || *in.Size.CountedLines != 67 {
		t.Fatalf("rarity = %+v inputs %+v", r, in)
	}
}

func TestCardRarity_RevealedAtTheMergeAndFrozen(t *testing.T) {
	w := newRarityWorld(t)
	older := w.item("older", "silver")
	ancient := w.item("ancient", "silver")
	item := w.item("reveal", "silver")
	w.mint(item)
	w.play(older, 50, "merged", 31, 1, 1, rarityFileLines{"pkg/store/card.go", 1, 1})
	w.play(ancient, 51, "merged", 202, 1, 1, rarityFileLines{"docs/a.md", 1, 1})
	merged := w.play(item, 52, "merged", 1, 415, 325,
		rarityFileLines{"pkg/store/migrations/0099_x.sql", 10, 0}, rarityFileLines{"pkg/store/card.go", 100, 20},
		rarityFileLines{"docs/a.md", 5, 5}, rarityFileLines{"go.sum", 300, 300})

	card := w.rarityCard(item, CardOptions{})
	r := card.Rarity
	if card.Release == nil || card.Release.Source != "merge" || r == nil {
		t.Fatalf("release %+v rarity %+v", card.Release, r)
	}
	if r.Revealed == nil || *r.Revealed != "uncommon" || r.Predicted == nil || *r.Predicted != "common" || r.Tier != "uncommon" ||
		!near(r.Score, 45.9) || r.Percentile != nil || r.RevealedAt == nil || !r.RevealedAt.Equal(merged) {
		t.Fatalf("rarity = %+v; revealed from the real change, predicted from its diff size", r)
	}
	if r.Cohort == nil || *r.Cohort != (CardRarityCohort{Target: "webgrip/ploeg", Quarter: rarity.Quarter(merged), Size: 1}) {
		t.Errorf("cohort = %+v", r.Cohort)
	}
	in := r.Inputs
	if *in.Reach.Modules != 2 || *in.Reach.Repos != 1 || *in.Sensitive.Files != 1 || *in.Novelty.Files != 3 || *in.Novelty.Novel != 2 ||
		*in.Size.CountedLines != 140 || in.Set != nil || in.Truncated {
		t.Errorf("inputs = %+v; go.sum counts for nothing, a file touched 30 days earlier is not novel, one touched 200 days earlier is", in)
	}

	w.cohort(rarity.Quarter(merged), 90, 91, 92, 93, 94, 95, 96, 97, 98, 99, 90, 91, 92, 93, 94, 95, 96, 97, 98, 99,
		90, 91, 92, 93, 94, 95, 96, 97, 98, 99, 90, 91, 92, 93, 94, 95, 96, 97, 98, 99)
	again := w.rarityCard(item, CardOptions{}).Rarity
	if *again.Revealed != "uncommon" || again.Cohort.Size != 1 || !near(again.Score, 45.9) || !again.RevealedAt.Equal(merged) {
		t.Fatalf("rarity after the cohort grew = %+v; a revealed tier is frozen", again)
	}
	ids, err := testStore.RarityCandidates(w.ctx, time.Now(), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if id == item {
			t.Errorf("candidates = %v; a revealed card is no candidate", ids)
		}
	}
}

func TestCardRarity_PercentileOnceTheCohortIsLarge(t *testing.T) {
	w := newRarityWorld(t)
	item := w.item("ranked", "silver")
	w.mint(item)
	quarter := rarity.Quarter(w.now.Add(-24 * time.Hour))
	scores := make([]float64, 0, 39)
	for i := 0; i < 39; i++ {
		scores = append(scores, float64(i))
	}
	w.cohort(quarter, scores...)
	w.play(item, 53, "merged", 1, 10, 0, rarityFileLines{"README.md", 10, 0})
	r := w.rarityCard(item, CardOptions{}).Rarity
	if r == nil || r.Revealed == nil || r.Cohort == nil || r.Cohort.Size != 40 || r.Percentile == nil {
		t.Fatalf("rarity = %+v", r)
	}
	below := 0
	for _, s := range scores {
		if s < *r.Score {
			below++
		}
	}
	want, _, _ := rarity.Tier(*r.Score, scores)
	if !near(r.Percentile, math.Round(1000*float64(below+1)/40)/10) || *r.Revealed != want {
		t.Errorf("percentile %v tier %s; want %d below of 40 and %s", *r.Percentile, *r.Revealed, below, want)
	}
}

func TestCardRarity_NotRevealedWithoutFiles(t *testing.T) {
	w := newRarityWorld(t)
	item := w.item("no-files", "silver")
	w.mint(item)
	w.play(item, 54, "merged", 1, 10, 0)
	r := w.rarityCard(item, CardOptions{}).Rarity
	if r == nil || r.Revealed != nil || r.Predicted == nil {
		t.Fatalf("rarity = %+v; a merged play whose files were never read reveals nothing", r)
	}
	if ids, err := testStore.RarityCandidates(w.ctx, time.Now(), 10); err != nil || len(ids) != 0 {
		t.Errorf("candidates = %v, %v", ids, err)
	}
}

func TestCardRarity_SweepCandidatesAndChecks(t *testing.T) {
	w := newRarityWorld(t)
	item := w.item("unreleased", "silver")
	w.play(item, 55, "merged", 1, 10, 0, rarityFileLines{"a.go", 10, 0})
	ids, err := testStore.RarityCandidates(w.ctx, time.Now(), 10)
	if err != nil || len(ids) != 1 || ids[0] != item {
		t.Fatalf("candidates = %v, %v", ids, err)
	}
	if err := testStore.MarkRarityChecked(w.ctx, item, time.Now()); err != nil {
		t.Fatal(err)
	}
	if ids, _ := testStore.RarityCandidates(w.ctx, time.Now().Add(-time.Hour), 10); len(ids) != 0 {
		t.Errorf("a card checked just now is a candidate again: %v", ids)
	}
	if ids, _ := testStore.RarityCandidates(w.ctx, time.Now().Add(time.Minute), 10); len(ids) != 1 {
		t.Errorf("a card checked before the cut-off is no candidate: %v", ids)
	}
	if r := w.rarityCard(item, CardOptions{}).Rarity; r == nil || r.Revealed == nil || r.Predicted == nil {
		t.Fatalf("an unminted merged card reveals with a prediction: %+v", r)
	}
	if err := testStore.MarkRarityChecked(w.ctx, item, time.Now()); err != nil {
		t.Fatal(err)
	}
	if r := w.rarityCard(item, CardOptions{}).Rarity; r.Revealed == nil {
		t.Fatal("a check erased a revealed tier")
	}
}

func TestCardRarity_EpicIsLegendaryWhileItsSetIsComplete(t *testing.T) {
	w := newRarityWorld(t)
	a := w.item("a", "silver")
	w.epics("a", EpicRef{ExternalID: "epic", Title: "Run cards"})
	w.shift(a, "agent/a")
	epic := w.item("epic", "silver")
	if r := w.rarityCard(epic, CardOptions{}).Rarity; r != nil {
		t.Fatalf("an incomplete epic without a Run has rarity %+v", r)
	}
	w.play(a, 56, "merged", 40, 10, 0, rarityFileLines{"a.go", 10, 0})
	r := w.rarityCard(epic, CardOptions{}).Rarity
	if r == nil || r.Tier != "legendary" || r.Predicted != nil || r.Revealed != nil || r.Score != nil || r.Cohort != nil {
		t.Fatalf("epic rarity = %+v; a complete set makes its epic legendary", r)
	}
	child := w.rarityCard(a, CardOptions{}).Rarity
	if child == nil || child.Revealed == nil || child.Tier != *child.Revealed {
		t.Fatalf("child rarity = %+v; the set rule is for the epic's own card", child)
	}
	if r := w.rarityCard(epic, CardOptions{Now: time.Now().Add(-20 * 24 * time.Hour)}).Rarity; r != nil {
		t.Fatalf("an epic whose set is not settled yet = %+v", r)
	}
}

func TestCountedLines(t *testing.T) {
	m, err := rarity.Rules{}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	n := func(v int) *int { return &v }
	f := func(path string, a, d int) rarityFile { return rarityFile{path: path, additions: n(a), deletes: n(d)} }
	bare := func(path string) rarityFile { return rarityFile{path: path} }
	play := CardPlay{Additions: n(700), Deletions: n(100)}
	for _, tc := range []struct {
		name string
		play CardPlay
		rp   *rarityPlay
		want *int64
	}{
		{"not captured", play, &rarityPlay{}, nil},
		{"no record", play, nil, nil},
		{"every file counted: the lockfile is left out", play,
			&rarityPlay{captured: true, files: []rarityFile{f("a.go", 90, 10), f("yarn.lock", 600, 90)}}, lp64(100)},
		{"uncounted files, none excluded: the play's total", play,
			&rarityPlay{captured: true, files: []rarityFile{bare("a.go"), bare("b.go")}}, lp64(800)},
		{"uncounted files with a lockfile: unknown", play,
			&rarityPlay{captured: true, files: []rarityFile{bare("a.go"), bare("go.sum")}}, nil},
		{"truncated: the total less the counted excluded lines", play,
			&rarityPlay{captured: true, truncated: true, files: []rarityFile{f("a.go", 5, 0), f("vendor/x/y.go", 300, 0)}}, lp64(500)},
		{"truncated without a total: unknown", CardPlay{},
			&rarityPlay{captured: true, truncated: true, files: []rarityFile{f("a.go", 5, 0)}}, nil},
		{"no files at all: zero", CardPlay{}, &rarityPlay{captured: true}, lp64(0)},
	} {
		got := countedLines(tc.play, tc.rp, m)
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("%s: counted = %v; want %v", tc.name, deref(got), deref(tc.want))
		}
	}
}

func lp64(v int64) *int64 { return &v }

func deref(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}
