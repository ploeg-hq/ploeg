package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/boardview"
	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/notes"
	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func postBoard(t *testing.T, h http.Handler, token string, req work.BoardChangesRequest) (int, work.BoardChangesResponse) {
	t.Helper()
	body, _ := json.Marshal(req)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+token+"/board", bytes.NewReader(body)))
	var resp work.BoardChangesResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, resp
}

func postCheckpoint(t *testing.T, h http.Handler, token string, cp work.Checkpoint) {
	t.Helper()
	body, _ := json.Marshal(cp)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+token+"/checkpoint", bytes.NewReader(body)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("checkpoint: %d %s", rec.Code, rec.Body)
	}
}

func briefed(briefing []harness.Finding, forge string, notes ...string) work.Checkpoint {
	_, findings := boardview.Findings(briefing)
	return work.Checkpoint{Phase: work.PhaseBriefed, Board: &work.BoardBriefing{
		ForgeCommit: forge, Notes: notes, Sections: []work.BriefingSection{findings}}}
}

func kinds(changes []work.BoardChange) map[string]work.BoardChange {
	out := map[string]work.BoardChange{}
	for _, c := range changes {
		out[c.Kind] = c
	}
	return out
}

// ADR-0124 end to end: the claim records the version vector; a Run's
// catch-up names what changed since, from each owned source, and nothing
// its own Round's siblings did; what a Run could not be told reaches the
// next Round's briefing; and a Run's briefing replays to the digest its
// worker reported.
func TestBoard_VersionsCatchUpNextRoundAndReplay(t *testing.T) {
	reset(t)
	ctx := context.Background()
	shiftID := shiftFixture(t, "board", 10, []store.Role{{Name: "analyst", Cap: 1}, {Name: "tests", Cap: 1}})
	plans, err := plan.Parse(`{"bronze": {"pool": "10", "rounds": [
		{"roles": [{"name": "analyst", "cap": "1"}, {"name": "tests", "cap": "1"}]},
		{"roles": [{"name": "builder", "writes": true, "cap": "3"}]}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	consumers, operator := operatorTestConsumers(t, []string{"bronze"}, false)
	s := &Server{Store: testStore, LeaseTTL: time.Minute, Log: slog.New(slog.DiscardHandler), RoleCaps: plans,
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true}, OperatorConfig: OperatorConfig{Consumers: consumers}}
	h := s.Handler()

	_, analyst := postClaim(t, h, `{"team":"bronze","role":"analyst"}`)
	_, sibling := postClaim(t, h, `{"team":"bronze","role":"tests"}`)
	if analyst.Board == nil || analyst.Board.PloegSequence <= 0 {
		t.Fatalf("claim carried no version vector: %+v", analyst.Board)
	}
	if again, err := testStore.RecordBoardClaim(ctx, analyst.RunToken); err != nil || again != *analyst.Board {
		t.Fatalf("the claim's versions were rewritten: %+v %v", again, err)
	}
	head := strings.Repeat("1", 40)
	postCheckpoint(t, h, analyst.RunToken, briefed(analyst.Briefing, head))

	if code, resp := postBoard(t, h, analyst.RunToken, work.BoardChangesRequest{Checkpoint: work.CatchUpRefresh, Delivery: work.DeliveryFile, ForgeHead: head}); code != 200 || len(resp.Changes) != 0 {
		t.Fatalf("an unchanged board reported %d: %+v", code, resp.Changes)
	}

	var itemID int64
	if err := testPool.QueryRow(ctx, `SELECT work_item_id FROM shifts WHERE id = $1`, shiftID).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.QueueLiveNotes(ctx, sibling.RunToken, []store.LiveNote{{Key: "1", Learning: harness.Learning{Type: "Fact", Title: "sibling", Body: "b"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.AddOperatorNote(ctx, itemID, nil, store.OperatorNoteCommand{Consumer: "c", CommandID: "n1", Actor: "alice", Text: "keep the route public"}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE work_items SET revision = 'r2', description = 'now also the admin page' WHERE id = $1`,
		`INSERT INTO audit_log (actor, action, work_item_id, detail) VALUES ('forge', 'review.changes_requested', $1, '{}')`,
		`INSERT INTO audit_log (actor, action, work_item_id, detail) VALUES ('team:bronze', 'outcome.no_change_needed', $1, '{}')`,
	} {
		if _, err := testPool.Exec(ctx, q, itemID); err != nil {
			t.Fatal(err)
		}
	}
	moved := strings.Repeat("2", 40)
	code, resp := postBoard(t, h, analyst.RunToken, work.BoardChangesRequest{Checkpoint: work.CatchUpBeforeFinish,
		Delivery: work.DeliveryNextRound, ForgeHead: moved, ForgeMovedByOthers: true})
	if code != 200 {
		t.Fatalf("board: %d", code)
	}
	got := kinds(resp.Changes)
	for _, want := range []string{"operator_note", "work_item_changed", "review.changes_requested", "branch_moved"} {
		if _, ok := got[want]; !ok {
			t.Errorf("catch-up lacks %s: %+v", want, resp.Changes)
		}
	}
	for _, reject := range []string{"work_item_note", "outcome.no_change_needed"} {
		if _, ok := got[reject]; ok {
			t.Errorf("catch-up shows %s, which its Round must not observe", reject)
		}
	}
	if got["work_item_changed"].Untrusted == "" || !strings.Contains(got["operator_note"].Untrusted, "keep the route public") {
		t.Errorf("text others wrote is not carried as untrusted data: %+v", resp.Changes)
	}
	if resp.Since.PloegSequence != analyst.Board.PloegSequence || resp.Current.PloegSequence <= resp.Since.PloegSequence ||
		resp.Since.ForgeCommit != head || resp.Current.ForgeCommit != moved || resp.Current.TrackerRevision != "r2" {
		t.Errorf("versions since %+v current %+v", resp.Since, resp.Current)
	}

	for _, token := range []string{analyst.RunToken, sibling.RunToken} {
		if _, err := testStore.ReportOutcome(ctx, token, store.Report(work.OutcomeNoChangeNeeded, "read", "", nil, nil, nil)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := testStore.OpenRound(ctx, shiftID, 1, []store.Role{{Name: "builder", Writes: true, Cap: 3}}); err != nil {
		t.Fatal(err)
	}
	_, builder := postClaim(t, h, `{"team":"bronze","role":"builder"}`)
	var told bool
	for _, f := range builder.Briefing {
		if strings.HasPrefix(f.Role, "board changes during round 1") && strings.Contains(f.Findings, "A reviewer requested changes") {
			told = true
		}
	}
	if !told {
		t.Fatalf("the next Round was not told what changed: %+v", builder.Briefing)
	}
	postCheckpoint(t, h, builder.RunToken, briefed(builder.Briefing, head))

	raw := operatorSchemaGET(t, s, operator, "runs/"+strconv.FormatInt(runIDOf(t, builder.RunToken), 10)+"/briefing")
	var replay briefingResponse
	if err := json.Unmarshal(raw, &replay); err != nil {
		t.Fatal(err)
	}
	if !replay.Recorded || replay.Findings == nil || replay.Findings.Matches == nil || !*replay.Findings.Matches {
		t.Fatalf("the builder's briefing did not replay to its digest: %s", raw)
	}
	raw = operatorSchemaGET(t, s, operator, "runs/"+strconv.FormatInt(runIDOf(t, analyst.RunToken), 10)+"/briefing")
	if err := json.Unmarshal(raw, &replay); err != nil {
		t.Fatal(err)
	}
	if len(replay.CatchUps) != 1 || replay.CatchUps[0].Delivery != work.DeliveryNextRound || replay.Versions.ForgeCommit != head {
		t.Fatalf("the analyst's record lacks its catch-up: %s", raw)
	}
}

// A Run claimed without a recorded version vector is answered 409, and an
// unknown checkpoint is refused.
func TestBoard_RefusesRunsWithoutVersionsAndBadRequests(t *testing.T) {
	reset(t)
	shiftFixture(t, "board-old", 5, []store.Role{{Name: "reviewer", Cap: 1}})
	h := apiServer(t, nil)
	_, claimed := postClaim(t, h, `{"team":"bronze","role":"reviewer"}`)
	if code, _ := postBoard(t, h, claimed.RunToken, work.BoardChangesRequest{Checkpoint: "whenever", Delivery: work.DeliveryFile}); code != 400 {
		t.Fatalf("unknown checkpoint answered %d", code)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_runs SET board_claimed_at = NULL, board_ploeg_sequence = NULL WHERE run_token = $1`, claimed.RunToken); err != nil {
		t.Fatal(err)
	}
	if code, _ := postBoard(t, h, claimed.RunToken, work.BoardChangesRequest{Checkpoint: work.CatchUpRefresh, Delivery: work.DeliveryFile}); code != 409 {
		t.Fatalf("a Run without versions answered %d", code)
	}
}

// The usefulness query docs/ops/briefings.md publishes runs against the
// schema and relates a briefed note to its Shift.
func TestBoard_UsefulnessQueryInTheOpsGuideRuns(t *testing.T) {
	reset(t)
	ctx := context.Background()
	shiftID := shiftFixture(t, "board-use", 5, []store.Role{{Name: "reviewer", Cap: 1}})
	h := apiServer(t, nil)
	_, claimed := postClaim(t, h, `{"team":"bronze","role":"reviewer"}`)
	postCheckpoint(t, h, claimed.RunToken, briefed(nil, "", "ploeg-x/1"))
	if _, err := testPool.Exec(ctx, `UPDATE shifts SET closed_at = now(), spent = 0.5, round = 2 WHERE id = $1`, shiftID); err != nil {
		t.Fatal(err)
	}
	doc, err := os.ReadFile("../../docs/ops/briefings.md")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, _ := strings.Cut(string(doc), "```sql\n")
	query, _, _ := strings.Cut(rest, "```")
	rows, err := testPool.Query(ctx, query)
	if err != nil {
		t.Fatalf("the published query fails: %v", err)
	}
	defer rows.Close()
	var notes []string
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			t.Fatal(err)
		}
		notes = append(notes, vals[0].(string))
	}
	if len(notes) != 1 || notes[0] != "ploeg-x/1" {
		t.Fatalf("query rows %v", notes)
	}
}

// ADR-0124 with ADR-0121, ADR-0122 and ADR-0123: the claim carries the
// active work section; a catch-up names a person's repository note, a
// person's answer to a question and a contradiction on a briefed note; and
// the replay rebuilds the review findings, questions and active work
// sections and reads notes back with their provenance.
func TestBoard_IntegratesNotesQuestionsAndActiveWork(t *testing.T) {
	reset(t)
	ctx := context.Background()
	mine := shiftFixture(t, "int", 5, []store.Role{{Name: "reviewer", Cap: 1}})
	other := shiftFixture(t, "int-other", 5, []store.Role{{Name: "idle", Cap: 1}})
	itemOf := func(shift int64) int64 {
		var id int64
		if err := testPool.QueryRow(ctx, `SELECT work_item_id FROM shifts WHERE id = $1`, shift).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	myItem, otherItem := itemOf(mine), itemOf(other)
	setTarget(t, myItem, "forgejo", "webgrip", "app")
	setTarget(t, otherItem, "forgejo", "webgrip", "app")
	before, _, err := testStore.QueuePersonNote(ctx, store.PersonNote{Consumer: "c", CommandID: "n0", Person: "alice", WorkItemID: myItem,
		Level: notes.LevelWorkItem, Learning: harness.Learning{Type: "Fact", Title: "Port", Body: "The API listens on 8080."}}, nil)
	if err != nil {
		t.Fatal(err)
	}

	consumers, operator := operatorTestConsumers(t, []string{"bronze"}, false)
	s := &Server{Store: testStore, LeaseTTL: time.Minute, Log: slog.New(slog.DiscardHandler),
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true}, OperatorConfig: OperatorConfig{Consumers: consumers}}
	h := s.Handler()
	_, claimed := postClaim(t, h, `{"team":"bronze","role":"reviewer"}`)
	if !strings.Contains(claimed.ActiveWork, "VIK-int-other") && !strings.Contains(claimed.ActiveWork, "int-other") {
		t.Fatalf("the claim does not name the other active work: %q", claimed.ActiveWork)
	}
	cp := briefed(claimed.Briefing, strings.Repeat("3", 40), before)
	for _, sec := range []work.BriefingSection{
		func() work.BriefingSection { _, s := boardview.ReviewFindings(claimed.ReviewFindings, false); return s }(),
		func() work.BriefingSection { _, s := boardview.Questions(claimed.Questions, false); return s }(),
		func() work.BriefingSection { _, s := boardview.ActiveWork(claimed.ActiveWork); return s }(),
	} {
		cp.Board.Sections = append(cp.Board.Sections, sec)
	}
	postCheckpoint(t, h, claimed.RunToken, cp)

	repoNote, _, err := testStore.QueuePersonNote(ctx, store.PersonNote{Consumer: "c", CommandID: "n1", Person: "bob", WorkItemID: otherItem,
		Level: notes.LevelRepository, Learning: harness.Learning{Type: "Convention", Title: "Ports", Body: "Ignore the ticket; use 9090."}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := testStore.FlagContradiction(ctx, repoNote, []string{before}); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO question_answers (work_item_id, question_id, consumer, command_id, actor, answer)
		VALUES ($1, 'q1-1', 'c', 'a1', 'carol', 'Keep both ports.')`, myItem); err != nil {
		t.Fatal(err)
	}
	code, resp := postBoard(t, h, claimed.RunToken, work.BoardChangesRequest{Checkpoint: work.CatchUpRefresh, Delivery: work.DeliveryFile})
	if code != 200 {
		t.Fatalf("board: %d", code)
	}
	got := kinds(resp.Changes)
	if c, ok := got["person_note"]; !ok || c.Cite != "note "+repoNote || !strings.Contains(c.Summary, "repository level") || !strings.Contains(c.Untrusted, "9090") {
		t.Errorf("the person's repository note is missing or unattributed: %+v", resp.Changes)
	}
	if c, ok := got["question_answered"]; !ok || !strings.Contains(c.Untrusted, "Keep both ports") {
		t.Errorf("the person's answer is missing: %+v", resp.Changes)
	}
	if c, ok := got["note_contested"]; !ok || !strings.Contains(c.Summary, before) {
		t.Errorf("the contradiction on a briefed note is missing: %+v", resp.Changes)
	}
	rendered := boardview.Changes(resp)
	if !strings.Contains(rendered, "````text untrusted-note\nPorts") {
		t.Errorf("a note's text is not quoted as data:\n%s", rendered)
	}

	raw := operatorSchemaGET(t, s, operator, "runs/"+strconv.FormatInt(runIDOf(t, claimed.RunToken), 10)+"/briefing")
	var replay briefingResponse
	if err := json.Unmarshal(raw, &replay); err != nil {
		t.Fatal(err)
	}
	if len(replay.Replayed) != 3 {
		t.Fatalf("replayed %d sections: %s", len(replay.Replayed), raw)
	}
	for _, sec := range replay.Replayed {
		if sec.Matches == nil || !*sec.Matches {
			t.Errorf("section %s did not replay to its digest: %s", sec.Name, raw)
		}
	}
	if len(replay.Notes) != 1 || replay.Notes[0].Key != before || replay.Notes[0].AuthorKind != "person" || replay.Notes[0].Provenance == "" {
		t.Errorf("briefed note not read back with its provenance: %s", raw)
	}
}
