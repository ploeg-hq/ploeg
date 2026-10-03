package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/forgejo"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type fakeChangeForge struct {
	mu      sync.Mutex
	pulls   map[int]string
	files   map[int][]string
	commits map[int][]string
	reads   []string
}

func (f *fakeChangeForge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, r.URL.Path)
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/v1/repos/webgrip/ploeg/pulls/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	number, tail, _ := strings.Cut(rest, "/")
	var n int
	fmt.Sscan(number, &n)
	switch tail {
	case "":
		if body, ok := f.pulls[n]; ok {
			fmt.Fprint(w, body)
			return
		}
	case "files":
		out := []map[string]string{}
		if r.URL.Query().Get("page") == "1" {
			for _, path := range f.files[n] {
				out = append(out, map[string]string{"filename": path})
			}
		}
		_ = json.NewEncoder(w).Encode(out)
		return
	case "commits":
		out := []map[string]any{}
		if r.URL.Query().Get("page") == "1" {
			for _, msg := range f.commits[n] {
				out = append(out, map[string]any{"commit": map[string]string{"message": msg}})
			}
		}
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	http.NotFound(w, r)
}

func mergedPull(number int, title, body, mergedAt string, labels ...string) string {
	ls := make([]map[string]string, 0, len(labels))
	for _, l := range labels {
		ls = append(ls, map[string]string{"name": l})
	}
	raw, _ := json.Marshal(map[string]any{"number": number, "state": "closed", "merged": true, "title": title, "body": body,
		"labels": ls, "merged_at": mergedAt, "closed_at": mergedAt, "merge_commit_sha": strings.Repeat(fmt.Sprintf("%02d", number), 20),
		"merged_by": map[string]string{"login": "stewart"}, "head": map[string]string{"sha": fmt.Sprintf("head%d", number)}})
	return string(raw)
}

func mergeEvent(number int, branch, mergedAt, by string) map[string]any {
	return map[string]any{
		"action":     "closed",
		"repository": map[string]any{"full_name": "webgrip/ploeg"},
		"sender":     map[string]any{"login": by},
		"pull_request": map[string]any{
			"number": number, "merged": true,
			"head":             map[string]any{"ref": branch, "sha": fmt.Sprintf("head%d", number)},
			"merge_commit_sha": strings.Repeat(fmt.Sprintf("%02d", number), 20),
			"merged_at":        mergedAt, "closed_at": mergedAt,
			"merged_by": map[string]any{"login": by},
		},
	}
}

func crackServer(t *testing.T, forge *fakeChangeForge, execute bool) (*Server, string) {
	t.Helper()
	reset(t)
	api := httptest.NewServer(forge)
	t.Cleanup(api.Close)
	consumers, token := operatorTestConsumers(t, []string{"silver"}, execute)
	return &Server{
		Store: testStore, Log: slog.New(slog.DiscardHandler),
		Forges: map[string]provider.ForgeProvider{
			"forgejo": &forgejo.Provider{BaseURL: api.URL, Secret: "shh", Log: slog.New(slog.DiscardHandler)},
		},
		ForgeBots:      []string{"ploeg-bot"},
		OperatorConfig: OperatorConfig{Consumers: consumers, Teams: map[string][]string{"silver": {"builder"}}},
	}, token
}

func crackItem(t *testing.T, externalID, branch string) int64 {
	t.Helper()
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: externalID, Team: "silver", Title: "Item " + externalID,
		Target: &work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "ploeg", BaseBranch: "development"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.OpenShift(ctx, id, "silver", branch, 0); err != nil {
		t.Fatal(err)
	}
	return id
}

func operatorPost(t *testing.T, h http.Handler, token, actor, path, body string) (int, []byte) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/operator/"+path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	if actor != "" {
		r.Header.Set("X-Ploeg-Actor", actor)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, w.Body.Bytes()
}

func errorCode(t *testing.T, raw []byte) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	return body.Error.Code
}

