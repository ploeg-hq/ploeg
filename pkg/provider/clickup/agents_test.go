package clickup

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func assigneeEvent(items ...map[string]any) map[string]any {
	return map[string]any{"event": "taskAssigneeUpdated", "task_id": "t1", "history_items": items}
}

func TestAgentMembersDecideWhichAssignmentsCount(t *testing.T) {
	p := &Provider{Agents: []string{"agent@example.com"}, DefaultTeam: "bronze"}
	person := map[string]any{"field": "assignee_add", "after": map[string]string{"username": "person"}}
	agent := map[string]any{"field": "assignee_add", "after": map[string]string{"username": "bot", "email": "AGENT@example.com"}}
	personLeft := map[string]any{"field": "assignee_rem", "before": map[string]string{"username": "person"}}

	for name, body := range map[string]map[string]any{"a person assigned": assigneeEvent(person), "a person removed": assigneeEvent(personLeft)} {
		if evs, err := post(t, p, body); err != nil || len(evs) != 0 {
			t.Errorf("%s: events = %+v, %v; want none", name, evs, err)
		}
	}
	evs, err := post(t, p, assigneeEvent(personLeft, agent))
	if err != nil || len(evs) != 1 || evs[0].Kind != provider.TrackerAssigned {
		t.Fatalf("agent added beside a person removed = %+v, %v; want one assignment", evs, err)
	}
}

func TestWithoutAgentsAPersonAssigningThemselvesStartsNothing(t *testing.T) {
	p := &Provider{DefaultTeam: "bronze"}
	person := map[string]any{"field": "assignee_add", "after": map[string]any{"id": 7, "username": "person"}}
	personLeft := map[string]any{"field": "assignee_rem", "before": map[string]any{"id": 7, "username": "person"}}
	for name, body := range map[string]map[string]any{"assigned": assigneeEvent(person), "removed": assigneeEvent(personLeft)} {
		if evs, err := post(t, p, body); err != nil || len(evs) != 0 {
			t.Errorf("a person %s with no agents configured: events = %+v, %v; want none", name, evs, err)
		}
	}
}

func TestTheTokensOwnAccountHandsWorkOverWithoutConfiguration(t *testing.T) {
	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" || r.Header.Get("Authorization") != "pk_test" {
			http.NotFound(w, r)
			return
		}
		reads.Add(1)
		_, _ = io.WriteString(w, `{"user":{"id":42,"username":"ploeg"}}`)
	}))
	defer srv.Close()
	p := &Provider{DefaultTeam: "bronze", BaseURL: srv.URL, Token: "pk_test"}

	person := map[string]any{"field": "assignee_add", "after": map[string]any{"id": 7, "username": "person"}}
	if evs, err := post(t, p, assigneeEvent(person)); err != nil || len(evs) != 0 {
		t.Fatalf("a person assigned = %+v, %v; want none", evs, err)
	}
	self := map[string]any{"field": "assignee_add", "after": map[string]any{"id": "42", "username": "ploeg"}}
	evs, err := post(t, p, assigneeEvent(person, self))
	if err != nil || len(evs) != 1 || evs[0].Kind != provider.TrackerAssigned {
		t.Fatalf("own account assigned beside a person = %+v, %v; want one assignment", evs, err)
	}
	selfLeft := map[string]any{"field": "assignee_rem", "before": map[string]any{"id": 42, "username": "ploeg"}}
	evs, err = post(t, p, assigneeEvent(selfLeft))
	if err != nil || len(evs) != 1 || evs[0].Kind != provider.TrackerUnassigned {
		t.Fatalf("own account removed = %+v, %v; want one unassignment", evs, err)
	}
	if n := reads.Load(); n != 1 {
		t.Errorf("GET /user read %d times; want once, then cached", n)
	}
}

func TestAnUnreadOwnAccountIsRetriedAndIgnoresTheAssignmentMeanwhile(t *testing.T) {
	var up atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			http.Error(w, `{"err":"down"}`, http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, `{"user":{"id":42}}`)
	}))
	defer srv.Close()
	p := &Provider{DefaultTeam: "bronze", BaseURL: srv.URL, Token: "pk_test"}
	self := map[string]any{"field": "assignee_add", "after": map[string]any{"id": 42}}
	if evs, err := post(t, p, assigneeEvent(self)); err != nil || len(evs) != 0 {
		t.Fatalf("own account unknown: events = %+v, %v; want none", evs, err)
	}
	up.Store(true)
	if evs, err := post(t, p, assigneeEvent(self)); err != nil || len(evs) != 1 {
		t.Fatalf("own account readable again: events = %+v, %v; want one", evs, err)
	}
}
