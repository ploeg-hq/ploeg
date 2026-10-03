package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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

type fakeForgejo struct {
	mu        sync.Mutex
	pull      string
	status    string
	pullReads int
	statusAt  []string
}

func (f *fakeForgejo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.HasSuffix(r.URL.Path, "/pulls/18"):
		f.pullReads++
		_, _ = w.Write([]byte(f.pull))
	case strings.Contains(r.URL.Path, "/commits/") && strings.HasSuffix(r.URL.Path, "/status"):
		f.statusAt = append(f.statusAt, strings.TrimSuffix(strings.Split(r.URL.Path, "/commits/")[1], "/status"))
		if f.status == "" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(f.status))
	default:
		http.NotFound(w, r)
	}
}

func forgePostEvent(t *testing.T, h http.Handler, secret, event, delivery string, body any) int {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/forge/forgejo", strings.NewReader(string(b)))
	req.Header.Set("X-Forgejo-Event", event)
	req.Header.Set("X-Forgejo-Delivery", delivery)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(b)
	req.Header.Set("X-Forgejo-Signature", hex.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func pullRequestEvent(action, head string) map[string]any {
	return map[string]any{
		"action":     action,
		"repository": map[string]any{"full_name": "webgrip/ploeg"},
		"sender":     map[string]any{"login": "ploeg-bot"},
		"pull_request": map[string]any{"number": 18, "mergeable": true,
			"head": map[string]any{"ref": factsBranch, "sha": head}},
	}
}

func cardServer(t *testing.T, forge *fakeForgejo) (*Server, string) {
	t.Helper()
	reset(t)
	srv := httptest.NewServer(forge)
	t.Cleanup(srv.Close)
	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	return &Server{
		Store: testStore, Log: slog.New(slog.DiscardHandler),
		Forges: map[string]provider.ForgeProvider{"forgejo": &forgejo.Provider{BaseURL: srv.URL, Secret: "shh",
			Log: slog.New(slog.DiscardHandler)}},
		ForgeBots:      []string{"ploeg-bot"},
		OperatorConfig: OperatorConfig{Consumers: consumers, Teams: map[string][]string{"silver": {"builder"}}},
	}, token
}

type diffCI struct {
	additions, deletions, changedFiles *int
	ciState, ciHead                    *string
}

func readCardFacts(t *testing.T) diffCI {
	t.Helper()
	var d diffCI
	if err := testPool.QueryRow(context.Background(), `SELECT additions, deletions, changed_files, ci_state, ci_head_sha
		FROM pull_requests WHERE number = 18`).Scan(&d.additions, &d.deletions, &d.changedFiles, &d.ciState, &d.ciHead); err != nil {
		t.Fatalf("pull request 18: %v", err)
	}
	return d
}

func TestForgeWebhook_OpenedPullRequestCapturesDiffAndCI(t *testing.T) {
	forge := &fakeForgejo{
		pull:   `{"state":"open","merged":false,"head":{"sha":"h1"},"additions":214,"deletions":38,"changed_files":6}`,
		status: `{"state":"pending","sha":"h1","total_count":1,"statuses":[{"context":"verify","status":"pending"}]}`,
	}
	s, _ := cardServer(t, forge)
	factsItem(t)
	h := s.Handler()
	if code := forgePostEvent(t, h, "shh", "pull_request", "open-1", pullRequestEvent("opened", "h1")); code != http.StatusAccepted {
		t.Fatalf("webhook returned %d", code)
	}
	d := readCardFacts(t)
	if d.additions == nil || *d.additions != 214 || *d.deletions != 38 || *d.changedFiles != 6 ||
		d.ciState == nil || *d.ciState != "pending" || *d.ciHead != "h1" {
		t.Fatalf("captured = %+v", d)
	}

	forge.mu.Lock()
	forge.pull = `{"state":"open","merged":false,"head":{"sha":"h2"},"additions":220,"deletions":38,"changed_files":7}`
	forge.status = `{"state":"success","sha":"h2","total_count":1,"statuses":[{"context":"verify","status":"success"}]}`
	forge.mu.Unlock()
	if code := forgePostEvent(t, h, "shh", "pull_request_sync", "sync-1", pullRequestEvent("synchronized", "h2")); code != http.StatusAccepted {
		t.Fatalf("webhook returned %d", code)
	}
	d = readCardFacts(t)
	if *d.additions != 220 || *d.changedFiles != 7 || *d.ciState != "success" || *d.ciHead != "h2" {
		t.Errorf("after a push = %+v", d)
	}
	forge.mu.Lock()
	defer forge.mu.Unlock()
	if fmt.Sprint(forge.statusAt) != "[h1 h2]" {
		t.Errorf("status read at %v; want each head", forge.statusAt)
	}
}

func TestForgeWebhook_FailedCaptureKeepsTheWebhooksFacts(t *testing.T) {
	forge := &fakeForgejo{pull: `not json`}
	s, _ := cardServer(t, forge)
	factsItem(t)
	if code := forgePostEvent(t, s.Handler(), "shh", "pull_request", "open-2", pullRequestEvent("opened", "h1")); code != http.StatusAccepted {
		t.Fatalf("webhook returned %d", code)
	}
	var state, head string
	if err := testPool.QueryRow(context.Background(), `SELECT state, head_sha FROM pull_requests WHERE number = 18`).Scan(&state, &head); err != nil {
		t.Fatal(err)
	}
	d := readCardFacts(t)
	if state != "open" || head != "h1" || d.additions != nil || d.ciState != nil {
		t.Errorf("state %s head %s diff/ci %+v; failed reads must leave the figures unknown", state, head, d)
	}
}

func TestForgeWebhook_HumanPullRequestCostsNoForgeRead(t *testing.T) {
	forge := &fakeForgejo{pull: `{"state":"open","merged":false,"head":{"sha":"h1"}}`}
	s, _ := cardServer(t, forge)
	factsItem(t)
	ev := pullRequestEvent("opened", "h1")
	ev["pull_request"].(map[string]any)["head"] = map[string]any{"ref": "feature/human", "sha": "h1"}
	if code := forgePostEvent(t, s.Handler(), "shh", "pull_request", "open-3", ev); code != http.StatusAccepted {
		t.Fatalf("webhook returned %d", code)
	}
	forge.mu.Lock()
	defer forge.mu.Unlock()
	if forge.pullReads != 0 || len(forge.statusAt) != 0 {
		t.Errorf("reads = %d pulls, %v statuses; a pull request on no Ploeg branch is never read", forge.pullReads, forge.statusAt)
	}
}

func TestOperatorCard_EndpointMatchesTheSchema(t *testing.T) {
	forge := &fakeForgejo{
		pull:   `{"state":"open","merged":false,"head":{"sha":"h1"},"additions":5,"deletions":1,"changed_files":2}`,
		status: `{"state":"success","sha":"h1","total_count":1,"statuses":[{"context":"verify","status":"success"}]}`,
	}
	s, token := cardServer(t, forge)
	theme := "acme"
	s.OperatorConfig.CardStyles = map[string]store.CardStyle{"webgrip/ploeg": {Skin: "foil", Theme: &theme}}
	ctx := context.Background()
	draft, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "1801", Team: "silver", Title: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	raw := operatorSchemaGET(t, s, token, fmt.Sprintf("work-items/%d/card", draft))
	var body struct {
		SchemaVersion int                `json:"schemaVersion"`
		Card          store.OperatorCard `json:"card"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body.SchemaVersion != 1 || body.Card.State != "drafting" || body.Card.Target != nil ||
		body.Card.Style.Skin != store.DefaultCardSkin || body.Card.Style.Theme != nil {
		t.Errorf("draft card = %s", raw)
	}
	for _, key := range []string{`"rarity":null`, `"grade":null`, `"condition":null`, `"finish":"matte"`, `"demo":false`, `"theme":null`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("card lacks %s: %s", key, raw)
		}
	}

	item, shift := factsItem(t)
	if _, err := testStore.OpenRound(ctx, shift, 0, []store.Role{{Name: "builder", Writes: true, Cap: 1}}); err != nil {
		t.Fatal(err)
	}
	run, err := testStore.ClaimRole(ctx, "silver", "builder", time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	usage := json.RawMessage(`{"inputTokens": 1200, "outputTokens": 98, "costUsd": 0.58}`)
	if _, err := testStore.ReportOutcome(ctx, run.RunToken, store.Report(work.OutcomePROpened, "opened", "",
		[]string{"https://forge.example/webgrip/ploeg/pulls/18?token=x"}, usage, nil)); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	if code := forgePostEvent(t, h, "shh", "pull_request", "c-open", pullRequestEvent("opened", "h1")); code != http.StatusAccepted {
		t.Fatalf("open returned %d", code)
	}
	for delivery, review := range map[string]map[string]any{
		"c-r1": factsReview("approved", "anna", factsBranch), "c-r2": factsReview("approved", "ploeg-bot", factsBranch),
	} {
		if code := forgePost(t, h, "shh", delivery, review); code != http.StatusAccepted {
			t.Fatalf("%s returned %d", delivery, code)
		}
	}

	raw = operatorSchemaGET(t, s, token, fmt.Sprintf("work-items/%d/card", item))
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	c := body.Card
	if c.State != "in_review" || c.ExternalRef != "VIK-1800" || c.Style.Skin != "foil" || c.Style.Theme == nil || *c.Style.Theme != "acme" {
		t.Errorf("card = %s", raw)
	}
	if len(c.Plays) != 1 || c.Plays[0].URL != "https://forge.example/webgrip/ploeg/pulls/18" || *c.Plays[0].Additions != 5 ||
		c.Plays[0].CI == nil || c.Plays[0].CI.State != "success" || len(c.Plays[0].Reviews) != 2 {
		t.Errorf("plays = %+v", c.Plays)
	}
	if c.Steward == nil || c.Steward.Name != "anna" || len(c.Roster) != 1 || c.Roster[0].Name != "anna" {
		t.Errorf("steward %+v, roster %+v; the configured forge bot is no person", c.Steward, c.Roster)
	}
	if c.Totals.CostUSD == nil || *c.Totals.CostUSD != 0.58 || !c.Totals.UsageComplete || c.Totals.CostStatus != "observed" {
		t.Errorf("totals = %+v", c.Totals)
	}
	if strings.Contains(string(raw), run.RunToken) {
		t.Error("the card leaked a run token")
	}
}

func TestOperatorCard_EndpointIsScopedAndReadOnly(t *testing.T) {
	s, token := cardServer(t, &fakeForgejo{})
	ctx := context.Background()
	other, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "1802", Team: "gold", Title: "hidden"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", fmt.Sprintf("/api/v1/operator/work-items/%d/card", other), 404},
		{"GET", fmt.Sprintf("/api/v1/operator/work-items/%d/card", other+1000), 404},
		{"GET", "/api/v1/operator/work-items/0/card", 400},
		{"GET", fmt.Sprintf("/api/v1/operator/work-items/%d/card?x=1", other), 400},
		{"POST", fmt.Sprintf("/api/v1/operator/work-items/%d/card", other), 405},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.status || strings.Contains(w.Body.String(), "hidden") {
			t.Errorf("%s %s: %d %s; want %d", tc.method, tc.path, w.Code, w.Body, tc.status)
		}
	}
	r := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/operator/work-items/%d/card", other), nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Errorf("unauthenticated card read: %d", w.Code)
	}
}

func TestOperatorCard_RunningRunCarriesTheGatewaysUsageSoFar(t *testing.T) {
	ctx := context.Background()
	g, broker := newFakeGateway(t)
	reset(t)
	shiftFixture(t, "1700", 5, []store.Role{{Name: "builder", Cap: 1}})
	control, err := NewLLMControl(testStore, broker, `[{"team":"bronze","role":"builder","budgetUsd":2,"models":["trusted-model"],"ttl":"1h"}]`)
	if err != nil {
		t.Fatal(err)
	}
	run, err := testStore.ClaimRole(ctx, "bronze", "builder", time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.Reserve(ctx, run.RunToken); err != nil {
		t.Fatal(err)
	}
	cred, err := control.Issue(ctx, run.RunToken)
	if err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	g.logs[hashedKey(cred.APIKey)] = []map[string]any{
		{"spend": 0.2, "model": "deepseek-chat", "prompt_tokens": 9000, "completion_tokens": 100},
		{"spend": 0.07, "model": "deepseek-chat", "prompt_tokens": 1400, "completion_tokens": 27},
	}
	g.mu.Unlock()
	var item int64
	if err := testPool.QueryRow(ctx, `SELECT work_item_id FROM agent_runs WHERE run_token = $1`, run.RunToken).Scan(&item); err != nil {
		t.Fatal(err)
	}
	consumers, token := operatorTestConsumers(t, []string{"bronze"}, false)
	s := &Server{Store: testStore, Log: slog.New(slog.DiscardHandler), LLMControl: control,
		OperatorConfig: OperatorConfig{Consumers: consumers, Teams: map[string][]string{"bronze": {"builder"}}}}

	raw := operatorSchemaGET(t, s, token, fmt.Sprintf("work-items/%d/card", item))
	var body struct {
		Card store.OperatorCard `json:"card"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	live := body.Card.Live
	if live == nil || live.RunningRuns != 1 || live.CostUSD == nil || *live.CostUSD < 0.2699 || *live.CostUSD > 0.2701 ||
		*live.InputTokens != 10400 || *live.OutputTokens != 127 || !live.UsageComplete {
		t.Fatalf("live = %s; want the gateway's spend and tokens so far", raw)
	}
	if body.Card.Totals.CostUSD != nil || body.Card.Totals.CostStatus != "reserved" {
		t.Errorf("totals = %+v; a live reading is not recorded", body.Card.Totals)
	}
	a, err := testStore.LLMAccount(ctx, run.RunToken)
	if err != nil || a.ObservedSpend != nil || a.State != "issued" {
		t.Errorf("account = %+v, %v; reading a card must not touch the account", a, err)
	}

	s.LLMControl = nil
	raw = operatorSchemaGET(t, s, token, fmt.Sprintf("work-items/%d/card", item))
	body.Card = store.OperatorCard{}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if l := body.Card.Live; l == nil || l.CostUSD != nil || l.InputTokens != nil || l.UsageComplete {
		t.Errorf("live without managed inference = %s; cost and tokens stay unknown", raw)
	}
}
