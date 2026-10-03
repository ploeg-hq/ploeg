package worker

import (
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

func gitlabSpec(role string) harness.TaskSpec {
	spec := specFor(testCfg("main"), testItem(), "agent/vik-585", "ploeg-abc123def456")
	spec.Role = role
	spec.Repo = harness.RepoRef{
		Forge:      harness.ForgeGitLab,
		ForgeURL:   "https://gitlab.com",
		Owner:      "acme",
		Name:       "internal/widgets",
		BaseBranch: "main",
	}
	return spec
}

func TestComposePrompt_GitLabWriterOpensAMergeRequest(t *testing.T) {
	task := ComposePrompt(gitlabSpec("builder"), true, "", true)

	for _, want := range []string{
		"open a merge request",
		"https://gitlab.com/api/v4/projects/acme%2Finternal%2Fwidgets/merge_requests",
		"PRIVATE-TOKEN",
		"Do NOT merge the merge request",
	} {
		if !strings.Contains(task, want) {
			t.Errorf("GitLab writer prompt missing %q:\n%s", want, task)
		}
	}
	for _, reject := range []string{"Forgejo", "/api/v1/repos/", "pull request"} {
		if strings.Contains(task, reject) {
			t.Errorf("GitLab writer prompt still says %q:\n%s", reject, task)
		}
	}
}

func TestComposePrompt_ForgejoWriterUnchanged(t *testing.T) {
	task := ComposePrompt(roleSpec("builder", nil), true, "", true)

	for _, want := range []string{
		"open a pull request",
		"via the Forgejo API",
		"/api/v1/repos/webgrip/ploeg/pulls",
		"authenticated as agent-builder",
		"Do NOT merge the pull request",
	} {
		if !strings.Contains(task, want) {
			t.Errorf("Forgejo writer prompt missing %q:\n%s", want, task)
		}
	}
	if strings.Contains(task, "merge request") {
		t.Errorf("Forgejo writer prompt leaked GitLab vocabulary:\n%s", task)
	}
}

func TestComposePrompt_GitLabReaderUsesMergeRequestVocabulary(t *testing.T) {
	task := ComposePrompt(gitlabSpec("reviewer"), false, "https://gitlab.com/acme/internal/widgets/-/merge_requests/4", true)

	for _, want := range []string{
		"Do not open, update, comment on, or merge a merge request",
		"posted as a comment on this merge request",
		"-/merge_requests/4",
	} {
		if !strings.Contains(task, want) {
			t.Errorf("GitLab reader prompt missing %q:\n%s", want, task)
		}
	}
	if strings.Contains(task, "pull request") {
		t.Errorf("GitLab reader prompt still says pull request:\n%s", task)
	}
}

func TestComposePrompt_GitLabWriterUpdatesExistingMR(t *testing.T) {
	pr := "https://gitlab.com/acme/internal/widgets/-/merge_requests/4"
	task := ComposePrompt(gitlabSpec("builder"), true, pr, true)

	for _, want := range []string{
		"A merge request is ALREADY OPEN on this branch: " + pr,
		"Do NOT open a second merge request",
	} {
		if !strings.Contains(task, want) {
			t.Errorf("GitLab update-existing prompt missing %q:\n%s", want, task)
		}
	}
}
