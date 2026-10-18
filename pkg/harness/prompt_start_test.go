//go:build unix

package harness

import (
	"context"
	"testing"
	"time"
)

func TestReportPromptStartTakesTheEarlierOfThePromptLimitAndTheDeadline(t *testing.T) {
	report := func(ctx context.Context, prompt, idle time.Duration) PromptStart {
		t.Helper()
		var got []PromptStart
		env := RunEnv{PromptStarted: func(p PromptStart) { got = append(got, p) }}
		ReportPromptStart(ctx, env, prompt, idle)
		if len(got) != 1 {
			t.Fatalf("reports = %+v, want one", got)
		}
		return got[0]
	}
	if p := report(context.Background(), 45*time.Minute, 10*time.Minute); p.Prompt != 45*time.Minute || p.Idle != 10*time.Minute || time.Since(p.At) > time.Minute {
		t.Fatalf("without a deadline = %+v", p)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if p := report(ctx, 45*time.Minute, 0); p.Prompt > 30*time.Minute || p.Prompt < 29*time.Minute || p.Idle != 0 {
		t.Fatalf("with an earlier deadline = %+v, want about 30m and no idle limit", p)
	}
	if p := report(ctx, 0, time.Minute); p.Prompt > 30*time.Minute || p.Prompt < 29*time.Minute {
		t.Fatalf("with only a deadline = %+v, want about 30m", p)
	}
	if p := report(ctx, 10*time.Minute, 0); p.Prompt != 10*time.Minute {
		t.Fatalf("with a later deadline = %+v, want the prompt limit", p)
	}
	ReportPromptStart(ctx, RunEnv{}, time.Minute, time.Minute)
}

func TestRunCommandReportsThePromptStartWithItsIdleWatchdog(t *testing.T) {
	var got []PromptStart
	env := testEnv(t)
	env.IdleTimeout = 15 * time.Minute
	env.PromptStarted = func(p PromptStart) { got = append(got, p) }
	if _, err := RunCommand(scriptAdapter{argv: []string{"/bin/sh", "-c", "echo hi"}}).Run(context.Background(), TaskSpec{}, env); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Prompt != 0 || got[0].Idle != 15*time.Minute {
		t.Fatalf("prompt starts = %+v, want one with the 15m idle watchdog and no prompt limit of its own", got)
	}

	got = nil
	if _, err := RunCommand(scriptAdapter{argv: []string{"/nonexistent/harness"}}).Run(context.Background(), TaskSpec{}, env); err == nil {
		t.Fatal("a missing binary ran")
	}
	if len(got) != 0 {
		t.Fatalf("a harness that never started reported %+v", got)
	}
}
