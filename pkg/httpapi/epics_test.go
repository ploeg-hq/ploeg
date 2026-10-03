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

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/vikunja"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type fakeRelations struct {
	mu      sync.Mutex
	parents map[string]string
	reads   int
}

func (f *fakeRelations) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := strings.CutPrefix(r.URL.Path, "/tasks/")
	if !ok || strings.Contains(id, "/") || r.URL.Query().Get("expand") != "" {
		http.NotFound(w, r)
		return
	}
	f.reads++
	related := "{}"
	if parent := f.parents[id]; parent != "" {
		related = fmt.Sprintf(`{"parenttask":[{"id":%s,"title":"Epic %s"}]}`, parent, parent)
	}
	fmt.Fprintf(w, `{"id":%s,"title":"task %s","project_id":10,"related_tasks":%s}`, id, id, related)
}

func TestTrackerWebhook_RecordsEpicsBeforeTheFirstShiftAndShowsTheSet(t *testing.T) {
	reset(t)
	relations := &fakeRelations{parents: map[string]string{"2001": "2000", "2002": "2000"}}
	api := httptest.NewServer(relations)
	t.Cleanup(api.Close)
	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	s := &Server{
		Store: testStore, Log: slog.New(slog.DiscardHandler),
		Trackers:       map[string]provider.TrackerProvider{"vikunja": &vikunja.Provider{Secret: testTrackerSecret, BaseURL: api.URL, Token: "fixture", DefaultTeam: "silver"}},
		OperatorConfig: OperatorConfig{Consumers: consumers, Teams: map[string][]string{"silver": {"builder"}}},
	}
	h := s.Handler()
	post := func(event, id string) {
		t.Helper()
		body := fmt.Sprintf(`{"event_name":%q,"data":{"task":{"id":%s,"project_id":10},"assignee":{"username":"ploeg"}}}`, event, id)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, signedTrackerHook("vikunja", body))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%s returned %d: %s", event, rec.Code, rec.Body)
		}
	}

	post("task.updated", "2001")
	if relations.reads != 0 {
		t.Fatalf("a task without a Work Item had its relations read %d times", relations.reads)
	}
	post("task.assignee.created", "2001")
	post("task.assignee.created", "2002")
	ctx := context.Background()
	var ids []int64
	for _, ext := range []string{"2001", "2002"} {
		id, _, err := testStore.TrackerWorkItemID(ctx, "vikunja", ext)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := testStore.OpenShift(ctx, id, "silver", "agent/vik-"+ext, 0); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	relations.mu.Lock()
	relations.parents["2003"] = "2000"
	relations.mu.Unlock()
	if _, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "2003", Team: "silver", Title: "late"}); err != nil {
		t.Fatal(err)
	}
	late, _, _ := testStore.TrackerWorkItemID(ctx, "vikunja", "2003")
	if _, err := testStore.OpenShift(ctx, late, "silver", "agent/vik-2003", 0); err != nil {
		t.Fatal(err)
	}
	post("task.updated", "2003")

	raw := operatorSchemaGET(t, s, token, fmt.Sprintf("work-items/%d/card", ids[1]))
	var body struct {
		Card store.OperatorCard `json:"card"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	set := body.Card.Set
	if set == nil || set.Role != "child" || set.Size != 2 || *set.Position != 2 || set.Epic.Ref != "VIK-2000" ||
		set.Epic.Title != "Epic 2000" || set.Epic.WorkItemID != nil || set.Complete {
		t.Fatalf("set = %s", raw)
	}
	lateRaw := operatorSchemaGET(t, s, token, fmt.Sprintf("work-items/%d/card", late))
	if strings.Contains(string(lateRaw), `"set"`) {
		t.Fatalf("a child whose relation appeared after its first Shift has a set: %s", lateRaw)
	}
}
