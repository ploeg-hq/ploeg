package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestOperatorRunsExplainTheirFailure(t *testing.T) {
	reset(t)
	ctx := context.Background()
	if _, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "2240", Team: "silver", Title: "t"}); err != nil {
		t.Fatal(err)
	}
	claimed, err := testStore.Claim(ctx, "silver", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	reason := string(work.FailureHarnessConfig)
	if _, err := testStore.ReportOutcome(ctx, claimed.RunToken, store.Report(work.OutcomeFailed,
		"acp agent rejected its configuration: Error: 6 validation errors for MCPConfig", "", nil, nil, &reason)); err != nil {
		t.Fatal(err)
	}
	consumers, token := operatorTestConsumers(t, []string{"silver"}, false)
	s := &Server{Store: testStore, OperatorConfig: OperatorConfig{Consumers: consumers}}

	list := operatorGet(s, token, "/api/v1/operator/runs")
	validateOperatorSchema(t, list.Body.Bytes())
	var page struct {
		Runs []struct {
			ID      string                   `json:"id"`
			Failure *work.FailureExplanation `json:"failure"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil || len(page.Runs) != 1 {
		t.Fatalf("runs: %v %s", err, list.Body)
	}
	if f := page.Runs[0].Failure; f == nil || f.Reason != reason || f.Cause != work.CauseSystem || f.Action == "" {
		t.Fatalf("run list failure = %+v, want harness_config explained as a system cause", f)
	}

	detail := operatorGet(s, token, "/api/v1/operator/runs/"+page.Runs[0].ID)
	validateOperatorSchema(t, detail.Body.Bytes())
	var one struct {
		Run struct {
			Failure *work.FailureExplanation `json:"failure"`
		} `json:"run"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &one); err != nil {
		t.Fatal(err)
	}
	if f := one.Run.Failure; f == nil || f.Cause != work.CauseSystem {
		t.Fatalf("run detail failure = %+v, want a system cause", f)
	}
}

func TestOperatorShiftExplainsAFailureClose(t *testing.T) {
	path, err := filepath.Abs("../../docs/contracts/operator-api.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := jsonschema.NewCompiler().Compile(path + "#/$defs/shift")
	if err != nil {
		t.Fatal(err)
	}
	closed := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	for reason, want := range map[string]any{
		work.CloseRunCapReached: string(work.CauseTicket),
		work.CloseHarnessConfig: string(work.CauseSystem),
		"review_approved":       nil,
	} {
		body, err := json.Marshal(store.OperatorShift{ID: "1", WorkItemID: "1", Team: "bronze", OpenedAt: closed, ClosedAt: &closed, CloseReason: reason})
		if err != nil {
			t.Fatal(err)
		}
		instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(instance); err != nil {
			t.Fatalf("%s: shift violates the published schema: %v\n%s", reason, err, body)
		}
		var got struct {
			CloseFailure *struct {
				Cause string `json:"cause"`
			} `json:"closeFailure"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		switch {
		case want == nil && got.CloseFailure != nil:
			t.Errorf("%s: closeFailure = %+v, want null for a close that is not a failure", reason, got.CloseFailure)
		case want != nil && (got.CloseFailure == nil || got.CloseFailure.Cause != want):
			t.Errorf("%s: closeFailure = %+v, want cause %v", reason, got.CloseFailure, want)
		}
	}
}
