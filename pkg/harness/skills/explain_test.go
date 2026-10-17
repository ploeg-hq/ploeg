package skills

import (
	"strings"
	"testing"
)

func TestForExplainerGetsTheExplainingAndVerifyingSkills(t *testing.T) {
	set, err := ForExplainer()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range set {
		names = append(names, s.Name)
	}
	if got := strings.Join(names, ","); got != ExplainDependencyUpdate+","+VerifyBeforeHandoff {
		t.Errorf("skills = %q", got)
	}
	if !strings.Contains(string(set[0].Content), "createdWorkItems") || !strings.Contains(set[0].Description, "explain a dependency update") {
		t.Errorf("explainer skill = %q", set[0].Description)
	}
}
