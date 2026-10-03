package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

var reviewFetchTimeout = 2 * time.Minute

const lsRemoteNoMatchingRefs = 2

func remoteBranchProbeArgs(branch string) []string {
	return []string{"ls-remote", "--exit-code", "origin", "refs/heads/" + branch}
}

func remoteBranchAbsent(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == lsRemoteNoMatchingRefs
}

func checkoutBranchUnderReview(ctx context.Context, log *slog.Logger, cloneDir, cloneURL, token, branch, base string,
	preAuthor bool) (commit string, report harness.OutcomeReport, failed bool) {

	fetchCtx, cancel := context.WithTimeout(ctx, reviewFetchTimeout)
	defer cancel()

	out, err := runGit(fetchCtx, cloneDir, cloneURL, token, remoteBranchProbeArgs(branch)...)
	switch {
	case err == nil:
	case remoteBranchAbsent(err) && preAuthor:
		log.Info("no branch under review before the first writing Round; reviewing the base branch",
			"branch", branch, "base", base)
		return "", harness.OutcomeReport{}, false
	case remoteBranchAbsent(err):
		return "", stuckReport("the branch under review does not exist",
			fmt.Sprintf("branch %s is absent on the forge although a writing Round ran before this reader. "+
				"Reviewing the base branch %s instead would judge code nobody wrote for this Work Item.", branch, base)), true
	default:
		return "", branchUnreachableReport(fetchCtx, branch, out), true
	}

	if out, err := runGit(fetchCtx, cloneDir, cloneURL, token, fetchBranchArgs(branch)...); err != nil {
		return "", branchUnreachableReport(fetchCtx, branch, out), true
	}
	if out, err := runGit(ctx, cloneDir, cloneURL, token, "checkout", branch); err != nil {
		return "", stuckReport("could not check out the branch under review", tail(out, 2000)), true
	}
	out, err = runGit(ctx, cloneDir, cloneURL, token, "rev-parse", "HEAD")
	if err != nil {
		return "", stuckReport("could not read the commit under review", tail(out, 2000)), true
	}
	return strings.TrimSpace(string(out)), harness.OutcomeReport{}, false
}

func branchUnreachableReport(fetchCtx context.Context, branch string, gitOutput []byte) harness.OutcomeReport {
	reason := strings.TrimSpace(tail(gitOutput, 400))
	if errors.Is(fetchCtx.Err(), context.DeadlineExceeded) {
		reason = fmt.Sprintf("no answer from the forge within %s", reviewFetchTimeout)
	}
	return harness.OutcomeReport{
		Outcome:       work.OutcomeFailed,
		Summary:       fmt.Sprintf("could not fetch the branch under review %s from the forge: %s", branch, reason),
		FailureReason: string(work.FailureInfraNode),
	}
}

func withReviewedCommit(report harness.OutcomeReport, branch, prURL, commit string) harness.OutcomeReport {
	if commit == "" {
		return report
	}
	if report.Checkpoint == nil {
		report.Checkpoint = &work.Checkpoint{Phase: "reviewed", Branch: branch, PRURL: prURL}
	} else {
		checkpoint := *report.Checkpoint
		report.Checkpoint = &checkpoint
	}
	report.Checkpoint.Commit = commit
	return report
}
