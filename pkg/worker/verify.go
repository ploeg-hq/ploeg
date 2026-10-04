package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// DefaultVerifyTimeout bounds the worker's own run of the verification
// commands after a writing Run (PLOEG_VERIFY_TIMEOUT).
const DefaultVerifyTimeout = 15 * time.Minute

const (
	verifyScriptName    = "ploeg-verify"
	verifyOutputLimit   = 3000
	verifyScriptEnv     = "PLOEG_VERIFY_SCRIPT"
	skillsDirectoryEnv  = "PLOEG_SKILLS_DIR"
	verificationHeading = "### Ploeg verification"
)

// ParseVerifyCommands decodes PLOEG_VERIFY_COMMANDS: a JSON array of shell
// command lines, run in order from the repository root.
func ParseVerifyCommands(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var cmds []string
	if err := json.Unmarshal([]byte(raw), &cmds); err != nil {
		return nil, fmt.Errorf("PLOEG_VERIFY_COMMANDS: %w", err)
	}
	for i, c := range cmds {
		if strings.TrimSpace(c) == "" {
			return nil, fmt.Errorf("PLOEG_VERIFY_COMMANDS[%d] is empty", i)
		}
		if strings.ContainsAny(c, "\n\r") {
			return nil, fmt.Errorf("PLOEG_VERIFY_COMMANDS[%d] spans more than one line", i)
		}
	}
	return cmds, nil
}

