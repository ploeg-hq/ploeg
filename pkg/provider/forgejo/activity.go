package forgejo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

const (
	activityPageSize    = 50
	activityPages       = provider.MaxActivityEvents / activityPageSize
	reviewPages         = 4
	commitPages         = provider.MaxActivityCommits / activityPageSize
	runPageSize         = 20
	statusPageSize      = 50
	statusPages         = 4
	runAssignmentLeeway = 5 * time.Second
)

// WIPPrefixes are the title prefixes Forgejo treats as a draft by default
// (WORK_IN_PROGRESS_PREFIXES), compared without case.
var WIPPrefixes = []string{"WIP:", "[WIP]"}

func isWIP(title string) bool {
	upper := strings.ToUpper(strings.TrimSpace(title))
	for _, p := range WIPPrefixes {
		if strings.HasPrefix(upper, strings.ToUpper(p)) {
			return true
		}
	}
	return false
}

func forgeTime(s string) *time.Time {
	t := provider.ParseForgeTime(s)
	if t == nil || t.Unix() <= 0 {
		return nil
	}
	return t
}

func (p *Provider) repoBase(repo string) (string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return "", fmt.Errorf("forgejo: repo %q must be owner/name", repo)
	}
	return fmt.Sprintf("%s/api/v1/repos/%s/%s", strings.TrimRight(p.BaseURL, "/"), url.PathEscape(owner), url.PathEscape(name)), nil
}

