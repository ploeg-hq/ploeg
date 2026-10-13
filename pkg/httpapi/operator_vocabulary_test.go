package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestOperatorStateFilterReadsTheRetiredLeasedAsActive(t *testing.T) {
	reset(t)
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "vocabulary-active", Team: "silver", Title: "Active item"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.Claim(ctx, "silver", time.Minute); err != nil {
		t.Fatal(err)
	}
	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	s := &Server{Store: testStore, OperatorConfig: OperatorConfig{Consumers: consumers, Teams: map[string][]string{"silver": {"builder"}}}}
	for _, filter := range []string{"active", "leased", "active,leased"} {
		var page struct {
			Items []struct {
				ID    string `json:"id"`
				State string `json:"state"`
			} `json:"items"`
		}
		if err := json.Unmarshal(operatorSchemaGET(t, s, token, "work-items?state="+filter), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || page.Items[0].ID != itoa(id) || page.Items[0].State != "active" {
			t.Fatalf("state=%s: %+v, want the one item, reported active", filter, page.Items)
		}
	}
	r := httptest.NewRequest("GET", "/api/v1/operator/work-items?state=leased,leased", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("a repeated state answered %d, want 400", w.Code)
	}

	var summary struct {
		Totals struct {
			WorkItems map[string]int64 `json:"workItems"`
		} `json:"totals"`
	}
	if err := json.Unmarshal(operatorSchemaGET(t, s, token, "summary"), &summary); err != nil {
		t.Fatal(err)
	}
	if counts := summary.Totals.WorkItems; counts["active"] != 1 || counts["leased"] != counts["active"] {
		t.Fatalf("summary counts %v: want active 1 and the deprecated leased equal to it", counts)
	}
}

func TestPublishedCostStatusIsPloegsOneValueSet(t *testing.T) {
	raw, err := os.ReadFile("../../docs/contracts/operator-api.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Defs map[string]struct {
			Enum []string `json:"enum"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, status := range work.CostStatuses {
		want = append(want, string(status))
	}
	if got := schema.Defs["costStatus"].Enum; !slices.Equal(got, want) {
		t.Fatalf("schema costStatus %v, want work.CostStatuses %v", got, want)
	}
}
