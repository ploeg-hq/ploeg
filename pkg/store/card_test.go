package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestMigration0024AddsDiffAndCIColumns(t *testing.T) {
	ctx := context.Background()
	var applied bool
	if err := testStore.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = '0024_pull_request_diff_and_ci.sql')`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("migration 0024 was not applied")
	}
	for _, column := range []string{"additions", "deletions", "changed_files", "ci_state", "ci_checks", "ci_head_sha", "ci_captured_at"} {
		var nullable string
		if err := testStore.pool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns
			WHERE table_name = 'pull_requests' AND column_name = $1`, column).Scan(&nullable); err != nil {
			t.Errorf("pull_requests.%s missing: %v", column, err)
			continue
		}
		if nullable != "YES" {
			t.Errorf("pull_requests.%s must be nullable: an unreported fact is NULL, never a default", column)
		}
	}
}

func intp(n int) *int { return &n }

type storedDiffCI struct {
	additions, deletions, changedFiles *int
	ciState, ciHead                    *string
	ciChecks                           []byte
	ciAt                               *time.Time
}

func readDiffCI(t *testing.T, number int) storedDiffCI {
	t.Helper()
	var s storedDiffCI
	if err := testStore.pool.QueryRow(context.Background(), `SELECT additions, deletions, changed_files, ci_state, ci_head_sha,
		ci_checks, ci_captured_at FROM pull_requests WHERE number = $1`, number).
		Scan(&s.additions, &s.deletions, &s.changedFiles, &s.ciState, &s.ciHead, &s.ciChecks, &s.ciAt); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRecordPullRequestFacts_DiffAndCI(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	item, _ := pullRequestItem(t, "agent/vik-1900")
	record := func(f PullRequestFacts) {
		t.Helper()
		f.Forge, f.Repo, f.Number, f.WorkItemID = "forgejo", "webgrip/ploeg", 30, item
		if ok, err := testStore.RecordPullRequestFacts(ctx, f); err != nil || !ok {
			t.Fatalf("recorded = %v, err = %v", ok, err)
		}
	}

	record(PullRequestFacts{State: "open", HeadSHA: "h1"})
	if s := readDiffCI(t, 30); s.additions != nil || s.changedFiles != nil || s.ciState != nil || s.ciChecks != nil {
		t.Fatalf("unreported diff and CI stored as %+v; want NULL", s)
	}

	at := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	record(PullRequestFacts{Additions: intp(214), Deletions: intp(0), ChangedFiles: intp(6),
		CI: &PullRequestCI{State: "failure", HeadSHA: "h1", CapturedAt: at, Checks: []PullRequestCheck{
			{Context: "verify", State: "failure"}, {Context: "odd", State: "warning"}}}})
	s := readDiffCI(t, 30)
	if s.additions == nil || *s.additions != 214 || s.deletions == nil || *s.deletions != 0 || *s.changedFiles != 6 {
		t.Errorf("diff = %v %v %v; want 214, a reported 0, 6", s.additions, s.deletions, s.changedFiles)
	}
	if s.ciState == nil || *s.ciState != "failure" || *s.ciHead != "h1" || !s.ciAt.Equal(at) ||
		string(s.ciChecks) != `[{"state": "failure", "context": "verify"}]` {
		t.Errorf("ci = %v %v %s %v; the warning check must be left out", s.ciState, s.ciHead, s.ciChecks, s.ciAt)
	}

	record(PullRequestFacts{HeadSHA: "h2", Additions: intp(-1), CI: &PullRequestCI{State: "neutral", HeadSHA: "h2"}})
	s = readDiffCI(t, 30)
	if *s.additions != 214 || *s.ciState != "failure" || *s.ciHead != "h1" {
		t.Errorf("a negative count or an unknown CI state erased a known fact: %+v", s)
	}

	record(PullRequestFacts{Additions: intp(220), CI: &PullRequestCI{State: "success", HeadSHA: "h2"}})
	s = readDiffCI(t, 30)
	if *s.additions != 220 || *s.deletions != 0 || *s.ciState != "success" || *s.ciHead != "h2" ||
		string(s.ciChecks) != `[]` || s.ciAt == nil {
		t.Errorf("a newer reading must replace the diff figure it reports and the whole CI: %+v (%s)", s, s.ciChecks)
	}
}

type cardFixture struct {
	t       *testing.T
	item    int64
	shift   int64
	base    time.Time
	runs    int
	release map[string]string
}

func newCardFixture(t *testing.T, externalID string) *cardFixture {
	t.Helper()
	resetTables(t)
	ctx := context.Background()
	item, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: externalID, Team: "silver",
		Title: "Retry sandbox claims", URL: "https://board.example/tasks/" + externalID,
		Target: &work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "ploeg", BaseBranch: "development"}})
	if err != nil {
		t.Fatal(err)
	}
	return &cardFixture{t: t, item: item, base: time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)}
}