// PullRequestActivity reads a pull request's comments, inline review
// comments, pushes, force pushes and WIP title changes from
// GET /repos/{owner}/{repo}/issues/{index}/timeline, its reviews from
// .../pulls/{index}/reviews and its commits from .../pulls/{index}/commits,
// page by page up to provider.MaxActivityEvents events and
// provider.MaxActivityCommits commits (ADR-0058). Bodies are read only to
// classify a push or a title change and are never returned.
func (p *Provider) PullRequestActivity(ctx context.Context, repo string, pr int) (provider.PullRequestActivity, error) {
	base, err := p.repoBase(repo)
	if err != nil {
		return provider.PullRequestActivity{}, err
	}
	if pr <= 0 {
		return provider.PullRequestActivity{}, fmt.Errorf("forgejo: pull request number must be positive, got %d", pr)
	}
	out := provider.PullRequestActivity{Events: []provider.ActivityEvent{}, ForcePushesKnown: true}
	add := func(e provider.ActivityEvent) bool {
		if len(out.Events) == provider.MaxActivityEvents {
			out.EventsTruncated = true
			return false
		}
		out.Events = append(out.Events, e)
		return true
	}

	full := true
timeline:
	for page := 1; page <= activityPages; page++ {
		var items []struct {
			Type      string `json:"type"`
			Body      string `json:"body"`
			CreatedAt string `json:"created_at"`
			OldTitle  string `json:"old_title"`
			NewTitle  string `json:"new_title"`
			User      *struct {
				Login string `json:"login"`
			} `json:"user"`
		}
		status, err := p.get(ctx, fmt.Sprintf("%s/issues/%d/timeline?page=%d&limit=%d", base, pr, page, activityPageSize), &items)
		if err != nil {
			return provider.PullRequestActivity{}, err
		}
		if status != http.StatusOK {
			return provider.PullRequestActivity{}, fmt.Errorf("forgejo: timeline of %s#%d: HTTP %d", repo, pr, status)
		}
		for _, it := range items {
			at := forgeTime(it.CreatedAt)
			if at == nil {
				continue
			}
			actor := ""
			if it.User != nil {
				actor = it.User.Login
			}
			e := provider.ActivityEvent{Actor: actor, At: *at}
			switch it.Type {
			case "comment":
				e.Kind = provider.ActivityComment
			case "code":
				e.Kind = provider.ActivityReviewComment
			case "pull_push":
				var push struct {
					IsForcePush bool     `json:"is_force_push"`
					CommitIDs   []string `json:"commit_ids"`
				}
				if json.Unmarshal([]byte(it.Body), &push) != nil {
					continue
				}
				e.Kind = provider.ActivityPush
				if push.IsForcePush {
					e.Kind = provider.ActivityForcePush
				}
				if n := len(push.CommitIDs); n > 0 {
					e.HeadSHA = push.CommitIDs[n-1]
				}
			case "change_title":
				was, is := isWIP(it.OldTitle), isWIP(it.NewTitle)
				switch {
				case was && !is:
					e.Kind = provider.ActivityReady
				case !was && is:
					e.Kind = provider.ActivityDraft
				default:
					continue
				}
			default:
				continue
			}
			if !add(e) {
				break timeline
			}
		}
		if len(items) < activityPageSize {
			full = false
			break
		}
	}
	if full && !out.EventsTruncated {
		out.EventsTruncated = true
	}

	for page := 1; page <= reviewPages && !out.EventsTruncated; page++ {
		var reviews []struct {
			State       string `json:"state"`
			SubmittedAt string `json:"submitted_at"`
			CommitID    string `json:"commit_id"`
			User        *struct {
				Login string `json:"login"`
			} `json:"user"`
		}
		status, err := p.get(ctx, fmt.Sprintf("%s/pulls/%d/reviews?page=%d&limit=%d", base, pr, page, activityPageSize), &reviews)
		if err != nil {
			return provider.PullRequestActivity{}, err
		}
		if status != http.StatusOK {
			return provider.PullRequestActivity{}, fmt.Errorf("forgejo: reviews of %s#%d: HTTP %d", repo, pr, status)
		}
		for _, r := range reviews {
			state := apiReviewState(r.State)
			at := forgeTime(r.SubmittedAt)
			if state == "" || at == nil {
				continue
			}
			actor := ""
			if r.User != nil {
				actor = r.User.Login
			}
			if !add(provider.ActivityEvent{Kind: provider.ActivityReview, Actor: actor, At: *at, State: state, HeadSHA: r.CommitID}) {
				break
			}
		}
		if len(reviews) < activityPageSize {
			break
		}
	}
	sort.SliceStable(out.Events, func(i, j int) bool { return out.Events[i].At.Before(out.Events[j].At) })

	for page := 1; ; page++ {
		var commits []struct {
			Commit struct {
				Author struct {
					Date string `json:"date"`
				} `json:"author"`
			} `json:"commit"`
		}
		status, err := p.get(ctx, fmt.Sprintf("%s/pulls/%d/commits?page=%d&limit=%d&files=false&verification=false", base, pr, page, activityPageSize), &commits)
		if err != nil {
			return provider.PullRequestActivity{}, err
		}
		if status != http.StatusOK {
			return provider.PullRequestActivity{}, fmt.Errorf("forgejo: commits of %s#%d: HTTP %d", repo, pr, status)
		}
		for _, c := range commits {
			if out.Commits == provider.MaxActivityCommits {
				out.CommitsTruncated = true
				break
			}
			out.Commits++
			if at := forgeTime(c.Commit.Author.Date); at != nil && (out.FirstCommitAt == nil || at.Before(*out.FirstCommitAt)) {
				out.FirstCommitAt = at
			}
		}
		if len(commits) < activityPageSize || out.CommitsTruncated {
			break
		}
		if page == commitPages {
			out.CommitsTruncated = true
			break
		}
	}
	return out, nil
}

func apiReviewState(s string) provider.ForgeReviewState {
	switch strings.ToUpper(s) {
	case "APPROVED":
		return provider.ForgeReviewApproved
	case "REQUEST_CHANGES":
		return provider.ForgeReviewChangesRequested
	case "COMMENT":
		return provider.ForgeReviewCommented
	}
	return ""
}

type actionRun struct {
	ID         int64  `json:"id"`
	CommitSHA  string `json:"commit_sha"`
	WorkflowID string `json:"workflow_id"`
	Status     string `json:"status"`
	Created    string `json:"created"`
	Started    string `json:"started"`
	Stopped    string `json:"stopped"`
}