// writeVerifyScript writes the script the harness runs as $PLOEG_VERIFY_SCRIPT.
// It runs the same commands, in the same order, as runVerification.
func writeVerifyScript(dir string, cmds []string) (string, error) {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("run() {\n")
	b.WriteString("\tprintf '+ %s\\n' \"$1\" >&2\n")
	b.WriteString("\tsh -c \"$1\" || { status=$?; printf 'ploeg-verify: \"%s\" failed with exit status %s\\n' \"$1\" \"$status\" >&2; exit \"$status\"; }\n")
	b.WriteString("}\n")
	for _, c := range cmds {
		fmt.Fprintf(&b, "run %s\n", shellQuote(c))
	}
	fmt.Fprintf(&b, "printf 'ploeg-verify: all %d checks passed\\n' >&2\n", len(cmds))
	path := filepath.Join(dir, verifyScriptName)
	if err := os.WriteFile(path, []byte(b.String()), 0o755); err != nil {
		return "", err
	}
	return path, nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

type checkResult struct {
	Command    string
	Ran        bool
	ExitCode   int
	Output     string
	StartedAt  time.Time
	FinishedAt time.Time
}

type verification struct {
	Commit string
	Dirty  bool
	// Fresh says the checks ran on a fresh checkout of the pushed commit.
	Fresh      bool
	Checks     []checkResult
	StartedAt  time.Time
	FinishedAt time.Time
	// Stopped says why the result is not a pass when no check failed, or why
	// checks after the failing one did not run; empty when every check ran
	// and passed on a commit that stayed the pull request's head.
	Stopped string
}

const shortCommitLength = 12

func (v verification) shortCommit() string {
	if len(v.Commit) > shortCommitLength {
		return v.Commit[:shortCommitLength]
	}
	return v.Commit
}

func (v verification) result() string {
	if _, failed := v.failed(); failed {
		return harness.VerificationFailed
	}
	if v.Stopped != "" {
		return harness.VerificationIncomplete
	}
	return harness.VerificationPassed
}

func (v verification) record() *harness.Verification {
	rec := &harness.Verification{
		Result: v.result(), Commit: v.Commit, Dirty: v.Dirty, Stopped: v.Stopped,
		StartedAt: v.StartedAt.UTC(), FinishedAt: v.FinishedAt.UTC(),
		Checks: make([]harness.VerificationCheck, 0, len(v.Checks)),
	}
	for _, c := range v.Checks {
		check := harness.VerificationCheck{Command: c.Command, Result: harness.VerificationCheckNotRun}
		if c.Ran {
			check.Result = harness.VerificationPassed
			if c.ExitCode != 0 {
				check.Result = harness.VerificationFailed
			}
			exitCode, started, finished := c.ExitCode, c.StartedAt.UTC(), c.FinishedAt.UTC()
			check.ExitCode, check.StartedAt, check.FinishedAt = &exitCode, &started, &finished
		}
		rec.Checks = append(rec.Checks, check)
	}
	return rec
}

func (v verification) ran() bool {
	for _, c := range v.Checks {
		if c.Ran {
			return true
		}
	}
	return false
}

func (v verification) failed() (checkResult, bool) {
	for _, c := range v.Checks {
		if c.Ran && c.ExitCode != 0 {
			return c, true
		}
	}
	return checkResult{}, false
}

// pushedCandidate is the commit a writing Run delivered: the head of its pull
// request as the forge reported it, on the Run's branch.
type pushedCandidate struct {
	dir      string
	cloneURL string
	token    string
	branch   string
	commit   string
}

// verifyPushedCommit runs cmds on a fresh clone of the candidate's branch,
// never on the agent's checkout, so an uncommitted fix or a local file cannot
// make the pushed commit pass. The clone must be at the candidate's commit,
// and the branch must still point there after the checks; otherwise the
// verification is incomplete and names why.
func verifyPushedCommit(ctx context.Context, c pushedCandidate, env, cmds []string, limit time.Duration) verification {
	defer os.RemoveAll(c.dir)
	if err := c.checkout(ctx); err != nil {
		return notVerified(c.commit, cmds, err.Error())
	}
	v := runVerification(ctx, c.dir, env, cmds, limit)
	v.Fresh = true
	if v.Stopped != "" {
		return v
	}
	after := readBranchHead(ctx, c.dir, c.cloneURL, c.token, c.branch)
	switch {
	case after.err != nil:
		v.Stopped = "Ploeg could not confirm that branch " + c.branch + " still points at the verified commit: " + after.err.Error()
	case after.commit != c.commit:
		v.Stopped = fmt.Sprintf("branch %s moved from %s to %s while the checks ran, so they describe a commit that is no longer the pull request's head",
			c.branch, shortCommit(c.commit), after)
	}
	return v
}

func (c pushedCandidate) checkout(ctx context.Context) error {
	if c.commit == "" {
		return errors.New("the forge reported no head commit for the pull request, so there was no pushed commit to verify")
	}
	if err := os.RemoveAll(c.dir); err != nil {
		return fmt.Errorf("could not prepare a fresh checkout: %v", err)
	}
	if out, err := runGit(ctx, "", c.cloneURL, c.token, cloneArgs(c.branch, c.cloneURL, c.dir)...); err != nil {
		return fmt.Errorf("could not clone branch %s to verify commit %s: %s", c.branch, shortCommit(c.commit), strings.TrimSpace(tail(out, 400)))
	}
	if head := gitOutput(ctx, c.dir, "rev-parse", "HEAD"); head != c.commit {
		return fmt.Errorf("branch %s is at %s, not at the pull request's head %s, so a newer push replaced the commit before it was verified",
			c.branch, shortCommit(head), shortCommit(c.commit))
	}
	return nil
}

func notVerified(commit string, cmds []string, reason string) verification {
	now := time.Now()
	v := verification{StartedAt: now, FinishedAt: now, Stopped: reason}
	if objectName(commit) {
		v.Commit = commit
	}
	for _, c := range cmds {
		v.Checks = append(v.Checks, checkResult{Command: c})
	}
	return v
}

func objectName(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	return strings.Trim(s, "0123456789abcdef") == ""
}

// runVerification runs cmds in order in dir with env, stopping at the first
// failure. The worker runs it after the harness exits, so what it reports is
// Ploeg's observation, not the agent's claim.
func runVerification(ctx context.Context, dir string, env, cmds []string, limit time.Duration) verification {
	v := verification{StartedAt: time.Now(), Commit: gitOutput(ctx, dir, "rev-parse", "HEAD")}
	v.Dirty = gitOutput(ctx, dir, "status", "--porcelain") != ""
	if limit <= 0 {
		limit = DefaultVerifyTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	for _, c := range cmds {
		if v.Stopped != "" {
			v.Checks = append(v.Checks, checkResult{Command: c})
			continue
		}
		cmd := exec.CommandContext(runCtx, "sh", "-c", c)
		cmd.Dir = dir
		cmd.Env = env
		harness.KillProcessGroupOnCancel(cmd)
		cmd.WaitDelay = harness.ProcessWaitDelay
		var out harness.TailBuffer
		cmd.Stdout, cmd.Stderr = &out, &out
		started := time.Now()
		err := cmd.Run()
		res := checkResult{Command: c, Ran: true, Output: tail(out.Bytes(), verifyOutputLimit), StartedAt: started, FinishedAt: time.Now()}
		if cmd.ProcessState != nil {
			res.ExitCode = cmd.ProcessState.ExitCode()
		}
		if err != nil && res.ExitCode == 0 {
			res.ExitCode = -1
			res.Output = strings.TrimSpace(res.Output + "\n" + err.Error())
		}
		v.Checks = append(v.Checks, res)
		switch {
		case errors.Is(runCtx.Err(), context.DeadlineExceeded):
			v.Stopped = fmt.Sprintf("verification exceeded its %s limit", limit)
		case runCtx.Err() != nil:
			v.Stopped = "the Run was cancelled"
		case res.ExitCode != 0:
			v.Stopped = "an earlier check failed"
		}
	}
	v.FinishedAt = time.Now()
	return v
}

func gitOutput(ctx context.Context, dir string, args ...string) string {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// markdown renders the verification for the pull request and the next
// Round's briefing.
func (v verification) markdown() string {
	var b strings.Builder
	b.WriteString(verificationHeading + "\n\n")
	commit := "the Run's checkout"
	if v.Commit != "" {
		commit = "commit `" + v.shortCommit() + "`"
	}
	switch {
	case !v.ran():
		fmt.Fprintf(&b, "Ploeg could not run the configured checks on %s: %s.", commit, v.Stopped)
	case v.Fresh:
		fmt.Fprintf(&b, "Ploeg ran the configured checks on a fresh checkout of %s, the pushed head of the pull request, after the agent finished.", commit)
	default:
		fmt.Fprintf(&b, "Ploeg ran the configured checks on %s after the agent finished.", commit)
	}
	if _, failed := v.failed(); !failed && v.ran() && v.Stopped != "" {
		b.WriteString(" The result is incomplete: " + v.Stopped + ".")
	}
	if v.Dirty {
		b.WriteString(" The working tree had uncommitted changes, so the result may not match what was pushed.")
	}
	b.WriteString("\n\n| Check | Result |\n| --- | --- |\n")
	for _, c := range v.Checks {
		result := "passed"
		switch {
		case !c.Ran:
			result = "not run: " + v.Stopped
		case c.ExitCode != 0:
			result = fmt.Sprintf("**failed** (exit status %d)", c.ExitCode)
		}
		fmt.Fprintf(&b, "| `%s` | %s |\n", strings.ReplaceAll(c.Command, "|", `\|`), result)
	}
	if f, failed := v.failed(); failed {
		fmt.Fprintf(&b, "\nOutput of `%s`, last %d characters:\n\n```text\n%s\n```\n", f.Command, verifyOutputLimit, strings.TrimSpace(f.Output))
	}
	return b.String()
}

// withVerification attaches a writing Run's verification to its report: the
// structured record is what ploegd stores and renders from, the findings
// reach the pull request and the next Round's briefing, and the summary
// names the result for a person reading it.
func withVerification(report harness.OutcomeReport, v verification) harness.OutcomeReport {
	report.Verification = v.record()
	report.Findings = strings.TrimSpace(strings.TrimSpace(report.Findings) + "\n\n" + v.markdown())
	if f, failed := v.failed(); failed {
		report.Summary += fmt.Sprintf(" [Ploeg verification failed: %s]", f.Command)
	} else if v.Stopped != "" {
		report.Summary += " [Ploeg verification incomplete: " + v.Stopped + "]"
	} else {
		report.Summary += " [Ploeg verification passed]"
	}
	return report
}

func verifiesOutcome(o work.Outcome) bool {
	return o == work.OutcomePROpened || o == work.OutcomePRUpdated
}

// runSupportSection tells the agent what Ploeg put in its sandbox: the skills,
// the toolchains on PATH and the verification script.
func runSupportSection(skillPaths []string, tcs []Toolchain, cmds []string, writes bool) string {
	if len(skillPaths) == 0 && len(tcs) == 0 && len(cmds) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## What Ploeg provides in this sandbox\n\n")
	if len(skillPaths) > 0 {
		b.WriteString("- Skills from Ploeg (Agent Skills format). Your tool may have loaded them\n  already; if not, read each one before you start:\n")
		for _, p := range skillPaths {
			fmt.Fprintf(&b, "    %s\n", p)
		}
	}
	for _, tc := range tcs {
		fmt.Fprintf(&b, "- Toolchain %s is on PATH (%s).\n", tc.Name, strings.Join(tc.Path, ", "))
	}
	if len(cmds) > 0 {
		b.WriteString("- The script named by $" + verifyScriptEnv + " runs these checks in order and\n  stops at the first failure:\n")
		for _, c := range cmds {
			fmt.Fprintf(&b, "      %s\n", c)
		}
		if writes {
			b.WriteString("  Run it before every push and commit only when it passes. Ploeg runs the\n  same checks after you finish and posts the result on the pull request.\n")
		} else {
			b.WriteString("  Run it on the checkout before you decide your verdict.\n")
		}
	}
	return b.String()
}
