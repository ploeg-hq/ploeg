package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestTeamCardRules_LoadsRefereesAndHotfixLabels(t *testing.T) {
	f, err := Load(write(t, `
teams:
  silver:
    assignees: [jake]
    cards:
      referees: [anna, bram]
      hotfixLabels: [hotfix, urgent]
  bronze:
    assignees: [kim]
`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]TeamCards{"silver": {Referees: []string{"anna", "bram"}, HotfixLabels: []string{"hotfix", "urgent"}}}
	if got := f.TeamCardRules(); !reflect.DeepEqual(got, want) {
		t.Fatalf("TeamCardRules = %+v; want %+v", got, want)
	}
}

func TestTeamCardRules_RefusesEmptyAndDuplicateEntries(t *testing.T) {
	for name, cards := range map[string]string{
		"empty referee":       `referees: [""]`,
		"duplicate referee":   `referees: [anna, Anna]`,
		"empty hotfix label":  `hotfixLabels: ["  "]`,
		"duplicate label":     `hotfixLabels: [hotfix, HOTFIX]`,
		"overlong hotfix tag": `hotfixLabels: [` + strings.Repeat("x", 129) + `]`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(write(t, "teams:\n  silver:\n    cards:\n      "+cards+"\n"))
			if err == nil || !strings.Contains(err.Error(), "teams.silver.cards") {
				t.Fatalf("err = %v; want a teams.silver.cards error", err)
			}
		})
	}
}

func TestTeamCardRules_PRCommentIsOptInPerTeam(t *testing.T) {
	f, err := Load(write(t, `
teams:
  silver:
    assignees: [jake]
    cards:
      prComment: true
  bronze:
    assignees: [kim]
    cards:
      hotfixLabels: [hotfix]
  gold:
    assignees: [lee]
`))
	if err != nil {
		t.Fatal(err)
	}
	got := f.TeamCardRules()
	if !got["silver"].PRComment {
		t.Errorf("silver = %+v; want the card comment on", got["silver"])
	}
	if got["bronze"].PRComment || got["gold"].PRComment {
		t.Errorf("bronze = %+v, gold = %+v; a team that does not opt in posts no card comment", got["bronze"], got["gold"])
	}
}
