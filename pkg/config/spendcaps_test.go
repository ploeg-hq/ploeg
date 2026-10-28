package config

import (
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/spendcap"
)

func TestSpendCaps_FileOverlaysEnvironmentAndDefaultsToNone(t *testing.T) {
	f, err := Load(write(t, `
teams:
  bronze:
    spendCap: {usd: 5, period: week}
  copper: {}
`))
	if err != nil {
		t.Fatal(err)
	}
	env, err := spendcap.Parse(`{"bronze": {"usd": 20, "period": "day"}, "silver": {"usd": 30, "period": "month"}}`)
	if err != nil {
		t.Fatal(err)
	}
	caps := f.SpendCaps(env)
	want := map[string]spendcap.Cap{
		"bronze": {USD: 5, Period: spendcap.Week},
		"silver": {USD: 30, Period: spendcap.Month},
		"copper": {},
	}
	for team, cap := range want {
		if caps[team] != cap {
			t.Errorf("SpendCaps[%s] = %+v, want %+v", team, caps[team], cap)
		}
	}
}

func TestSpendCaps_RejectsACapThatCannotBound(t *testing.T) {
	for name, body := range map[string]string{
		"negative":       "teams:\n  bronze:\n    spendCap: {usd: -1, period: day}\n",
		"unknown period": "teams:\n  bronze:\n    spendCap: {usd: 5, period: hour}\n",
	} {
		_, err := Load(write(t, body))
		if err == nil || !strings.Contains(err.Error(), "teams.bronze.spendCap") {
			t.Errorf("%s: Load = %v, want an error naming teams.bronze.spendCap", name, err)
		}
	}
}
