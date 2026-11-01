package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/clickup"
	"github.com/ploeg-hq/ploeg/pkg/spendcap"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// gateEngine answers the fit gate's writer question and opens no Shift.
type gateEngine struct {
	ShiftEngine
	writes map[string]bool
}

func (gateEngine) EnsureShift(context.Context, int64, work.WorkItem) error { return nil }

func (e gateEngine) TeamWrites(team string) (bool, bool) {
	writes, planned := e.writes[team]
	return writes, planned
}

// gateServer is delegateServer with both Teams writing and List 10 routed
// to webgrip/app@main, so the first activity names a repository.
func fitGateServer(t *testing.T, board *delegateBoard) *Server {
	t.Helper()
	s := delegateServer(board)
	s.Engine = gateEngine{writes: map[string]bool{"bronze": true, "silver": true}}
	targets, err := target.NewMapResolver("10=webgrip/app@main", "forgejo")
	if err != nil {
		t.Fatal(err)
	}
	s.Targets = targets
	return s
}

func gateRefusal(t *testing.T, taskID string) (code, reason, team string) {
	t.Helper()
	if _, _, found := clickupItem(t, taskID); found {
		t.Fatalf("%s was queued; the gate should have refused it", taskID)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT detail->>'code', detail->>'reason', detail->>'team' FROM audit_log
		WHERE action='work_item.route_refused' AND detail->>'external_id'=$1`, taskID).Scan(&code, &reason, &team); err != nil {
		t.Fatalf("refusal audit row for %s: %v", taskID, err)
	}
	return code, reason, team
}

func operatorRefusalCodes(t *testing.T, teams []string) []string {
	t.Helper()
	refusals, err := testStore.RecentRouteRefusals(context.Background(), teams, time.Now().Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	codes := make([]string, 0, len(refusals))
	for _, r := range refusals {
		codes = append(codes, r.Code)
	}
	return codes
}

func TestAStartingCardGetsOneThoughtAndNeitherARedeliveryNorARefreshRepeatsIt(t *testing.T) {
	reset(t)
	board := newDelegateBoard(t)
	board.set("cu-start", "")
	h := fitGateServer(t, board).Handler()

	clickupAssigneeHook(t, h, "cu-start", "assignee_add", "agent-member")
	if state, team, _ := clickupItem(t, "cu-start"); state != "queued" || team != "bronze" {
		t.Fatalf("item = %q/%q, want queued for bronze", state, team)
	}
	want := "Team bronze takes this card.\nRepository webgrip/app, base branch main.\nThe pull request link lands here when the work is ready for review."
	if got := board.commentsOn("cu-start"); len(got) != 1 || got[0] != want {
		t.Fatalf("comments = %q, want exactly %q", got, want)
	}

	clickupAssigneeHook(t, h, "cu-start", "assignee_add", "agent-member")
	again := `{"event":"taskAssigneeUpdated","task_id":"cu-start","history_items":[{"id":"cu-start-again","field":"assignee_add","date":"1759310500000","after":{"username":"agent-member"}}]}`
	w := httptest.NewRecorder()
	h.ServeHTTP(w, signedTrackerHook("clickup", again))
	if w.Code != http.StatusAccepted {
		t.Fatalf("second assignment: HTTP %d", w.Code)
	}
	if got := board.commentsOn("cu-start"); len(got) != 1 {
		t.Fatalf("comments after a redelivery and a refresh = %q, want the one thought", got)
	}
}

func TestATeamWithoutAWritingRoleIsRefusedAtIntakeAndTheCardNamesTheTeamsThatFit(t *testing.T) {
	reset(t)
	board := newDelegateBoard(t)
	board.set("cu-readers", "Silver")
	s := fitGateServer(t, board)
	s.Engine = gateEngine{writes: map[string]bool{"bronze": true, "silver": false}}
	clickupAssigneeHook(t, s.Handler(), "cu-readers", "assignee_add", "agent-member")

	code, reason, team := gateRefusal(t, "cu-readers")
	if code != target.RefusalTeamCannotWrite || reason != "Team silver has no Role that writes code" || team != "silver" {
		t.Fatalf("refusal = %q %q under %q", code, reason, team)
	}
	want := "Not starting · Team silver has no Role that writes code.\nTeams that can take it: bronze."
	if got := board.commentsOn("cu-readers"); len(got) != 1 || got[0] != want {
		t.Fatalf("comments = %q, want %q", got, want)
	}
	if codes := operatorRefusalCodes(t, []string{"silver"}); len(codes) != 1 || codes[0] != target.RefusalTeamCannotWrite {
		t.Fatalf("operator refusals for silver = %v", codes)
	}
}

func settleSpend(t *testing.T, taskID string, usd float64) {
	t.Helper()
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "clickup", ExternalID: taskID, Team: "bronze", Title: "spent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO audit_log (actor, action, work_item_id, detail) VALUES ('test', 'llm.reconciled', $1, $2)`,
		id, fmt.Sprintf(`{"delta": %v}`, usd)); err != nil {
		t.Fatal(err)
	}
}

