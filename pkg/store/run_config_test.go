package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// ADR-0091: each field is recorded once, an empty or malformed value stays
// unknown, and modelMismatch compares the served model groups with the
// configured model only when both are known; provider model names alone
// never decide it.
func TestRecordRunConfigWritesOnceAndModelMismatchNeedsBothSides(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	if _, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "run-config", Team: "silver", Title: "configured"}); err != nil {
		t.Fatal(err)
	}
	claimed, err := testStore.Claim(ctx, "silver", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var runID int64
	if err := testStore.pool.QueryRow(ctx, `SELECT id FROM agent_runs WHERE run_token = $1`, claimed.RunToken).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	read := func() OperatorRunConfig {
		t.Helper()
		run, err := testStore.OperatorRun(ctx, runID, nil)
		if err != nil {
			t.Fatal(err)
		}
		runs, _, err := testStore.OperatorRuns(ctx, OperatorRunFilter{Limit: 10})
		if err != nil || len(runs) != 1 {
			t.Fatalf("runs = %v, %v", runs, err)
		}
		if a, b := run.OperatorRunConfig, runs[0].OperatorRunConfig; str(a.ConfiguredModel) != str(b.ConfiguredModel) ||
			str(a.Harness) != str(b.Harness) || boolStr(a.ModelMismatch) != boolStr(b.ModelMismatch) {
			t.Fatalf("detail %+v and list %+v disagree", a, b)
		}
		return run.OperatorRunConfig
	}

	if c := read(); c.ConfiguredModel != nil || c.Harness != nil || c.HarnessProfile != nil || c.PlanHash != nil || c.PloegdVersion != nil || c.ModelMismatch != nil {
		t.Fatalf("unrecorded Run = %+v, want every field unknown", c)
	}
	hash := "sha256:" + strings.Repeat("a", 64)
	if err := testStore.RecordRunConfig(ctx, claimed.RunToken, RunConfig{ConfiguredModel: "model-a", Harness: "openhands",
		HarnessProfile: "Not A Profile!", PlanHash: hash, PloegdVersion: "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	if err := testStore.RecordRunConfig(ctx, claimed.RunToken, RunConfig{ConfiguredModel: "model-b", Harness: "acp",
		PlanHash: "sha256:" + strings.Repeat("b", 64), PloegdVersion: "0.3.0"}); err != nil {
		t.Fatal(err)
	}
	c := read()
	if str(c.ConfiguredModel) != "model-a" || str(c.Harness) != "openhands" || c.HarnessProfile != nil || str(c.PlanHash) != hash || str(c.PloegdVersion) != "0.2.0" {
		t.Fatalf("recorded = model %s harness %s profile %s plan %s version %s, want the first values and no malformed profile",
			str(c.ConfiguredModel), str(c.Harness), str(c.HarnessProfile), str(c.PlanHash), str(c.PloegdVersion))
	}
	if c.ModelMismatch != nil {
		t.Fatalf("mismatch without usage = %s, want unknown", boolStr(c.ModelMismatch))
	}

	for _, tc := range []struct {
		usage string
		want  string
	}{
		{`{"modelGroups":[]}`, "<nil>"},
		{`{"models":["provider/model-a"],"modelGroups":["model-a"]}`, "false"},
		{`{"models":["provider/model-a"],"modelGroups":["model-a","fallback-c"]}`, "true"},
		{`{"models":["model-c"]}`, "<nil>"},
		{`{"inputTokens":3}`, "<nil>"},
	} {
		mustExec(t, `UPDATE agent_runs SET usage = $2::jsonb WHERE id = $1`, runID, tc.usage)
		if got := boolStr(read().ModelMismatch); got != tc.want {
			t.Fatalf("usage %s: modelMismatch = %s, want %s", tc.usage, got, tc.want)
		}
	}
}

