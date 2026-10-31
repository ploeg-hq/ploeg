package clickup

import (
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

	open := &Provider{DefaultTeam: "bronze"}
	if evs, err := post(t, open, assigneeEvent(person)); err != nil || len(evs) != 1 || evs[0].Kind != provider.TrackerAssigned {
		t.Fatalf("without agents every assignee dispatches: %+v, %v", evs, err)
	}
}
