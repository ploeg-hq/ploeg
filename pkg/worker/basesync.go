package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const (
	fastCheckScriptName = "ploeg-fast-check"
	fastCheckScriptEnv  = "PLOEG_FAST_CHECK_SCRIPT"
	baseSyncHeading     = "### Ploeg base sync"
	rebaseGitDirPattern = "ploeg-rebase-git-"
	rebaseHeadRef       = "refs/ploeg/head"
	rebaseBaseRef       = "refs/ploeg/base"
	maxNamedConflicts   = 50
	committerName       = "agent-builder"
	committerEmail      = "agent-builder@webgrip.dev"
)

type baseSyncResult string

const (
	baseCurrent         baseSyncResult = "current"
	baseRebased         baseSyncResult = "rebased"
	baseConflicted      baseSyncResult = "conflicted"
	baseFastCheckFailed baseSyncResult = "fast_check_failed"
	baseUnchecked       baseSyncResult = "unchecked"
)

type baseSync struct {
	result     baseSyncResult
	base       string
	baseCommit string
	head       string
	rebased    string
	conflicts  []string
	fastCheck  *verification
	reason     string
}

type baseCandidate struct {
	pushedCandidate
	base string
}

func syncWithBase(ctx context.Context, side harnessSide, c baseCandidate, env, fastCheck []string, limit time.Duration) baseSync {
	s := baseSync{base: c.base, head: c.commit}
	unchecked := func(reason string) baseSync {
		s.result, s.reason = baseUnchecked, reason
		return s
	}
	switch {
	case c.base == "":
		return unchecked("the Run's repository names no base branch")
	case c.commit == "":
		return unchecked("the forge reported no head commit for the pull request")
	}
	gitDir, err := os.MkdirTemp("", rebaseGitDirPattern)
	if err != nil {
		return unchecked("could not prepare a private git directory: " + err.Error())
	}
	defer os.RemoveAll(gitDir)
	defer os.RemoveAll(c.dir)
	repo := &workerRepository{gitDir: gitDir, workTree: c.dir, remote: c.cloneURL, token: c.token}
	if out, err := repo.local(ctx, "", "init", "--quiet", "--bare", "--template=").CombinedOutput(); err != nil {
		return unchecked(fmt.Sprintf("git init: %v: %s", err, strings.TrimSpace(tail(out, 400))))
	}
	if err := repo.fetchBranch(ctx, c.branch, rebaseHeadRef); err != nil {
		return unchecked(err.Error())
	}
	if err := repo.fetchBranch(ctx, c.base, rebaseBaseRef); err != nil {
		return unchecked(err.Error())
	}
	if head := repo.revParse(ctx, rebaseHeadRef); head != c.commit {
		return unchecked(fmt.Sprintf("branch %s is at %s, not at the pull request's head %s", c.branch, shortCommit(head), shortCommit(c.commit)))
	}
	s.baseCommit = repo.revParse(ctx, rebaseBaseRef)
	contains, err := repo.contains(ctx, rebaseHeadRef, rebaseBaseRef)
	if err != nil {
		return unchecked(err.Error())
	}
	if contains {
		s.result = baseCurrent
		return s
	}
	if err := os.RemoveAll(c.dir); err != nil {
		return unchecked("could not prepare the rebase's working tree: " + err.Error())
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return unchecked("could not prepare the rebase's working tree: " + err.Error())
	}
	if out, err := repo.inWorkTree(ctx, "checkout", "--quiet", "--detach", rebaseHeadRef).CombinedOutput(); err != nil {
		return unchecked(fmt.Sprintf("git checkout: %v: %s", err, strings.TrimSpace(tail(out, 400))))
	}
	if out, err := repo.inWorkTree(ctx, "rebase", "--quiet", rebaseBaseRef).CombinedOutput(); err != nil {
		conflicts := repo.conflictedPaths(ctx)
		_ = repo.inWorkTree(ctx, "rebase", "--abort").Run()
		if len(conflicts) == 0 {
			return unchecked(fmt.Sprintf("git rebase: %v: %s", err, strings.TrimSpace(tail(out, 400))))
		}
		s.result, s.conflicts = baseConflicted, conflicts
		return s
	}
	rebased := repo.revParse(ctx, "HEAD")
	if !objectName(rebased) {
		return unchecked("the rebase left no commit to push")
	}
	if len(fastCheck) > 0 {
		v := runVerification(ctx, side, c.dir, env, fastCheck, limit)
		s.fastCheck = &v
		if _, failed := v.failed(); failed || v.Stopped != "" {
			s.result = baseFastCheckFailed
			return s
		}
	}
	lease := fmt.Sprintf("--force-with-lease=refs/heads/%s:%s", c.branch, c.commit)
	if out, err := repo.networked(ctx, "push", "--quiet", "--no-verify", lease, c.cloneURL, rebased+":refs/heads/"+c.branch).CombinedOutput(); err != nil {
		return unchecked(fmt.Sprintf("the rebased branch was not pushed: %v: %s", err, strings.TrimSpace(tail(out, 400))))
	}
	s.result, s.rebased = baseRebased, rebased
	return s
}

