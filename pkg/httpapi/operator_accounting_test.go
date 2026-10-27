package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type accountingResponse struct {
	SchemaVersion string                      `json:"schemaVersion"`
	Accounting    store.OperatorRunAccounting `json:"accounting"`
}

func readOnlyOperatorConsumers(t *testing.T, teams []string) ([]OperatorConsumer, string) {
	t.Helper()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(secret)
	raw, _ := json.Marshal([]map[string]any{{"name": "dashboard", "tokenEnv": "TEST_OPERATOR_CREDENTIAL", "teams": teams, "execute": false, "readContent": false}})
	consumers, err := ParseOperatorConsumers(string(raw), func(name string) (string, bool) { return token, name == "TEST_OPERATOR_CREDENTIAL" })
	if err != nil {
		t.Fatal(err)
	}
	if consumers[0].Principal.CanReadContent || consumers[0].Principal.CanExecute {
		t.Fatalf("fixture consumer is not read-only: %+v", consumers[0].Principal)
	}
	return consumers, token
}

func getRunAccounting(t *testing.T, consumers []OperatorConsumer, token, path string) (int, string) {
	t.Helper()
	s := &Server{Store: testStore, OperatorConfig: OperatorConfig{Consumers: consumers}}
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

func decodeRunAccounting(t *testing.T, raw string) store.OperatorRunAccounting {
	t.Helper()
	path, err := filepath.Abs("../../docs/contracts/operator-api.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := jsonschema.NewCompiler().Compile(path)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader([]byte(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("run accounting violates published schema: %v\n%s", err, raw)
	}
	var body accountingResponse
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	if body.SchemaVersion != "1.0" {
		t.Fatalf("envelope: %s", raw)
	}
	return body.Accounting
}

func eventActions(events []store.OperatorAccountingEvent) []string {
	actions := make([]string, 0, len(events))
	for _, e := range events {
		actions = append(actions, e.Action)
	}
	return actions
}

func TestARunsSpendIsTracedFromAuthorizationToCorrectionWithoutTheDatabase(t *testing.T) {
	reset(t)
	ctx := context.Background()
	token, runID := seedOperatorUnsettled(t, "801", "silver", "blocked", true, 2, time.Hour)
	alias := "ploeg-" + token[:12]
	if err := testStore.RecordLLMObserved(ctx, token, 0.5); err != nil {
		t.Fatal(err)
	}
	first := fmt.Sprintf("litellm:spend-logs alias=%s keys=1 entries=3 usd=0.7500", alias)
	if _, err := testStore.SettleLLMAccount(ctx, token, store.LLMSettlement{Spend: 0.75, Evidence: first, CostKnown: true, CorrectionWindow: time.Hour}); err != nil {
		t.Fatal(err)
	}
	late := fmt.Sprintf("litellm:spend-logs alias=%s keys=1 entries=4 usd=0.9000", alias)
	if _, err := testStore.SettleLLMAccount(ctx, token, store.LLMSettlement{Spend: 0.9, Evidence: late, CostKnown: true, CorrectionWindow: time.Hour}); err != nil {
		t.Fatal(err)
	}

	consumers, credential := readOnlyOperatorConsumers(t, []string{"silver"})
	code, raw := getRunAccounting(t, consumers, credential, fmt.Sprintf("/api/v1/operator/runs/%d/accounting", runID))
	if code != 200 {
		t.Fatalf("a read-only consumer of the Run's Team: %d %s", code, raw)
	}
	got := decodeRunAccounting(t, raw)
	if got.RunID != fmt.Sprint(runID) || got.Team != "silver" || got.RunState != "finished" || got.WorkItemID == nil {
		t.Fatalf("run identity: %s", raw)
	}
	if got.AuthorizedUSD != 2 || got.HeldUSD != 0 || got.CostStatus != "provisional" || got.HarnessReportedUSD != nil {
		t.Fatalf("authorization, hold and cost status: %s", raw)
	}
	a := got.Account
	if a == nil || a.State != "reconciled" || a.KeyAlias != alias || a.ObservedUSD == nil || *a.ObservedUSD != 0.5 ||
		a.SettledUSD == nil || *a.SettledUSD != 0.75 || a.ReconciledUSD == nil || *a.ReconciledUSD != 0.9 ||
		!a.CostKnown || a.Evidence != first || a.CorrectionsUntil == nil || a.SettledAt == nil || a.ListedUnsettled {
		t.Fatalf("account: %s", raw)
	}
	if len(got.Adjustments) != 1 || got.Adjustments[0].PreviousUSD != 0.75 || got.Adjustments[0].SpendUSD != 0.9 || got.Adjustments[0].Evidence != late {
		t.Fatalf("adjustments: %s", raw)
	}
	if got.OperatorSettlement != nil {
		t.Fatalf("a sweep settlement reported an operator settlement: %s", raw)
	}
	want := []string{"llm.reserved", "llm.observed", "llm.reconciled", "llm.reconciled"}
	if fmt.Sprint(eventActions(got.Events)) != fmt.Sprint(want) || got.EventsTruncated {
		t.Fatalf("events %v, want %v: %s", eventActions(got.Events), want, raw)
	}
	settled, corrected := got.Events[2], got.Events[3]
	if settled.SpendUSD == nil || *settled.SpendUSD != 0.75 || settled.DeltaUSD == nil || *settled.DeltaUSD != 0.75 || settled.Adjustment {
		t.Fatalf("first settlement event: %s", raw)
	}
	if !corrected.Adjustment || corrected.DeltaUSD == nil || *corrected.DeltaUSD != 0.15 {
		t.Fatalf("correction event: %s", raw)
	}
	for _, leak := range []string{token, "key-" + token[:8], "Unsettled 801"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("run accounting carries %q, which is a credential, a key id or tracker text: %s", leak, raw)
		}
	}
}

