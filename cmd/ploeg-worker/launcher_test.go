//go:build linux

package main

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness/launch"
	"github.com/ploeg-hq/ploeg/pkg/worker"
)

func TestWithoutALauncherTheHarnessRunsBesideTheWorker(t *testing.T) {
	t.Setenv(launcherRendezvousEnv, "")
	side, err := connectHarnessSide(slog.New(slog.DiscardHandler), worker.HarnessConfig{Name: "exec"})
	if err != nil || side.remote != nil || side.spawner == nil {
		t.Fatalf("side = %+v, err = %v", side, err)
	}
}

func TestClaudeEnvironmentRefusesASeparateHarnessContainer(t *testing.T) {
	t.Setenv(launcherRendezvousEnv, filepath.Join(t.TempDir(), "launcher.json"))
	_, err := connectHarnessSide(slog.New(slog.DiscardHandler), worker.HarnessConfig{Name: "claude-environment"})
	if err == nil || !strings.Contains(err.Error(), "harnessContainer: shared") {
		t.Fatalf("err = %v", err)
	}
}

func TestALauncherThatIsNotItsContainersFirstProcessIsRefused(t *testing.T) {
	dir, err := os.MkdirTemp("", "plw")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	rendezvous := filepath.Join(dir, "launcher.json")
	t.Setenv(launcherRendezvousEnv, rendezvous)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = launch.Serve(ctx, rendezvous) }()
	_, err = connectHarnessSide(slog.New(slog.DiscardHandler), worker.HarnessConfig{Name: "exec"})
	if err == nil || !strings.Contains(err.Error(), "shareProcessNamespace") {
		t.Fatalf("err = %v, want a refusal naming the shared PID namespace", err)
	}
}

func TestASeparateHarnessContainerNeedsGitInTheWorkerImage(t *testing.T) {
	t.Setenv(launcherRendezvousEnv, filepath.Join(t.TempDir(), "launcher.json"))
	previous := lookGit
	lookGit = func(string) (string, error) { return "", exec.ErrNotFound }
	t.Cleanup(func() { lookGit = previous })
	_, err := connectHarnessSide(slog.New(slog.DiscardHandler), worker.HarnessConfig{Name: "exec"})
	if err == nil || !strings.Contains(err.Error(), "ploeg-worker image") {
		t.Fatalf("err = %v", err)
	}
}
