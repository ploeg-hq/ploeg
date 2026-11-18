package httpapi

import (
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/team"
)

func TestTeamSeriesCarryTheTeamIdAndTeamInfoJoinsItToTheKey(t *testing.T) {
	directory := team.NewDirectory([]team.Team{
		{ID: "team_01hf7yat00aaaaaaaaaaaaaaaa", Key: "docs-review", Aliases: []string{"bronze"}, Source: "static", State: "active", Name: "Docs reviewers"},
		{ID: "team_01hf7yat00bbbbbbbbbbbbbbbb", Key: "silver", Source: "static", State: "active"},
	})
	body := string(renderMetrics(store.OperationalMetrics{
		FinishedRuns: map[store.RunOutcomeKey]int{
			{Team: "bronze", Role: "builder", Outcome: "pr_opened"}:      1,
			{Team: "docs-review", Role: "builder", Outcome: "pr_opened"}: 2,
			{Team: "retired", Role: "builder", Outcome: "pr_opened"}:     3,
		},
		OpenShifts: map[string]int{"silver": 1},
	}, nil, nil, directory))
	for _, want := range []string{
		`ploeg_runs_finished_total{team="bronze",team_id="team_01hf7yat00aaaaaaaaaaaaaaaa",role="builder",outcome="pr_opened",reason=""} 1`,
		`ploeg_runs_finished_total{team="docs-review",team_id="team_01hf7yat00aaaaaaaaaaaaaaaa",role="builder",outcome="pr_opened",reason=""} 2`,
		`ploeg_runs_finished_total{team="retired",role="builder",outcome="pr_opened",reason=""} 3`,
		`ploeg_shifts_open{team="silver",team_id="team_01hf7yat00bbbbbbbbbbbbbbbb"} 1`,
		`ploeg_team_info{team_id="team_01hf7yat00aaaaaaaaaaaaaaaa",key="docs-review",source="static",state="active"} 1`,
		`ploeg_team_info{team_id="team_01hf7yat00bbbbbbbbbbbbbbbb",key="silver",source="static",state="active"} 1`,
		`ploeg_teams{source="static",state="active"} 2`,
		`ploeg_teams{source="runtime",state="shadowed"} 0`,
	} {
		if !strings.Contains(body, "\n"+want+"\n") {
			t.Errorf("missing %s in:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Docs reviewers") {
		t.Error("a display name is a label")
	}
	if strings.Count(body, "ploeg_team_info{") != 2 {
		t.Error("ploeg_team_info is not one series per live Team")
	}
}

func TestWithoutATeamDirectoryNoSeriesCarriesATeamId(t *testing.T) {
	body := string(renderMetrics(store.OperationalMetrics{OpenShifts: map[string]int{"silver": 1}}, nil, nil, nil))
	if strings.Contains(body, "team_id") || strings.Contains(body, "ploeg_team_info") {
		t.Fatalf("team identity rendered without a directory:\n%s", body)
	}
}