func TestASpentCapRefusesTheCardAndHeadroomIsOnTheStartingThought(t *testing.T) {
	reset(t)
	testStore.SetTeamSpendCaps(spendcap.Caps{"bronze": {USD: 5, Period: spendcap.Week}})
	t.Cleanup(func() { testStore.SetTeamSpendCaps(nil) })
	board := newDelegateBoard(t)
	board.set("cu-budget", "")
	board.set("cu-capped", "")
	h := fitGateServer(t, board).Handler()

	settleSpend(t, "cu-spent-1", 2)
	clickupAssigneeHook(t, h, "cu-budget", "assignee_add", "agent-member")
	if got := board.commentsOn("cu-budget"); len(got) != 1 || !strings.Contains(got[0], "\nBudget left this week: US$ 3.00 of US$ 5.00.\n") {
		t.Fatalf("comments = %q, want the headroom line", got)
	}

	settleSpend(t, "cu-spent-2", 3)
	clickupAssigneeHook(t, h, "cu-capped", "assignee_add", "agent-member")
	code, reason, team := gateRefusal(t, "cu-capped")
	from := spendcap.Week.End(time.Now()).Format("Monday 2 January 2006")
	wantReason := "Team bronze has spent US$ 5.00 of its US$ 5.00 this week; it can take this card again from " + from
	if code != target.RefusalBudgetExhausted || reason != wantReason || team != "bronze" {
		t.Fatalf("refusal = %q %q under %q, want %q", code, reason, team, wantReason)
	}
	want := "Not starting · " + wantReason + ".\nTeams that can take it: silver."
	if got := board.commentsOn("cu-capped"); len(got) != 1 || got[0] != want {
		t.Fatalf("comments = %q, want %q", got, want)
	}
	if codes := operatorRefusalCodes(t, []string{"bronze"}); len(codes) != 1 || codes[0] != target.RefusalBudgetExhausted {
		t.Fatalf("operator refusals for bronze = %v", codes)
	}
}

func TestARoleOnOpenHandsIsRefusedARepositoryThatShipsItsHooksFile(t *testing.T) {
	reset(t)
	board := newDelegateBoard(t)
	board.set("cu-hooks", "")
	board.set("cu-hooks-silver", "Silver")
	s := fitGateServer(t, board)
	targets, err := target.New(target.Table{Targets: map[string]work.Target{"app": appTarget}, Rules: []target.Rule{{Scope: "10", Default: "app"}}}, "forgejo")
	if err != nil {
		t.Fatal(err)
	}
	s.Targets = targets
	s.Readiness = target.NewGate(targets.Registered(), map[string]provider.RepositoryInspector{"forgejo": &fakeForge{states: map[string]provider.RepositoryState{
		"webgrip/app": {AgentsFile: true, HooksFile: true},
	}}})
	profile := "openhands"
	s.OperatorConfig.TeamHarnesses = TeamHarnesses{
		"bronze": {"builder": {OperatorRoleHarness: store.OperatorRoleHarness{Name: "acp", Profile: &profile}}},
		"silver": {"builder": {OperatorRoleHarness: store.OperatorRoleHarness{Name: "opencode"}}},
	}
	h := s.Handler()

	clickupAssigneeHook(t, h, "cu-hooks", "assignee_add", "agent-member")
	code, reason, team := gateRefusal(t, "cu-hooks")
	wantReason := "Team bronze runs OpenHands for its builder Role, and OpenHands refuses a repository that ships .openhands/hooks.json, which webgrip/app does"
	if code != target.RefusalHarnessRefusesRepository || reason != wantReason || team != "bronze" {
		t.Fatalf("refusal = %q %q under %q, want %q", code, reason, team, wantReason)
	}
	if got := board.commentsOn("cu-hooks"); len(got) != 1 || got[0] != "Not starting · "+wantReason+".\nTeams that can take it: silver." {
		t.Fatalf("comments = %q", got)
	}

	clickupAssigneeHook(t, h, "cu-hooks-silver", "assignee_add", "agent-member")
	if state, team, _ := clickupItem(t, "cu-hooks-silver"); state != "queued" || team != "silver" {
		t.Fatalf("silver's card = %q/%q, want queued: its harness enters the repository", state, team)
	}
}

