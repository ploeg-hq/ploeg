package store

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

var (
	builderSelection = RoleSelection{
		Harnesses: []string{"openhands", "acp/opencode"}, DefaultHarness: "openhands",
		Models: []string{"deepseek-v4", "kimi-k3"}, DefaultModel: "deepseek-v4",
	}
	reviewerSelection = RoleSelection{
		Harnesses: []string{"openhands"}, DefaultHarness: "openhands",
		Models: []string{"glm-4.7"}, DefaultModel: "glm-4.7",
	}
)

func selectionItem(t *testing.T, externalID, harness, model string) (itemID, shiftID int64) {
	t.Helper()
	return teamSelectionItem(t, "silver", externalID, harness, model)
}

func teamSelectionItem(t *testing.T, team, externalID, harness, model string) (itemID, shiftID int64) {
	t.Helper()
	ctx := context.Background()
	itemID, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: externalID, Team: team, Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE work_items SET harness_requested = NULLIF($2, ''), model_requested = NULLIF($3, '') WHERE id = $1`,
		itemID, harness, model); err != nil {
		t.Fatal(err)
	}
	shiftID, err = testStore.OpenShift(ctx, itemID, team, "agent/"+externalID, 20)
	if err != nil {
		t.Fatal(err)
	}
	return itemID, shiftID
}

func storedSelection(t *testing.T, shiftID int64, role string) (harness, model *string) {
	t.Helper()
	if err := testStore.pool.QueryRow(context.Background(), `SELECT harness_requested, model_selected FROM agent_runs
		WHERE shift_id = $1 AND role = $2 AND state = 'pending' ORDER BY id DESC LIMIT 1`, shiftID, role).Scan(&harness, &model); err != nil {
		t.Fatalf("pending %s Run: %v", role, err)
	}
	return harness, model
}

func wantStored(t *testing.T, what string, got *string, want string) {
	t.Helper()
	switch {
	case want == "" && got != nil:
		t.Errorf("%s = %q, want none", what, *got)
	case want != "" && (got == nil || *got != want):
		t.Errorf("%s = %v, want %q", what, got, want)
	}
}

func finishRuns(t *testing.T, shiftID int64, outcome string) {
	t.Helper()
	if _, err := testStore.pool.Exec(context.Background(), `UPDATE agent_runs SET state = 'finished', finished_at = now(), outcome = $2
		WHERE shift_id = $1 AND state <> 'finished'`, shiftID, outcome); err != nil {
		t.Fatal(err)
	}
}

func TestEveryRunOfASessionStoresItsRolesResolution(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	_, shiftID := selectionItem(t, "910", "acp/opencode", "kimi-k3")

	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "builder", Writes: true, Cap: 1, Selection: builderSelection}}); err != nil {
		t.Fatal(err)
	}
	harness, model := storedSelection(t, shiftID, "builder")
	wantStored(t, "builder harness", harness, "acp/opencode")
	wantStored(t, "builder model", model, "kimi-k3")

	finishRuns(t, shiftID, "failed")
	if _, err := testStore.ReopenRound(ctx, shiftID, 1, []Role{{Name: "builder", Writes: true, Cap: 1, Selection: builderSelection}}); err != nil {
		t.Fatal(err)
	}
	harness, model = storedSelection(t, shiftID, "builder")
	wantStored(t, "retried builder harness", harness, "acp/opencode")
	wantStored(t, "retried builder model", model, "kimi-k3")

	finishRuns(t, shiftID, "pr_opened")
	if _, err := testStore.OpenRound(ctx, shiftID, 1, []Role{{Name: "reviewer", Cap: 1, Selection: reviewerSelection}}); err != nil {
		t.Fatal(err)
	}
	harness, model = storedSelection(t, shiftID, "reviewer")
	wantStored(t, "reviewer harness", harness, "openhands")
	wantStored(t, "reviewer model", model, "glm-4.7")
}

func TestARunWithoutARequestStoresItsRolesDefaultsAndWithoutConfigurationNothing(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	_, shiftID := selectionItem(t, "911", "", "")
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []Role{{Name: "builder", Writes: true, Cap: 1, Selection: builderSelection}}); err != nil {
		t.Fatal(err)
	}
	harness, model := storedSelection(t, shiftID, "builder")
	wantStored(t, "builder harness", harness, "openhands")
	wantStored(t, "builder model", model, "deepseek-v4")

	_, unconfigured := selectionItem(t, "912", "acp/opencode", "kimi-k3")
	if _, err := testStore.OpenRound(ctx, unconfigured, 0, []Role{{Name: "builder", Writes: true, Cap: 1}}); err != nil {
		t.Fatal(err)
	}
	harness, model = storedSelection(t, unconfigured, "builder")
	wantStored(t, "unconfigured harness", harness, "")
	wantStored(t, "unconfigured model", model, "")
}

func TestARoleThatDoesNotAdmitTheRequestUsesItsDefault(t *testing.T) {
	got, model := reviewerSelection.Resolve("acp/opencode", "kimi-k3")
	if got != "openhands" || model != "glm-4.7" {
		t.Fatalf("reviewer resolves to %q/%q, want its defaults", got, model)
	}
	got, model = builderSelection.Resolve("acp/qwen-code", "")
	if got != "openhands" || model != "deepseek-v4" {
		t.Fatalf("builder resolves an unadmitted Harness to %q/%q, want its defaults", got, model)
	}
}

func TestAWorkerClaimsOnlyItsOwnHarnessAndTheRunsModel(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	builder := []Role{{Name: "builder", Writes: true, Cap: 1, Selection: builderSelection}}
	_, opencode := selectionItem(t, "920", "acp/opencode", "kimi-k3")
	_, openhands := selectionItem(t, "921", "openhands", "")
	for _, shiftID := range []int64{opencode, openhands} {
		if _, err := testStore.OpenRound(ctx, shiftID, 0, builder); err != nil {
			t.Fatal(err)
		}
	}
	run, err := testStore.ClaimRoleFor(ctx, "silver", "builder", "openhands", time.Minute, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if run.ShiftID != openhands || run.Model != "deepseek-v4" {
		t.Fatalf("openhands worker claimed shift %d with model %q, want shift %d with deepseek-v4", run.ShiftID, run.Model, openhands)
	}
	if _, err := testStore.ClaimRoleFor(ctx, "silver", "builder", "openhands", time.Minute, 1, 0); !errors.Is(err, ErrNoWork) {
		t.Fatalf("second openhands claim: %v, want ErrNoWork while only an acp/opencode Run waits", err)
	}
	run, err = testStore.ClaimRoleFor(ctx, "silver", "builder", "acp/opencode", time.Minute, 1, 0)
	if err != nil || run.ShiftID != opencode || run.Model != "kimi-k3" {
		t.Fatalf("acp/opencode claim = %+v, %v; want shift %d with kimi-k3", run, err, opencode)
	}
}

func claimRoleAgreesAcrossHarnesses(t *testing.T) {
	ctx := context.Background()
	for _, key := range []string{"openhands", "acp/opencode", "acp/qwen-code", ""} {
		t.Run(key, func(t *testing.T) {
			resetTables(t)
			builder := []Role{{Name: "builder", Writes: true, Cap: 1, Selection: builderSelection}}
			for i, request := range []string{"acp/opencode", "openhands", "acp/opencode", ""} {
				_, shiftID := selectionItem(t, "93"+string(rune('0'+i)), request, "")
				if _, err := testStore.OpenRound(ctx, shiftID, 0, builder); err != nil {
					t.Fatal(err)
				}
			}
			_, unconfigured := selectionItem(t, "939", "acp/opencode", "")
			if _, err := testStore.OpenRound(ctx, unconfigured, 0, []Role{{Name: "builder", Writes: true, Cap: 1}}); err != nil {
				t.Fatal(err)
			}
			want, err := testStore.PendingRunsFor(ctx, "silver", "builder", key)
			if err != nil {
				t.Fatal(err)
			}
			got := 0
			for {
				if _, err := testStore.ClaimRoleFor(ctx, "silver", "builder", key, time.Minute, 1, 0); err != nil {
					if errors.Is(err, ErrNoWork) {
						break
					}
					t.Fatal(err)
				}
				got++
			}
			if got != want || (key == "" && want != 5) {
				t.Errorf("harness %q: PendingRunsFor said %d, ClaimRoleFor yielded %d — the KEDA mirror has drifted", key, want, got)
			}
		})
	}
}

func TestARetryAfterAnExpiredClaimKeepsTheStoredHarness(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	builder := []Role{{Name: "builder", Writes: true, Cap: 1, Selection: builderSelection}}
	_, shiftID := selectionItem(t, "940", "acp/opencode", "kimi-k3")
	if _, err := testStore.OpenRound(ctx, shiftID, 0, builder); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ClaimRoleFor(ctx, "silver", "builder", "acp/opencode", -time.Second, 1, 0); err != nil {
		t.Fatal(err)
	}
	if expired, err := testStore.ExpireRuns(ctx); err != nil || len(expired) != 1 {
		t.Fatalf("ExpireRuns = %v, %v; want the killed pod's Run", expired, err)
	}
	if _, err := testStore.ReopenRound(ctx, shiftID, 1, builder); err != nil {
		t.Fatal(err)
	}
	harness, model := storedSelection(t, shiftID, "builder")
	wantStored(t, "retried harness", harness, "acp/opencode")
	wantStored(t, "retried model", model, "kimi-k3")
}

func TestPendingRunsByHarnessCountsStoredHarnesses(t *testing.T) {
	ctx := context.Background()
	resetTables(t)
	builder := []Role{{Name: "builder", Writes: true, Cap: 1, Selection: builderSelection}}
	for i, request := range []string{"acp/opencode", "acp/opencode", ""} {
		_, shiftID := selectionItem(t, "95"+string(rune('0'+i)), request, "")
		if _, err := testStore.OpenRound(ctx, shiftID, 0, builder); err != nil {
			t.Fatal(err)
		}
	}
	got, err := testStore.PendingRunsByHarness(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []PendingHarnessRuns{{Team: "silver", Role: "builder", Harness: "acp/opencode", Pending: 2}, {Team: "silver", Role: "builder", Harness: "openhands", Pending: 1}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("PendingRunsByHarness = %+v, want %+v", got, want)
	}
	m, err := testStore.OperationalMetrics(ctx)
	if err != nil || len(m.PendingRunsByHarness) != 2 {
		t.Fatalf("OperationalMetrics pending by harness = %+v, %v", m.PendingRunsByHarness, err)
	}
}

func TestHarnessKeyMatchesTheChart(t *testing.T) {
	for _, tc := range []struct{ name, profile, want string }{
		{"acp", "", "acp/opencode"}, {"acp", "qwen-code", "acp/qwen-code"}, {"openhands", "", "openhands"}, {"exec", "ignored", "exec"},
	} {
		if got := HarnessKey(tc.name, tc.profile); got != tc.want {
			t.Errorf("HarnessKey(%q, %q) = %q, want %q", tc.name, tc.profile, got, tc.want)
		}
	}
}

var scalerQuery = regexp.MustCompile(`query: "(SELECT COUNT\(\*\) FROM agent_runs WHERE team = '([^']+)' AND role = '([^']+)' AND state = 'pending' AND \(harness_requested IS NULL OR harness_requested = '([^']+)'\)[^"]*)"`)

