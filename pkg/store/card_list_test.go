package store

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/gate"
)

func (w *crackWorld) activeAt(item int64, at time.Time) {
	w.t.Helper()
	if _, err := testStore.pool.Exec(w.ctx, `UPDATE pull_requests SET first_seen_at = $2::timestamptz,
		merged_at = CASE WHEN merged_at IS NULL THEN NULL ELSE $2::timestamptz END WHERE work_item_id = $1`, item, at); err != nil {
		w.t.Fatal(err)
	}
	if _, err := testStore.pool.Exec(w.ctx, `UPDATE work_items SET created_at = $2 WHERE id = $1`, item, at); err != nil {
		w.t.Fatal(err)
	}
}

func (w *crackWorld) review(item int64, number int, reviewer, state string) {
	w.t.Helper()
	if ok, err := testStore.RecordPullRequestFacts(w.ctx, PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg", Number: number,
		WorkItemID: item, State: "open", Review: &PullRequestReview{Reviewer: reviewer, State: state}}); err != nil || !ok {
		w.t.Fatalf("review on %d: %v %v", number, ok, err)
	}
}

func (w *crackWorld) gateMove(externalID string, g gate.Gate, actor string, minutes int) {
	w.t.Helper()
	if _, err := testStore.RecordGateMove(w.ctx, GateMove{Provider: "vikunja", ExternalID: externalID, Gate: g, Status: string(g),
		Actor: actor, At: w.now.Add(time.Duration(minutes) * time.Minute)}); err != nil {
		w.t.Fatal(err)
	}
}

func (w *crackWorld) cards(f CardListFilter) CardPage {
	w.t.Helper()
	page, err := testStore.OperatorCards(w.ctx, f, CardOptions{Bots: []string{"ploeg-bot"}})
	if err != nil {
		w.t.Fatal(err)
	}
	return page
}

func cardIDs(cards []OperatorCard) []string {
	out := []string{}
	for _, c := range cards {
		out = append(out, c.WorkItemID)
	}
	return out
}

func ids(items ...int64) []string {
	out := []string{}
	for _, id := range items {
		out = append(out, strconv.FormatInt(id, 10))
	}
	return out
}

func TestOperatorCardsListsEveryRosterRoleAndTheSteward(t *testing.T) {
	w := newCrackWorld(t)
	merger := w.item("merger", "silver")
	w.merged(merger, 1, 9, "Mira")
	reviewer := w.item("reviewer", "silver")
	w.review(reviewer, 2, "rex", "changes_requested")
	approver := w.item("approver", "silver")
	w.review(approver, 3, "abe", "approved")
	gates := w.item("gates", "silver")
	w.gateMove("gates", gate.Development, "dev", 0)
	w.gateMove("gates", gate.Test, "dev", 10)
	w.gateMove("gates", gate.Acceptance, "quinn", 20)
	w.gateMove("gates", gate.Done, "ada", 30)
	cosigned := w.item("cosigned", "silver")
	w.merged(cosigned, 4, 20, "stewart")
	bug := w.item("bug", "silver")
	w.merged(bug, 5, 0, "helper")
	crack, err := w.propose(bug, cosigned, "fixer", "S2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ConfirmCrack(w.ctx, w.decision(crack.ID, "second")); err != nil {
		t.Fatal(err)
	}
	botOnly := w.item("bot", "silver")
	w.merged(botOnly, 6, 1, "ploeg-bot")

	for _, tc := range []struct {
		member string
		want   []string
	}{
		{"mira", ids(merger)},
		{"MIRA", ids(merger)},
		{"rex", ids(reviewer)},
		{"abe", ids(approver)},
		{"quinn", ids(gates)},
		{"ada", ids(gates)},
		{"dev", []string{}},
		{"ploeg-bot", []string{}},
		{"nobody", []string{}},
	} {
		page := w.cards(CardListFilter{Members: []string{tc.member}})
		if got := cardIDs(page.Cards); !reflect.DeepEqual(got, tc.want) || page.NextBefore != nil {
			t.Errorf("member %s: cards %v next %v; want %v", tc.member, got, page.NextBefore, tc.want)
		}
	}

	page := w.cards(CardListFilter{Members: []string{"Helper"}})
	got := map[string]bool{}
	for _, c := range page.Cards {
		got[c.WorkItemID] = true
	}
	if len(page.Cards) != 2 || !got[ids(cosigned)[0]] || !got[ids(bug)[0]] {
		t.Fatalf("helper's cards = %v; want the cosigned card and the bug they merged", cardIDs(page.Cards))
	}
	for _, c := range page.Cards {
		if c.WorkItemID == ids(cosigned)[0] && (c.Condition == nil || len(c.Roster) == 0) {
			t.Errorf("listed card is not fully assembled: %+v", c)
		}
	}

	both := w.cards(CardListFilter{Members: []string{"rex", "abe"}})
	if len(both.Cards) != 2 {
		t.Errorf("rex or abe = %v; want both cards", cardIDs(both.Cards))
	}
}

