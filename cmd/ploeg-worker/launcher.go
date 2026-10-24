package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/harness/adapters/claudeenv"
	"github.com/ploeg-hq/ploeg/pkg/harness/launch"
	"github.com/ploeg-hq/ploeg/pkg/worker"
)

const (
	launcherSocketEnv     = "PLOEG_HARNESS_LAUNCHER"
	launcherAcceptTimeout = 2 * time.Minute
)

type harnessSide struct {
	spawner harness.Spawner
	environ []string
	remote  *launch.Client
}

func (s harnessSide) close() {
	if s.remote != nil {
		_ = s.remote.Close()
	}
}

func (s harnessSide) executable() (string, error) {
	if s.remote == nil {
		return os.Executable()
	}
	if s.remote.Executable() == "" {
		return "", errors.New("the harness launcher did not report its own path")
	}
	return s.remote.Executable(), nil
}

func (s harnessSide) scratch(workDir string) string {
	if s.remote != nil {
		return workDir
	}
	return ""
}

func connectHarnessSide(log *slog.Logger, hc worker.HarnessConfig) (harnessSide, error) {
	socket := os.Getenv("PLOEG_HARNESS_LAUNCHER")
	if socket == "" {
		return harnessSide{spawner: harness.NewLocalSpawner(), environ: os.Environ()}, nil
	}
	if hc.Name == claudeenv.Name {
		return harnessSide{}, fmt.Errorf("%s is set, but harness %s runs its claude CLI in the worker's own container; render it with executor.harnessContainer: shared", launcherSocketEnv, claudeenv.Name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), launcherAcceptTimeout)
	defer cancel()
	client, err := launch.Accept(ctx, socket)
	if err != nil {
		return harnessSide{}, err
	}
	if client.PID() != 1 {
		client.Close()
		return harnessSide{}, fmt.Errorf("the harness launcher runs as process %d, not as the first process of its own container: the pod shares one PID namespace (shareProcessNamespace), so the harness could see the worker's processes", client.PID())
	}
	environ, err := client.Environ(ctx)
	if err != nil {
		client.Close()
		return harnessSide{}, fmt.Errorf("read the harness container's environment: %w", err)
	}
	log.Info("harness runs in its own container behind the launcher", "socket", socket)
	return harnessSide{spawner: client, environ: environ, remote: client}, nil
}

func runHarnessServe(socket string) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	err := launch.Serve(ctx, socket)
	if err != nil && !errors.Is(err, context.Canceled) && ctx.Err() == nil {
		return err
	}
	<-ctx.Done()
	return nil
}
