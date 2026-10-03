package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/sandboxlaunch"
	"github.com/ploeg-hq/ploeg/pkg/worker"
)

func runSandboxLaunch(log *slog.Logger) error {
	deadline, err := boundEnv("PLOEG_SANDBOX_RUN_DEADLINE", 0)
	if err != nil {
		return err
	}
	margin, err := boundEnv("PLOEG_SANDBOX_SHUTDOWN_MARGIN", 10*time.Minute)
	if err != nil {
		return err
	}
	startTimeout, err := boundEnv("PLOEG_SANDBOX_START_TIMEOUT", 10*time.Minute)
	if err != nil {
		return err
	}
	ttl, err := strconv.ParseInt(envOr("PLOEG_SANDBOX_TTL_SECONDS_AFTER_FINISHED", "60"), 10, 32)
	if err != nil || ttl < 0 {
		return fmt.Errorf("PLOEG_SANDBOX_TTL_SECONDS_AFTER_FINISHED must be a non-negative integer")
	}
	base, token, client, err := sandboxlaunch.InCluster(sandboxlaunch.ServiceAccountDir)
	if err != nil {
		return err
	}
	cfg := sandboxlaunch.Config{
		APIBase:                 base,
		Token:                   token,
		HTTPClient:              client,
		Namespace:               os.Getenv("POD_NAMESPACE"),
		ClaimName:               sandboxlaunch.ClaimNameFor(os.Getenv("POD_NAME")),
		JobName:                 os.Getenv("PLOEG_SANDBOX_JOB_NAME"),
		JobUID:                  os.Getenv("PLOEG_SANDBOX_JOB_UID"),
		WarmPool:                os.Getenv("PLOEG_SANDBOX_WARM_POOL"),
		RunDeadline:             deadline,
		StartTimeout:            startTimeout,
		ShutdownMargin:          margin,
		TTLSecondsAfterFinished: int32(ttl),
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, deadline+margin)
	defer cancel()
	err = sandboxlaunch.Launch(ctx, cfg, log)
	if errors.Is(err, sandboxlaunch.ErrNeverReady) {
		failUnstartedRun(log, err)
	}
	return err
}

func failUnstartedRun(log *slog.Logger, cause error) {
	claimed, err := worker.FailUnstartedRun(os.Getenv("PLOEG_API_URL"), os.Getenv("PLOEG_WORKER_BOOTSTRAP_TOKEN"),
		envOr("PLOEG_WORKER_ID", os.Getenv("POD_UID")), os.Getenv("PLOEG_TEAM"), os.Getenv("PLOEG_ROLE"), cause.Error())
	switch {
	case err != nil:
		log.Error("unstarted run not reported; the sweeper or the next launcher finds it", "err", err)
	case claimed:
		log.Info("unstarted run reported as an infra failure", "failure_reason", "infra_node")
	default:
		log.Info("no run was waiting for the unstarted sandbox")
	}
}
