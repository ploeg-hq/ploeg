package worker

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type recordingSpawner struct {
	*harness.LocalSpawner
	mu      sync.Mutex
	started [][]string
	envs    [][]string
	killed  int
}

func newRecordingSpawner() *recordingSpawner {
	return &recordingSpawner{LocalSpawner: harness.NewLocalSpawner()}
}

func (r *recordingSpawner) Start(ctx context.Context, c harness.Command) (harness.Process, error) {
	r.mu.Lock()
	r.started = append(r.started, c.Argv)
	r.envs = append(r.envs, c.Env)
	r.mu.Unlock()
	return r.LocalSpawner.Start(ctx, c)
}

func (r *recordingSpawner) KillAll(ctx context.Context) error {
	r.mu.Lock()
	r.killed++
	r.mu.Unlock()
	return r.LocalSpawner.KillAll(ctx)
}

func (r *recordingSpawner) ran(argv0 string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.ContainsFunc(r.started, func(argv []string) bool { return len(argv) > 0 && argv[0] == argv0 })
}

func TestVerificationRunsWhereTheHarnessRuns(t *testing.T) {
	rec := newRecordingSpawner()
	dir := t.TempDir()
	v := runVerification(context.Background(), harnessSide{spawner: rec, environ: os.Environ()}, dir, os.Environ(), []string{"true"}, time.Minute)
	if len(v.Checks) != 1 || !v.Checks[0].Ran || v.Checks[0].ExitCode != 0 {
		t.Fatalf("verification = %+v", v)
	}
	if !rec.ran("sh") {
		t.Fatalf("the check did not start through the harness's spawner: %v", rec.started)
	}
}

func TestOpenSpecResolvesOnTheHarnessPathThroughItsSpawner(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "openspec"), []byte(fakeOpenSpecScript), 0o755); err != nil {
		t.Fatal(err)
	}
	previous := openSpecBinary
	openSpecBinary = "openspec"
	t.Cleanup(func() { openSpecBinary = previous })
	repo := t.TempDir()
	writeTree(t, repo, changeFiles(".", "add-widget", "Widgets."))
	rec := newRecordingSpawner()

	gate := validateOpenSpecChange(context.Background(), harnessSide{spawner: rec, environ: []string{"PATH=" + bin + ":/usr/bin:/bin"}}, repo, "add-widget", t.TempDir())
	if !gate.Passed {
		t.Fatalf("gate = %+v", gate)
	}
	if !rec.ran(filepath.Join(bin, "openspec")) {
		t.Fatalf("openspec did not run from the harness's PATH through its spawner: %v", rec.started)
	}

	hidden := validateOpenSpecChange(context.Background(), harnessSide{spawner: rec, environ: []string{"PATH=/nonexistent"}}, repo, "add-widget", t.TempDir())
	if hidden.Ran || !strings.Contains(hidden.Output, "not on the harness's PATH") {
		t.Fatalf("an openspec off the harness's PATH was found: %+v", hidden)
	}
}

func TestAWriterRunStartsItsHarnessThroughTheSpawnerAndEndsWhatItLeftBehind(t *testing.T) {
	forge := newWriterForge(t, false, pullsNone)
	adapter := acpAgentDuringPrompt(t, "true")
	rec := newRecordingSpawner()
	var api checkpointRecorder
	w := New(Config{APIURL: api.server(t), ForgeURL: forge.url, DefaultForge: "forgejo", BuilderToken: "tok",
		RepoOwner: "webgrip", RepoName: "example", BaseBranch: "development", WorkDir: t.TempDir()},
		adapter, llmbroker.Static{}, discardLog())
	w.Spawner = rec
	claimed := &ClaimResponse{RunToken: "rt", Role: "builder", Writes: true, WorkItem: work.WorkItem{ID: "1", ExternalID: "7", Title: "t"}}
	_ = w.execute(context.Background(), claimed, writerBranch, "trace", "", "")
	if len(rec.started) == 0 {
		t.Fatal("the harness did not start through the worker's spawner")
	}
	if rec.killed == 0 {
		t.Fatal("the worker did not end the harness's leftover processes before verifying")
	}
}