func (f *cardFixture) openShift(branch string) int64 {
	f.t.Helper()
	shift, err := testStore.OpenShift(context.Background(), f.item, "silver", branch, 2)
	if err != nil {
		f.t.Fatal(err)
	}
	f.shift = shift
	return shift
}

func (f *cardFixture) closeShift() {
	f.t.Helper()
	if _, err := testStore.pool.Exec(context.Background(), `UPDATE shifts SET closed_at = now() WHERE id = $1`, f.shift); err != nil {
		f.t.Fatal(err)
	}
}

type fixtureRun struct {
	role          string
	round         int
	writes        bool
	startMin      int
	durationMin   int
	outcome       string
	usage         string
	links         []string
	authorizedUSD float64
}

func (f *cardFixture) run(r fixtureRun) {
	f.t.Helper()
	f.runs++
	started := f.base.Add(time.Duration(r.startMin) * time.Minute)
	var finished *time.Time
	state := "running"
	var outcome *string
	if r.durationMin > 0 {
		at := started.Add(time.Duration(r.durationMin) * time.Minute)
		finished, state, outcome = &at, "finished", &r.outcome
	}
	if r.links == nil {
		r.links = []string{}
	}
	if _, err := testStore.pool.Exec(context.Background(), `
		INSERT INTO agent_runs (work_item_id, shift_id, team, run_token, state, started_at, finished_at, outcome,
			role, round, writes, authorized, usage, links)
		VALUES ($1, $2, 'silver', $3, $4, $5, $6, $7, $8, $9, $10, $11, NULLIF($12, '')::jsonb, $13)`,
		f.item, f.shift, fmt.Sprintf("card-run-%d-%d", f.item, f.runs), state, started, finished, outcome,
		r.role, r.round, r.writes, r.authorizedUSD, r.usage, r.links); err != nil {
		f.t.Fatalf("insert run: %v", err)
	}
}

func (f *cardFixture) pr(facts PullRequestFacts) {
	f.t.Helper()
	facts.Forge, facts.Repo, facts.WorkItemID = "forgejo", "webgrip/ploeg", f.item
	if ok, err := testStore.RecordPullRequestFacts(context.Background(), facts); err != nil || !ok {
		f.t.Fatalf("recorded = %v, err = %v", ok, err)
	}
}

func (f *cardFixture) card(bots ...string) OperatorCard {
	f.t.Helper()
	card, err := testStore.OperatorCard(context.Background(), f.item, []string{"silver"}, CardOptions{Bots: bots, ReleaseEnvironments: f.release})
	if err != nil {
		f.t.Fatal(err)
	}
	return card
}

func (f *cardFixture) at(minutes int) *time.Time {
	at := f.base.Add(time.Duration(minutes) * time.Minute)
	return &at
}

func TestOperatorCard_WithoutAShiftIsADraft(t *testing.T) {
	f := newCardFixture(t, "1612")
	card := f.card()
	if card.WorkItemID != fmt.Sprint(f.item) || card.Title != "Retry sandbox claims" || card.ExternalRef != "VIK-1612" ||
		card.URL != "https://board.example/tasks/1612" || card.Team != "silver" {
		t.Errorf("identity = %+v", card)
	}
	if card.Target == nil || *card.Target != (OperatorCardTarget{Forge: "forgejo", Owner: "webgrip", Repo: "ploeg"}) {
		t.Errorf("target = %+v", card.Target)
	}
	if card.State != "drafting" || card.Rarity != nil || card.Grade != nil || card.Condition != nil || card.Finish != "matte" ||
		card.Demo || card.Steward != nil {
		t.Errorf("card = %+v", card)
	}
	if len(card.Plays) != 0 || len(card.Crew) != 0 || len(card.Roster) != 0 || len(card.Events) != 0 {
		t.Errorf("a card without work has plays %v, crew %v, roster %v, events %v", card.Plays, card.Crew, card.Roster, card.Events)
	}
	tt := card.Totals
	if tt.Runs != 0 || tt.Shifts != 0 || tt.CostUSD != nil || tt.InputTokens != nil || tt.CostStatus != "not_reported" ||
		!tt.UsageComplete || tt.FirstRunAt != nil || tt.RunSeconds != nil {
		t.Errorf("totals = %+v; nothing ran, so nothing is known and nothing is zero-filled", tt)
	}
}