func crackOf(t *testing.T, raw []byte) store.Crack {
	t.Helper()
	validateOperatorSchema(t, raw)
	var body struct {
		Crack store.Crack `json:"crack"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	return body.Crack
}

func TestCracks_FilesRevertsAndMendsComeFromMergedPullRequests(t *testing.T) {
	forge := &fakeChangeForge{pulls: map[int]string{}, files: map[int][]string{}, commits: map[int][]string{}}
	s, token := crackServer(t, forge, true)
	h := s.Handler()
	card := crackItem(t, "card", "agent/vik-card")
	merged := time.Now().Add(-20 * 24 * time.Hour).UTC().Format(time.RFC3339)
	forge.pulls[18] = mergedPull(18, "Add run cards", "", merged)
	forge.files[18] = []string{"pkg/store/card.go", "docs/cards.md"}
	if code := forgePostEvent(t, h, "shh", "pull_request", "m-18", mergeEvent(18, "agent/vik-card", merged, "stewart")); code != http.StatusAccepted {
		t.Fatalf("merge returned %d", code)
	}
	var stored int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM pull_request_files`).Scan(&stored); err != nil || stored != 2 {
		t.Fatalf("stored files = %d, %v", stored, err)
	}

	bug := crackItem(t, "bug", "agent/vik-bug")
	code, raw := operatorPost(t, h, token, "fixer", fmt.Sprintf("work-items/%d/cracks", bug),
		fmt.Sprintf(`{"card":"%d","severity":"S2","share":"primary"}`, card))
	if code != http.StatusCreated {
		t.Fatalf("propose = %d %s", code, raw)
	}
	crack := crackOf(t, raw)
	if crack.Mended != nil {
		t.Fatalf("a crack whose fix has not merged is mended: %+v", crack.Mended)
	}
	code, raw = operatorPost(t, h, token, "second", "cracks/"+crack.ID+"/confirm", `{}`)
	if code != http.StatusOK {
		t.Fatalf("confirm = %d %s", code, raw)
	}

	fixed := time.Now().UTC().Format(time.RFC3339)
	forge.pulls[30] = mergedPull(30, "Fix the card total", "", fixed, "hotfix")
	forge.files[30] = []string{"pkg/store/card.go"}
	if code := forgePostEvent(t, h, "shh", "pull_request", "m-30", mergeEvent(30, "agent/vik-bug", fixed, "stewart")); code != http.StatusAccepted {
		t.Fatalf("fix merge returned %d", code)
	}
	forge.pulls[31] = mergedPull(31, `Revert "Add run cards"`, "Reverts webgrip/ploeg#18", fixed)
	forge.commits[31] = []string{"Revert \"Add run cards\"\n\nThis reverts commit " + strings.Repeat("18", 20) + "."}
	if code := forgePostEvent(t, h, "shh", "pull_request", "m-31", mergeEvent(31, "revert-18", fixed, "anna")); code != http.StatusAccepted {
		t.Fatalf("revert merge returned %d", code)
	}

	cardRaw := operatorSchemaGET(t, s, token, fmt.Sprintf("work-items/%d/card", card))
	var body struct {
		Card store.OperatorCard `json:"card"`
	}
	if err := json.Unmarshal(cardRaw, &body); err != nil {
		t.Fatal(err)
	}
	c := body.Card
	if c.Condition == nil || c.Condition.State != "cracked" || c.Condition.Cracks[0].Mended == nil || c.Condition.Cracks[0].Mended.PR != 30 ||
		!c.Condition.Cracks[0].Mended.BySteward {
		t.Fatalf("condition = %+v", c.Condition)
	}
	g := c.Grade
	if g == nil || g.Formula != store.GradeFormula || !*g.Inputs.Reliability.Reverted || *g.Inputs.Durability.Reverts != 1 ||
		*g.Inputs.Durability.Hotfixes != 1 || strings.Join(g.Qualifiers, ",") != "RV,HF" {
		t.Fatalf("grade = %+v", g)
	}

	candidatesRaw := operatorSchemaGET(t, s, token, fmt.Sprintf("work-items/%d/crack-candidates", bug))
	if !strings.Contains(string(candidatesRaw), `"play":18`) || !strings.Contains(string(candidatesRaw), `"reverted":true`) {
		t.Fatalf("candidates = %s", candidatesRaw)
	}
	operatorSchemaGET(t, s, token, fmt.Sprintf("work-items/%d/cracks", card))
}

