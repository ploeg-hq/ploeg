package trackerbinding

import (
	"context"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/team"
)

func TestABindingFollowsItsRuntimeTeamsState(t *testing.T) {
	r := testRegistry()
	teams := map[string]team.Team{"team_docs": {ID: "team_docs", Key: "docs", Source: team.SourceRuntime, State: team.StateActive}}
	r.Teams = func(id string) (team.Team, bool) { t, ok := teams[id]; return t, ok }
	b := store.TrackerBinding{ID: 1, Team: "docs", TeamID: "team_docs", Connection: "clickup", ScopeID: "list-1",
		TrackerBindingFields: store.TrackerBindingFields{Default: "app"}, Webhook: store.BindingWebhook{State: provider.WebhookPresent}}
	if got := r.StateOf(context.Background(), b); got.State != store.BindingActive {
		t.Fatalf("an active runtime Team's binding is %+v", got)
	}
	for _, state := range []string{team.StateShadowed, team.StateInvalid} {
		teams["team_docs"] = team.Team{ID: "team_docs", Key: "docs", Source: team.SourceRuntime, State: state}
		if got := r.StateOf(context.Background(), b); got.State != store.BindingInvalid || got.InvalidReason != store.BindingTeamUnavailable {
			t.Errorf("a %s runtime Team's binding is %+v, want invalid team_unavailable", state, got)
		}
	}
	delete(teams, "team_docs")
	if got := r.StateOf(context.Background(), b); got.InvalidReason != store.BindingTeamRemoved {
		t.Errorf("a removed runtime Team's binding is %+v, want team_removed", got)
	}
	static := binding(2, "list-2", store.BindingActive)
	static.TeamID = "team_silver"
	static.Webhook.State = provider.WebhookPresent
	if got := r.StateOf(context.Background(), static); got.State != store.BindingActive {
		t.Errorf("a static Team's binding is %+v", got)
	}
}
