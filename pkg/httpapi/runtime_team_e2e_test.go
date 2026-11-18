package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/shiftengine"
	"github.com/ploeg-hq/ploeg/pkg/spendcap"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/team"
	"github.com/ploeg-hq/ploeg/pkg/teamruntime"
	"github.com/ploeg-hq/ploeg/pkg/work"
	"github.com/ploeg-hq/ploeg/pkg/worker"
)

type runtimeE2E struct {
	server   *Server
	svc      *teamruntime.Service
	token    string
	teamID   string
	broker   *managedBrokerFixture
	poolBoot string
}

func newRuntimeE2E(t *testing.T, intake team.RuntimeIntake) runtimeE2E {
	t.Helper()
	reset(t)
	resetTeamIdentity(t)
	t.Cleanup(func() { reset(t); resetTeamIdentity(t) })
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	directory := team.NewDirectory(nil)
	svc := &teamruntime.Service{Store: testStore, Directory: directory, Log: log,
		Config: &config.TeamCreation{Enabled: true, Models: []string{"trusted-model"},
			Tenants: map[string]config.TeamCreationTenant{"default": {Spend: spendcap.Cap{USD: 100, Period: "month"}}}},
		Pools:        team.Pools{{Tenant: "default", Harness: "exec", MaxReplicaCount: 2, Isolation: team.IsolationProxy}},
		TargetExists: func(key string) bool { return key == "example" }}
	manifest := team.Manifest{Key: "docs", Name: "builder · exec", Sponsor: "oidc-person",
		Plan:   team.RuntimePlan{Pool: 5, Rounds: []team.RuntimeRound{{Roles: []team.RuntimeRole{{Name: "builder", Writes: true, Cap: 1, Harness: "exec", Model: "trusted-model"}}}}},
		Spend:  team.RuntimeSpend{Cap: &spendcap.Cap{USD: 50, Period: "month"}},
		Intake: intake}
	created, _, err := testStore.CreateRuntimeTeam(ctx, "default", manifest, team.Decision{State: team.StateActive},
		store.TeamLimits{Max: 10, Allowance: spendcap.Cap{USD: 100, Period: "month"}, Reservation: team.AliasReservation, Now: time.Now()},
		store.TeamAudit{Actor: "oidc-person"}, team.MustNewID)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	poolBoot := strings.Repeat("p", 32)
	registry, _ := json.Marshal([]WorkerBootstrap{{Token: poolBoot, Pool: &BootstrapPool{Tenant: "default", Harness: "exec"}}})
	security, err := NewWorkerSecurity(string(registry), strings.Repeat("s", 32), false)
	if err != nil {
		t.Fatal(err)
	}
	broker := &managedBrokerFixture{}
	control, err := NewLLMControl(testStore, broker, `[]`)
	if err != nil {
		t.Fatal(err)
	}
	control.Runtime = func(teamKey, role string) (LLMPolicy, bool) {
		models, budget, ok := svc.LLMPolicy(teamKey, role)
		if !ok {
			return LLMPolicy{}, false
		}
		return LLMPolicy{Team: teamKey, Role: role, Models: models, DefaultModel: models[0], BudgetUSD: budget, TTL: "1h"}, true
	}
	gate := target.NewGate(map[string]work.Target{"example": exampleTarget}, map[string]provider.RepositoryInspector{
		"forgejo": &fakeForge{states: map[string]provider.RepositoryState{"webgrip/example": {AgentsFile: true}}}})
	engine := &shiftengine.Engine{Store: testStore, Plans: plan.Plans{}, Runtime: svc.Plan, Log: log,
		Selections: RunSelections{LLM: control, Runtime: svc.Harness}}
	consumers, token := operatorTestConsumers(t, []string{"docs"}, true)
	s := &Server{Store: testStore, LeaseTTL: time.Minute, Log: log, Engine: engine, Readiness: gate, Teams: directory, RuntimeTeams: svc,
		WorkerSecurity: security, LLMControl: control, OperatorConfig: OperatorConfig{Consumers: consumers}}
	return runtimeE2E{server: s, svc: svc, token: token, teamID: created.ID, broker: broker, poolBoot: poolBoot}
}

func runtimeAdmission(requestID, teamRef string) map[string]any {
	body := admissionBody(requestID)
	body["team"], body["target"] = teamRef, "example"
	return body
}