func TestOperatorCard_AnOpenPullRequestIsInReview(t *testing.T) {
	f := newCardFixture(t, "1613")
	f.openShift("agent/vik-1613")
	f.run(fixtureRun{role: "builder", writes: true, startMin: 0, durationMin: 30, outcome: "pr_opened", authorizedUSD: 2,
		usage: `{"inputTokens": 12100000, "outputTokens": 88000, "costUsd": 0.58, "turns": 61, "toolCalls": 143,
			"cacheReadInputTokens": 9800000, "cacheCreationInputTokens": 120000}`,
		links: []string{"https://forge.example/webgrip/ploeg/pulls/57"}})
	f.pr(PullRequestFacts{Number: 57, Branch: "agent/vik-1613", State: "open", HeadSHA: "h1",
		Additions: intp(214), Deletions: intp(38), ChangedFiles: intp(6),
		CI: &PullRequestCI{State: "success", HeadSHA: "h1", Checks: []PullRequestCheck{{Context: "verify", State: "success"}}}})

	card := f.card()
	if card.State != "in_review" || len(card.Plays) != 1 {
		t.Fatalf("state %s, plays %+v", card.State, card.Plays)
	}
	p := card.Plays[0]
	if p.Number != 57 || p.URL != "https://forge.example/webgrip/ploeg/pulls/57" || p.State != "open" ||
		p.ShiftID != fmt.Sprint(f.shift) || p.Branch != "agent/vik-1613" || p.HeadSHA != "h1" ||
		*p.Additions != 214 || *p.Deletions != 38 || *p.ChangedFiles != 6 || p.MergedAt != nil || len(p.Reviews) != 0 {
		t.Errorf("play = %+v", p)
	}
	if p.CI == nil || p.CI.State != "success" || len(p.CI.Checks) != 1 || p.CI.HeadSHA != "h1" || p.CI.CapturedAt.IsZero() {
		t.Errorf("ci = %+v", p.CI)
	}
	if len(card.Crew) != 1 || card.Crew[0].Role != "builder" || !card.Crew[0].Writes || card.Crew[0].Runs != 1 ||
		*card.Crew[0].CostUSD != 0.58 || *card.Crew[0].InputTokens != 12100000 || *card.Crew[0].OutputTokens != 88000 {
		t.Errorf("crew = %+v", card.Crew)
	}
	tt := card.Totals
	if *tt.CostUSD != 0.58 || tt.AuthorizedUSD != 2 || tt.CostStatus != "observed" || *tt.InputTokens != 12100000 ||
		*tt.CacheReadInputTokens != 9800000 || *tt.CacheCreationInputTokens != 120000 || *tt.Turns != 61 ||
		*tt.ToolCalls != 143 || !tt.UsageComplete || tt.Runs != 1 || tt.FailedRuns != 0 || tt.Rounds != 1 ||
		tt.Shifts != 1 || *tt.RunSeconds != 1800 || !tt.FirstRunAt.Equal(f.base) || !tt.LastRunAt.Equal(*f.at(30)) {
		t.Errorf("totals = %+v", tt)
	}
	kinds := eventKinds(card.Events)
	if fmt.Sprint(kinds) != "[minted run_started run_finished pr_opened]" {
		t.Errorf("events = %v", kinds)
	}
	if card.Events[3].Detail["number"] != float64(57) || !card.Events[3].At.Equal(*f.at(30)) {
		t.Errorf("pr_opened = %+v; opened when the Run that reported the link finished", card.Events[3])
	}
}

