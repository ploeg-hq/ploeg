package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/shiftengine"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/work"
	"github.com/ploeg-hq/ploeg/pkg/worker"
)

var exampleTarget = work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "example", BaseBranch: "development"}

func admissionGitForge(t *testing.T) string {
	t.Helper()
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skip("git is not available")
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend is not available")
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=development"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	src := t.TempDir()
	for name, content := range map[string]string{"README.md": "# example\n", "AGENTS.md": "# Agents\n\nRun the tests.\n"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(src, "init")
	git(src, "add", ".")
	git(src, "commit", "-m", "fixture")
	projects := t.TempDir()
	git(projects, "clone", "--bare", src, filepath.Join(projects, "webgrip", "example.git"))
	mux := http.NewServeMux()
	mux.Handle("/webgrip/", &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + projects, "GIT_HTTP_EXPORT_ALL=1"}})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("[]")) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestOperatorWorkAdmissionEndToEndWithAWorker(t *testing.T) {
	reset(t)
	ctx := context.Background()
	forgeURL := admissionGitForge(t)
	log := slog.New(slog.DiscardHandler)
	plans, err := plan.Parse(`{"bronze": {"pool": 5, "rounds": [{"roles": [{"name": "builder", "writes": true, "cap": 1}]}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := strings.Repeat("b", 32)
	registry, _ := json.Marshal([]WorkerBootstrap{{Token: bootstrap, Team: "bronze", Role: "builder"}})
	security, err := NewWorkerSecurity(string(registry), strings.Repeat("s", 32), false)
	if err != nil {
		t.Fatal(err)
	}
	broker := &managedBrokerFixture{}
	control, err := NewLLMControl(testStore, broker, `[{"team":"bronze","role":"builder","budgetUsd":1,"models":["trusted-model"],"ttl":"1h"}]`)
	if err != nil {
		t.Fatal(err)
	}
	gate := target.NewGate(map[string]work.Target{"example": exampleTarget}, map[string]provider.RepositoryInspector{
		"forgejo": &fakeForge{states: map[string]provider.RepositoryState{"webgrip/example": {AgentsFile: true}}}})
	engine := &shiftengine.Engine{Store: testStore, Plans: plans, Log: log}
	consumers, token := operatorTestConsumers(t, []string{"bronze"}, true)
	s := &Server{Store: testStore, LeaseTTL: time.Minute, Log: log, RoleCaps: plans, Engine: engine, Readiness: gate,
		WorkerSecurity: security, LLMControl: control, OperatorConfig: OperatorConfig{Consumers: consumers}}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	body := admissionBody("req-e2e")
	body["target"] = "example"
	got, id := admissionFixture{server: s, token: token}.admit(t, body, http.StatusCreated)
	if got.WorkItem.ShiftID == nil || got.WorkItem.PoolUSD == nil || *got.WorkItem.PoolUSD != 5 {
		t.Fatalf("admission = %+v", got)
	}

	adapter, err := worker.NewAdapter(worker.HarnessConfig{Name: "exec", Args: []string{"/bin/sh", "-c", "exit 0"}})
	if err != nil {
		t.Fatal(err)
	}
	w := worker.New(worker.Config{Team: "bronze", Role: "builder", APIURL: srv.URL, BootstrapToken: bootstrap, WorkerID: "pod",
		WorkDir: t.TempDir(), ForgeURL: forgeURL, DefaultForge: harness.ForgeForgejo, BuilderToken: "tok",
		HarnessTimeout: time.Minute}, adapter, nil, log)
	if err := w.RunContext(ctx); err != nil {
		t.Fatalf("worker: %v", err)
	}

	if state := snapshotItem(t, id); state.liveShifts != 0 || state.unfinished != 0 {
		t.Fatalf("after the worker: %+v, want the Shift closed", state)
	}
	if broker.calls != 1 {
		t.Fatalf("keys minted = %d, want one for the one Run", broker.calls)
	}
	if n := countRows(t, `SELECT count(*) FROM agent_runs WHERE work_item_id=$1 AND role='builder' AND state='finished' AND outcome IS NOT NULL`, id); n != 1 {
		t.Fatalf("finished builder Runs = %d, want 1", n)
	}
	events := admissionEvents(t, admissionFixture{server: s, token: token}, id)
	var actions []string
	for _, e := range events {
		actions = append(actions, e.Action)
	}
	closed := false
	for _, action := range actions {
		closed = closed || action == "shift.closed"
	}
	if len(actions) == 0 || actions[0] != "operator.work_item.admitted" || !closed {
		t.Fatalf("event actions = %v, want the Admission first and through shift.closed", actions)
	}
}
