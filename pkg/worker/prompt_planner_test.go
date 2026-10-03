package worker

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files")

func TestComposePlannerPrompt_Golden(t *testing.T) {
	spec := roleSpec("planner", []harness.Finding{{Role: "analyst", Round: 1, Findings: "## analyst\n- the importer and the exporter share no code"}})
	got := ComposePlannerPrompt(spec)

	path := filepath.Join("testdata", "planner_prompt.golden")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	if got != string(want) {
		t.Errorf("planner prompt drifted from %s (run go test ./pkg/worker -run Golden -update after reviewing):\n%s", path, got)
	}
}

func TestComposePlannerPrompt_AsksForCreatedWorkNotCode(t *testing.T) {
	task := ComposePlannerPrompt(roleSpec("planner", nil))
	for _, want := range []string{
		"# Your role: planner",
		"Delivery contract (planning only)",
		"Do NOT modify, commit or push",
		harness.DropBoxEnv,
		`"createdWorkItems"`,
		`"follow_up_created"`,
		"split", "clarify", "discovered",
	} {
		if !strings.Contains(task, want) {
			t.Errorf("planner prompt missing %q", want)
		}
	}
	for _, reject := range []string{"Agent-Trace-Id", "open a pull request", `"verdict"`} {
		if strings.Contains(task, reject) {
			t.Errorf("planner prompt carries another contract (%q)", reject)
		}
	}
}

func TestComposePlannerPrompt_ExampleIsAValidOutcome(t *testing.T) {
	task := ComposePlannerPrompt(roleSpec("planner", nil))
	start := strings.Index(task, `{"outcome": "follow_up_created"`)
	end := strings.Index(task[start:], "]}") + start + 2
	if start < 0 || end <= start {
		t.Fatalf("no example outcome in the planner prompt")
	}
	example := regexp.MustCompile(`"<[^"]*>"`).ReplaceAllString(task[start:end], `"x"`)
	var report harness.OutcomeReport
	dec := json.NewDecoder(bytes.NewBufferString(example))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&report); err != nil {
		t.Fatalf("example does not decode as an OutcomeReport: %v\n%s", err, example)
	}
	if report.Outcome != work.OutcomeFollowUpCreated || len(report.CreatedWorkItems) != 1 {
		t.Fatalf("example = %+v", report)
	}
	if err := harness.ValidateCreatedWorkItems(report.CreatedWorkItems); err != nil {
		t.Fatalf("example violates the contract: %v", err)
	}
}

func TestTaskPrompt_PlannerOnlyForReaders(t *testing.T) {
	spec := roleSpec("planner", nil)
	if got := taskPrompt(spec, true, false, "", false); got != ComposePlannerPrompt(spec) {
		t.Error("a planner reader did not get the planner prompt")
	}
	if got := taskPrompt(spec, false, false, "", false); got != ComposePrompt(spec, false, "", false) {
		t.Error("a reader that is not a planner lost its review prompt")
	}
}

func TestDiscardMalformedCreated_RecordsTheDiscard(t *testing.T) {
	report := harness.OutcomeReport{
		Outcome: work.OutcomeFollowUpCreated, Summary: "planned",
		CreatedWorkItems: []harness.CreatedWorkItem{{Title: "ok", Kind: work.CreatedSplit}, {Title: "bad", Kind: "rewrite"}},
	}
	if err := discardMalformedCreated(&report); err == nil {
		t.Fatal("a malformed entry was not reported")
	}
	if report.CreatedWorkItems != nil || !strings.Contains(report.Summary, "2 created Work Items discarded") {
		t.Fatalf("report = %+v", report)
	}

	valid := harness.OutcomeReport{Outcome: work.OutcomeFollowUpCreated, Summary: "planned",
		CreatedWorkItems: []harness.CreatedWorkItem{{Title: "ok", Kind: work.CreatedDiscovered}}}
	if err := discardMalformedCreated(&valid); err != nil || len(valid.CreatedWorkItems) != 1 || valid.Summary != "planned" {
		t.Fatalf("a valid report was changed: %+v, %v", valid, err)
	}
}

func TestResolveOutcome_CarriesCreatedWorkItems(t *testing.T) {
	created := []harness.CreatedWorkItem{{Title: "follow on", Kind: work.CreatedDiscovered, Ready: true}}
	cases := map[string]harness.OutcomeReport{
		"new pull request": resolveOutcome("exec", harness.OutcomeReport{CreatedWorkItems: created}, nil, nil,
			"https://forgejo.example/o/r/pulls/1", false, "t", "b", nil, false, true),
		"reader on a branch": resolveOutcome("exec", harness.OutcomeReport{CreatedWorkItems: created}, nil, nil,
			"https://forgejo.example/o/r/pulls/1", true, "t", "b", nil, false, false),
		"structured report": resolveOutcome("exec", harness.OutcomeReport{Outcome: work.OutcomeFollowUpCreated, Summary: "s", CreatedWorkItems: created}, nil, nil,
			"", false, "t", "b", nil, false, false),
	}
	for name, got := range cases {
		if len(got.CreatedWorkItems) != 1 || got.CreatedWorkItems[0].Title != "follow on" {
			t.Errorf("%s: created work items lost: %+v", name, got)
		}
	}
}