func TestOperatorCard_ABouncedPullRequestAndItsSuccessorAreTwoPlays(t *testing.T) {
	f := newCardFixture(t, "1614")
	f.openShift("agent/vik-1614")
	f.run(fixtureRun{role: "builder", writes: true, startMin: 0, durationMin: 10, outcome: "pr_opened", usage: `{"costUsd": 0.2}`,
		links: []string{"https://forge.example/webgrip/ploeg/pulls/57"}})
	f.pr(PullRequestFacts{Number: 57, Branch: "agent/vik-1614", State: "open"})
	f.pr(PullRequestFacts{Number: 57, Review: &PullRequestReview{Reviewer: "anna", State: "changes_requested"}})
	f.pr(PullRequestFacts{Number: 57, Review: &PullRequestReview{Reviewer: "ploeg-bot", State: "approved"}})
	f.pr(PullRequestFacts{Number: 57, State: "closed", ClosedAt: f.at(20)})
	f.closeShift()
	second := f.openShift("agent/vik-1614-2")
	f.run(fixtureRun{role: "builder", writes: true, startMin: 30, durationMin: 10, outcome: "pr_opened", usage: `{"costUsd": 0.3}`,
		links: []string{"https://forge.example/webgrip/ploeg/pulls/58"}})
	f.pr(PullRequestFacts{Number: 58, Branch: "agent/vik-1614-2", State: "open"})
	f.pr(PullRequestFacts{Number: 58, Review: &PullRequestReview{Reviewer: "bert", State: "approved"}})
	f.pr(PullRequestFacts{Number: 58, State: "merged", MergedAt: f.at(55), MergedBy: "ryan", MergeCommitSHA: "m1"})

	card := f.card("Ploeg-Bot")
	if card.State != "merged" || len(card.Plays) != 2 {
		t.Fatalf("state %s, plays %+v", card.State, card.Plays)
	}
	if card.Plays[0].Number != 57 || card.Plays[0].State != "closed" || card.Plays[0].ClosedAt == nil ||
		card.Plays[1].Number != 58 || card.Plays[1].State != "merged" || card.Plays[1].ShiftID != fmt.Sprint(second) ||
		card.Plays[1].MergedBy != "ryan" || card.Plays[1].MergeCommitSHA != "m1" {
		t.Errorf("plays = %+v", card.Plays)
	}
	if len(card.Plays[0].Reviews) != 2 || card.Plays[0].Reviews[0].Reviewer != "anna" {
		t.Errorf("reviews of the bounced play = %+v; every recorded review stays on its play", card.Plays[0].Reviews)
	}
	if card.Steward == nil || *card.Steward != (CardSteward{Name: "ryan", Source: "merged_by"}) {
		t.Errorf("steward = %+v", card.Steward)
	}
	if fmt.Sprintf("%+v", card.Roster) != "[{Name:anna Roles:[reviewer]} {Name:bert Roles:[reviewer]} {Name:ryan Roles:[merger]}]" {
		t.Errorf("roster = %+v; the bot is not a person", card.Roster)
	}
	if card.Totals.Shifts != 2 || card.Totals.Rounds != 2 || *card.Totals.CostUSD != 0.5 || card.Totals.UsageComplete {
		t.Errorf("totals = %+v; two Runs reported cost but not tokens", card.Totals)
	}
	if !sort.SliceIsSorted(card.Events, func(i, j int) bool { return card.Events[i].At.Before(card.Events[j].At) }) {
		t.Errorf("events are not oldest first: %v", eventKinds(card.Events))
	}
	var closedAt, mergedAt time.Time
	for _, ev := range card.Events {
		switch ev.Kind {
		case "closed":
			closedAt = ev.At
		case "merged":
			mergedAt = ev.At
			if ev.Actor != "ryan" {
				t.Errorf("merged actor = %q", ev.Actor)
			}
		case "review":
			if ev.Actor == "ploeg-bot" {
				t.Error("a bot review names the bot as a human actor")
			}
		}
	}
	if closedAt.IsZero() || mergedAt.IsZero() || !closedAt.Before(mergedAt) {
		t.Errorf("closed %v, merged %v", closedAt, mergedAt)
	}
}