func TestTheChartsScalerQueryCountsWhatItsWorkerClaims(t *testing.T) {
	ctx := context.Background()
	golden, err := os.ReadFile("../../ops/helm/ploeg/ci/golden/executor-harness-selection.yaml")
	if err != nil {
		t.Fatal(err)
	}
	queries := scalerQuery.FindAllStringSubmatch(string(golden), -1)
	if len(queries) != 4 {
		t.Fatalf("found %d Role scaler queries in the golden, want 3 builder workloads and 1 reviewer workload", len(queries))
	}
	resetTables(t)
	builder := []Role{{Name: "builder", Writes: true, Cap: 1, Selection: RoleSelection{
		Harnesses: []string{"openhands", "acp/opencode", "acp/qwen-code"}, DefaultHarness: "openhands"}}}
	for i, request := range []string{"acp/opencode", "acp/opencode", "acp/qwen-code", "", "openhands"} {
		_, shiftID := teamSelectionItem(t, "gold", "96"+string(rune('0'+i)), request, "")
		if _, err := testStore.OpenRound(ctx, shiftID, 0, builder); err != nil {
			t.Fatal(err)
		}
	}
	_, unconfigured := teamSelectionItem(t, "gold", "969", "acp/opencode", "")
	if _, err := testStore.OpenRound(ctx, unconfigured, 0, []Role{{Name: "builder", Writes: true, Cap: 1}}); err != nil {
		t.Fatal(err)
	}
	for _, harness := range []string{"openhands", "acp/opencode"} {
		_, clientCarried := teamSelectionItem(t, "gold", "97"+harness, harness, "")
		if _, err := testStore.OpenRound(ctx, clientCarried, 0, []Role{{Name: "builder", Writes: true, Cap: 1, Selection: RoleSelection{
			Harnesses: []string{harness}, DefaultHarness: harness, ClientCarried: []string{harness}}}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range queries {
		var scaled int
		if err := testStore.pool.QueryRow(ctx, q[1]).Scan(&scaled); err != nil {
			t.Fatalf("%s: %v", q[1], err)
		}
		pending, err := testStore.PendingRunsFor(ctx, q[2], q[3], q[4])
		if err != nil {
			t.Fatal(err)
		}
		if scaled != pending {
			t.Errorf("%s/%s/%s: the KEDA query counts %d, PendingRunsFor %d", q[2], q[3], q[4], scaled, pending)
		}
	}
}

var roleLessScalerQuery = regexp.MustCompile(`query: "(SELECT COUNT\(\*\) FROM work_items WHERE team = '([^']+)'[^"]*)"`)

func TestTheChartsRoleLessScalerQueryCountsWhatItsWorkerClaims(t *testing.T) {
	ctx := context.Background()
	golden, err := os.ReadFile("../../ops/helm/ploeg/ci/golden/executor.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var query string
	for _, q := range roleLessScalerQuery.FindAllStringSubmatch(string(golden), -1) {
		if q[2] == "bronze" {
			query = q[1]
		}
	}
	if query == "" {
		t.Fatal("the executor golden has no Role-less scaler query for team bronze")
	}
	resetTables(t)
	for _, id := range []string{"980", "981"} {
		if _, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: id, Team: "bronze", Title: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	teamSelectionItem(t, "bronze", "982", "", "")
	deferred, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "983", Team: "bronze", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.pool.Exec(ctx, `UPDATE work_items SET next_eligible_at = now() + interval '1 hour' WHERE id = $1`, deferred); err != nil {
		t.Fatal(err)
	}
	var scaled int
	if err := testStore.pool.QueryRow(ctx, query).Scan(&scaled); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	claimed := 0
	for {
		if _, err := testStore.Claim(ctx, "bronze", time.Minute); err != nil {
			if errors.Is(err, ErrNoWork) {
				break
			}
			t.Fatal(err)
		}
		claimed++
	}
	if scaled != claimed {
		t.Errorf("the Role-less KEDA query counts %d, Claim yields %d", scaled, claimed)
	}
}

func TestTheChartsScalerQueriesUseTheClaimableIndexes(t *testing.T) {
	cases := []struct {
		golden string
		query  *regexp.Regexp
		index  string
	}{
		{"executor-harness-selection.yaml", scalerQuery, "agent_runs_claimable_harness"},
		{"executor.yaml", roleLessScalerQuery, "work_items_claimable"},
	}
	for _, tc := range cases {
		golden, err := os.ReadFile("../../ops/helm/ploeg/ci/golden/" + tc.golden)
		if err != nil {
			t.Fatal(err)
		}
		queries := tc.query.FindAllStringSubmatch(string(golden), -1)
		if len(queries) == 0 {
			t.Fatalf("%s has no scaler query", tc.golden)
		}
		for _, q := range queries {
			if plan := explainWithoutSeqScan(t, q[1]); !strings.Contains(plan, tc.index) {
				t.Errorf("%s no longer uses %s:\n%s", q[1], tc.index, plan)
			}
		}
	}
}

func explainWithoutSeqScan(t *testing.T, query string) string {
	t.Helper()
	ctx := context.Background()
	tx, err := testStore.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, "EXPLAIN "+query)
	if err != nil {
		t.Fatalf("EXPLAIN %s: %v", query, err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return plan.String()
}
