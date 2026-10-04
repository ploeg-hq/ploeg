package worker

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const maxNamedUnpublished = 20

var branchReadTimeout = 2 * time.Minute

type branchHead struct {
	commit string
	err    error
}

func (h branchHead) String() string {
	if h.commit == "" {
		return "absent"
	}
	return shortCommit(h.commit)
}

func readBranchHead(ctx context.Context, dir, cloneURL, token, branch string) branchHead {
	readCtx, cancel := context.WithTimeout(ctx, branchReadTimeout)
	defer cancel()
	out, err := runGit(readCtx, dir, cloneURL, token, remoteBranchProbeArgs(branch)...)
	return parseBranchHead(branch, out, err)
}

func parseBranchHead(branch string, out []byte, err error) branchHead {
	switch {
	case err == nil:
		fields := strings.Fields(string(out))
		if len(fields) == 0 {
			return branchHead{err: fmt.Errorf("git ls-remote listed nothing for %s", branch)}
		}
		return branchHead{commit: fields[0]}
	case remoteBranchAbsent(err):
		return branchHead{}
	default:
		return branchHead{err: fmt.Errorf("git ls-remote %s: %v: %s", branch, err, strings.TrimSpace(tail(out, 400)))}
	}
}

type writerBaseline struct {
	start  string
	branch branchHead
}

func recordWriterBaseline(ctx context.Context, dir, cloneURL, token, branch string) (writerBaseline, error) {
	out, err := runGit(ctx, dir, "", "", "rev-parse", "HEAD")
	if err != nil {
		return writerBaseline{}, fmt.Errorf("git rev-parse HEAD: %v: %s", err, tail(out, 400))
	}
	return writerBaseline{
		start:  strings.TrimSpace(string(out)),
		branch: readBranchHead(ctx, dir, cloneURL, token, branch),
	}, nil
}

type deliveryCheck struct {
	writes   bool
	dir      string
	cloneURL string
	token    string
	branch   string
	baseline writerBaseline
	prErr    error
}

type checkoutChanges struct {
	commits []string
	files   []string
}

func (c checkoutChanges) any() bool { return len(c.commits) > 0 || len(c.files) > 0 }

func (c checkoutChanges) describe() string {
	var parts []string
	if n := len(c.commits); n > 0 {
		parts = append(parts, fmt.Sprintf("%d commit(s) not on the forge: %s", n, strings.Join(capList(c.commits), "; ")))
	}
	if n := len(c.files); n > 0 {
		parts = append(parts, fmt.Sprintf("%d uncommitted path(s): %s", n, strings.Join(capList(c.files), ", ")))
	}
	return strings.Join(parts, ". ")
}

func capList(xs []string) []string {
	if len(xs) <= maxNamedUnpublished {
		return xs
	}
	return append(append([]string{}, xs[:maxNamedUnpublished]...), fmt.Sprintf("and %d more", len(xs)-maxNamedUnpublished))
}

func guardUnpublishedWork(ctx context.Context, report harness.OutcomeReport, c deliveryCheck) harness.OutcomeReport {
	if !c.writes {
		return report
	}
	if report.Outcome != work.OutcomeNoChangeNeeded && report.Outcome != work.OutcomePRUpdated {
		return report
	}
	stuck := func(summary, reason string) harness.OutcomeReport {
		report.Outcome = work.OutcomeStuck
		report.Summary = summary
		report.StuckReason = reason
		report.Verdict = ""
		return report
	}
	unknown := func(err error) harness.OutcomeReport {
		return stuck("Ploeg could not confirm what the writer delivered",
			"Ploeg could not read the forge, so whether this Run pushed or opened a pull request is unknown: "+err.Error())
	}
	if c.prErr != nil {
		return unknown(fmt.Errorf("pull request lookup on %s: %w", c.branch, c.prErr))
	}
	if c.baseline.branch.err != nil {
		return unknown(c.baseline.branch.err)
	}
	repo, err := openWorkerRepository(ctx, c.dir, c.cloneURL, c.token)
	if err != nil {
		return stuck("could not inspect the writer's checkout for unpublished changes", err.Error())
	}
	defer repo.remove()
	after := repo.branchHead(ctx, c.branch)
	if after.err != nil {
		return unknown(after.err)
	}
	moved := after.commit != c.baseline.branch.commit
	if moved && report.Outcome == work.OutcomePRUpdated {
		return report
	}
	if moved {
		return stuck("the writer pushed its branch but opened no pull request",
			fmt.Sprintf("branch %s changed on the forge during the Run (%s to %s), but no pull request was found for it, so nothing was delivered",
				c.branch, c.baseline.branch, after))
	}
	changes, err := repo.inspect(ctx, c.baseline.start, after.commit)
	if err != nil {
		return stuck("could not inspect the writer's checkout for unpublished changes", err.Error())
	}
	if !changes.any() {
		return report
	}
	claim := "reported no change needed"
	if report.Outcome == work.OutcomePRUpdated {
		claim = "pushed nothing to its open pull request"
	}
	return stuck("the writer changed the checkout but delivered none of it",
		"the writer "+claim+", but its checkout differs from "+shortCommit(c.baseline.start)+
			" and branch "+c.branch+" on the forge did not move. "+changes.describe())
}

func shortCommit(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