func TestOperatorCardsAreTeamScopedNewestFirstAndFilteredBySince(t *testing.T) {
	w := newCrackWorld(t)
	old := w.item("old", "silver")
	w.merged(old, 1, 0, "mira")
	recent := w.item("recent", "silver")
	w.merged(recent, 2, 0, "mira")
	hidden := w.item("hidden", "gold")
	w.merged(hidden, 3, 0, "mira")
	w.activeAt(old, w.now.Add(-48*time.Hour))
	w.activeAt(recent, w.now.Add(-time.Hour))
	w.activeAt(hidden, w.now)

	if got := cardIDs(w.cards(CardListFilter{Members: []string{"mira"}}).Cards); !reflect.DeepEqual(got, ids(hidden, recent, old)) {
		t.Errorf("unscoped = %v", got)
	}
	if got := cardIDs(w.cards(CardListFilter{Teams: []string{"silver"}, Members: []string{"mira"}}).Cards); !reflect.DeepEqual(got, ids(recent, old)) {
		t.Errorf("silver consumer = %v; another team's card must not show", got)
	}
	if got := cardIDs(w.cards(CardListFilter{Team: "gold", Members: []string{"mira"}}).Cards); !reflect.DeepEqual(got, ids(hidden)) {
		t.Errorf("team gold = %v", got)
	}
	if got := cardIDs(w.cards(CardListFilter{Teams: []string{"silver"}, Team: "gold", Members: []string{"mira"}}).Cards); len(got) != 0 {
		t.Errorf("team outside the scope = %v", got)
	}
	since := w.now.Add(-2 * time.Hour)
	if got := cardIDs(w.cards(CardListFilter{Teams: []string{"silver"}, Members: []string{"mira"}, Since: &since}).Cards); !reflect.DeepEqual(got, ids(recent)) {
		t.Errorf("since = %v", got)
	}
	exact := w.now.Add(-time.Hour)
	if got := cardIDs(w.cards(CardListFilter{Teams: []string{"silver"}, Members: []string{"mira"}, Since: &exact}).Cards); !reflect.DeepEqual(got, ids(recent)) {
		t.Errorf("since at the activity itself = %v; since is inclusive", got)
	}
}

