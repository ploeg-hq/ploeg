package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestOperatorWorkItemShowsWhatHoldsItAndItsPremiseCheck(t *testing.T) {
	reset(t)
	ctx := context.Background()
	blocked, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "2042", Team: "silver", Title: "Blocked"})
	if err != nil {
		t.Fatal(err)
	}
	blocker, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "2041", Team: "silver", Title: "Schema first"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.RecordDependencies(ctx, blocked, "vikunja", []store.Dependency{
		{ExternalID: "2041", Title: "Schema first", Kind: "blocked_by"},
		{ExternalID: "2039", Title: "Done already", Kind: "follows", Done: true},
		{ExternalID: "999", Title: "Not in Ploeg", Kind: "follows"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := testStore.RecordPremiseVerdict(ctx, blocker, "silver", "free-qwen",
		store.PremiseCheck{Verdict: store.PremiseAsk, Question: "Which schema version?", Reason: "two named"}); err != nil {
		t.Fatal(err)
	}

	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	s := &Server{Store: testStore, OperatorConfig: OperatorConfig{Consumers: consumers, Teams: map[string][]string{"silver": {"builder"}}}}
	operatorSchemaGET(t, s, token, "work-items")
	var held struct {
		Item store.OperatorItem `json:"item"`
	}
	if err := json.Unmarshal(operatorSchemaGET(t, s, token, fmt.Sprintf("work-items/%d", blocked)), &held); err != nil {
		t.Fatal(err)
	}
	w := held.Item.WaitingFor
	if len(w) != 2 || w[0].ExternalID != "2041" || w[0].WorkItemID == nil || *w[0].WorkItemID != fmt.Sprint(blocker) ||
		w[1].ExternalID != "999" || w[1].WorkItemID != nil || held.Item.PremiseCheck != nil {
		t.Fatalf("held item = %+v / %+v", w, held.Item.PremiseCheck)
	}
	var asked struct {
		Item store.OperatorItem `json:"item"`
	}
	if err := json.Unmarshal(operatorSchemaGET(t, s, token, fmt.Sprintf("work-items/%d", blocker)), &asked); err != nil {
		t.Fatal(err)
	}
	p := asked.Item.PremiseCheck
	if asked.Item.State != "needs_human" || p == nil || p.Verdict == nil || *p.Verdict != "ask" || p.Question != "Which schema version?" ||
		p.Model != "free-qwen" || len(asked.Item.WaitingFor) != 0 {
		t.Fatalf("asked item = %s / %+v", asked.Item.State, p)
	}
}
