package taskprompt

import (
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func prepareSpec() (spec harness.TaskSpec) {
	spec = hostileSpec()
	spec.WorkItem.Kind = work.KindPrepareRepository
	spec.WorkItem.Title = "Add AGENTS.md to o/r"
	spec.WorkItem.Description = hostileDescription
	return spec
}

func TestAPrepareRepositoryWriterGetsPloegsBriefAndNotTheDescription_Golden(t *testing.T) {
	prompt := Compose(prepareSpec(), true, "", false)

	if strings.Contains(prompt, "Push straight to development") {
		t.Errorf("the prepare writer was briefed with the stored description:\n%s", prompt)
	}
	for _, want := range []string{
		"# Work Item VIK-1304: prepare this repository",
		"adds AGENTS.md at the repository root and changes no other path",
		"cite for each the\n   file it came from",
		"Never invent a\n   command, a convention or a person",
		"Write no secret, host or credential",
		"\"Fast check\"",
		"## Delivery contract",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prepare brief lacks %q:\n%s", want, prompt)
		}
	}
	if strings.Index(prompt, "## Delivery contract") > strings.Index(prompt, "## Repository instructions") {
		t.Errorf("the repository instructions come before the delivery contract")
	}
	if strings.Contains(prompt, "is outside the Work Item unless the Work Item asks for it") {
		t.Errorf("the prepare writer got the ordinary writer's instruction-file rule")
	}
	assertGolden(t, "writer_prepare_repository", prompt)
}

func TestAPrepareRepositoryReaderChecksTheCitationsAndOmitsTheVerdict_Golden(t *testing.T) {
	spec := prepareSpec()
	spec.Role = "reviewer"
	prompt := Compose(spec, false, "https://forge.example/o/r/pulls/7", true)

	if !strings.Contains(prompt, "Check every command it names against the file it cites") {
		t.Errorf("the prepare reader is not told to check citations:\n%s", prompt)
	}
	assertGolden(t, "reader_prepare_repository", prompt)
}

func TestAChangeWorkItemKeepsTheOrdinaryBrief(t *testing.T) {
	spec := hostileSpec()
	spec.WorkItem.Kind = work.KindChange
	if got, want := Compose(spec, true, "", false), Compose(hostileSpec(), true, "", false); got != want {
		t.Errorf("an explicit change kind changed the writer's prompt")
	}
}