// PullRequestCI reads a pull request's Forgejo Actions runs from
// GET /repos/{owner}/{repo}/actions/runs?ref=refs/pull/{index}/head, plus
// the runs on refs/heads/{branch} at one of heads, up to
// provider.MaxCIRuns. Job timings come from the commit status history of
// each run's head commit (GET .../commits/{sha}/statuses), newest commit
// first, up to provider.MaxCIRunsWithJobs commits: Forgejo Actions posts
// "Waiting to run", "Has started running" and a final status per job, and a
// rerun posts them again. A Forgejo without the runs endpoint (HTTP 404),
// or a pull request with no Actions run, falls back to the status history
// of heads alone, one run per commit, so a CI outside Forgejo Actions still
// counts. Logs are never read.
func (p *Provider) PullRequestCI(ctx context.Context, repo string, pr int, branch string, heads []string) (provider.PullRequestCI, error) {
	base, err := p.repoBase(repo)
	if err != nil {
		return provider.PullRequestCI{}, err
	}
	if pr <= 0 {
		return provider.PullRequestCI{}, fmt.Errorf("forgejo: pull request number must be positive, got %d", pr)
	}
	out := provider.PullRequestCI{Runs: []provider.CIRun{}, Source: provider.CISourceActions}
	runs, ok, truncated, err := p.actionRuns(ctx, base, "refs/pull/"+strconv.Itoa(pr)+"/head")
	if err != nil {
		return provider.PullRequestCI{}, fmt.Errorf("forgejo: runs of %s#%d: %w", repo, pr, err)
	}
	out.Truncated = truncated
	if ok && branch != "" {
		known := map[string]bool{}
		for _, h := range heads {
			known[h] = true
		}
		branchRuns, _, more, err := p.actionRuns(ctx, base, "refs/heads/"+branch)
		if err != nil {
			return provider.PullRequestCI{}, fmt.Errorf("forgejo: branch runs of %s#%d: %w", repo, pr, err)
		}
		seen := map[int64]bool{}
		for _, r := range runs {
			seen[r.ID] = true
		}
		for _, r := range branchRuns {
			if !seen[r.ID] && known[r.CommitSHA] {
				runs = append(runs, r)
			}
		}
		out.Truncated = out.Truncated || more
	}
	statusesOnly := !ok || len(runs) == 0
	if statusesOnly {
		out.Source = provider.CISourceStatuses
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].ID > runs[j].ID })
	if len(runs) > provider.MaxCIRuns {
		runs, out.Truncated = runs[:provider.MaxCIRuns], true
	}

	var shas []string
	seenSHA := map[string]bool{}
	addSHA := func(sha string) {
		if sha != "" && !seenSHA[sha] {
			seenSHA[sha] = true
			shas = append(shas, sha)
		}
	}
	for _, r := range runs {
		addSHA(r.CommitSHA)
	}
	if statusesOnly {
		for i := len(heads) - 1; i >= 0; i-- {
			addSHA(heads[i])
		}
	}
	if len(shas) > provider.MaxCIRunsWithJobs {
		shas, out.Truncated = shas[:provider.MaxCIRunsWithJobs], true
	}

	bySHA := map[string][]*provider.CIRun{}
	for _, r := range runs {
		run := provider.CIRun{ID: strconv.FormatInt(r.ID, 10), SHA: r.CommitSHA, Workflow: r.WorkflowID, Status: actionStatus(r.Status),
			CreatedAt: forgeTime(r.Created), StartedAt: forgeTime(r.Started), CompletedAt: forgeTime(r.Stopped), Jobs: []provider.CIJob{}}
		if !run.Status.Terminal() {
			run.CompletedAt = nil
		}
		out.Runs = append(out.Runs, run)
	}
	for i := range out.Runs {
		bySHA[out.Runs[i].SHA] = append(bySHA[out.Runs[i].SHA], &out.Runs[i])
	}
	for _, sha := range shas {
		history, more, err := p.statusHistory(ctx, base, sha)
		if err != nil {
			return provider.PullRequestCI{}, fmt.Errorf("forgejo: status history of %s@%s: %w", repo, sha, err)
		}
		out.Truncated = out.Truncated || more
		attempts := statusAttempts(history)
		if len(attempts) == 0 {
			continue
		}
		owners := bySHA[sha]
		if len(owners) == 0 {
			out.Runs = append(out.Runs, statusRun(sha, attempts))
			continue
		}
		sort.SliceStable(owners, func(i, j int) bool { return timeOf(owners[i].CreatedAt).Before(timeOf(owners[j].CreatedAt)) })
		count := map[*provider.CIRun]map[string]int{}
		for _, a := range attempts {
			owner := owners[0]
			for _, r := range owners {
				if r.CreatedAt != nil && !r.CreatedAt.After(a.firstAt.Add(runAssignmentLeeway)) {
					owner = r
				}
			}
			if count[owner] == nil {
				count[owner] = map[string]int{}
			}
			count[owner][a.job.Name]++
			a.job.Attempt = count[owner][a.job.Name]
			if len(owner.Jobs) < provider.MaxCIJobs {
				owner.Jobs = append(owner.Jobs, a.job)
			} else {
				out.Truncated = true
			}
		}
	}
	return out, nil
}