func TestARuntimeTeamsAdmittedWorkBecomesAShiftWhoseRunThePoolClaimsAndFinishes(t *testing.T) {
	rig := newRuntimeE2E(t, team.RuntimeIntake{Admissions: true, Targets: []string{"example"}})
	ctx := context.Background()
	forgeURL := admissionGitForge(t)
	srv := httptest.NewServer(rig.server.Handler())
	t.Cleanup(srv.Close)

	got, id := admissionFixture{server: rig.server, token: rig.token}.admit(t, runtimeAdmission("rt-e2e", rig.teamID), http.StatusCreated)
	if got.WorkItem.Team != "docs" || got.WorkItem.ShiftID == nil {
		t.Fatalf("admission = %+v, want a Shift for docs", got)
	}
	if n := countRows(t, `SELECT count(*) FROM agent_runs WHERE work_item_id = $1 AND state = 'pending' AND harness_requested = 'exec' AND team_id = $2`, id, rig.teamID); n != 1 {
		t.Fatalf("pending exec Runs of the runtime Team = %d, want 1", n)
	}

	adapter, err := worker.NewAdapter(worker.HarnessConfig{Name: "exec", Args: []string{"/bin/sh", "-c", "exit 0"}})
	if err != nil {
		t.Fatal(err)
	}
	w := worker.New(worker.Config{APIURL: srv.URL, BootstrapToken: rig.poolBoot, WorkerID: "pool-pod",
		WorkDir: t.TempDir(), ForgeURL: forgeURL, DefaultForge: harness.ForgeForgejo, BuilderToken: "tok",
		HarnessTimeout: time.Minute}, adapter, nil, slog.New(slog.DiscardHandler))
	if err := w.RunContext(ctx); err != nil {
		t.Fatalf("pool worker: %v", err)
	}
	if state := snapshotItem(t, id); state.liveShifts != 0 || state.unfinished != 0 {
		t.Fatalf("after the pool worker: %+v, want the Shift closed", state)
	}
	if n := countRows(t, `SELECT count(*) FROM agent_runs WHERE work_item_id = $1 AND role = 'builder' AND state = 'finished' AND outcome IS NOT NULL`, id); n != 1 {
		t.Fatalf("finished builder Runs = %d, want 1", n)
	}
	if rig.broker.calls != 1 {
		t.Fatalf("keys minted = %d, want one under the runtime Role's policy", rig.broker.calls)
	}
}

func TestARuntimeTeamTakesAdmissionsOnlyAsItsIntakeAllows(t *testing.T) {
	for name, tc := range map[string]struct {
		intake team.RuntimeIntake
		edit   func(*testing.T, runtimeE2E)
		code   string
	}{
		"no admissions":   {team.RuntimeIntake{}, nil, "admissions_not_accepted"},
		"other target":    {team.RuntimeIntake{Admissions: true, Targets: []string{"app"}}, nil, "target_not_allowed"},
		"shadowed": {team.RuntimeIntake{Admissions: true}, func(t *testing.T, r runtimeE2E) {
			if _, err := testStore.ApplyTeamDecision(context.Background(), r.teamID, team.Decision{State: team.StateShadowed, ShadowedBy: "team_x"}, nil); err != nil {
				t.Fatal(err)
			}
		}, "team_unavailable"},
		"draining": {team.RuntimeIntake{Admissions: true}, func(t *testing.T, r runtimeE2E) {
			if _, _, err := testStore.IngestAssigned(context.Background(), work.WorkItem{Provider: "vikunja", ExternalID: "open", Team: "docs", Title: "t"}); err != nil {
				t.Fatal(err)
			}
			if _, err := testStore.DrainRuntimeTeam(context.Background(), r.teamID, nil, store.TeamAudit{Actor: "p"}); err != nil {
				t.Fatal(err)
			}
		}, "team_draining"},
	} {
		t.Run(name, func(t *testing.T) {
			rig := newRuntimeE2E(t, tc.intake)
			if tc.edit != nil {
				tc.edit(t, rig)
				if err := rig.svc.Refresh(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			w := operatorExecutionRequest(rig.server, "POST", admissionPath, rig.token, "alice", runtimeAdmission("rt-"+strings.ReplaceAll(name, " ", "-"), "docs"))
			if w.Code < 400 || operatorErrorCode(t, w) != tc.code {
				t.Fatalf("%d %s, want %s", w.Code, w.Body, tc.code)
			}
		})
	}
}

func TestAStaticTeamsWorkerGetsNoRunOfARuntimeTeam(t *testing.T) {
	rig := newRuntimeE2E(t, team.RuntimeIntake{Admissions: true})
	_, id := admissionFixture{server: rig.server, token: rig.token}.admit(t, runtimeAdmission("rt-static", "docs"), http.StatusCreated)
	if _, err := testStore.ClaimRoleFor(context.Background(), "docs", "builder", "exec", time.Minute, 0, 0); err == nil {
		t.Fatal("a claim without the pool marker took the runtime Team's Run")
	}
	if n := countRows(t, `SELECT count(*) FROM agent_runs WHERE work_item_id = $1 AND state = 'pending'`, id); n != 1 {
		t.Fatalf("pending Runs = %d", n)
	}
}
