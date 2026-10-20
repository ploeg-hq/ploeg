package taskprompt

import (
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func limitedSpec(limits harness.RunLimits) harness.TaskSpec {
	return harness.TaskSpec{
		WorkItem: work.WorkItem{ID: "1", ExternalID: "42", Title: "Fix the thing"},
		Role:     "builder",
		Repo:     harness.RepoRef{Forge: harness.ForgeForgejo, ForgeURL: "https://forge.example", Owner: "o", Name: "r", BaseBranch: "development"},
		Branch:   "agent/vik-42",
		TraceID:  "ploeg-000000000000",
		Limits:   limits,
	}
}

func yourRun(t *testing.T, prompt string) string {
	t.Helper()
	start := strings.Index(prompt, "## Your Run\n")
	if start < 0 {
		return ""
	}
	rest := prompt[start:]
	if end := strings.Index(rest[len("## Your Run\n"):], "\n## "); end >= 0 {
		return rest[:len("## Your Run\n")+end]
	}
	return rest
}

func TestAWritingRunIsToldItsLimitsAndTheOrientationBudget(t *testing.T) {
	spec := limitedSpec(harness.RunLimits{Time: 100 * time.Minute, Idle: 15 * time.Minute, BudgetUSD: 2, Round: 3, MaxRounds: 5})
	got := yourRun(t, Compose(spec, true, "", false))
	want := `## Your Run

- Time: Ploeg stops this Run 100 minutes after your prompt starts, or after 15 minutes without activity (no output, tool call or model call). A stopped Run delivers nothing, so leave time to finish.
- Model budget: US$ 2.00 for this Run. Model calls past it are refused.
- Round 3 of at most 5 in this Shift.
- Orient for at most about 10 minutes or 30 tool calls: read what the Work Item needs, not the whole repository. Then write a short numbered plan (in your plan or todo tool if you have one), then edit.
- Commit after each completed plan step and push it to agent/vik-42, so a retry continues from your work instead of starting over. If agent/vik-42 already exists on the forge, an earlier attempt pushed it: continue from it.
`
	if got != want {
		t.Fatalf("Your Run =\n%s\nwant\n%s", got, want)
	}
}

func TestAReadingRunGetsItsLimitsWithoutTheWritersGuidance(t *testing.T) {
	spec := limitedSpec(harness.RunLimits{Time: 45 * time.Minute, Idle: 10 * time.Minute, BudgetUSD: 0.25, Round: 2, MaxRounds: 4})
	for name, prompt := range map[string]string{
		"reviewer": Compose(spec, false, "", true),
		"planner":  ComposePlanner(spec),
	} {
		t.Run(name, func(t *testing.T) {
			got := yourRun(t, prompt)
			for _, want := range []string{"45 minutes after your prompt starts", "10 minutes without activity", "US$ 0.25", "Round 2 of at most 4"} {
				if !strings.Contains(got, want) {
					t.Errorf("Your Run lacks %q:\n%s", want, got)
				}
			}
			for _, writerOnly := range []string{"Orient for at most", "Commit after each", "push it to"} {
				if strings.Contains(got, writerOnly) {
					t.Errorf("a reading Run was told %q:\n%s", writerOnly, got)
				}
			}
		})
	}
}

func TestUnknownLimitsAreLeftOutNotPrintedAsZero(t *testing.T) {
	reader := yourRun(t, Compose(limitedSpec(harness.RunLimits{}), false, "", true))
	if reader != "" {
		t.Fatalf("a reading Run with no known limits got a section:\n%s", reader)
	}

	writer := yourRun(t, Compose(limitedSpec(harness.RunLimits{Idle: 90 * time.Second, Round: 1}), true, "", false))
	for _, absent := range []string{"Model budget", "after your prompt starts", "at most 0", " 0 minutes", "US$ 0.00"} {
		if strings.Contains(writer, absent) {
			t.Errorf("Your Run states an unknown limit (%q):\n%s", absent, writer)
		}
	}
	for _, want := range []string{"after 1 minute without activity", "Round 1 of this Shift.", "Orient for at most"} {
		if !strings.Contains(writer, want) {
			t.Errorf("Your Run lacks %q:\n%s", want, writer)
		}
	}
}

func TestTheSectionComesBeforeTheDeliveryContract(t *testing.T) {
	prompt := Compose(limitedSpec(harness.RunLimits{Time: time.Hour}), true, "", false)
	run, contract := strings.Index(prompt, "## Your Run"), strings.Index(prompt, "## Delivery contract")
	if run < 0 || contract < 0 || run > contract {
		t.Fatalf("Your Run at %d, delivery contract at %d:\n%s", run, contract, prompt)
	}
}
