package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/forgejo"
	"github.com/ploeg-hq/ploeg/pkg/provider/vikunja"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/work"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type operatorTrackerFixture struct {
	server   *Server
	token    string
	api      *httptest.Server
	revision string
	scope    int
	open     bool
	reads    int
	itemID   int64
}

func trackerOperatorHTTPFixture(t *testing.T) *operatorTrackerFixture {
	t.Helper()
	reset(t)
	f := &operatorTrackerFixture{revision: "2026-09-11T10:00:00Z", scope: 11, open: true}
	f.api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/tasks/585" {
			t.Errorf("unexpected provider side effect: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
			return
		}
		f.reads++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 585, "project_id": f.scope, "title": "Canonical fixture", "updated": f.revision, "done": !f.open})
	}))
	t.Cleanup(f.api.Close)
	consumers, token := operatorTestConsumers(t, []string{"silver"}, true)
	f.token = token
	f.server = &Server{Store: testStore, OperatorConfig: OperatorConfig{Consumers: consumers}, Trackers: map[string]provider.TrackerProvider{"vikunja": &vikunja.Provider{Secret: testTrackerSecret, BaseURL: f.api.URL + "/api/v1", Token: "fixture"}}, Forges: map[string]provider.ForgeProvider{"forgejo": &forgejo.Provider{BaseURL: "https://forge.example"}}}
	f.server.Targets, _ = target.NewMapResolver("11=webgrip/example@development", "forgejo")
	id, _, err := testStore.IngestAssigned(context.Background(), work.WorkItem{Provider: "vikunja", ExternalID: "585", Revision: f.revision, ExternalScope: "11", Team: "silver", Title: "Canonical fixture", Target: &work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "example", BaseBranch: "development"}})
	if err != nil {
		t.Fatal(err)
	}
	f.itemID = id
	return f
}

func (f *operatorTrackerFixture) lookupPath() string {
	return "/api/v1/operator/work-items/lookup?provider=vikunja&externalId=585&scope=11&baseUrl=" + url.QueryEscape(f.api.URL+"/api/v1/")
}

