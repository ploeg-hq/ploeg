package skills

import (
	"strings"
	"testing"
)

func TestForExplainerGetsTheExplainingAndVerifyingSkills(t *testing.T) {
	set, err := ForPullRequestReader()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range set {
		names = append(names, s.Name)
	}
	if got := strings.Join(names, ","); got != ReadAPullRequest+","+VerifyBeforeHandoff {
		t.Errorf("skills = %q", got)
	}
	if !strings.Contains(string(set[0].Content), "createdWorkItems") || !strings.Contains(set[0].Description, "read a pull request") {
		t.Errorf("reader skill = %q", set[0].Description)
	}
}
