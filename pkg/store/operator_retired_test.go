package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenamedOperatorFieldsKeepTheirDeprecatedAliases(t *testing.T) {
	out, err := json.Marshal(OperatorTeamSummary{WorkItems: OperatorWorkItemCounts{Active: 3}, Spend: OperatorSpend{HeldUSD: 1.5}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"active":3`, `"leased":3`, `"heldUsd":1.5`, `"reservedUsd":1.5`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("summary %s lacks %s", out, want)
		}
	}
	shift, err := json.Marshal(OperatorShift{HeldUSD: 2})
	if err != nil || !strings.Contains(string(shift), `"heldUsd":2`) || !strings.Contains(string(shift), `"reservedUsd":2`) {
		t.Errorf("shift %s (%v) lacks heldUsd and its alias", shift, err)
	}

	var old OperatorTeamSummary
	if err := json.Unmarshal([]byte(`{"workItems":{"leased":4,"queued":1},"spend":{"settledUsd":1,"reservedUsd":0.5}}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.WorkItems.Active != 4 || old.WorkItems.Queued != 1 || old.Spend.HeldUSD != 0.5 || old.Spend.SettledUSD != 1 {
		t.Errorf("an older ploegd's summary decoded as %+v", old)
	}
	var oldShift OperatorShift
	if err := json.Unmarshal([]byte(`{"id":"7","reservedUsd":2.5}`), &oldShift); err != nil || oldShift.HeldUSD != 2.5 || oldShift.ID != "7" {
		t.Errorf("an older ploegd's shift decoded as %+v (%v)", oldShift, err)
	}
}