func (f *operatorTrackerFixture) source(t *testing.T) store.OperatorSource {
	t.Helper()
	w := operatorExecutionRequest(f.server, "GET", f.lookupPath(), f.token, "", nil)
	if w.Code != 200 {
		t.Fatalf("lookup without actor header: %d %s", w.Code, w.Body)
	}
	var response struct {
		SchemaVersion string               `json:"schemaVersion"`
		Item          store.OperatorItem   `json:"item"`
		Source        store.OperatorSource `json:"source"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.SchemaVersion != "1.0" || response.Source.WorkItemID != fmt.Sprint(f.itemID) || response.Source.ExpectedRevision != f.revision || response.Source.ExpectedScope != "11" || response.Item.Target == nil || response.Source.ExpectedTarget != *response.Item.Target {
		t.Fatalf("lookup lost binding expectations: %s", w.Body)
	}
	validateTrackerContract(t, w.Body.Bytes(), "lookup")
	return response.Source
}

func TestOperatorSourceHTTPBindsOneItemAndReplaysAfterProviderChanges(t *testing.T) {
	f := trackerOperatorHTTPFixture(t)
	source := f.source(t)
	input := operatorHTTPInput("canonical-source-http")
	input.Source = &source
	e := executionFromResponse(t, operatorExecutionRequest(f.server, "POST", "/api/v1/operator/executions", f.token, "alice", input))
	if e.WorkItemID != source.WorkItemID || f.reads != 2 {
		t.Fatalf("did not bind existing source after two fresh reads: %+v reads%d", e, f.reads)
	}
	f.open = false
	f.revision = "later"
	f.server.Targets = nil
	before := f.reads
	w := operatorExecutionRequest(f.server, "POST", "/api/v1/operator/executions", f.token, "alice", input)
	if w.Code != 200 || executionFromResponse(t, w).ID != e.ID || f.reads != before {
		t.Fatalf("accepted lost-response replay consulted changed provider: %d %s reads%d", w.Code, w.Body, f.reads)
	}
	if w := operatorExecutionRequest(f.server, "POST", "/api/v1/operator/executions", f.token, "bob", input); w.Code != 409 {
		t.Fatalf("another actor reused admission: %d %s", w.Code, w.Body)
	}
	items, _, err := testStore.OperatorItems(context.Background(), store.OperatorFilter{Teams: []string{"silver"}, Limit: 10})
	if err != nil || len(items) != 1 || items[0].ID != source.WorkItemID {
		t.Fatalf("tracker source became a duplicate manual item: %+v %v", items, err)
	}
}

func validateTrackerContract(t *testing.T, body []byte, definition string) {
	t.Helper()
	path, err := filepath.Abs("../../docs/contracts/tracker-execution.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := jsonschema.NewCompiler().Compile(path + "#/$defs/" + definition)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatal(err)
	}
}

func TestOperatorSourceHTTPRejectsUnavailableStaleClosedAndRetargetedSources(t *testing.T) {
	for _, mismatch := range []string{"provider-revision", "closed", "scope", "instance", "unknown-provider", "repository-url", "base-branch", "target", "work-item-revision", "work-item-timestamp", "current-route", "missing-resolver", "pinned-team"} {
		t.Run(mismatch, func(t *testing.T) {
			f := trackerOperatorHTTPFixture(t)
			source := f.source(t)
			input := operatorHTTPInput("source-rejected")
			input.Source = &source
			switch mismatch {
			case "provider-revision":
				f.revision = "new"
			case "closed":
				f.open = false
			case "scope":
				f.scope = 12
			case "instance":
				source.ExpectedBaseURL = "https://different.example/api/v1"
			case "unknown-provider":
				source.Provider = "unconfigured"
			case "repository-url":
				input.RepositoryURL = "https://elsewhere.example/webgrip/example.git"
			case "base-branch":
				input.BaseBranch = "main"
			case "target":
				source.ExpectedTarget.Repo = "different"
			case "current-route":
				f.server.Targets, _ = target.NewMapResolver("11=webgrip/other@development", "forgejo")
			case "missing-resolver":
				f.server.Targets = nil
			case "pinned-team":
				f.server.ScopeTeams = map[string]string{"11": "gold"}
			case "work-item-revision", "work-item-timestamp":
				newRevision := "later"
				if mismatch == "work-item-timestamp" {
					newRevision = f.revision
				}
				if _, _, err := testStore.IngestAssigned(context.Background(), work.WorkItem{Provider: "vikunja", ExternalID: "585", Revision: newRevision, ExternalScope: "11", Team: "silver", Title: "Updated", Target: &work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "example", BaseBranch: "development"}}); err != nil {
					t.Fatal(err)
				}
			}
			if mismatch == "current-route" || mismatch == "missing-resolver" || mismatch == "pinned-team" {
				if w := operatorExecutionRequest(f.server, "GET", f.lookupPath(), f.token, "", nil); w.Code != 409 {
					t.Fatalf("lookup ignored current routing authority: %d %s", w.Code, w.Body)
				}
			}
			w := operatorExecutionRequest(f.server, "POST", "/api/v1/operator/executions", f.token, "alice", input)
			if w.Code != 409 {
				t.Fatalf("mismatched source admitted: %d %s", w.Code, w.Body)
			}
			item, err := testStore.WorkItem(context.Background(), f.itemID)
			if err != nil || item.State != work.StateQueued {
				t.Fatalf("rejected source changed work or acquired paid authority: %+v %v", item, err)
			}
		})
	}
}

func TestOperatorSourceLookupEnforcesTeamAuthAndBoundedParameters(t *testing.T) {
	f := trackerOperatorHTTPFixture(t)
	other, token := operatorTestConsumers(t, []string{"gold"}, false)
	other[0].Principal.Name = "other-team"
	f.server.OperatorConfig.Consumers = append(f.server.OperatorConfig.Consumers, other...)
	for _, tc := range []struct {
		path, token string
		status      int
	}{
		{f.lookupPath(), "", 401},
		{f.lookupPath(), token, 404},
		{f.lookupPath() + "&scope=11", f.token, 400},
		{f.lookupPath() + "&arbitrary=true", f.token, 400},
		{"/api/v1/operator/work-items/lookup?provider=vikunja&externalId=585&scope=11&baseUrl=http://169.254.169.254", f.token, 400},
		{"/api/v1/operator/work-items/lookup?provider=vikunja&externalId=585&scope=11&baseUrl=https://different.example/api/v1", f.token, 409},
	} {
		w := operatorExecutionRequest(f.server, "GET", tc.path, tc.token, "", nil)
		if w.Code != tc.status {
			t.Fatalf("source lookup: %d want%d %s", w.Code, tc.status, w.Body)
		}
	}
	if f.reads != 0 {
		t.Fatalf("rejected lookup reached configured provider: %d", f.reads)
	}
}
