package config

import (
	"slices"
	"strings"
	"testing"
)

func TestClickupAgentsAreReadAndRefusedElsewhere(t *testing.T) {
	f, err := Load(write(t, `
trackers:
  clickup:
    agents: [agent-member, agent@example.com]
`))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.Trackers.Clickup.Agents, []string{"agent-member", "agent@example.com"}) {
		t.Fatalf("agents = %v", f.Trackers.Clickup.Agents)
	}
	for _, body := range []string{"trackers:\n  vikunja:\n    agents: [x]\n", "trackers:\n  clickup:\n    agents: [\"\"]\n"} {
		if _, err := Load(write(t, body)); err == nil || !strings.Contains(err.Error(), "agents") {
			t.Errorf("%q = %v; want a refusal naming agents", body, err)
		}
	}
}