func TestOperatorCard_FailedRunsAreCounted(t *testing.T) {
	f := newCardFixture(t, "1615")
	f.openShift("agent/vik-1615")
	f.run(fixtureRun{role: "builder", writes: true, round: 0, startMin: 0, durationMin: 5, outcome: "failed", usage: `{"costUsd": 0.1, "inputTokens": 5, "outputTokens": 1}`})
	f.run(fixtureRun{role: "builder", writes: true, round: 1, startMin: 10, durationMin: 5, outcome: "stuck", usage: `{"costUsd": 0.1, "inputTokens": 5, "outputTokens": 1}`})
	f.run(fixtureRun{role: "reviewer", writes: false, round: 1, startMin: 10, durationMin: 2, outcome: "no_change_needed", usage: `{"costUsd": 0, "inputTokens": 0, "outputTokens": 0}`})
	card := f.card()
	tt := card.Totals
	if tt.Runs != 3 || tt.FailedRuns != 2 || tt.Rounds != 2 || !tt.UsageComplete || *tt.InputTokens != 10 || card.State != "drafting" {
		t.Errorf("totals = %+v, state %s", tt, card.State)
	}
	if len(card.Crew) != 2 || card.Crew[1].Role != "reviewer" || card.Crew[1].Writes || *card.Crew[1].CostUSD != 0 {
		t.Errorf("crew = %+v; a reported zero stays zero", card.Crew)
	}
}

func TestOperatorCard_PartialUsageIsSummedAndFlagged(t *testing.T) {
	f := newCardFixture(t, "1616")
	f.openShift("agent/vik-1616")
	f.run(fixtureRun{role: "builder", writes: true, startMin: 0, durationMin: 10, outcome: "pr_opened",
		usage: `{"costUsd": 0.4, "inputTokens": 100, "outputTokens": 10, "turns": 7}`})
	f.run(fixtureRun{role: "reviewer", startMin: 20, durationMin: 5, outcome: "no_change_needed",
		usage: `{"costUsd": 0.1, "inputTokens": 50, "outputTokens": 5}`})
	f.run(fixtureRun{role: "builder", writes: true, round: 1, startMin: 40, authorizedUSD: 1})
	card := f.card()
	tt := card.Totals
	if *tt.CostUSD != 0.5 || *tt.InputTokens != 150 || *tt.Turns != 7 || tt.ToolCalls != nil || tt.UsageComplete ||
		tt.CostStatus != "reserved" || tt.Runs != 3 || *tt.RunSeconds != 900 || !tt.LastRunAt.Equal(*f.at(40)) {
		t.Errorf("totals = %+v; a running Run holds budget and has not reported", tt)
	}
}

func TestOperatorCard_StewardFallsBackToTheLastApprover(t *testing.T) {
	f := newCardFixture(t, "1617")
	f.openShift("agent/vik-1617")
	f.pr(PullRequestFacts{Number: 60, State: "open"})
	f.pr(PullRequestFacts{Number: 60, Review: &PullRequestReview{Reviewer: "anna", State: "approved"}})
	f.pr(PullRequestFacts{Number: 60, Review: &PullRequestReview{Reviewer: "bert", State: "commented"}})
	f.pr(PullRequestFacts{Number: 60, Review: &PullRequestReview{Reviewer: "ploeg-bot", State: "approved"}})
	card := f.card("ploeg-bot")
	if card.Steward == nil || *card.Steward != (CardSteward{Name: "anna", Source: "approver"}) {
		t.Errorf("steward = %+v", card.Steward)
	}
	if card.Plays[0].URL != "" {
		t.Errorf("url = %q; no Run or Checkpoint reported a link", card.Plays[0].URL)
	}
}

func TestOperatorCard_UnknownPlayStateIsStillInReview(t *testing.T) {
	f := newCardFixture(t, "1618")
	f.openShift("agent/vik-1618")
	f.pr(PullRequestFacts{Number: 61, Review: &PullRequestReview{Reviewer: "anna", State: "commented"}})
	card := f.card()
	if card.State != "in_review" || card.Plays[0].State != "" {
		t.Errorf("state %s, play %+v", card.State, card.Plays[0])
	}
}

func TestOperatorCard_WithdrawnWorkItem(t *testing.T) {
	f := newCardFixture(t, "1619")
	if _, err := testStore.WithdrawWorkItem(context.Background(), f.item, nil, "operator:workbench:ryan", CloseReasonWithdrawnByOperator); err != nil {
		t.Fatal(err)
	}
	card := f.card()
	if card.State != "withdrawn" || len(card.Events) != 1 || card.Events[0].Kind != "withdrawn" ||
		card.Events[0].Actor != "ryan" || card.Events[0].Detail["source"] != "operator" {
		t.Errorf("state %s, events %+v", card.State, card.Events)
	}
}