func TestOperatorCardsActivityCountsReleaseAndCracks(t *testing.T) {
	w := newCrackWorld(t)
	released := w.item("released", "silver")
	w.merged(released, 1, 0, "mira")
	cracked := w.item("cracked", "silver")
	w.merged(cracked, 2, 0, "mira")
	w.activeAt(released, w.now.Add(-72*time.Hour))
	w.activeAt(cracked, w.now.Add(-48*time.Hour))
	if got := cardIDs(w.cards(CardListFilter{Members: []string{"mira"}}).Cards); !reflect.DeepEqual(got, ids(cracked, released)) {
		t.Fatalf("by merge = %v", got)
	}

	deploy, err := testStore.RecordDeployment(w.ctx, Deployment{Forge: "forgejo", Owner: "webgrip", Name: "ploeg", Environment: "production",
		SHA: strings.Repeat("01", 20), DeployedAt: w.now.Add(-24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	var play int64
	if err := testStore.pool.QueryRow(w.ctx, `SELECT id FROM pull_requests WHERE work_item_id = $1`, released).Scan(&play); err != nil {
		t.Fatal(err)
	}
	if ok, err := testStore.MarkDeployed(w.ctx, play, deploy.ID); err != nil || !ok {
		t.Fatal(ok, err)
	}
	staging, err := testStore.RecordDeployment(w.ctx, Deployment{Forge: "forgejo", Owner: "webgrip", Name: "ploeg", Environment: "staging",
		SHA: strings.Repeat("02", 20), DeployedAt: w.now})
	if err != nil {
		t.Fatal(err)
	}
	if err := testStore.pool.QueryRow(w.ctx, `SELECT id FROM pull_requests WHERE work_item_id = $1`, cracked).Scan(&play); err != nil {
		t.Fatal(err)
	}
	if ok, err := testStore.MarkDeployed(w.ctx, play, staging.ID); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if got := cardIDs(w.cards(CardListFilter{Members: []string{"mira"}}).Cards); !reflect.DeepEqual(got, ids(released, cracked)) {
		t.Fatalf("after the release = %v; a release is activity", got)
	}

	bug := w.item("bug", "silver")
	w.merged(bug, 3, 0, "other")
	crack, err := w.propose(bug, cracked, "fixer", "S3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ConfirmCrack(w.ctx, w.decision(crack.ID, "second")); err != nil {
		t.Fatal(err)
	}
	if got := cardIDs(w.cards(CardListFilter{Members: []string{"mira"}}).Cards); !reflect.DeepEqual(got, ids(cracked, released)) {
		t.Fatalf("after the crack = %v; a confirmed crack is activity", got)
	}
}

func TestOperatorCardsPageWithAStableCursor(t *testing.T) {
	w := newCrackWorld(t)
	var all []int64
	same := w.now.Add(-time.Hour)
	for i := 1; i <= 5; i++ {
		item := w.item("page-"+strconv.Itoa(i), "silver")
		w.merged(item, i, 0, "mira")
		at := w.now.Add(-time.Duration(10-i) * time.Hour)
		if i >= 4 {
			at = same
		}
		w.activeAt(item, at)
		all = append([]int64{item}, all...)
	}
	if all[0] < all[1] {
		t.Fatal("fixture: items 4 and 5 tie on activity and must order by id descending")
	}

	var got []string
	var before *CardCursor
	pages := 0
	for {
		page := w.cards(CardListFilter{Members: []string{"mira"}, Limit: 2, Before: before})
		pages++
		got = append(got, cardIDs(page.Cards)...)
		if page.NextBefore == nil {
			break
		}
		parsed, err := ParseCardCursor(page.NextBefore.String())
		if err != nil || parsed != *page.NextBefore {
			t.Fatalf("cursor %q round trip = %+v, %v", page.NextBefore.String(), parsed, err)
		}
		before = &parsed
		if pages == 1 {
			w.activeAt(all[1], w.now)
		}
		if pages > 5 {
			t.Fatal("paging does not end")
		}
	}
	if !reflect.DeepEqual(got, ids(all...)) || pages != 3 {
		t.Errorf("paged %v over %d pages; want %v over 3, a card active again after its page not repeated", got, pages, ids(all...))
	}
	whole := cardIDs(w.cards(CardListFilter{Members: []string{"mira"}, Limit: 50}).Cards)
	if len(whole) != 5 || whole[0] != ids(all[1])[0] {
		t.Errorf("a fresh first page = %v; the card active again leads it", whole)
	}
}

func TestOperatorCardsCapsThePageAndRejectsForeignCursors(t *testing.T) {
	w := newCrackWorld(t)
	for i := 1; i <= CardListLimit+2; i++ {
		item := w.item("cap-"+strconv.Itoa(i), "silver")
		w.merged(item, i, 0, "mira")
	}
	page := w.cards(CardListFilter{Members: []string{"mira"}, Limit: 500})
	if len(page.Cards) != CardListLimit || page.NextBefore == nil {
		t.Fatalf("limit 500 gave %d cards, next %v; want %d and a cursor", len(page.Cards), page.NextBefore, CardListLimit)
	}
	if rest := w.cards(CardListFilter{Members: []string{"mira"}, Limit: 50, Before: page.NextBefore}); len(rest.Cards) != 2 || rest.NextBefore != nil {
		t.Fatalf("second page = %d cards, next %v", len(rest.Cards), rest.NextBefore)
	}
	if page := w.cards(CardListFilter{Members: []string{"mira"}}); len(page.Cards) != DefaultCardListLimit {
		t.Errorf("default page = %d cards", len(page.Cards))
	}
	for _, bad := range []string{"", "42", "c1.", "c1.!!", "c1." + "MTIz", "c1.MTIzLjA", "c1.YS4x"} {
		if _, err := ParseCardCursor(bad); err == nil {
			t.Errorf("cursor %q accepted", bad)
		}
	}
}

func TestOperatorCardsFindCandidatesThroughTheLoginIndexes(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	conn, err := testStore.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SET enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(ctx, `RESET enable_seqscan`) //nolint:errcheck
	rows, err := conn.Query(ctx, "EXPLAIN "+cardCandidateQuery, []string{"mira"}, nil, "", []string{}, []string{}, "production",
		nil, nil, int64(0), 40)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line + "\n")
	}
	for _, index := range []string{"pull_requests_by_merger", "pull_request_reviews_by_reviewer", "gate_transitions_by_actor",
		"card_cracks_by_mender", "agent_runs_by_work_item"} {
		if !strings.Contains(plan.String(), index) {
			t.Errorf("candidate query does not use %s:\n%s", index, plan.String())
		}
	}
}