func (r *workerRepository) inWorkTree(ctx context.Context, args ...string) *exec.Cmd {
	identity := []string{"-c", "core.bare=false", "-c", "user.name=" + committerName, "-c", "user.email=" + committerEmail}
	return r.local(ctx, r.workTree, append(identity, args...)...)
}

func (r *workerRepository) revParse(ctx context.Context, ref string) string {
	out, err := r.local(ctx, "", "rev-parse", "--verify", "--quiet", ref+"^{commit}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (r *workerRepository) contains(ctx context.Context, head, base string) (bool, error) {
	err := r.local(ctx, "", "merge-base", "--is-ancestor", base, head).Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false, nil
	default:
		return false, fmt.Errorf("could not compare the branch with its base: %v", err)
	}
}

func (r *workerRepository) conflictedPaths(ctx context.Context) []string {
	out, err := r.inWorkTree(ctx, "diff", "--name-only", "--diff-filter=U").Output()
	if err != nil {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			paths = append(paths, line)
		}
	}
	return paths
}

func (s baseSync) namedConflicts() string {
	named := s.conflicts
	more := ""
	if len(named) > maxNamedConflicts {
		more = fmt.Sprintf(" and %d more", len(named)-maxNamedConflicts)
		named = named[:maxNamedConflicts]
	}
	return strings.Join(named, ", ") + more
}

func (s baseSync) markdown() string {
	var b strings.Builder
	b.WriteString(baseSyncHeading + "\n\n")
	base := "`" + s.base + "`"
	if s.baseCommit != "" {
		base += " at `" + shortCommit(s.baseCommit) + "`"
	}
	switch s.result {
	case baseCurrent:
		fmt.Fprintf(&b, "The pull request's head `%s` already contains %s, so Ploeg did not rebase it.\n", shortCommit(s.head), base)
	case baseRebased:
		fmt.Fprintf(&b, "The base branch moved after the agent pushed. Ploeg rebased `%s` onto %s and pushed `%s`.\n", shortCommit(s.head), base, shortCommit(s.rebased))
	case baseConflicted:
		fmt.Fprintf(&b, "The pull request's head `%s` conflicts with %s in %d path(s), and the agent did not resolve them: %s. Ploeg pushed nothing, and a person is asked to resolve them.\n",
			shortCommit(s.head), base, len(s.conflicts), s.namedConflicts())
	case baseFastCheckFailed:
		fmt.Fprintf(&b, "Ploeg rebased `%s` onto %s, but the repository's fast check did not pass on the rebased tree, so Ploeg pushed nothing and the pull request stays at `%s`.\n",
			shortCommit(s.head), base, shortCommit(s.head))
	default:
		fmt.Fprintf(&b, "Ploeg could not check the pull request against %s: %s.\n", base, s.reason)
	}
	switch {
	case s.fastCheck != nil:
		b.WriteString("\nThe repository's fast check on the rebased tree:\n\n" + s.fastCheck.checkTable())
	case s.result == baseRebased || s.result == baseFastCheckFailed:
		b.WriteString("\nThe repository declares no fast check, so none ran on the rebased tree.\n")
	}
	return b.String()
}

func withBaseSync(report harness.OutcomeReport, s baseSync) harness.OutcomeReport {
	report.Findings = strings.TrimSpace(strings.TrimSpace(report.Findings) + "\n\n" + s.markdown())
	switch s.result {
	case baseConflicted:
		report.Outcome = work.OutcomeStuck
		report.Summary = fmt.Sprintf("the pull request conflicts with %s in %d path(s): %s", s.base, len(s.conflicts), s.namedConflicts())
		report.StuckReason = fmt.Sprintf("the pull request's head %s conflicts with %s at %s in %d path(s) the agent did not resolve: %s. Resolve them on the branch, or rebase it, and hand the Work Item back",
			shortCommit(s.head), s.base, shortCommit(s.baseCommit), len(s.conflicts), s.namedConflicts())
	case baseFastCheckFailed:
		failed, _ := s.fastCheck.failed()
		why := s.fastCheck.Stopped
		if failed.Command != "" {
			why = "`" + failed.Command + "` failed"
		}
		report.Outcome = work.OutcomeStuck
		report.Summary = fmt.Sprintf("the pull request rebases cleanly onto %s, but the fast check did not pass on the rebased tree: %s", s.base, why)
		report.StuckReason = report.Summary + ". The pull request was left at " + shortCommit(s.head)
	case baseRebased:
		report.Summary += " [rebased onto " + s.base + "]"
	}
	return report
}

func pullRequestBase(pr changeRequest, ref harness.RepoRef) string {
	if pr.BaseBranch != "" {
		return pr.BaseBranch
	}
	return ref.BaseBranch
}

func afterRebase(ref harness.RepoRef, token, branch string, before changeRequest, rebased string) changeRequest {
	reread, err := readChangeRequest(ref, token, branch)
	if err != nil || reread.URL == "" {
		before.HeadSHA = rebased
		return before
	}
	return reread
}

func fastCheckSection(cmds []string) string {
	if len(cmds) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("- The script named by $" + fastCheckScriptEnv + " runs this repository's fast check\n  in order and stops at the first failure:\n")
	for _, c := range cmds {
		fmt.Fprintf(&b, "      %s\n", c)
	}
	b.WriteString("  Run it after every rebase onto the base branch and before you push.\n")
	return b.String()
}
