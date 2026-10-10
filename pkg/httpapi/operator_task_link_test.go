package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/vikunja"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestOperatorReadsLinkVikunjaItemsStoredWithoutATaskURL(t *testing.T) {
	reset(t)
	ctx := context.Background()
	unlinked, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "1279", Team: "bronze", Title: "stored before links"})
	if err != nil {
		t.Fatal(err)
	}
	const stored = "https://tracker.example/tasks/1280"
	linked, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "1280", Team: "bronze", Title: "stored with a link", URL: stored})
	if err != nil {
		t.Fatal(err)
	}
	consumers, token := operatorTestConsumers(t, []string{"bronze"}, false)
	s := &Server{
		Store:          testStore,
		Trackers:       map[string]provider.TrackerProvider{"vikunja": &vikunja.Provider{BaseURL: "https://vikunja.example/api/v1"}},
		OperatorConfig: OperatorConfig{Consumers: consumers, Teams: map[string][]string{"bronze": {"builder"}}},
	}

	var list struct {
		Items []struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		} `json:"items"`
	}
	if err := json.Unmarshal(operatorSchemaGET(t, s, token, "work-items"), &list); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{fmt.Sprint(unlinked): "https://vikunja.example/tasks/1279", fmt.Sprint(linked): stored}
	if len(list.Items) != 2 {
		t.Fatalf("items = %+v, want two", list.Items)
	}
	for _, it := range list.Items {
		if it.URL != want[it.ID] {
			t.Errorf("work-items: item %s url = %q, want %q", it.ID, it.URL, want[it.ID])
		}
	}

	var detail struct {
		Item struct {
			URL string `json:"url"`
		} `json:"item"`
	}
	if err := json.Unmarshal(operatorSchemaGET(t, s, token, fmt.Sprintf("work-items/%d", unlinked)), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Item.URL != "https://vikunja.example/tasks/1279" {
		t.Errorf("work item detail url = %q", detail.Item.URL)
	}

	var facts struct {
		Facts struct {
			WorkItem struct {
				URL string `json:"url"`
			} `json:"workItem"`
		} `json:"facts"`
	}
	if err := json.Unmarshal(operatorSchemaGET(t, s, token, fmt.Sprintf("work-items/%d/facts", unlinked)), &facts); err != nil {
		t.Fatal(err)
	}
	if facts.Facts.WorkItem.URL != "https://vikunja.example/tasks/1279" {
		t.Errorf("facts url = %q", facts.Facts.WorkItem.URL)
	}

	s.Trackers = nil
	if err := json.Unmarshal(operatorSchemaGET(t, s, token, fmt.Sprintf("work-items/%d", unlinked)), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Item.URL != "" {
		t.Errorf("without a tracker the url was invented: %q", detail.Item.URL)
	}
}
