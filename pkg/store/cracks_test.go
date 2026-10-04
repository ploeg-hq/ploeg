package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

type crackWorld struct {
	t   *testing.T
	ctx context.Context
	now time.Time
}

func newCrackWorld(t *testing.T) *crackWorld {
	t.Helper()
	resetTables(t)
	return &crackWorld{t: t, ctx: context.Background(), now: time.Now().UTC().Truncate(time.Second)}
}

func (w *crackWorld) item(externalID, team string) int64 {
	w.t.Helper()
	id, _, err := testStore.IngestAssigned(w.ctx, work.WorkItem{Provider: "vikunja", ExternalID: externalID, Team: team,
		Title: "Item " + externalID, Target: &work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "ploeg", BaseBranch: "development"}})
	if err != nil {
		w.t.Fatal(err)
	}
	return id
}

func (w *crackWorld) merged(item int64, number int, daysAgo int, by string, files ...string) {
	w.t.Helper()
	at := w.now.Add(-time.Duration(daysAgo) * 24 * time.Hour)
	if ok, err := testStore.RecordPullRequestFacts(w.ctx, PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg", Number: number,
		WorkItemID: item, State: "merged", MergedAt: &at, MergedBy: by, HeadSHA: fmt.Sprintf("%040d", number),
		MergeCommitSHA: strings.Repeat(fmt.Sprintf("%02d", number%100), 20)}); err != nil || !ok {
		w.t.Fatalf("record play %d: %v %v", number, ok, err)
	}
	if len(files) > 0 {
		if ok, err := testStore.RecordPullRequestChange(w.ctx, PullRequestChange{Forge: "forgejo", Repo: "webgrip/ploeg", Number: number,
			Files: files}); err != nil || !ok {
			w.t.Fatalf("record files of %d: %v %v", number, ok, err)
		}
	}
}

func (w *crackWorld) card(id int64, opts CardOptions) OperatorCard {
	w.t.Helper()
	card, err := testStore.OperatorCard(w.ctx, id, nil, opts)
	if err != nil {
		w.t.Fatal(err)
	}
	return card
}

func (w *crackWorld) propose(bug, card int64, by string, severity string) (Crack, error) {
	return testStore.ProposeCrack(w.ctx, CrackProposal{Bug: bug, Card: card, Severity: severity, Share: "primary",
		By: Actor{Person: by, Audit: "operator:console:" + by}, Teams: []string{"silver"}})
}

func (w *crackWorld) decision(id string, by string) CrackDecision {
	var crack int64
	fmt.Sscan(id, &crack)
	return CrackDecision{Crack: crack, By: Actor{Person: by, Audit: "operator:console:" + by}, Teams: []string{"silver"}}
}

func refusal(t *testing.T, err error, code string) {
	t.Helper()
	var r *AttributionError
	if !errors.As(err, &r) || r.Code != code {
		t.Fatalf("err = %v; want refusal %s", err, code)
	}
}

