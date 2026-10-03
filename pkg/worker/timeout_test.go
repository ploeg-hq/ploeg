//go:build unix

package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/harness/adapters/openhands"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func hangingAdapter(t *testing.T, body string) harness.Adapter {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "hanging-agent.sh")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return harness.RunCommand(openhands.New(bin))
}

func TestHungHarnessIsStoppedReportedFailedAndItsKeyBlocked(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		limit  time.Duration
		idle   time.Duration
		cause  error
		reason work.FailureReason
	}{
		{"run timeout on a chatty hang", "while true; do echo working; sleep 0.05; done", 400 * time.Millisecond, 0, errHarnessTimeout, work.FailureTimeout},
		{"idle watchdog on a silent hang", "echo started; sleep 60 & sleep 60", time.Minute, 300 * time.Millisecond, harness.ErrIdle, work.FailureIdle},
	} {
		t.Run(tc.name, func(t *testing.T) {
			broker := &recordingBroker{key: "sk-test-fake-key"}
			env := runEnv(t)
			env.IdleTimeout = tc.idle
			start := time.Now()
			_, mintErr, runErr := runAgent(context.Background(), discardLog(), broker,
				hangingAdapter(t, tc.body), testTaskSpec(), env, llmbroker.MintRequest{RunToken: "abc123def456ff"}, tc.limit, "")
			if mintErr != nil {
				t.Fatal(mintErr)
			}
			if !errors.Is(runErr, tc.cause) {
				t.Fatalf("run error=%v, want %v", runErr, tc.cause)
			}
			if elapsed := time.Since(start); elapsed > 10*time.Second {
				t.Fatalf("hung harness survived for %s", elapsed)
			}
			if broker.revoked != 1 {
				t.Fatalf("key not blocked after a hung harness was stopped: revoked=%d", broker.revoked)
			}
			report := resolveOutcome("openhands", harness.OutcomeReport{}, runErr, nil, "", false, "item", "agent/vik-1", nil, true, true)
			if report.Outcome != work.OutcomeFailed || report.FailureReason != string(tc.reason) {
				t.Fatalf("hung harness reported %+v", report)
			}
		})
	}
}

func TestTimedOutHarnessStillCreditsAPullRequestItOpened(t *testing.T) {
	runErr := errors.Join(errHarnessTimeout, errors.New("signal: killed"))
	report := resolveOutcome("openhands", harness.OutcomeReport{}, runErr, nil, "https://forge.example/pulls/1", false, "item", "agent/vik-1", nil, true, true)
	if report.Outcome != work.OutcomePROpened {
		t.Fatalf("a PR opened before the timeout was discarded: %+v", report)
	}
}

func TestLeaseLossIsNotReportedAsATimeout(t *testing.T) {
	report := resolveOutcome("openhands", harness.OutcomeReport{}, context.Canceled, errLeaseLost, "", false, "item", "agent/vik-1", nil, true, true)
	if report.FailureReason == string(work.FailureTimeout) {
		t.Fatalf("lease loss classified as timeout: %+v", report)
	}
}

func TestSilenceAfterAPullRequestIsRecordedAsIdleNotTimeout(t *testing.T) {
	runErr := errors.Join(harness.ErrIdle, errors.New("signal: killed"))
	report := resolveOutcome("openhands", harness.OutcomeReport{}, runErr, nil, "https://forge.example/pulls/1", false, "item", "agent/vik-1", nil, true, true)
	if report.Outcome != work.OutcomePROpened || report.FailureReason != string(work.FailureIdle) {
		t.Fatalf("silence after the PR opened recorded as %+v", report)
	}
}
