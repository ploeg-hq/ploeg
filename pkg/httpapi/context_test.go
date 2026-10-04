package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func zipBundle(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type contextBody struct {
	Context store.WorkItemContext `json:"context"`
	Error   struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func contextServer(t *testing.T, teams []string, execute bool) (*Server, string) {
	t.Helper()
	consumers, token := operatorTestConsumers(t, teams, execute)
	return &Server{Store: testStore, LeaseTTL: time.Minute, Log: slog.New(slog.DiscardHandler),
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true}, OperatorConfig: OperatorConfig{Consumers: consumers}}, token
}

func uploadContext(t *testing.T, s *Server, token, path, name, note string, data []byte) (int, contextBody) {
	t.Helper()
	q := url.Values{"name": {name}}
	if note != "" {
		q.Set("note", note)
	}
	r := httptest.NewRequest(http.MethodPost, path+"?"+q.Encode(), bytes.NewReader(data))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Ploeg-Actor", "console")
	r.Header.Set("X-Ploeg-Acting-User", "ryan")
	r.Header.Set("Content-Type", "application/octet-stream")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	var body contextBody
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body
}

func ingestFor(t *testing.T, externalID, team string) int64 {
	t.Helper()
	id, _, err := testStore.IngestAssigned(context.Background(), work.WorkItem{Provider: "vikunja", ExternalID: externalID, Team: team, Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestOperatorContextUploadValidatesAndIsIdempotent(t *testing.T) {
	reset(t)
	s, token := contextServer(t, []string{"silver"}, true)
	id := ingestFor(t, "9001", "silver")
	path := "/api/v1/operator/work-items/" + strconv.FormatInt(id, 10) + "/context"
	bundle := zipBundle(t, map[string]string{"design/a.md": "# A\n", "b.txt": "b"})

	code, body := uploadContext(t, s, token, path, "uploads/design.zip", "the customer changed their mind", bundle)
	if code != 201 {
		t.Fatalf("upload: %d %+v", code, body)
	}
	c := body.Context
	if c.Name != "design.zip" || c.MediaType != "application/zip" || c.Files != 2 || c.AddedBy != "ryan" ||
		c.Phase != store.ContextBeforeStart || c.WorkItemID != strconv.FormatInt(id, 10) || len(c.SHA256) != 64 {
		t.Errorf("stored %+v", c)
	}
	if code, again := uploadContext(t, s, token, path, "design.zip", "", bundle); code != 200 || again.Context.ID != c.ID {
		t.Errorf("same bytes again: %d %+v", code, again)
	}

	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	var list struct {
		Context []store.WorkItemContext `json:"context"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Context) != 1 || list.Context[0].ID != c.ID {
		t.Errorf("list: %d %s", w.Code, w.Body)
	}

	for _, tc := range []struct {
		name, file string
		data       []byte
		status     int
		code, says string
	}{
		{"path traversal", "evil.zip", zipBundle(t, map[string]string{"../../etc/x": "x"}), 400, "unsafe_bundle", "path_traversal"},
		{"empty archive", "empty.zip", zipBundle(t, nil), 400, "unsafe_bundle", "no files"},
		{"control character in the name", "a\x01.md", []byte("x"), 400, "invalid_name", "control character"},
		{"no name", "", []byte("x"), 400, "invalid_name", "empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := uploadContext(t, s, token, path, tc.file, "", tc.data)
			if code != tc.status || body.Error.Code != tc.code || !strings.Contains(body.Error.Message, tc.says) {
				t.Errorf("got %d %+v, want %d %s naming %q", code, body.Error, tc.status, tc.code, tc.says)
			}
		})
	}
	if code, body := uploadContext(t, s, token, path, "n.md", strings.Repeat("n", 501), []byte("x")); code != 400 || body.Error.Code != "invalid_note" {
		t.Errorf("long note: %d %+v", code, body.Error)
	}

	s.ContextMaxBytes = 10
	if code, body := uploadContext(t, s, token, path, "big.md", "", []byte(strings.Repeat("x", 11))); code != 413 || body.Error.Code != "too_large" {
		t.Errorf("upload past the per-upload limit: %d %+v", code, body.Error)
	}
	s.ContextMaxBytes, s.ContextMaxTotalBytes = 0, int64(len(bundle))+5
	if code, body := uploadContext(t, s, token, path, "more.md", "", []byte("123456")); code != 413 || body.Error.Code != "total_too_large" {
		t.Errorf("upload past the Work Item total: %d %+v", code, body.Error)
	}
	s.ContextMaxTotalBytes = 0

	if code, _ := uploadContext(t, s, token, "/api/v1/operator/work-items/999999/context", "a.md", "", []byte("x")); code != 404 {
		t.Errorf("unknown Work Item: %d", code)
	}
	gold := ingestFor(t, "9002", "gold")
	if code, _ := uploadContext(t, s, token, "/api/v1/operator/work-items/"+strconv.FormatInt(gold, 10)+"/context", "a.md", "", []byte("x")); code != 404 {
		t.Errorf("Work Item outside the consumer's teams: %d", code)
	}
	forceState(t, id, "done")
	if code, body := uploadContext(t, s, token, path, "late.md", "", []byte("late")); code != 409 || body.Error.Code != "work_item_finished" {
		t.Errorf("finished Work Item: %d %+v", code, body.Error)
	}
	reader, readToken := contextServer(t, []string{"silver"}, false)
	if code, _ := uploadContext(t, reader, readToken, path, "a.md", "", []byte("x")); code != 403 {
		t.Errorf("a consumer without execute permission uploaded: %d", code)
	}
}

func forceState(t *testing.T, id int64, state string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `UPDATE work_items SET state = $1 WHERE id = $2`, state, id); err != nil {
		t.Fatal(err)
	}
}

func runContextGET(s *Server, token, id string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+token+"/context/"+id, nil))
	return w
}

// Steering reaches the next Run: the claim lists only what was attached
// before it, and the run route serves only that, only to that Run.
func TestClaimCarriesContextAndTheRunRouteServesOnlyItsOwn(t *testing.T) {
	reset(t)
	s, token := contextServer(t, []string{"bronze"}, true)
	shiftFixture(t, "9100", 10, []store.Role{{Name: "builder", Writes: true, Cap: 1}})
	id, _, err := testStore.TrackerWorkItemID(context.Background(), "vikunja", "9100")
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/operator/work-items/" + strconv.FormatInt(id, 10) + "/context"
	code, before := uploadContext(t, s, token, path, "notes.md", "read me", []byte("# Notes\n"))
	if code != 201 {
		t.Fatalf("upload: %d", code)
	}

	code, claim := postClaim(t, s.Handler(), `{"team":"bronze","role":"builder"}`)
	if code != 200 || len(claim.Context) != 1 {
		t.Fatalf("claim: %d context %+v", code, claim.Context)
	}
	ref := claim.Context[0]
	if ref.ID != before.Context.ID || ref.SHA256 != before.Context.SHA256 || ref.Bytes != 8 || ref.Note != "read me" || ref.MediaType != "text/markdown" {
		t.Errorf("claim ref %+v", ref)
	}

	code, steering := uploadContext(t, s, token, path, "later.md", "", []byte("# Changed my mind\n"))
	if code != 201 || steering.Context.Phase != store.ContextWhileSteering {
		t.Fatalf("steering upload: %d %+v", code, steering.Context)
	}

	if w := runContextGET(s, claim.RunToken, ref.ID); w.Code != 200 || w.Body.String() != "# Notes\n" ||
		w.Header().Get("Content-Type") != "application/octet-stream" {
		t.Errorf("run download: %d %q", w.Code, w.Body)
	}
	if w := runContextGET(s, claim.RunToken, steering.Context.ID); w.Code != 404 {
		t.Errorf("the running Run downloaded steering meant for the next Run: %d", w.Code)
	}
	other := ingestFor(t, "9101", "bronze")
	_, foreign := uploadContext(t, s, token, "/api/v1/operator/work-items/"+strconv.FormatInt(other, 10)+"/context", "x.md", "", []byte("other"))
	if w := runContextGET(s, claim.RunToken, foreign.Context.ID); w.Code != 404 {
		t.Errorf("a Run downloaded another Work Item's context: %d", w.Code)
	}
	if w := runContextGET(s, strings.Repeat("0", 48), ref.ID); w.Code != 404 {
		t.Errorf("an unknown Run downloaded context: %d", w.Code)
	}
}

func TestExecutionContextStoresAgainstItsWorkItem(t *testing.T) {
	reset(t)
	s, token := contextServer(t, []string{"silver"}, true)
	w := operatorExecutionRequest(s, "POST", "/api/v1/operator/executions", token, "console", operatorHTTPInput("context-session"))
	e := executionFromResponse(t, w)
	code, body := uploadContext(t, s, token, "/api/v1/operator/executions/"+e.ID+"/context", "brief.md", "", []byte("# Brief\n"))
	if code != 201 || body.Context.WorkItemID != e.WorkItemID {
		t.Fatalf("execution upload: %d %+v (execution Work Item %s)", code, body, e.WorkItemID)
	}
	if code, _ := uploadContext(t, s, token, "/api/v1/operator/executions/"+strings.Repeat("f", 32)+"/context", "a.md", "", []byte("x")); code != 404 {
		t.Errorf("unknown execution: %d", code)
	}
}

// The run route sits behind the managed worker-auth wrapper like every other
// run route: the Run's signed capability and worker identity, nothing less.
func TestManagedRunContextNeedsTheRunCapability(t *testing.T) {
	s, bootstrap := secureWorkerFixture(t, &managedBrokerFixture{})
	id, _, err := testStore.TrackerWorkItemID(context.Background(), "vikunja", "auth-fixture")
	if err != nil {
		t.Fatal(err)
	}
	stored, _, err := testStore.AddWorkItemContext(context.Background(), store.NewWorkItemContext{WorkItemID: id, Name: "n.md",
		MediaType: "text/markdown", SHA256: strings.Repeat("ab", 32), Files: 1, Content: []byte("# n\n"), AddedBy: "ryan", Actor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	w := workerRequest(s, "POST", "/api/v1/claim", bootstrap, "pod", `{"team":"bronze","role":"reviewer"}`)
	var claim struct {
		RunToken     string `json:"runToken"`
		ControlToken string `json:"controlToken"`
		Context      []struct {
			ID string `json:"id"`
		} `json:"context"`
	}
	if w.Code != 200 || json.NewDecoder(w.Body).Decode(&claim) != nil || len(claim.Context) != 1 || claim.Context[0].ID != stored.ID {
		t.Fatalf("managed claim=%d %s", w.Code, w.Body.String())
	}
	path := "/api/v1/runs/" + claim.RunToken + "/context/" + stored.ID
	for _, tc := range []struct{ token, worker string }{{"", "pod"}, {bootstrap, "pod"}, {claim.ControlToken, "other-pod"}} {
		if got := workerRequest(s, "GET", path, tc.token, tc.worker, "").Code; got != 401 {
			t.Errorf("download with token %q worker %s = %d, want 401", tc.token, tc.worker, got)
		}
	}
	if got := workerRequest(s, "GET", path, claim.ControlToken, "pod", ""); got.Code != 200 || got.Body.String() != "# n\n" {
		t.Errorf("download with the Run capability = %d %q", got.Code, got.Body)
	}
}
