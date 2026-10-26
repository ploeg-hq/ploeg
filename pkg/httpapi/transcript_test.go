package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/transcript"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func transcriptBody(from, n int, text string) string {
	var entries []transcript.Entry
	for i := from; i < from+n; i++ {
		entries = append(entries, transcript.Entry{Seq: int64(i), At: time.Now().UTC(), Kind: transcript.KindStage, Text: fmt.Sprintf("%s %d", text, i)})
	}
	b, _ := json.Marshal(map[string]any{"entries": entries})
	return string(b)
}

func runIDOf(t *testing.T, token string) int64 {
	t.Helper()
	var id int64
	if err := testPool.QueryRow(context.Background(), `SELECT id FROM agent_runs WHERE run_token = $1`, token).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func transcriptServer(t *testing.T, teams []string) (*Server, string, string) {
	t.Helper()
	reset(t)
	shiftFixture(t, "transcript", 5, []store.Role{{Name: "reviewer", Cap: 1}})
	consumers, operator := operatorTestConsumers(t, teams, false)
	s := &Server{Store: testStore, LeaseTTL: time.Minute, Log: slog.New(slog.DiscardHandler),
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true}, OperatorConfig: OperatorConfig{Consumers: consumers}}
	code, claimed := postClaim(t, s.Handler(), `{"team":"bronze","role":"reviewer"}`)
	if code != http.StatusOK {
		t.Fatalf("claim: %d", code)
	}
	return s, claimed.RunToken, operator
}

