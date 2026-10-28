package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/spendcap"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

func capTeams(t *testing.T, caps spendcap.Caps) {
	t.Helper()
	testStore.SetTeamSpendCaps(caps)
	t.Cleanup(func() { testStore.SetTeamSpendCaps(nil) })
}

func TestOperatorTeamsShowTheSpendCapAndItsPeriod(t *testing.T) {
	reset(t)
	capTeams(t, spendcap.Caps{"silver": {USD: 20, Period: spendcap.Day}})
	consumers, token := operatorTestConsumers(t, []string{"silver", "gold"}, false)
	s := &Server{Store: testStore, OperatorConfig: OperatorConfig{
		Consumers: consumers,
		Teams:     map[string][]string{"silver": {"builder"}, "gold": {"writer"}},
	}}
	var body struct {
		Teams []struct {
			ID       string                  `json:"id"`
			SpendCap *store.OperatorSpendCap `json:"spendCap"`
		} `json:"teams"`
	}
	if err := json.Unmarshal(operatorSchemaGET(t, s, token, "teams"), &body); err != nil {
		t.Fatal(err)
	}
	caps := map[string]*store.OperatorSpendCap{}
	for _, team := range body.Teams {
		caps[team.ID] = team.SpendCap
	}
	silver := caps["silver"]
	if silver == nil || silver.USD != 20 || silver.Period != "day" || silver.RemainingUSD != 20 {
		t.Fatalf("silver spendCap = %+v, want 20 USD per day with all of it remaining", silver)
	}
	if want := spendcap.Day.Start(time.Now()); !silver.PeriodStart.Equal(want) || !silver.PeriodEnd.Equal(want.AddDate(0, 0, 1)) {
		t.Fatalf("silver period = %s to %s, want the UTC day starting %s", silver.PeriodStart, silver.PeriodEnd, want)
	}
	if caps["gold"] != nil {
		t.Fatalf("gold has no cap but the read shows %+v", caps["gold"])
	}
}

func TestMetricsExposeEachCappedTeamsSpendAgainstItsCap(t *testing.T) {
	reset(t)
	capTeams(t, spendcap.Caps{"bronze": {USD: 2, Period: spendcap.Week}})
	shiftFixture(t, "902", 5, []store.Role{{Name: "builder", Writes: true, Cap: 1.5}})
	if _, err := testStore.ClaimRole(context.Background(), "bronze", "builder", time.Minute, 1.5); err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: testStore, Log: slog.New(slog.DiscardHandler), MetricsCacheTTL: -1}
	requireLines(t, scrape(t, s.Handler()),
		"# TYPE ploeg_team_spend_cap_usd gauge",
		`ploeg_team_spend_cap_usd{team="bronze",period="week"} 2`,
		"# TYPE ploeg_team_period_spend_usd gauge",
		`ploeg_team_period_spend_usd{team="bronze",period="week",kind="settled"} 0`,
		`ploeg_team_period_spend_usd{team="bronze",period="week",kind="held"} 1.5`,
	)
}
