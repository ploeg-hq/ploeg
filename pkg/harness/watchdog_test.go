//go:build unix

package harness

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunCommand_IdleWatchdogKillsASilentHarnessAndItsChildren(t *testing.T) {
	bin := writeScript(t, "echo started; sleep 60 & sleep 60")
	env := testEnv(t)
	env.IdleTimeout = 300 * time.Millisecond
	start := time.Now()
	_, err := RunCommand(scriptAdapter{argv: []string{bin}}).Run(context.Background(), TaskSpec{}, env)
	if !errors.Is(err, ErrIdle) {
		t.Fatalf("silent harness not reported idle: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("idle harness and its background child took %s to die", elapsed)
	}
}

func TestRunCommand_IdleWatchdogSparesAHarnessThatKeepsTalking(t *testing.T) {
	talking := "echo tick; for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do sleep 0.1; echo tick; done"
	env := testEnv(t)
	env.IdleTimeout = time.Second
	if _, err := RunCommand(scriptAdapter{argv: []string{"/bin/sh", "-c", talking}}).Run(context.Background(), TaskSpec{}, env); err != nil {
		t.Fatalf("talking harness was stopped: %v", err)
	}
}

func TestRunCommand_IdleWatchdogSparesASilentHarnessWhoseActivityIsTouched(t *testing.T) {
	env := testEnv(t)
	env.IdleTimeout = 300 * time.Millisecond
	env.Activity = NewActivity()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
				env.Activity.Touch()
			}
		}
	}()
	if _, err := RunCommand(scriptAdapter{argv: []string{"/bin/sh", "-c", "sleep 1"}}).Run(context.Background(), TaskSpec{}, env); err != nil {
		t.Fatalf("a harness with model traffic but no output was stopped: %v", err)
	}
}

func TestRunCommand_CancelKillsTheWholeProcessGroup(t *testing.T) {
	bin := writeScript(t, "sleep 60 & sleep 60")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := RunCommand(scriptAdapter{argv: []string{bin}}).Run(ctx, TaskSpec{}, testEnv(t))
	if err == nil || errors.Is(err, ErrIdle) {
		t.Fatalf("cancelled harness error=%v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("background child kept the harness alive for %s", elapsed)
	}
}