func operatorGet(s *Server, token, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestRunTranscriptRequiresTheRunsOwnCapability(t *testing.T) {
	s, bootstrap := secureWorkerFixture(t, &managedBrokerFixture{})
	w := workerRequest(s, "POST", "/api/v1/claim", bootstrap, "pod", `{"team":"bronze","role":"reviewer"}`)
	var claim struct {
		RunToken     string `json:"runToken"`
		ControlToken string `json:"controlToken"`
	}
	if w.Code != 200 || json.NewDecoder(w.Body).Decode(&claim) != nil {
		t.Fatalf("claim=%d", w.Code)
	}
	path := "/api/v1/runs/" + claim.RunToken + "/transcript"
	body := transcriptBody(1, 2, "step")
	for _, tc := range []struct{ token, worker string }{{"", "pod"}, {bootstrap, "pod"}, {claim.ControlToken, "other-pod"}, {claim.RunToken, "pod"}} {
		if got := workerRequest(s, "POST", path, tc.token, tc.worker, body).Code; got != http.StatusUnauthorized {
			t.Fatalf("token %q worker %q: %d, want 401", tc.token, tc.worker, got)
		}
	}
	w = workerRequest(s, "POST", path, claim.ControlToken, "pod", body)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"accepted":2`) {
		t.Fatalf("own capability: %d %s", w.Code, w.Body)
	}
	if _, err := testStore.ReportOutcome(context.Background(), claim.RunToken, store.Report(work.OutcomeNoChangeNeeded, "done", "", nil, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if got := workerRequest(s, "POST", path, claim.ControlToken, "pod", transcriptBody(3, 1, "late")).Code; got != http.StatusUnauthorized {
		t.Fatalf("after the outcome: %d, want 401", got)
	}
}

func TestRunTranscriptRefusesMalformedBatches(t *testing.T) {
	s, token, _ := transcriptServer(t, []string{AllTeams})
	path := "/api/v1/runs/" + token + "/transcript"
	for name, body := range map[string]string{
		"no entries":   `{"entries":[]}`,
		"not json":     `entries`,
		"unknown kind": `{"entries":[{"seq":1,"at":"2026-10-11T12:00:00Z","kind":"shell"}]}`,
		"zero seq":     `{"entries":[{"seq":0,"at":"2026-10-11T12:00:00Z","kind":"stage"}]}`,
		"too many":     transcriptBody(1, transcript.MaxBatchEntries+1, "x"),
	} {
		if got := workerRequest(s, "POST", path, "", "pod", body).Code; got != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, got)
		}
	}
	if got := workerRequest(s, "POST", "/api/v1/runs/"+strings.Repeat("0", 48)+"/transcript", "", "pod", transcriptBody(1, 1, "x")).Code; got != http.StatusNotFound {
		t.Errorf("unknown run: %d, want 404", got)
	}
	s.DisableRunTranscripts = true
	if got := workerRequest(s, "POST", path, "", "pod", transcriptBody(1, 1, "x")).Code; got != http.StatusNotFound {
		t.Errorf("switched off: %d, want 404", got)
	}
}

func TestRunTranscriptIsRedactedWhenStored(t *testing.T) {
	s, token, operator := transcriptServer(t, []string{AllTeams})
	body, _ := json.Marshal(map[string]any{"entries": []transcript.Entry{
		{Seq: 1, At: time.Now(), Kind: transcript.KindToolOutput, Text: "run " + token + " key sk-abcdefghijklmnopqrstuvwx " + strings.Repeat("z", 3*transcript.MaxTextBytes)},
		{Seq: 2, Kind: transcript.KindStage, Text: "no time given"},
		{Seq: 3, At: time.Now(), Kind: transcript.KindToolCall, ToolCallID: "c1", Title: "Run tests", ToolKind: "shell", Status: "running",
			Input: "go test ./...", Paths: []string{"main.go"}, Plan: []transcript.Step{{Content: "x", Status: "blocked"}}},
	}})
	if w := workerRequest(s, "POST", "/api/v1/runs/"+token+"/transcript", "", "pod", string(body)); w.Code != http.StatusOK {
		t.Fatalf("append: %d %s", w.Code, w.Body)
	}
	var page transcriptResponse
	if err := json.Unmarshal(operatorSchemaGET(t, s, operator, fmt.Sprintf("runs/%d/transcript", runIDOf(t, token))), &page); err != nil {
		t.Fatal(err)
	}
	if tool := page.Entries[2]; tool.ToolKind != "other" || tool.Status != "" || tool.Plan[0].Status != "pending" || tool.Input != "go test ./..." {
		t.Fatalf("tool call stored as %+v", tool)
	}
	first := page.Entries[0]
	if strings.Contains(first.Text, token) || strings.Contains(first.Text, "sk-abcdefghij") || !strings.Contains(first.Text, "[redacted]") {
		t.Fatalf("stored text still carries a secret: %.200s", first.Text)
	}
	if len(first.Text) > transcript.MaxTextBytes || !first.Clipped {
		t.Fatalf("stored text is %d bytes, clipped=%v", len(first.Text), first.Clipped)
	}
	if page.Entries[1].At.IsZero() {
		t.Fatal("an entry without a time was stored without one")
	}
}

func TestOperatorReadsARunTranscriptPageByPage(t *testing.T) {
	s, token, operator := transcriptServer(t, []string{"bronze"})
	if w := workerRequest(s, "POST", "/api/v1/runs/"+token+"/transcript", "", "pod", transcriptBody(1, 5, "step")); w.Code != http.StatusOK {
		t.Fatalf("append: %d %s", w.Code, w.Body)
	}
	id := runIDOf(t, token)
	base := fmt.Sprintf("runs/%d/transcript", id)

	var page transcriptResponse
	if err := json.Unmarshal(operatorSchemaGET(t, s, operator, base+"?limit=3"), &page); err != nil {
		t.Fatal(err)
	}
	if page.RunID != fmt.Sprint(id) || len(page.Entries) != 3 || page.NextAfter != 3 || !page.HasMore || page.Complete || page.Truncated {
		t.Fatalf("first page: %+v", page)
	}
	if err := json.Unmarshal(operatorSchemaGET(t, s, operator, fmt.Sprintf("%s?after=%d&limit=3", base, page.NextAfter)), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 2 || page.Entries[0].Seq != 4 || page.NextAfter != 5 || page.HasMore {
		t.Fatalf("second page: %+v", page)
	}
	if err := json.Unmarshal(operatorSchemaGET(t, s, operator, base+"?after=5"), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 0 || page.NextAfter != 5 {
		t.Fatalf("caught-up page: %+v", page)
	}
	if _, err := testStore.ReportOutcome(context.Background(), token, store.Report(work.OutcomeNoChangeNeeded, "done", "", nil, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(operatorSchemaGET(t, s, operator, base+"?after=5"), &page); err != nil || !page.Complete {
		t.Fatalf("ended run: %+v %v", page, err)
	}

	for _, query := range []string{"?limit=0", "?limit=501", "?after=-1", "?after=x", "?cursor=1", "?after=1&after=2"} {
		if w := operatorGet(s, operator, "/api/v1/operator/"+base+query); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", query, w.Code)
		}
	}
	if w := operatorGet(s, "", "/api/v1/operator/"+base); w.Code != http.StatusUnauthorized {
		t.Errorf("no credential: %d", w.Code)
	}
	if w := operatorGet(s, operator, fmt.Sprintf("/api/v1/operator/runs/%d/transcript", id+1000)); w.Code != http.StatusNotFound {
		t.Errorf("unknown run: %d", w.Code)
	}

	gold, goldToken := operatorTestConsumers(t, []string{"gold"}, false)
	s.OperatorConfig.Consumers = gold
	if w := operatorGet(s, goldToken, "/api/v1/operator/"+base); w.Code != http.StatusNotFound {
		t.Errorf("another team's consumer: %d, want 404", w.Code)
	}
}

func TestTranscriptBodiesMatchTheRunAPIContract(t *testing.T) {
	path, err := filepath.Abs("../../docs/contracts/run-api.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	full := transcript.Entry{Seq: 3, At: time.Now(), Kind: transcript.KindToolCall, Text: "t", ToolCallID: "c1", Title: "Run", ToolKind: "execute",
		Status: "completed", Input: "go test", Paths: []string{"a.go"}, Plan: []transcript.Step{{Content: "x", Status: "pending"}}, Clipped: true}
	for def, body := range map[string]any{
		"transcriptRequest":        transcriptRequest{Entries: []transcript.Entry{full}},
		"transcriptAppendResponse": transcriptAppendResponse{Accepted: 1},
	} {
		schema, err := jsonschema.NewCompiler().Compile(path + "#/$defs/" + def)
		if err != nil {
			t.Fatalf("%s: %v", def, err)
		}
		b, _ := json.Marshal(body)
		instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(instance); err != nil {
			t.Errorf("%s violates the contract: %v", def, err)
		}
	}
}
