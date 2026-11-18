package config

import (
	"strings"
	"testing"
)

func TestATeamMayNameItselfAndListItsEarlierKeys(t *testing.T) {
	f, err := Load(write(t, `
teams:
  docs-review:
    name: "builder · openhands"
    aliases: [bronze]
  silver: {}
`))
	if err != nil {
		t.Fatal(err)
	}
	got := f.StaticTeams()
	if len(got) != 2 || got[0].Key != "docs-review" || got[0].Name != "builder · openhands" || len(got[0].Aliases) != 1 || got[0].Aliases[0] != "bronze" {
		t.Fatalf("StaticTeams = %+v", got)
	}
	if got[1].Key != "silver" || got[1].Name != "" {
		t.Fatalf("StaticTeams = %+v", got)
	}
}

func TestTeamIdentityIsRefusedWhenItCannotResolveToOneTeam(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"alias equals another key":      {"teams:\n  bronze:\n    aliases: [silver]\n  silver: {}\n", "teams.bronze.aliases[0]"},
		"alias held by two Teams":       {"teams:\n  bronze:\n    aliases: [old]\n  silver:\n    aliases: [OLD]\n", "teams.silver.aliases[0]"},
		"alias equals its own key":      {"teams:\n  bronze:\n    aliases: [bronze]\n", "teams.bronze.aliases[0]"},
		"empty alias":                   {"teams:\n  bronze:\n    aliases: [\"\"]\n", "teams.bronze.aliases[0]"},
		"name with a control character": {"teams:\n  bronze:\n    name: \"a\\nb\"\n", "teams.bronze.name"},
		"name too long":                 {"teams:\n  bronze:\n    name: \"" + strings.Repeat("a", 81) + "\"\n", "teams.bronze.name"},
		"keys differing only in case":   {"teams:\n  Builders: {}\n  builders: {}\n", "teams.builders"},
		"eleven aliases":                {"teams:\n  bronze:\n    aliases: [a1, a2, a3, a4, a5, a6, a7, a8, a9, a10, a11]\n", "teams.bronze.aliases"},
	} {
		if _, err := Load(write(t, tc.body)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Load = %v, want an error naming %s", name, err, tc.want)
		}
	}
}

func TestATeamsDigestIgnoresItsNameAndAliasesAndFollowsWhatItIsMadeOf(t *testing.T) {
	f, err := Load(write(t, `
teams:
  bronze:
    maxRunning: 2
  copper:
    name: Copper
    aliases: [old]
    maxRunning: 2
  tin:
    maxRunning: 3
`))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range f.StaticTeams() {
		got[s.Key] = s.Digest
	}
	if got["bronze"] == "" || got["bronze"] != got["copper"] {
		t.Fatalf("a Team that differs only in name and aliases has another digest: %v", got)
	}
	if got["tin"] == got["bronze"] {
		t.Fatalf("Teams made differently share a digest: %v", got)
	}
}