func auditActions(t *testing.T, item int64) []string {
	t.Helper()
	rows, err := testStore.pool.Query(context.Background(), `SELECT action FROM audit_log WHERE work_item_id = $1 AND action LIKE 'card.%' ORDER BY id`, item)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func TestCrackAttributionFlowNeedsTwoPeopleAndARefereeForDisputes(t *testing.T) {
	w := newCrackWorld(t)
	card := w.item("card", "silver")
	w.merged(card, 10, 20, "stewart", "a.go")
	bug := w.item("bug", "silver")

	crack, err := w.propose(bug, card, "fixer", "S2")
	if err != nil {
		t.Fatal(err)
	}
	if crack.State != "proposed" || *crack.Discovery != "discovered" || *crack.Steward != "stewart" || *crack.Play != 10 ||
		len(crack.ConfirmedBy) != 0 || crack.Bug.ExternalRef != "VIK-bug" || crack.Mended != nil {
		t.Fatalf("proposed crack = %+v", crack)
	}
	if c := w.card(card, CardOptions{}); c.Condition != nil {
		t.Fatalf("an unconfirmed proposal cracked the card: %+v", c.Condition)
	}

	for _, person := range []string{"Stewart", "fixer"} {
		_, err := testStore.ConfirmCrack(w.ctx, w.decision(crack.ID, person))
		refusal(t, err, AttributionForbidden)
	}
	confirm := w.decision(crack.ID, "second")
	confirm.Severity, confirm.Now = "S3", w.now
	crack, err = testStore.ConfirmCrack(w.ctx, confirm)
	if err != nil {
		t.Fatal(err)
	}
	if crack.State != "confirmed" || *crack.Severity != "S3" || !reflect.DeepEqual(crack.ConfirmedBy, []string{"fixer", "second"}) ||
		!crack.DisputeUntil.Equal(AddWorkdays(w.now, DisputeWorkdays)) {
		t.Fatalf("confirmed crack = %+v", crack)
	}
	_, err = testStore.ConfirmCrack(w.ctx, w.decision(crack.ID, "third"))
	refusal(t, err, AttributionState)

	c := w.card(card, CardOptions{})
	if c.Condition == nil || c.Condition.State != "cracked" || len(c.Condition.Cracks) != 1 ||
		c.Condition.Cracks[0].Weight != 1 || c.Condition.Cracks[0].Warranty != "full" || c.Condition.Cracks[0].Disputed {
		t.Fatalf("condition = %+v", c.Condition)
	}
	if c.Grade == nil || c.Grade.Subgrades.Reliability != 9 || *c.Grade.Inputs.Reliability.CrackWeight != 1 {
		t.Fatalf("grade = %+v", c.Grade)
	}

	dispute := w.decision(crack.ID, "second")
	dispute.Reason = "not mine"
	_, err = testStore.DisputeCrack(w.ctx, dispute)
	refusal(t, err, AttributionForbidden)
	late := w.decision(crack.ID, "stewart")
	late.Reason, late.Now = "too late", AddWorkdays(w.now, DisputeWorkdays).Add(time.Minute)
	_, err = testStore.DisputeCrack(w.ctx, late)
	refusal(t, err, AttributionDisputeClosed)
	dispute = w.decision(crack.ID, "stewart")
	dispute.Reason, dispute.Now = "the bug predates my change", w.now.Add(time.Hour)
	crack, err = testStore.DisputeCrack(w.ctx, dispute)
	if err != nil || crack.State != "disputed" || !crack.Disputed || *crack.DisputedBy != "stewart" {
		t.Fatalf("disputed crack = %+v, %v", crack, err)
	}
	if c := w.card(card, CardOptions{}); c.Condition == nil || !c.Condition.Cracks[0].Disputed {
		t.Fatalf("a disputed crack must still show on the card: %+v", c.Condition)
	}

	for _, person := range []string{"stewart", "fixer", "SECOND"} {
		d := w.decision(crack.ID, person)
		d.Resolution = "unlinked"
		_, err := testStore.ResolveCrack(w.ctx, d)
		refusal(t, err, AttributionForbidden)
	}
	unlisted := w.decision(crack.ID, "bystander")
	unlisted.Resolution, unlisted.Referees = "unlinked", map[string][]string{"silver": {"ref"}}
	_, err = testStore.ResolveCrack(w.ctx, unlisted)
	refusal(t, err, AttributionForbidden)
	resolve := w.decision(crack.ID, "ref")
	resolve.Resolution, resolve.Referees = "unlinked", map[string][]string{"silver": {"ref"}}
	crack, err = testStore.ResolveCrack(w.ctx, resolve)
	if err != nil || crack.State != "unlinked" || *crack.Resolution != "unlinked" || *crack.ResolvedBy != "ref" {
		t.Fatalf("resolved crack = %+v, %v", crack, err)
	}
	if c := w.card(card, CardOptions{}); c.Condition != nil || c.Grade.Subgrades.Reliability != 10 {
		t.Fatalf("an unlinked crack still shows: %+v %+v", c.Condition, c.Grade.Subgrades)
	}
	want := []string{"card.crack_proposed", "card.crack_confirmed", "card.crack_disputed", "card.crack_resolved"}
	if got := auditActions(t, card); !reflect.DeepEqual(got, want) {
		t.Fatalf("audit = %v; want %v", got, want)
	}
	listed, err := testStore.Cracks(w.ctx, bug, []string{"silver"})
	if err != nil || len(listed) != 1 || listed[0].State != "unlinked" {
		t.Fatalf("cracks of the bug = %+v, %v", listed, err)
	}
	if _, err := testStore.Cracks(w.ctx, bug, []string{"gold"}); !errors.Is(err, ErrOperatorNotFound) {
		t.Fatalf("another team read the cracks: %v", err)
	}
}

func TestCrackDisputeUpheldConfirmsAgain(t *testing.T) {
	w := newCrackWorld(t)
	card := w.item("card", "silver")
	w.merged(card, 10, 20, "stewart")
	bug := w.item("bug", "silver")
	crack, err := w.propose(bug, card, "fixer", "S1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ConfirmCrack(w.ctx, w.decision(crack.ID, "second")); err != nil {
		t.Fatal(err)
	}
	d := w.decision(crack.ID, "stewart")
	d.Reason = "no"
	if _, err := testStore.DisputeCrack(w.ctx, d); err != nil {
		t.Fatal(err)
	}
	r := w.decision(crack.ID, "anyone")
	r.Resolution = "upheld"
	crack, err = testStore.ResolveCrack(w.ctx, r)
	if err != nil || crack.State != "confirmed" || crack.Disputed || *crack.Resolution != "upheld" {
		t.Fatalf("upheld crack = %+v, %v", crack, err)
	}
	again := w.decision(crack.ID, "stewart")
	again.Reason = "again"
	_, err = testStore.DisputeCrack(w.ctx, again)
	refusal(t, err, AttributionState)
}

func TestCrackProposalRefusals(t *testing.T) {
	w := newCrackWorld(t)
	card := w.item("card", "silver")
	w.merged(card, 10, 20, "stewart")
	draft := w.item("draft", "silver")
	gold := w.item("gold-card", "gold")
	w.merged(gold, 30, 20, "goldie")
	bug := w.item("bug", "silver")
	later := w.item("later", "silver")
	w.merged(later, 11, 0, "stewart")
	if _, err := testStore.pool.Exec(w.ctx, `UPDATE pull_requests SET merged_at = now() + interval '1 hour' WHERE number = 11`); err != nil {
		t.Fatal(err)
	}

	_, err := w.propose(bug, card, "fixer", "S9")
	refusal(t, err, AttributionInvalid)
	_, err = w.propose(bug, bug, "fixer", "S2")
	refusal(t, err, AttributionInvalid)
	_, err = w.propose(bug, draft, "fixer", "S2")
	refusal(t, err, AttributionNotMerged)
	_, err = testStore.ProposeCrack(w.ctx, CrackProposal{Bug: bug, Card: later, Play: 11, Severity: "S2", Share: "primary",
		By: Actor{Person: "fixer", Audit: "a"}, Teams: []string{"silver"}})
	refusal(t, err, AttributionAfterBug)
	_, err = w.propose(bug, gold, "fixer", "S2")
	if !errors.Is(err, ErrOperatorNotFound) {
		t.Fatalf("another team's card was attributable: %v", err)
	}
	_, err = testStore.ProposeCrack(w.ctx, CrackProposal{Bug: bug, Card: card, Severity: "S2", Share: "primary", Discovery: "concealed",
		By: Actor{Person: "fixer", Audit: "a"}, Teams: []string{"silver"}})
	refusal(t, err, AttributionConcealUnknown)

	self, err := w.propose(bug, card, "stewart", "S2")
	if err != nil || *self.Discovery != "self" {
		t.Fatalf("the steward's own report = %+v, %v; want discovery self", self, err)
	}
	_, err = w.propose(bug, card, "fixer", "S2")
	refusal(t, err, AttributionDuplicate)

	for i := 0; i < 2; i++ {
		other := w.item(fmt.Sprintf("other-%d", i), "silver")
		w.merged(other, 40+i, 20, "someone")
		if _, err := w.propose(bug, other, "fixer", "S3"); err != nil {
			t.Fatal(err)
		}
	}
	fourth := w.item("fourth", "silver")
	w.merged(fourth, 50, 20, "someone")
	_, err = w.propose(bug, fourth, "fixer", "S3")
	refusal(t, err, AttributionLimit)
}

func TestRevertsMarkCardsAndFloorTheirCrackSeverity(t *testing.T) {
	w := newCrackWorld(t)
	card := w.item("card", "silver")
	w.merged(card, 10, 20, "stewart")
	other := w.item("other", "silver")
	w.merged(other, 12, 20, "stewart")

	marked, err := testStore.RecordRevert(w.ctx, Revert{Forge: "forgejo", Repo: "WebGrip/Ploeg", Number: 99, Numbers: []int{10, 99}})
	if err != nil || !reflect.DeepEqual(marked, []int64{card}) {
		t.Fatalf("marked = %v, %v; want the card of #10", marked, err)
	}
	again, err := testStore.RecordRevert(w.ctx, Revert{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 99, Numbers: []int{10}})
	if err != nil || len(again) != 0 {
		t.Fatalf("a repeated revert marked %v, %v", again, err)
	}
	bySHA, err := testStore.RecordRevert(w.ctx, Revert{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 100,
		SHAs: []string{strings.Repeat("12", 6)}})
	if err != nil || !reflect.DeepEqual(bySHA, []int64{other}) {
		t.Fatalf("revert by commit marked %v, %v", bySHA, err)
	}
	elsewhere, err := testStore.RecordRevert(w.ctx, Revert{Forge: "forgejo", Repo: "webgrip/site", Number: 5, Numbers: []int{10}})
	if err != nil || len(elsewhere) != 0 {
		t.Fatalf("a revert in another repository marked %v, %v", elsewhere, err)
	}

	c := w.card(card, CardOptions{})
	if c.Grade == nil || !*c.Grade.Inputs.Reliability.Reverted || *c.Grade.Inputs.Durability.Reverts != 1 ||
		!reflect.DeepEqual(c.Grade.Qualifiers, []string{"RV"}) || c.Grade.Subgrades.Reliability != 8 {
		t.Fatalf("reverted card grade = %+v", c.Grade)
	}
	if got := auditActions(t, card); !reflect.DeepEqual(got, []string{"card.reverted"}) {
		t.Fatalf("audit = %v", got)
	}
	bug := w.item("bug", "silver")
	crack, err := w.propose(bug, card, "fixer", "S4")
	if err != nil || *crack.Severity != "S2" {
		t.Fatalf("a reverted card's crack = %+v, %v; want at least S2", crack, err)
	}
}

func TestMendsAreRecordedConfirmedAndReopened(t *testing.T) {
	w := newCrackWorld(t)
	card := w.item("card", "silver")
	w.merged(card, 10, 20, "stewart")
	bug := w.item("bug", "silver")
	w.merged(bug, 20, 0, "stewart")
	if ok, err := testStore.RecordPullRequestChange(w.ctx, PullRequestChange{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 20,
		Labels: []string{"HotFix"}}); err != nil || !ok {
		t.Fatal(ok, err)
	}

	crack, err := w.propose(bug, card, "fixer", "S2")
	if err != nil {
		t.Fatal(err)
	}
	if crack.Mended == nil || crack.Mended.PR != 20 || !crack.Mended.BySteward || crack.Mended.By != "stewart" {
		t.Fatalf("mend recorded at proposal = %+v", crack.Mended)
	}
	if _, err := testStore.ConfirmCrack(w.ctx, w.decision(crack.ID, "second")); err != nil {
		t.Fatal(err)
	}
	c := w.card(card, CardOptions{})
	if c.Condition.State != "cracked" || c.Condition.Cracks[0].Weight != 2 || *c.Grade.Inputs.Durability.Hotfixes != 1 ||
		!reflect.DeepEqual(c.Grade.Qualifiers, []string{"HF"}) {
		t.Fatalf("before the mend is confirmed: %+v %+v", c.Condition, c.Grade)
	}
	if c := w.card(card, CardOptions{HotfixLabels: map[string][]string{"silver": {"urgent"}}}); *c.Grade.Inputs.Durability.Hotfixes != 0 {
		t.Fatalf("a team that names other hotfix labels counted %d hotfixes", *c.Grade.Inputs.Durability.Hotfixes)
	}

	confirmed, reopened, err := testStore.ConfirmMends(w.ctx, w.now.Add(29*24*time.Hour))
	if err != nil || confirmed != 0 || reopened != 0 {
		t.Fatalf("inside the window: %d %d %v", confirmed, reopened, err)
	}
	after := w.now.Add(MendWindow + time.Hour)
	confirmed, _, err = testStore.ConfirmMends(w.ctx, after)
	if err != nil || confirmed != 0 {
		t.Fatalf("a mend whose bug is still open was confirmed: %d %v", confirmed, err)
	}
	if _, err := testStore.pool.Exec(w.ctx, `UPDATE work_items SET state = 'done' WHERE id = $1`, bug); err != nil {
		t.Fatal(err)
	}
	confirmed, _, err = testStore.ConfirmMends(w.ctx, after)
	if err != nil || confirmed != 1 {
		t.Fatalf("confirmed = %d, %v", confirmed, err)
	}
	c = w.card(card, CardOptions{})
	if c.Condition.State != "mended" || c.Condition.Cracks[0].Weight != 1 || c.Condition.Cracks[0].Mended.ConfirmedAt == nil ||
		c.Grade.Subgrades.Reliability != 9 {
		t.Fatalf("after the mend is confirmed: %+v %+v", c.Condition, c.Grade.Subgrades)
	}
	want := []string{"card.crack_proposed", "card.crack_mended", "card.crack_confirmed", "card.mend_confirmed"}
	if got := auditActions(t, card); !reflect.DeepEqual(got, want) {
		t.Fatalf("audit = %v; want %v", got, want)
	}
}

func TestAMendByAnotherPersonCoSignsAndAReCrackReopensIt(t *testing.T) {
	w := newCrackWorld(t)
	card := w.item("card", "silver")
	w.merged(card, 10, 20, "stewart")
	bug := w.item("bug", "silver")
	w.merged(bug, 20, 0, "helper")
	crack, err := w.propose(bug, card, "fixer", "S2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ConfirmCrack(w.ctx, w.decision(crack.ID, "second")); err != nil {
		t.Fatal(err)
	}
	c := w.card(card, CardOptions{})
	roles := map[string][]string{}
	for _, p := range c.Roster {
		roles[p.Name] = p.Roles
	}
	if !reflect.DeepEqual(roles["helper"], []string{"cosigner"}) || c.Condition.Cracks[0].Mended.BySteward {
		t.Fatalf("roster = %+v, mend = %+v", c.Roster, c.Condition.Cracks[0].Mended)
	}

	second := w.item("bug-2", "silver")
	recrack, err := w.propose(second, card, "fixer", "S3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ConfirmCrack(w.ctx, w.decision(recrack.ID, "second")); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.pool.Exec(w.ctx, `UPDATE work_items SET state = 'done' WHERE id = $1`, bug); err != nil {
		t.Fatal(err)
	}
	confirmed, reopened, err := testStore.ConfirmMends(w.ctx, w.now.Add(MendWindow+time.Hour))
	if err != nil || confirmed != 0 || reopened != 1 {
		t.Fatalf("confirmed %d reopened %d, %v; want the mend reopened by the new crack", confirmed, reopened, err)
	}
	if c := w.card(card, CardOptions{}); c.Condition.State != "cracked" {
		t.Fatalf("condition = %+v", c.Condition)
	}
}

func TestMarkEvolvedGivesTheCardEvolvedAndNoCrack(t *testing.T) {
	w := newCrackWorld(t)
	card := w.item("card", "silver")
	w.merged(card, 10, 20, "stewart")
	bug := w.item("bug", "silver")
	_, err := testStore.MarkEvolved(w.ctx, EvolvedMark{Bug: bug, Card: card, By: Actor{Person: "Stewart", Audit: "a"}, Teams: []string{"silver"}})
	refusal(t, err, AttributionForbidden)
	crack, err := w.propose(bug, card, "fixer", "S2")
	if err != nil {
		t.Fatal(err)
	}
	evolved, err := testStore.MarkEvolved(w.ctx, EvolvedMark{Bug: bug, Card: card, Note: "the requirement changed",
		By: Actor{Person: "fixer", Audit: "a"}, Teams: []string{"silver"}})
	if err != nil || evolved.ID != crack.ID || evolved.State != "evolved" || *evolved.EvolvedBy != "fixer" {
		t.Fatalf("evolved = %+v, %v", evolved, err)
	}
	c := w.card(card, CardOptions{})
	if !c.Evolved || c.Condition != nil {
		t.Fatalf("card evolved %v condition %+v", c.Evolved, c.Condition)
	}
	_, err = testStore.ConfirmCrack(w.ctx, w.decision(crack.ID, "second"))
	refusal(t, err, AttributionState)

	fresh := w.item("bug-2", "silver")
	direct, err := testStore.MarkEvolved(w.ctx, EvolvedMark{Bug: fresh, Card: card, By: Actor{Person: "fixer", Audit: "a"}, Teams: []string{"silver"}})
	if err != nil || direct.State != "evolved" || direct.Severity != nil {
		t.Fatalf("direct evolved = %+v, %v", direct, err)
	}
}

func TestCrackCandidatesRankEarlierPlaysByTheFilesTheyShareWithTheFix(t *testing.T) {
	w := newCrackWorld(t)
	near := w.item("near", "silver")
	w.merged(near, 10, 10, "stewart", "a.go", "b.go", "c.go")
	far := w.item("far", "silver")
	w.merged(far, 11, 30, "stewart", "a.go")
	unrelated := w.item("unrelated", "silver")
	w.merged(unrelated, 12, 5, "stewart", "z.md")
	old := w.item("old", "silver")
	w.merged(old, 13, 400, "stewart", "a.go", "b.go")
	gold := w.item("gold", "gold")
	w.merged(gold, 14, 5, "goldie", "a.go", "b.go")
	bug := w.item("bug", "silver")
	w.merged(bug, 20, 0, "stewart", "a.go", "b.go", "new.go")

	got, err := testStore.CrackCandidates(w.ctx, bug, []string{"silver"})
	if err != nil {
		t.Fatal(err)
	}
	if got.FixFiles != 3 || len(got.Candidates) != 2 {
		t.Fatalf("candidates = %+v", got)
	}
	first, second := got.Candidates[0], got.Candidates[1]
	if first.Play != 10 || first.SharedFiles != 2 || !reflect.DeepEqual(first.Files, []string{"a.go", "b.go"}) ||
		first.Share != 2.0/3 || first.Attribution != nil || first.Repo != "webgrip/ploeg" || first.MergedBy != "stewart" {
		t.Fatalf("first candidate = %+v", first)
	}
	if second.Play != 11 || second.SharedFiles != 1 {
		t.Fatalf("second candidate = %+v", second)
	}
	if _, err := w.propose(bug, near, "fixer", "S3"); err != nil {
		t.Fatal(err)
	}
	if got, _ := testStore.CrackCandidates(w.ctx, bug, nil); got.Candidates[0].Attribution == nil || *got.Candidates[0].Attribution != "proposed" {
		t.Fatalf("an attributed candidate shows %+v", got.Candidates[0].Attribution)
	}
	if _, err := testStore.CrackCandidates(w.ctx, bug, []string{"gold"}); !errors.Is(err, ErrOperatorNotFound) {
		t.Fatalf("another team read the candidates: %v", err)
	}
	none := w.item("no-fix", "silver")
	if got, err := testStore.CrackCandidates(w.ctx, none, nil); err != nil || got.FixFiles != 0 || len(got.Candidates) != 0 {
		t.Fatalf("a bug without a fix = %+v, %v", got, err)
	}
}

func TestRecordPullRequestChangeKeepsABoundedFileList(t *testing.T) {
	w := newCrackWorld(t)
	item := w.item("card", "silver")
	w.merged(item, 10, 1, "stewart")
	files := make([]string, 0, MaxStoredFiles+1)
	for i := 0; i <= MaxStoredFiles; i++ {
		files = append(files, fmt.Sprintf("f%03d.go", i))
	}
	ok, err := testStore.RecordPullRequestChange(w.ctx, PullRequestChange{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 10,
		Files: append(files, "f000.go", ""), Labels: []string{"hotfix"}})
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	var stored int
	var truncated bool
	var labels []string
	if err := testStore.pool.QueryRow(w.ctx, `SELECT (SELECT count(*) FROM pull_request_files f WHERE f.pull_request_id = p.id),
		p.files_truncated, p.labels FROM pull_requests p WHERE p.number = 10`).Scan(&stored, &truncated, &labels); err != nil {
		t.Fatal(err)
	}
	if stored != MaxStoredFiles || !truncated || !reflect.DeepEqual(labels, []string{"hotfix"}) {
		t.Fatalf("stored %d files, truncated %v, labels %v", stored, truncated, labels)
	}
	ok, err = testStore.RecordPullRequestChange(w.ctx, PullRequestChange{Forge: "forgejo", Repo: "webgrip/ploeg", Number: 77, Files: []string{"a"}})
	if err != nil || ok {
		t.Fatalf("a pull request Ploeg never recorded stored files: %v %v", ok, err)
	}
	if has, err := testStore.HasMergedPlays(w.ctx, "forgejo", "WebGrip/ploeg"); err != nil || !has {
		t.Fatalf("HasMergedPlays = %v, %v", has, err)
	}
	if has, _ := testStore.HasMergedPlays(w.ctx, "forgejo", "webgrip/site"); has {
		t.Fatal("a repository without plays has merged plays")
	}
}
