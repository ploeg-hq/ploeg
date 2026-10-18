package config

import "testing"

func TestThePremiseCheckIsOffUnlessATeamNamesAModel(t *testing.T) {
	f, err := Load(write(t, `
teams:
  bronze:
    assignees: [jake]
    premiseCheck:
      model: hetzner-qwen3-8-27b
  copper:
    assignees: [kim]
`))
	if err != nil {
		t.Fatal(err)
	}
	got := f.PremiseChecks()
	if len(got) != 1 || got["bronze"] != "hetzner-qwen3-8-27b" {
		t.Fatalf("premise checks = %v; want only bronze", got)
	}
	for _, bad := range []string{"premiseCheck: {}", "premiseCheck: {model: \"two words\"}", "premiseCheck: {model: x, budget: 1}"} {
		if _, err := Load(write(t, "teams:\n  bronze:\n    "+bad+"\n")); err == nil {
			t.Errorf("%s loaded", bad)
		}
	}
}
