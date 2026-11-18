package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const (
	refusalOutsideDeliverable refusalReason = "outside_deliverable"
	outsideDeliverableSummary               = "outside_deliverable"
	deliverableBaseRef                      = "refs/ploeg/prepare-base"
	deliverableCheckTimeout                 = 2 * time.Minute
)

// deliverableFence is the worker's enforcement of a prepare_repository
// Run's one deliverable (ADR-0194): before a push reaches the forge, the
// pushed commit's changes against the forge's own base branch must be
// exactly an added root AGENTS.md. The base is fetched from the forge into a
// repository the agent cannot write, so no ref the agent moves can hide a
// change.
type deliverableFence struct {
	dir, cloneURL, base, start string
	token                      func(context.Context) string

	mu      sync.Mutex
	refused []string
}

func (f *deliverableFence) admit(u refUpdate) (refusalReason, error) {
	outside, err := f.outside(u.newID)
	if err != nil {
		return refusalUninspectablePush, fmt.Errorf("Ploeg could not check this prepare Run's push against its one deliverable: %v", err)
	}
	if len(outside) == 0 {
		return "", nil
	}
	f.mu.Lock()
	f.refused = appendNew(f.refused, outside...)
	f.mu.Unlock()
	return refusalOutsideDeliverable, fmt.Errorf("this prepare Run may only add %s at the repository root; the push also changes: %s",
		work.PrepareRepositoryDeliverable, strings.Join(capList(outside), ", "))
}

func (f *deliverableFence) outside(commit string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), deliverableCheckTimeout)
	defer cancel()
	repo, err := openWorkerRepository(ctx, f.dir, f.cloneURL, f.token(ctx))
	if err != nil {
		return nil, err
	}
	defer repo.remove()
	return f.outsideIn(ctx, repo, commit)
}

// outsideIn is outside for a commit in repo, such as the work in progress a
// yield commits in its own git directory.
func (f *deliverableFence) outsideIn(ctx context.Context, repo *workerRepository, commit string) ([]string, error) {
	changes, err := f.changes(ctx, repo, commit)
	if err != nil {
		return nil, err
	}
	outside := work.OutsidePrepareDeliverable(changes)
	if len(outside) == 1 && strings.HasPrefix(outside[0], "missing: ") && len(changes) == 0 {
		return nil, nil
	}
	return outside, nil
}

func (f *deliverableFence) changes(ctx context.Context, repo *workerRepository, commit string) ([]work.ChangedPath, error) {
	base := f.start
	if f.base != "" {
		if err := repo.fetchBranch(ctx, f.base, deliverableBaseRef); err != nil {
			return nil, err
		}
		base = deliverableBaseRef
	}
	out, err := repo.local(ctx, "", "merge-base", commit, base).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git merge-base: %v: %s", err, tail(out, 400))
	}
	fork := strings.TrimSpace(string(out))
	out, err = repo.local(ctx, "", "diff-tree", "-r", "-z", "--no-renames", "--raw", "--no-abbrev", fork, commit).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git diff-tree: %v: %s", err, tail(out, 400))
	}
	return parseRawDiff(string(out))
}

// parseRawDiff reads `git diff-tree -r -z --raw`: per change a header
// ":<old mode> <new mode> <old id> <new id> <status>" and its path.
func parseRawDiff(out string) ([]work.ChangedPath, error) {
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	var changes []work.ChangedPath
	for i := 0; i+1 < len(fields); i += 2 {
		header := strings.Fields(strings.TrimPrefix(fields[i], ":"))
		if len(header) != 5 {
			return nil, fmt.Errorf("git diff-tree printed an unexpected line %q", fields[i])
		}
		changes = append(changes, work.ChangedPath{Status: header[4], Mode: header[1], Path: fields[i+1]})
	}
	return changes, nil
}

func (f *deliverableFence) refusedPaths() []string {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.refused...)
}

// checkDelivered ends a prepare Run stuck with outside_deliverable when the
// fence refused a push, or when the branch it delivered changes anything but
// an added root AGENTS.md. Nothing outside the deliverable reached the forge.
func (f *deliverableFence) checkDelivered(report harness.OutcomeReport, deliveredHead string) harness.OutcomeReport {
	if f == nil {
		return report
	}
	outside := f.refusedPaths()
	if len(outside) == 0 && deliveredHead != "" && (report.Outcome == work.OutcomePROpened || report.Outcome == work.OutcomePRUpdated) {
		var err error
		if outside, err = f.outside(deliveredHead); err != nil {
			outside = []string{"unknown: " + err.Error()}
		}
	}
	if len(outside) == 0 {
		return report
	}
	report.Outcome = work.OutcomeStuck
	report.Summary = outsideDeliverableSummary
	report.StuckReason = fmt.Sprintf("outside_deliverable: this prepare Run may only add %s at the repository root, and it changed: %s.",
		work.PrepareRepositoryDeliverable, strings.Join(capList(outside), ", "))
	if len(f.refusedPaths()) > 0 {
		report.StuckReason += " Ploeg refused the push, so none of it reached the forge."
	}
	report.Verdict = ""
	return report
}

func appendNew(xs []string, more ...string) []string {
	for _, x := range more {
		found := false
		for _, have := range xs {
			if have == x {
				found = true
				break
			}
		}
		if !found {
			xs = append(xs, x)
		}
	}
	return xs
}

var pullRequestBaseFields = []string{"base", "target_branch"}

// admitAPI pins a prepare Run's pull request to the configured base branch:
// a request that opens or edits a pull request (merge request) naming
// another base, or that edits the base of one, is refused, so the one
// deliverable cannot be retargeted past the fence's base (ADR-0194).
func (f *deliverableFence) admitAPI(r *http.Request) (refusalReason, error) {
	if r.Method != http.MethodPost && r.Method != http.MethodPatch && r.Method != http.MethodPut {
		return "", nil
	}
	path := strings.ToLower(r.URL.Path)
	if !strings.Contains(path, "/pulls") && !strings.Contains(path, "/merge_requests") {
		return "", nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxInspectedAPIBody+1))
	if err != nil || len(body) > maxInspectedAPIBody {
		return refusalUninspectableBody, fmt.Errorf("the forge proxy could not read the pull request request's body")
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	values := map[string]any{}
	if json.Unmarshal(body, &values) != nil {
		form, _ := url.ParseQuery(string(body))
		for k := range form {
			values[k] = form.Get(k)
		}
		for k := range r.URL.Query() {
			values[k] = r.URL.Query().Get(k)
		}
	}
	for _, field := range pullRequestBaseFields {
		raw, named := values[field]
		if !named {
			continue
		}
		base, _ := raw.(string)
		if (f.base != "" && base == f.base) || (f.base == "" && r.Method == http.MethodPost) {
			continue
		}
		return refusalOutsideDeliverable, fmt.Errorf("this prepare Run's pull request must target %q; the request names base %q", f.base, base)
	}
	return "", nil
}
