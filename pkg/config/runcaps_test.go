package config

import "testing"

func TestRunCaps_FileOverlaysEnvironmentAndDefaultsToUnlimited(t *testing.T) {
	f, err := Load(write(t, `
teams:
  bronze:
    maxRunning: 2
  copper: {}
`))
	if err != nil {
		t.Fatal(err)
	}
	env, err := ParseRunCaps(`{"bronze": 5, "silver": 3}`)
	if err != nil {
		t.Fatal(err)
	}
	caps := f.RunCaps(env)
	for team, want := range map[string]int{"bronze": 2, "silver": 3, "copper": 0, "absent": 0} {
		if got := caps.MaxRunning(team); got != want {
			t.Errorf("MaxRunning(%s) = %d, want %d", team, got, want)
		}
	}
	if caps, err := ParseRunCaps(""); err != nil || len(caps) != 0 {
		t.Errorf("ParseRunCaps(\"\") = %v, %v; want no caps", caps, err)
	}
}

func TestRunCaps_RejectsNegativeCaps(t *testing.T) {
	if _, err := Load(write(t, "teams:\n  bronze:\n    maxRunning: -1\n")); err == nil {
		t.Error("accepted a negative maxRunning in the file")
	}
	if _, err := ParseRunCaps(`{"bronze": -1}`); err == nil {
		t.Error("accepted a negative maxRunning from the environment")
	}
}