func TestOpenHandsIsRecognisedAsItsOwnAdapterAndAsAnACPProfile(t *testing.T) {
	openhands, opencode := "openhands", "opencode"
	for name, c := range map[string]struct {
		h    RoleHarnesses
		want bool
	}{
		"openhands adapter":  {RoleHarnesses{OperatorRoleHarness: store.OperatorRoleHarness{Name: "openhands"}}, true},
		"acp openhands":      {RoleHarnesses{OperatorRoleHarness: store.OperatorRoleHarness{Name: "acp", Profile: &openhands}}, true},
		"acp opencode":       {RoleHarnesses{OperatorRoleHarness: store.OperatorRoleHarness{Name: "acp", Profile: &opencode}}, false},
		"acp without a name": {RoleHarnesses{OperatorRoleHarness: store.OperatorRoleHarness{Name: "acp"}}, false},
		"claude-environment": {RoleHarnesses{OperatorRoleHarness: store.OperatorRoleHarness{Name: "claude-environment"}}, false},
	} {
		if got := openHandsHarness(c.h); got != c.want {
			t.Errorf("%s: openHandsHarness = %v, want %v", name, got, c.want)
		}
	}
}

func TestACardThatNamesNoTeamIsAskedWhichOneAndToldTheTeamsThatFit(t *testing.T) {
	reset(t)
	board := newDelegateBoard(t)
	board.set("cu-nobody", "")
	s := fitGateServer(t, board)
	s.ScopeTeams = nil
	cu := s.Trackers["clickup"].(*clickup.Provider)
	cu.DefaultTeam = ""
	cu.TeamField = "Ploeg Team"
	clickupAssigneeHook(t, s.Handler(), "cu-nobody", "assignee_add", "agent-member")

	code, reason, team := gateRefusal(t, "cu-nobody")
	if code != target.RefusalTeamMissing || reason != "no Team is named for this card: set the Ploeg Team field" || team != "" {
		t.Fatalf("refusal = %q %q under %q", code, reason, team)
	}
	want := "Question · Which Team should take this card? Set the Ploeg Team field. Teams that can take this repository: bronze, silver."
	if got := board.commentsOn("cu-nobody"); len(got) != 1 || got[0] != want {
		t.Fatalf("comments = %q, want %q", got, want)
	}
	if codes := operatorRefusalCodes(t, nil); len(codes) != 1 || codes[0] != target.RefusalTeamMissing {
		t.Fatalf("operator refusals = %v", codes)
	}
}

func TestATrackerWithoutAgentIdentityGetsTheFirstActivityAsAComment(t *testing.T) {
	reset(t)
	board := newBoard(t)
	s := routingServer(t, board, readyForge(), nil)
	s.Engine = gateEngine{writes: map[string]bool{"bronze": true}}
	s.Log = slog.New(slog.DiscardHandler)
	assign(t, s.Handler(), "7101")

	if p := pinned(t, "7101"); p.workItems != 1 || p.target != appTarget {
		t.Fatalf("pinned = %+v, want one item on webgrip/app", p)
	}
	want := "<p>Team bronze takes this card.</p><p>Repository webgrip/app, base branch development.</p><p>The pull request link lands here when the work is ready for review.</p>"
	if got := board.commentsOn("7101"); len(got) != 1 || got[0] != want {
		t.Fatalf("comments = %q, want %q", got, want)
	}
}
