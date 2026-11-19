package worker

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/harness/adapters/openhands"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const (
	refusalRepositoryHooks      refusalReason = "repository_hooks"
	repositoryHooksSummary                    = "repository_hooks"
	repositoryHooksBaseRef                    = "refs/ploeg/hooks-base"
	repositoryHooksCheckRefusal               = "Ploeg could not check this push against the repository's OpenHands hooks file"
)

// repositoryHooksFence keeps an OpenHands Run from delivering a change to the
// repository's OpenHands hooks file (ADR-0148): the harness sets the file
// aside while the agent works, and this fence makes sure the pull request
// never shows that, nor a hooks file the agent wrote. A push whose changes
// against the forge's own base branch touch .openhands/hooks.json, or turn
// .openhands into something other than a directory, is refused.
type repositoryHooksFence struct {
	dir, cloneURL, base, start string
	token                      func(context.Context) string

	mu      sync.Mutex
	refused []string
}

// loadsRepositoryHooks reports whether the Run's harness is one of the two
// OpenHands harnesses, the headless adapter or the acp profile.
func loadsRepositoryHooks(a harness.Adapter) bool {
	if a == nil {
		return false
	}
	if a.Name() == "openhands" {
		return true
	}
	p, ok := a.(profiledAdapter)
	return ok && p.Profile() == "openhands"
}

func (f *repositoryHooksFence) admit(u refUpdate) (refusalReason, error) {
	touched, err := f.touched(u.newID)
	if err != nil {
		return refusalUninspectablePush, fmt.Errorf("%s: %v", repositoryHooksCheckRefusal, err)
	}
	if len(touched) == 0 {
		return "", nil
	}
	f.mu.Lock()
	f.refused = appendNew(f.refused, touched...)
	f.mu.Unlock()
	return refusalRepositoryHooks, fmt.Errorf("an OpenHands Run may not change the repository's %s; the push changes: %s",
		openhands.RepositoryHooksPath, strings.Join(touched, ", "))
}

func (f *repositoryHooksFence) admitPacked(u refUpdate, pack []byte) (refusalReason, error) {
	ctx, cancel := context.WithTimeout(context.Background(), deliverableCheckTimeout)
	defer cancel()
	repo, err := openPushedRepository(ctx, f.dir, f.cloneURL, f.base, f.token(ctx), u, pack)
	if err != nil {
		return refusalUninspectablePush, fmt.Errorf("%s: %v", repositoryHooksCheckRefusal, err)
	}
	defer repo.remove()
	touched, err := f.touchedIn(ctx, repo, u.newID)
	if err != nil {
		return refusalUninspectablePush, fmt.Errorf("%s: %v", repositoryHooksCheckRefusal, err)
	}
	if len(touched) == 0 {
		return "", nil
	}
	f.mu.Lock()
	f.refused = appendNew(f.refused, touched...)
	f.mu.Unlock()
	return refusalRepositoryHooks, fmt.Errorf("an OpenHands Run may not change the repository's %s; the push changes: %s",
		openhands.RepositoryHooksPath, strings.Join(touched, ", "))
}

func (f *repositoryHooksFence) touched(commit string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), deliverableCheckTimeout)
	defer cancel()
	repo, err := openWorkerRepository(ctx, f.dir, f.cloneURL, f.token(ctx))
	if err != nil {
		return nil, err
	}
	defer repo.remove()
	return f.touchedIn(ctx, repo, commit)
}

// touchedIn is touched for a commit in repo, such as the work in progress a
// yield commits in its own git directory.
func (f *repositoryHooksFence) touchedIn(ctx context.Context, repo *workerRepository, commit string) ([]string, error) {
	changes, err := changesSinceFork(ctx, repo, f.base, f.start, repositoryHooksBaseRef, commit)
	if err != nil {
		return nil, err
	}
	return touchesRepositoryHooks(changes), nil
}

func touchesRepositoryHooks(changes []work.ChangedPath) []string {
	var touched []string
	for _, c := range changes {
		if c.Path == openhands.RepositoryHooksPath || c.Path == openhands.RepositoryHooksDir {
			touched = append(touched, c.Status+" "+c.Path)
		}
	}
	return touched
}

func (f *repositoryHooksFence) refusedPaths() []string {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.refused...)
}

// checkDelivered ends the Run stuck with repository_hooks when the fence
// refused a push, or when the branch it delivered changes the hooks file,
// which happens only when the harness held the forge token itself.
func (f *repositoryHooksFence) checkDelivered(report harness.OutcomeReport, deliveredHead string) harness.OutcomeReport {
	if f == nil {
		return report
	}
	refused := f.refusedPaths()
	touched := refused
	if len(touched) == 0 && deliveredHead != "" && (report.Outcome == work.OutcomePROpened || report.Outcome == work.OutcomePRUpdated) {
		var err error
		if touched, err = f.touched(deliveredHead); err != nil {
			touched = []string{"unknown: " + err.Error()}
		}
	}
	if len(touched) == 0 {
		return report
	}
	report.Outcome = work.OutcomeStuck
	report.Summary = repositoryHooksSummary
	report.StuckReason = fmt.Sprintf("repository_hooks: an OpenHands Run may not change the repository's %s, and its branch changed: %s.",
		openhands.RepositoryHooksPath, strings.Join(capList(touched), ", "))
	if len(refused) > 0 {
		report.StuckReason += " Ploeg refused the push, so none of it reached the forge."
	} else {
		report.StuckReason += " The branch on the forge carries the change; remove it before review."
	}
	report.Verdict = ""
	return report
}