func TestRecordRunConfigKeepsTheFirstHarnessVersionAndAgent(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	if _, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "run-harness", Team: "silver", Title: "versioned"}); err != nil {
		t.Fatal(err)
	}
	claimed, err := testStore.Claim(ctx, "silver", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var runID int64
	if err := testStore.pool.QueryRow(ctx, `SELECT id FROM agent_runs WHERE run_token = $1`, claimed.RunToken).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	read := func() OperatorRunConfig {
		t.Helper()
		run, err := testStore.OperatorRun(ctx, runID, nil)
		if err != nil {
			t.Fatal(err)
		}
		return run.OperatorRunConfig
	}
	if c := read(); c.HarnessVersion != nil || c.HarnessAgent != nil {
		t.Fatalf("unrecorded Run = version %s agent %+v, want unknown", str(c.HarnessVersion), c.HarnessAgent)
	}
	for _, malformed := range []string{"two\nlines", strings.Repeat("v", 129), "\xff"} {
		if got, err := testStore.RecordRunConfigVersion(ctx, claimed.RunToken, RunConfig{HarnessVersion: malformed, AgentVersion: "1.0"}); err != nil || got != "" {
			t.Fatalf("malformed version %q: recorded %q, %v", malformed, got, err)
		}
	}
	if c := read(); c.HarnessVersion != nil || c.HarnessAgent != nil {
		t.Fatalf("malformed values = version %s agent %+v, want unknown", str(c.HarnessVersion), c.HarnessAgent)
	}
	got, err := testStore.RecordRunConfigVersion(ctx, claimed.RunToken, RunConfig{HarnessVersion: "opencode 1.18.30"})
	if err != nil || got != "opencode 1.18.30" {
		t.Fatalf("first version: recorded %q, %v", got, err)
	}
	got, err = testStore.RecordRunConfigVersion(ctx, claimed.RunToken, RunConfig{HarnessVersion: "opencode 1.19.0", AgentName: "opencode", AgentVersion: "1.19.0"})
	if err != nil || got != "opencode 1.18.30" {
		t.Fatalf("later version: holds %q, %v, want the first", got, err)
	}
	if _, err := testStore.RecordRunConfigVersion(ctx, claimed.RunToken, RunConfig{AgentName: "other", AgentVersion: "2"}); err != nil {
		t.Fatal(err)
	}
	c := read()
	if str(c.HarnessVersion) != "opencode 1.18.30" || c.HarnessAgent == nil || c.HarnessAgent.Name != "opencode" || str(c.HarnessAgent.Version) != "1.19.0" {
		t.Fatalf("recorded = version %s agent %+v, want the first of each", str(c.HarnessVersion), c.HarnessAgent)
	}
	var recordedAt *time.Time
	if err := testStore.pool.QueryRow(ctx, `SELECT harness_recorded_at FROM agent_runs WHERE id = $1`, runID).Scan(&recordedAt); err != nil || recordedAt == nil {
		t.Fatalf("recorded at = %v, %v", recordedAt, err)
	}
	if got, err := testStore.RecordRunConfigVersion(ctx, "no-such-run", RunConfig{HarnessVersion: "1"}); err != nil || got != "" {
		t.Fatalf("unknown Run: %q, %v", got, err)
	}
}

func TestOperatorTeamsReadTheNewestObservedHarnessPerRole(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	var tokens []string
	for i := 0; i < 3; i++ {
		if _, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "observed-" + string(rune('a'+i)), Team: "silver", Title: "observed"}); err != nil {
			t.Fatal(err)
		}
		claimed, err := testStore.Claim(ctx, "silver", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, claimed.RunToken)
	}
	for i, c := range []RunConfig{
		{Harness: "acp", HarnessProfile: "goose", HarnessVersion: "1.0"},
		{Harness: "acp", HarnessProfile: "opencode", HarnessVersion: "1.18.30", AgentName: "opencode", AgentVersion: "1.18.30"},
		{Harness: "acp", HarnessProfile: "opencode"},
	} {
		if err := testStore.RecordRunConfig(ctx, tokens[i], c); err != nil {
			t.Fatal(err)
		}
	}
	teams, err := testStore.OperatorTeams(ctx, []string{"silver"}, map[string][]string{"silver": {"reviewer"}})
	if err != nil || len(teams) != 1 || len(teams[0].Roles) != 2 {
		t.Fatalf("teams = %+v, %v", teams, err)
	}
	byRole := map[string]OperatorRole{}
	for _, role := range teams[0].Roles {
		byRole[role.ID] = role
	}
	o := byRole[""].ObservedHarness
	if o == nil || o.Version != "1.18.30" || str(o.Name) != "acp" || str(o.Profile) != "opencode" || o.Agent == nil ||
		o.Agent.Name != "opencode" || o.RecordedAt.IsZero() {
		t.Fatalf("observed harness = %+v, want the newest recorded version", o)
	}
	if byRole["reviewer"].ObservedHarness != nil || byRole["reviewer"].Harness != nil || byRole["reviewer"].Model != nil {
		t.Fatalf("a Role without a recorded Run = %+v, want unknown", byRole["reviewer"])
	}
}

func str(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func boolStr(p *bool) string {
	if p == nil {
		return "<nil>"
	}
	if *p {
		return "true"
	}
	return "false"
}