func timeOf(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func (p *Provider) actionRuns(ctx context.Context, base, ref string) ([]actionRun, bool, bool, error) {
	var out []actionRun
	for page := 1; ; page++ {
		var body struct {
			WorkflowRuns []actionRun `json:"workflow_runs"`
		}
		status, err := p.get(ctx, fmt.Sprintf("%s/actions/runs?ref=%s&page=%d&limit=%d", base, url.QueryEscape(ref), page, runPageSize), &body)
		if err != nil {
			return nil, false, false, err
		}
		if status == http.StatusNotFound {
			return nil, false, false, nil
		}
		if status != http.StatusOK {
			return nil, false, false, fmt.Errorf("HTTP %d", status)
		}
		out = append(out, body.WorkflowRuns...)
		if len(body.WorkflowRuns) < runPageSize {
			return out, true, false, nil
		}
		if len(out) >= provider.MaxCIRuns {
			return out, true, true, nil
		}
	}
}

func actionStatus(s string) provider.CIStatus {
	switch s {
	case "success":
		return provider.CISuccess
	case "failure":
		return provider.CIFailure
	case "cancelled":
		return provider.CICancelled
	case "skipped":
		return provider.CISkipped
	case "running":
		return provider.CIRunning
	case "waiting", "blocked":
		return provider.CIPending
	}
	return provider.CIError
}

type commitStatusRow struct {
	ID          int64  `json:"id"`
	Status      string `json:"status"`
	Context     string `json:"context"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
	at          time.Time
}

func (p *Provider) statusHistory(ctx context.Context, base, sha string) ([]commitStatusRow, bool, error) {
	var out []commitStatusRow
	for page := 1; page <= statusPages; page++ {
		var rows []commitStatusRow
		status, err := p.get(ctx, fmt.Sprintf("%s/commits/%s/statuses?page=%d&limit=%d", base, url.PathEscape(sha), page, statusPageSize), &rows)
		if err != nil {
			return nil, false, err
		}
		if status != http.StatusOK {
			return nil, false, fmt.Errorf("HTTP %d", status)
		}
		for _, r := range rows {
			if at := forgeTime(r.CreatedAt); at != nil && r.Context != "" {
				r.at = *at
				out = append(out, r)
			}
		}
		if len(rows) < statusPageSize {
			break
		}
		if page == statusPages {
			return sortStatuses(out), true, nil
		}
	}
	return sortStatuses(out), false, nil
}

func sortStatuses(rows []commitStatusRow) []commitStatusRow {
	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].at.Equal(rows[j].at) {
			return rows[i].at.Before(rows[j].at)
		}
		return rows[i].ID < rows[j].ID
	})
	return rows
}

type statusAttempt struct {
	job      provider.CIJob
	firstAt  time.Time
	queuedAt *time.Time
	terminal bool
}

const (
	actionsWaiting = "Waiting to run"
	actionsBlocked = "Blocked by required conditions"
	actionsStarted = "Has started running"
)

func statusAttempts(rows []commitStatusRow) []*statusAttempt {
	actions := map[string]bool{}
	for _, r := range rows {
		d := strings.TrimSpace(r.Description)
		if strings.HasPrefix(d, actionsWaiting) || strings.HasPrefix(d, actionsStarted) || strings.HasPrefix(d, actionsBlocked) {
			actions[r.Context] = true
		}
	}
	var out []*statusAttempt
	current := map[string]*statusAttempt{}
	for _, r := range rows {
		at := r.at
		a := current[r.Context]
		pending := r.Status == "pending"
		if a == nil || a.terminal {
			a = &statusAttempt{job: provider.CIJob{Name: r.Context, Status: provider.CIPending}, firstAt: at}
			current[r.Context] = a
			out = append(out, a)
		}
		d := strings.TrimSpace(r.Description)
		switch {
		case pending && actions[r.Context]:
			switch {
			case strings.HasPrefix(d, actionsStarted):
				if a.job.StartedAt == nil {
					started := at
					a.job.StartedAt = &started
					a.job.Status = provider.CIRunning
				}
			case a.job.StartedAt == nil:
				queued := at
				a.queuedAt = &queued
			}
		case pending:
			if a.job.StartedAt == nil {
				started := at
				a.job.StartedAt = &started
			}
		default:
			completed := at
			a.job.CompletedAt = &completed
			a.job.Status = statusOutcome(r.Status, d)
			a.terminal = true
		}
	}
	for _, a := range out {
		if a.queuedAt != nil && a.job.StartedAt != nil {
			if wait := int64(a.job.StartedAt.Sub(*a.queuedAt) / time.Second); wait >= 0 {
				a.job.QueuedSeconds = &wait
			}
		}
	}
	return out
}

func statusOutcome(state, description string) provider.CIStatus {
	switch state {
	case "success", "warning":
		return provider.CISuccess
	case "failure":
		if strings.HasPrefix(description, "Has been cancelled") {
			return provider.CICancelled
		}
		return provider.CIFailure
	}
	return provider.CIError
}

func statusRun(sha string, attempts []*statusAttempt) provider.CIRun {
	run := provider.CIRun{ID: "statuses:" + sha, SHA: sha, Jobs: []provider.CIJob{}}
	latest := map[string]*statusAttempt{}
	var order []string
	count := map[string]int{}
	for _, a := range attempts {
		count[a.job.Name]++
		a.job.Attempt = count[a.job.Name]
		if _, ok := latest[a.job.Name]; !ok {
			order = append(order, a.job.Name)
		}
		latest[a.job.Name] = a
		if len(run.Jobs) < provider.MaxCIJobs {
			run.Jobs = append(run.Jobs, a.job)
		}
		if run.CreatedAt == nil || a.firstAt.Before(*run.CreatedAt) {
			created := a.firstAt
			run.CreatedAt = &created
		}
	}
	failed, pending := false, false
	var completed time.Time
	for _, name := range order {
		a := latest[name]
		if a.job.StartedAt != nil && (run.StartedAt == nil || a.job.StartedAt.Before(*run.StartedAt)) {
			started := *a.job.StartedAt
			run.StartedAt = &started
		}
		switch {
		case !a.terminal:
			pending = true
		case a.job.Status == provider.CIFailure || a.job.Status == provider.CIError || a.job.Status == provider.CICancelled:
			failed = true
		}
		if a.job.CompletedAt != nil && a.job.CompletedAt.After(completed) {
			completed = *a.job.CompletedAt
		}
	}
	switch {
	case pending:
		run.Status = provider.CIPending
	case failed:
		run.Status = provider.CIFailure
	default:
		run.Status = provider.CISuccess
	}
	if !pending && !completed.IsZero() {
		run.CompletedAt = &completed
	}
	return run
}

// PullRequestDiff reads a pull request's unified diff from
// GET /repos/{owner}/{repo}/pulls/{index}.diff, at most maxBytes of it,
// cut at the last whole line (ADR-0058).
func (p *Provider) PullRequestDiff(ctx context.Context, repo string, pr int, maxBytes int) ([]byte, bool, error) {
	base, err := p.repoBase(repo)
	if err != nil {
		return nil, false, err
	}
	if pr <= 0 {
		return nil, false, fmt.Errorf("forgejo: pull request number must be positive, got %d", pr)
	}
	if maxBytes <= 0 || maxBytes > provider.MaxDiffBytes {
		maxBytes = provider.MaxDiffBytes
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/pulls/%d.diff", base, pr), nil)
	if err != nil {
		return nil, false, err
	}
	p.authorize(req)
	resp, err := p.client().Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil, false, fmt.Errorf("forgejo: diff of %s#%d: HTTP %d", repo, pr, resp.StatusCode)
	}
	return readCapped(resp.Body, maxBytes)
}

func readCapped(r io.Reader, maxBytes int) ([]byte, bool, error) {
	raw, err := io.ReadAll(io.LimitReader(r, int64(maxBytes)+1))
	if err != nil {
		return nil, false, err
	}
	if len(raw) <= maxBytes {
		return raw, false, nil
	}
	raw = raw[:maxBytes]
	if i := bytes.LastIndexByte(raw, '\n'); i >= 0 {
		raw = raw[:i+1]
	} else {
		raw = raw[:0]
	}
	return raw, true, nil
}