func TestOperatorCard_OutsideTheScopeIsNotFound(t *testing.T) {
	f := newCardFixture(t, "1620")
	if _, err := testStore.OperatorCard(context.Background(), f.item, []string{"gold"}, CardOptions{}); !errors.Is(err, ErrOperatorNotFound) {
		t.Errorf("other team: err = %v", err)
	}
	if _, err := testStore.OperatorCard(context.Background(), f.item+1000, nil, CardOptions{}); !errors.Is(err, ErrOperatorNotFound) {
		t.Errorf("missing item: err = %v", err)
	}
}

func eventKinds(events []CardEvent) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Kind)
	}
	return out
}

func TestOperatorCard_LiveAddsTheGatewayReadingOfRunningRuns(t *testing.T) {
	f := newCardFixture(t, "1617")
	f.openShift("agent/vik-1617")
	f.run(fixtureRun{role: "builder", writes: true, startMin: 0, durationMin: 10, outcome: "pr_opened",
		usage: `{"costUsd": 0.4, "inputTokens": 100, "outputTokens": 10}`})
	f.run(fixtureRun{role: "reviewer", round: 1, startMin: 20, authorizedUSD: 1})
	now := f.base.Add(50 * time.Minute)
	var read []string
	card, err := testStore.OperatorCard(context.Background(), f.item, []string{"silver"}, CardOptions{Now: now,
		Live: func(_ context.Context, runToken string) (LiveUsage, error) {
			read = append(read, runToken)
			return LiveUsage{CostUSD: 0.25, InputTokens: 900, OutputTokens: 40}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	live := card.Live
	if live == nil || live.RunningRuns != 1 || live.RunSeconds != 600+1800 || !live.ObservedAt.Equal(now) ||
		*live.CostUSD != 0.65 || *live.InputTokens != 1000 || *live.OutputTokens != 50 || !live.UsageComplete {
		t.Fatalf("live = %+v; want the finished Run's figures plus the gateway's", live)
	}
	if len(read) != 1 || read[0] != fmt.Sprintf("card-run-%d-2", f.item) {
		t.Errorf("gateway read for %v; want only the running Run", read)
	}
	if card.Totals.CostUSD == nil || *card.Totals.CostUSD != 0.4 || card.Totals.CostStatus != "reserved" || *card.Totals.RunSeconds != 600 {
		t.Errorf("totals = %+v; live figures never change the stored totals", card.Totals)
	}
}

func TestOperatorCard_LiveWithoutAGatewayReadingReportsOnlyRunTime(t *testing.T) {
	f := newCardFixture(t, "1618")
	f.openShift("agent/vik-1618")
	f.run(fixtureRun{role: "builder", writes: true, startMin: 0, authorizedUSD: 2})
	now := f.base.Add(31 * time.Minute)
	for name, live := range map[string]func(context.Context, string) (LiveUsage, error){
		"no reader":      nil,
		"failed reading": func(context.Context, string) (LiveUsage, error) { return LiveUsage{}, errors.New("gateway down") },
	} {
		card, err := testStore.OperatorCard(context.Background(), f.item, []string{"silver"}, CardOptions{Now: now, Live: live})
		if err != nil {
			t.Fatal(err)
		}
		l := card.Live
		if l == nil || l.RunningRuns != 1 || l.RunSeconds != 31*60 || l.CostUSD != nil || l.InputTokens != nil || l.UsageComplete {
			t.Errorf("%s: live = %+v; an unread gateway leaves cost and tokens unknown, never zero", name, l)
		}
	}
}

func TestOperatorCard_NoRunningRunHasNoLiveUsage(t *testing.T) {
	f := newCardFixture(t, "1619")
	f.openShift("agent/vik-1619")
	f.run(fixtureRun{role: "builder", writes: true, startMin: 0, durationMin: 10, outcome: "pr_opened", usage: `{"costUsd": 0.4}`})
	card, err := testStore.OperatorCard(context.Background(), f.item, []string{"silver"}, CardOptions{
		Live: func(context.Context, string) (LiveUsage, error) {
			t.Error("read the gateway without a running Run")
			return LiveUsage{}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	if card.Live != nil {
		t.Errorf("live = %+v; want none once every Run finished", card.Live)
	}
}
