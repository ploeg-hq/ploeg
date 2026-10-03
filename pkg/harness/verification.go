package harness

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Verification results. A Verification's Result is one of the first three;
// a VerificationCheck's Result is passed, failed or not_run.
const (
	VerificationPassed      = "passed"
	VerificationFailed      = "failed"
	VerificationIncomplete  = "incomplete"
	VerificationCheckNotRun = "not_run"
)

// Verification is the worker's record of the checks it ran on a writing
// Run's checkout after the harness exited. Only the worker sets it: the
// worker discards whatever an adapter or agent put here, and ploegd stores
// it only for a writing Run. Readers of a Run's verification use this
// record, never the summary or findings prose.
type Verification struct {
	// Result is passed, failed or incomplete.
	Result string `json:"result"`
	// Commit is the full object name of HEAD when the checks ran; empty when
	// git could not tell.
	Commit string `json:"commit,omitempty"`
	// Dirty is true when the working tree had uncommitted changes, so the
	// result may not describe Commit.
	Dirty bool `json:"dirty"`
	// Stopped says why checks after the failing one did not run.
	Stopped    string              `json:"stopped,omitempty"`
	StartedAt  time.Time           `json:"startedAt"`
	FinishedAt time.Time           `json:"finishedAt"`
	Checks     []VerificationCheck `json:"checks"`
}

// VerificationCheck is one configured command. ExitCode, StartedAt and
// FinishedAt are absent for a check that did not run.
type VerificationCheck struct {
	Command    string     `json:"command"`
	Result     string     `json:"result"`
	ExitCode   *int       `json:"exitCode,omitempty"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

var objectNameRe = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// FailedCheck returns the first check that failed.
func (v Verification) FailedCheck() (VerificationCheck, bool) {
	for _, c := range v.Checks {
		if c.Result == VerificationFailed {
			return c, true
		}
	}
	return VerificationCheck{}, false
}

// Validate checks the record against the contract, and ploegd refuses an
// outcome whose record fails it: known results, a full object name, start
// and finish times in order, a passed check with exit code 0 and a failed one
// without, each check that ran timed inside the record, a check that did not
// run with no exit code or times, and a Result that agrees with its checks.
// A passed record needs at least one check: a pass with no evidence is not one.
func (v Verification) Validate() error {
	switch v.Result {
	case VerificationPassed, VerificationFailed, VerificationIncomplete:
	default:
		return fmt.Errorf("verification.result %q must be passed, failed or incomplete", v.Result)
	}
	if v.Commit != "" && !objectNameRe.MatchString(v.Commit) {
		return errors.New("verification.commit must be a full lowercase hexadecimal object name")
	}
	if v.StartedAt.IsZero() || v.FinishedAt.IsZero() || v.FinishedAt.Before(v.StartedAt) {
		return errors.New("verification.startedAt and finishedAt must both be set, in that order")
	}
	failed, notRun := false, false
	for i, c := range v.Checks {
		switch c.Result {
		case VerificationPassed, VerificationFailed:
			if err := v.validateRanCheck(i, c); err != nil {
				return err
			}
			failed = failed || c.Result == VerificationFailed
		case VerificationCheckNotRun:
			if c.ExitCode != nil || c.StartedAt != nil || c.FinishedAt != nil {
				return fmt.Errorf("verification.checks[%d] did not run but has an exitCode or times", i)
			}
			notRun = true
		default:
			return fmt.Errorf("verification.checks[%d].result %q must be passed, failed or not_run", i, c.Result)
		}
	}
	switch {
	case failed && v.Result != VerificationFailed:
		return errors.New("verification.result must be failed when a check failed")
	case !failed && v.Result == VerificationFailed:
		return errors.New("verification.result is failed but no check failed")
	case !failed && notRun && v.Result == VerificationPassed:
		return errors.New("verification.result is passed but a check did not run")
	case v.Result == VerificationPassed && len(v.Checks) == 0:
		return errors.New("verification.result is passed but no check ran")
	}
	return nil
}

func (v Verification) validateRanCheck(i int, c VerificationCheck) error {
	switch {
	case c.ExitCode == nil:
		return fmt.Errorf("verification.checks[%d] ran but has no exitCode", i)
	case c.Result == VerificationPassed && *c.ExitCode != 0:
		return fmt.Errorf("verification.checks[%d] passed with exit code %d", i, *c.ExitCode)
	case c.Result == VerificationFailed && *c.ExitCode == 0:
		return fmt.Errorf("verification.checks[%d] failed with exit code 0", i)
	case c.StartedAt == nil || c.FinishedAt == nil:
		return fmt.Errorf("verification.checks[%d] ran but has no startedAt or finishedAt", i)
	case c.FinishedAt.Before(*c.StartedAt) || c.StartedAt.Before(v.StartedAt) || v.FinishedAt.Before(*c.FinishedAt):
		return fmt.Errorf("verification.checks[%d] times are out of order or outside the verification's", i)
	}
	return nil
}
