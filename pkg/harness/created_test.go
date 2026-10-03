package harness

import (
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func createdEntry(kind work.CreatedKind) CreatedWorkItem {
	return CreatedWorkItem{Title: "Split the importer", Description: "Move parsing into its own package.", Ready: true, Kind: kind}
}

func TestOutcomeReport_CreatedWorkItemsMatchSchema(t *testing.T) {
	sch := compileSchema(t, "outcomereport.v1.schema.json")
	report := OutcomeReport{
		Outcome: work.OutcomeFollowUpCreated, Summary: "split in two",
		CreatedWorkItems: []CreatedWorkItem{
			createdEntry(work.CreatedSplit),
			{Title: "Decide the export format", Description: "", Ready: false, Kind: work.CreatedClarify, Team: "silver"},
			createdEntry(work.CreatedDiscovered),
		},
	}
	if err := validate(t, sch, report); err != nil {
		t.Errorf("report with created work items does not validate: %v", err)
	}
	if err := ValidateCreatedWorkItems(report.CreatedWorkItems); err != nil {
		t.Errorf("Go validation rejects a schema-valid report: %v", err)
	}
	if err := validate(t, sch, OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "x"}); err != nil {
		t.Errorf("a report without createdWorkItems no longer validates: %v", err)
	}
}

func TestOutcomeReport_SchemaRejectsMalformedCreatedWork(t *testing.T) {
	sch := compileSchema(t, "outcomereport.v1.schema.json")
	entry := func(overrides map[string]any) map[string]any {
		e := map[string]any{"title": "t", "description": "d", "ready": true, "kind": "split"}
		for k, v := range overrides {
			if v == nil {
				delete(e, k)
				continue
			}
			e[k] = v
		}
		return e
	}
	tooMany := make([]any, MaxCreatedWorkItems+1)
	for i := range tooMany {
		tooMany[i] = entry(nil)
	}
	for name, items := range map[string]any{
		"unknown kind":     []any{entry(map[string]any{"kind": "rewrite"})},
		"empty title":      []any{entry(map[string]any{"title": ""})},
		"long title":       []any{entry(map[string]any{"title": strings.Repeat("x", MaxCreatedTitleLen+1)})},
		"missing ready":    []any{entry(map[string]any{"ready": nil})},
		"missing kind":     []any{entry(map[string]any{"kind": nil})},
		"unknown field":    []any{entry(map[string]any{"priority": 3})},
		"too many entries": tooMany,
		"not an array":     entry(nil),
	} {
		if err := validate(t, sch, map[string]any{"outcome": "follow_up_created", "summary": "x", "createdWorkItems": items}); err == nil {
			t.Errorf("%s: schema accepted it", name)
		}
	}
}

func TestValidateCreatedWorkItems_MirrorsTheSchema(t *testing.T) {
	many := make([]CreatedWorkItem, MaxCreatedWorkItems+1)
	for i := range many {
		many[i] = createdEntry(work.CreatedSplit)
	}
	for name, items := range map[string][]CreatedWorkItem{
		"unknown kind":     {createdEntry("rewrite")},
		"blank title":      {{Title: "  ", Kind: work.CreatedSplit}},
		"long title":       {{Title: strings.Repeat("x", MaxCreatedTitleLen+1), Kind: work.CreatedSplit}},
		"long description": {{Title: "t", Description: strings.Repeat("x", MaxCreatedDescriptionLen+1), Kind: work.CreatedSplit}},
		"long team":        {{Title: "t", Team: strings.Repeat("x", MaxCreatedTeamLen+1), Kind: work.CreatedSplit}},
		"too many entries": many,
	} {
		if err := ValidateCreatedWorkItems(items); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestMergeDropBox_CreatedWorkItemsSurviveTheAdapter(t *testing.T) {
	base := OutcomeReport{Outcome: work.OutcomeFailed, Summary: "adapter classified a failure"}
	box := OutcomeReport{Outcome: work.OutcomeFollowUpCreated, Summary: "planned",
		CreatedWorkItems: []CreatedWorkItem{createdEntry(work.CreatedSplit)}}
	got := MergeDropBox(base, box)
	if got.Outcome != work.OutcomeFailed {
		t.Errorf("the agent overturned the adapter's outcome: %s", got.Outcome)
	}
	if len(got.CreatedWorkItems) != 1 {
		t.Errorf("created work items were dropped: %+v", got)
	}
}

func TestMergeDropBox_ProblemAndSolutionSurviveTheAdapter(t *testing.T) {
	base := OutcomeReport{Outcome: work.OutcomeFailed, Summary: "adapter classified a failure"}
	box := OutcomeReport{Problem: "Refunds over €500 fail.", Solution: "Check the limit first."}
	got := MergeDropBox(base, box)
	if got.Outcome != work.OutcomeFailed || got.Summary != base.Summary {
		t.Errorf("an outcome-less drop box changed the adapter's outcome: %+v", got)
	}
	if got.Problem != box.Problem || got.Solution != box.Solution {
		t.Errorf("the writer's account was dropped: %+v", got)
	}
}