func TestCracks_OperatorFlowRefusesTheWrongPeople(t *testing.T) {
	forge := &fakeChangeForge{pulls: map[int]string{}, files: map[int][]string{}, commits: map[int][]string{}}
	s, token := crackServer(t, forge, true)
	s.CardRules = map[string]CardRules{"silver": {Referees: []string{"ref"}}}
	h := s.Handler()
	card := crackItem(t, "card", "agent/vik-card")
	at := time.Now().Add(-10 * 24 * time.Hour)
	if ok, err := testStore.RecordPullRequestFacts(context.Background(), store.PullRequestFacts{Forge: "forgejo", Repo: "webgrip/ploeg",
		Number: 18, WorkItemID: card, State: "merged", MergedAt: &at, MergedBy: "stewart"}); err != nil || !ok {
		t.Fatal(ok, err)
	}
	bug := crackItem(t, "bug", "agent/vik-bug")
	propose := fmt.Sprintf(`{"card":"%d","severity":"S1","share":"primary","note":"null pointer in totals"}`, card)

	if code, raw := operatorPost(t, h, token, "", fmt.Sprintf("work-items/%d/cracks", bug), propose); code != 400 || errorCode(t, raw) != "actor_required" {
		t.Fatalf("no actor = %d %s", code, raw)
	}
	if code, raw := operatorPost(t, h, token, "fixer", fmt.Sprintf("work-items/%d/cracks", bug), `{"card":"x"}`); code != 400 {
		t.Fatalf("bad card = %d %s", code, raw)
	}
	if code, raw := operatorPost(t, h, token, "fixer", fmt.Sprintf("work-items/%d/cracks", bug), `{"card":"1","oops":true}`); code != 400 {
		t.Fatalf("unknown field = %d %s", code, raw)
	}
	code, raw := operatorPost(t, h, token, "fixer", fmt.Sprintf("work-items/%d/cracks", bug), propose)
	if code != http.StatusCreated {
		t.Fatalf("propose = %d %s", code, raw)
	}
	crack := crackOf(t, raw)
	if code, raw := operatorPost(t, h, token, "fixer", fmt.Sprintf("work-items/%d/cracks", bug), propose); code != 409 || errorCode(t, raw) != "already_attributed" {
		t.Fatalf("duplicate = %d %s", code, raw)
	}
	if code, raw := operatorPost(t, h, token, "stewart", "cracks/"+crack.ID+"/confirm", `{}`); code != 403 || errorCode(t, raw) != "forbidden_actor" {
		t.Fatalf("steward confirm = %d %s", code, raw)
	}
	if code, raw := operatorPost(t, h, token, "second", "cracks/"+crack.ID+"/confirm", `{"severity":"S2"}`); code != 200 || *crackOf(t, raw).Severity != "S2" {
		t.Fatalf("confirm = %d %s", code, raw)
	}
	if code, raw := operatorPost(t, h, token, "second", "cracks/"+crack.ID+"/dispute", `{"reason":"x"}`); code != 403 {
		t.Fatalf("non-steward dispute = %d %s", code, raw)
	}
	if code, raw := operatorPost(t, h, token, "stewart", "cracks/"+crack.ID+"/dispute", `{"reason":"predates me"}`); code != 200 || !crackOf(t, raw).Disputed {
		t.Fatalf("dispute = %d %s", code, raw)
	}
	if code, raw := operatorPost(t, h, token, "bystander", "cracks/"+crack.ID+"/resolve", `{"resolution":"unlinked"}`); code != 403 {
		t.Fatalf("unlisted referee = %d %s", code, raw)
	}
	if code, raw := operatorPost(t, h, token, "ref", "cracks/"+crack.ID+"/resolve", `{"resolution":"maybe"}`); code != 400 {
		t.Fatalf("bad resolution = %d %s", code, raw)
	}
	if code, raw := operatorPost(t, h, token, "ref", "cracks/"+crack.ID+"/resolve", `{"resolution":"upheld"}`); code != 200 || crackOf(t, raw).State != "confirmed" {
		t.Fatalf("resolve = %d %s", code, raw)
	}
	if code, raw := operatorPost(t, h, token, "ref", "cracks/999999/confirm", `{}`); code != 404 {
		t.Fatalf("unknown crack = %d %s", code, raw)
	}
	other := crackItem(t, "other-bug", "agent/vik-other")
	code, raw = operatorPost(t, h, token, "fixer", fmt.Sprintf("work-items/%d/evolved", other), fmt.Sprintf(`{"card":"%d"}`, card))
	if code != 200 || crackOf(t, raw).State != "evolved" {
		t.Fatalf("evolved = %d %s", code, raw)
	}

	var actors []string
	rows, err := testPool.Query(context.Background(), `SELECT actor FROM audit_log WHERE action LIKE 'card.crack_%' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		actors = append(actors, a)
	}
	want := "operator:workbench:fixer,operator:workbench:second,operator:workbench:stewart,operator:workbench:ref,operator:workbench:fixer"
	if strings.Join(actors, ",") != want {
		t.Fatalf("audit actors = %v", actors)
	}

	readOnly, readToken := operatorTestConsumers(t, []string{"silver"}, false)
	s.OperatorConfig.Consumers = readOnly
	if code, raw := operatorPost(t, s.Handler(), readToken, "fixer", fmt.Sprintf("work-items/%d/cracks", bug), propose); code != 403 {
		t.Fatalf("a consumer without execute permission proposed: %d %s", code, raw)
	}
	gold, goldToken := operatorTestConsumers(t, []string{"gold"}, true)
	s.OperatorConfig.Consumers = gold
	if code, raw := operatorPost(t, s.Handler(), goldToken, "second", "cracks/"+crack.ID+"/confirm", `{}`); code != 404 {
		t.Fatalf("another team reached the crack: %d %s", code, raw)
	}
}
