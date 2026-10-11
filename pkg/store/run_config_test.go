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