func TestAnOperatorSettlementShowsInTheRunsAccounting(t *testing.T) {
	reset(t)
	ctx := context.Background()
	_, runID := seedOperatorUnsettled(t, "802", "silver", "unknown", true, 3, 4*time.Hour)
	consumers, credential := readOnlyOperatorConsumers(t, []string{AllTeams})
	path := fmt.Sprintf("/api/v1/operator/runs/%d/accounting", runID)

	code, raw := getRunAccounting(t, consumers, credential, path)
	if code != 200 {
		t.Fatalf("before settlement: %d %s", code, raw)
	}
	before := decodeRunAccounting(t, raw)
	if before.Account == nil || before.Account.State != "unknown" || !before.Account.ListedUnsettled || before.HeldUSD != 3 || before.CostStatus != "unknown" {
		t.Fatalf("an unsettled account is not shown as listed with its hold: %s", raw)
	}

	if _, err := testStore.SettleLLMAccountFromEvidence(ctx, runID, []string{"silver"}, store.OperatorSettlementCommand{
		Consumer: "workbench", CommandID: "settle-802", Actor: "operator:workbench:alice", AmountUSD: 1.25, Evidence: "provider invoice 2026-10 line 3",
	}); err != nil {
		t.Fatal(err)
	}
	code, raw = getRunAccounting(t, consumers, credential, path)
	if code != 200 {
		t.Fatalf("after settlement: %d %s", code, raw)
	}
	after := decodeRunAccounting(t, raw)
	o := after.OperatorSettlement
	if o == nil || o.Consumer != "workbench" || o.CommandID != "settle-802" || o.Actor != "operator:workbench:alice" ||
		o.PreviousState != "unknown" || o.SettledUSD != 1.25 || o.ReleasedUSD != 3 || o.Evidence != "provider invoice 2026-10 line 3" {
		t.Fatalf("operator settlement: %s", raw)
	}
	if after.Account.State != "reconciled" || after.Account.ListedUnsettled || after.HeldUSD != 0 || after.CostStatus != "settled" ||
		!strings.HasPrefix(after.Account.Evidence, "operator:evidence consumer=workbench command=settle-802") {
		t.Fatalf("account after an operator settlement: %s", raw)
	}
	last := after.Events[len(after.Events)-1]
	if last.Action != "llm.reconciled" || !last.OperatorEvidence || last.Actor != "operator:workbench:alice" || last.DeltaUSD == nil || *last.DeltaUSD != 1.25 {
		t.Fatalf("operator settlement event: %s", raw)
	}
}

func TestRunAccountingIsRefusedOutsideTheConsumersTeams(t *testing.T) {
	reset(t)
	_, runID := seedOperatorUnsettled(t, "803", "silver", "issued", true, 1, time.Hour)
	path := fmt.Sprintf("/api/v1/operator/runs/%d/accounting", runID)
	for _, tc := range []struct {
		name  string
		teams []string
	}{
		{"another Team", []string{"gold"}},
		{"no Team", []string{}},
	} {
		consumers, credential := operatorTestConsumers(t, tc.teams, true)
		code, raw := getRunAccounting(t, consumers, credential, path)
		if code != 404 || !strings.Contains(raw, `"not_found"`) || strings.Contains(raw, "silver") {
			t.Fatalf("%s read a silver Run's accounting: %d %s", tc.name, code, raw)
		}
	}
	consumers, credential := readOnlyOperatorConsumers(t, []string{"silver"})
	for _, tc := range []struct {
		path string
		code int
	}{
		{fmt.Sprintf("/api/v1/operator/runs/%d/accounting", runID+1000), 404},
		{path + "?limit=5", 400},
		{"/api/v1/operator/runs/abc/accounting", 400},
	} {
		if code, raw := getRunAccounting(t, consumers, credential, tc.path); code != tc.code {
			t.Fatalf("%s: %d %s, want %d", tc.path, code, raw, tc.code)
		}
	}
	if code, raw := getRunAccounting(t, consumers, "not-a-consumer-token", path); code != 401 {
		t.Fatalf("an unknown credential read run accounting: %d %s", code, raw)
	}
}
