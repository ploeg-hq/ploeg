package gitlab

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

const (
	activityPageSize = 100
	notePages        = provider.MaxActivityEvents / activityPageSize
	commitPages      = 3
	jobPageSize      = 100
)

func (p *Provider) mergeRequestBase(repo string, mr int) (string, error) {
	if err := validRepo(repo); err != nil {
		return "", err
	}
	if mr <= 0 {
		return "", fmt.Errorf("gitlab: merge request iid must be positive, got %d", mr)
	}
	return fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d", strings.TrimRight(p.BaseURL, "/"), url.PathEscape(repo), mr), nil
}

// PullRequestActivity reads a merge request's comments and inline comments
// from GET /projects/:id/merge_requests/:iid/notes, its approvals, requests
// for changes and draft changes from the system notes among them, its pushes
// from .../versions and its commits from .../commits (ADR-0058). A system
// note's text is read only to classify it and is never returned. GitLab
// reports no force push, so ForcePushesKnown is false.
func (p *Provider) PullRequestActivity(ctx context.Context, repo string, mr int) (provider.PullRequestActivity, error) {
	base, err := p.mergeRequestBase(repo, mr)
	if err != nil {
		return provider.PullRequestActivity{}, err
	}
	out := provider.PullRequestActivity{Events: []provider.ActivityEvent{}}
	add := func(e provider.ActivityEvent) bool {
		if len(out.Events) == provider.MaxActivityEvents {
			out.EventsTruncated = true
			return false
		}
		out.Events = append(out.Events, e)
		return true
	}
notes:
	for page := 1; ; page++ {
		var notes []struct {
			Body      string  `json:"body"`
			CreatedAt string  `json:"created_at"`
			System    bool    `json:"system"`
			Type      *string `json:"type"`
			Author    *struct {
				Username string `json:"username"`
			} `json:"author"`
		}
		if err := p.getJSON(ctx, fmt.Sprintf("%s/notes?sort=asc&order_by=created_at&page=%d&per_page=%d", base, page, activityPageSize), &notes); err != nil {
			return provider.PullRequestActivity{}, fmt.Errorf("gitlab: notes of %s!%d: %w", repo, mr, err)
		}
		for _, n := range notes {
			at := provider.ParseForgeTime(n.CreatedAt)
			if at == nil {
				continue
			}
			e := provider.ActivityEvent{At: *at}
			if n.Author != nil {
				e.Actor = n.Author.Username
			}
			switch {
			case n.System:
				kind, state, ok := systemNote(n.Body)
				if !ok {
					continue
				}
				e.Kind, e.State = kind, state
			case n.Type != nil && *n.Type == "DiffNote":
				e.Kind = provider.ActivityReviewComment
			default:
				e.Kind = provider.ActivityComment
			}
			if !add(e) {
				break notes
			}
		}
		if len(notes) < activityPageSize {
			break
		}
		if page == notePages {
			out.EventsTruncated = true
			break
		}
	}

	if !out.EventsTruncated {
		var versions []struct {
			HeadCommitSHA string `json:"head_commit_sha"`
			CreatedAt     string `json:"created_at"`
		}
		if err := p.getJSON(ctx, fmt.Sprintf("%s/versions?per_page=%d", base, activityPageSize), &versions); err != nil {
			return provider.PullRequestActivity{}, fmt.Errorf("gitlab: versions of %s!%d: %w", repo, mr, err)
		}
		for _, v := range versions {
			if at := provider.ParseForgeTime(v.CreatedAt); at != nil {
				if !add(provider.ActivityEvent{Kind: provider.ActivityPush, At: *at, HeadSHA: v.HeadCommitSHA}) {
					break
				}
			}
		}
		if len(versions) == activityPageSize {
			out.EventsTruncated = true
		}
	}
	sort.SliceStable(out.Events, func(i, j int) bool { return out.Events[i].At.Before(out.Events[j].At) })

	for page := 1; ; page++ {
		var commits []struct {
			AuthoredDate string `json:"authored_date"`
		}
		if err := p.getJSON(ctx, fmt.Sprintf("%s/commits?page=%d&per_page=%d", base, page, activityPageSize), &commits); err != nil {
			return provider.PullRequestActivity{}, fmt.Errorf("gitlab: commits of %s!%d: %w", repo, mr, err)
		}
		for _, c := range commits {
			if out.Commits == provider.MaxActivityCommits {
				out.CommitsTruncated = true
				break
			}
			out.Commits++
			if at := provider.ParseForgeTime(c.AuthoredDate); at != nil && (out.FirstCommitAt == nil || at.Before(*out.FirstCommitAt)) {
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

func systemNote(body string) (provider.ActivityKind, provider.ForgeReviewState, bool) {
	text := strings.ToLower(strings.TrimSpace(body))
	switch {
	case strings.HasPrefix(text, "approved this merge request"):
		return provider.ActivityReview, provider.ForgeReviewApproved, true
	case strings.HasPrefix(text, "requested changes"):
		return provider.ActivityReview, provider.ForgeReviewChangesRequested, true
	case strings.Contains(text, "as **ready**"), strings.HasPrefix(text, "unmarked as a **work in progress**"):
		return provider.ActivityReady, "", true
	case strings.Contains(text, "as **draft**"), strings.HasPrefix(text, "marked as a **work in progress**"):
		return provider.ActivityDraft, "", true
	}
	return "", "", false
}

// PullRequestCI reads a merge request's pipelines from
// GET /projects/:id/merge_requests/:iid/pipelines, up to provider.MaxCIRuns,
// and the jobs of the newest provider.MaxCIRunsWithJobs of them, retried
// attempts included, from GET /projects/:id/pipelines/:pipeline_id/jobs
// (ADR-0058). A job's queued seconds are GitLab's queued_duration. branch
// and heads are not needed: GitLab ties every pipeline to the merge request.
// Logs are never read.
func (p *Provider) PullRequestCI(ctx context.Context, repo string, mr int, _ string, _ []string) (provider.PullRequestCI, error) {
	base, err := p.mergeRequestBase(repo, mr)
	if err != nil {
		return provider.PullRequestCI{}, err
	}
	var pipelines []struct {
		ID        int64  `json:"id"`
		SHA       string `json:"sha"`
		Status    string `json:"status"`
		CreatedAt string `json:"created_at"`
	}
	if err := p.getJSON(ctx, fmt.Sprintf("%s/pipelines?per_page=%d", base, activityPageSize), &pipelines); err != nil {
		return provider.PullRequestCI{}, fmt.Errorf("gitlab: pipelines of %s!%d: %w", repo, mr, err)
	}
	out := provider.PullRequestCI{Runs: []provider.CIRun{}, Source: provider.CISourcePipelines}
	sort.SliceStable(pipelines, func(i, j int) bool { return pipelines[i].ID > pipelines[j].ID })
	if len(pipelines) > provider.MaxCIRuns || len(pipelines) == activityPageSize {
		out.Truncated = true
	}
	if len(pipelines) > provider.MaxCIRuns {
		pipelines = pipelines[:provider.MaxCIRuns]
	}
	project := fmt.Sprintf("%s/api/v4/projects/%s", strings.TrimRight(p.BaseURL, "/"), url.PathEscape(repo))
	for i, pl := range pipelines {
		run := provider.CIRun{ID: strconv.FormatInt(pl.ID, 10), SHA: pl.SHA, Status: pipelineStatus(pl.Status),
			CreatedAt: provider.ParseForgeTime(pl.CreatedAt), Jobs: []provider.CIJob{}}
		if i >= provider.MaxCIRunsWithJobs {
			out.Truncated = true
			out.Runs = append(out.Runs, run)
			continue
		}
		var jobs []struct {
			ID             int64    `json:"id"`
			Name           string   `json:"name"`
			Status         string   `json:"status"`
			StartedAt      string   `json:"started_at"`
			FinishedAt     string   `json:"finished_at"`
			QueuedDuration *float64 `json:"queued_duration"`
		}
		if err := p.getJSON(ctx, fmt.Sprintf("%s/pipelines/%d/jobs?include_retried=true&per_page=%d", project, pl.ID, jobPageSize), &jobs); err != nil {
			return provider.PullRequestCI{}, fmt.Errorf("gitlab: jobs of pipeline %d of %s!%d: %w", pl.ID, repo, mr, err)
		}
		if len(jobs) == jobPageSize {
			out.Truncated = true
		}
		sort.SliceStable(jobs, func(a, b int) bool { return jobs[a].ID < jobs[b].ID })
		attempts := map[string]int{}
		latest := map[string]int{}
		for _, j := range jobs {
			attempts[j.Name]++
			job := provider.CIJob{Name: j.Name, Status: pipelineStatus(j.Status), Attempt: attempts[j.Name],
				StartedAt: provider.ParseForgeTime(j.StartedAt), CompletedAt: provider.ParseForgeTime(j.FinishedAt)}
			if j.QueuedDuration != nil && *j.QueuedDuration >= 0 {
				q := int64(*j.QueuedDuration)
				job.QueuedSeconds = &q
			}
			if !job.Status.Terminal() {
				job.CompletedAt = nil
			}
			if len(run.Jobs) == provider.MaxCIJobs {
				out.Truncated = true
				break
			}
			latest[j.Name] = len(run.Jobs)
			run.Jobs = append(run.Jobs, job)
		}
		var completed time.Time
		for _, idx := range latest {
			j := run.Jobs[idx]
			if j.StartedAt != nil && (run.StartedAt == nil || j.StartedAt.Before(*run.StartedAt)) {
				started := *j.StartedAt
				run.StartedAt = &started
			}
			if j.CompletedAt != nil && j.CompletedAt.After(completed) {
				completed = *j.CompletedAt
			}
		}
		if run.Status.Terminal() && !completed.IsZero() {
			run.CompletedAt = &completed
		}
		out.Runs = append(out.Runs, run)
	}
	return out, nil
}

func pipelineStatus(s string) provider.CIStatus {
	switch s {
	case "success":
		return provider.CISuccess
	case "failed":
		return provider.CIFailure
	case "canceled", "canceling":
		return provider.CICancelled
	case "skipped", "manual":
		return provider.CISkipped
	case "running":
		return provider.CIRunning
	case "created", "pending", "waiting_for_resource", "waiting_for_callback", "preparing", "scheduled":
		return provider.CIPending
	}
	return provider.CIError
}

// PullRequestDiff assembles a merge request's unified diff from
// GET /projects/:id/merge_requests/:iid/diffs, page by page, at most
// maxBytes of it (ADR-0058). A file GitLab collapsed or found too large has
// no diff, and makes the result truncated.
func (p *Provider) PullRequestDiff(ctx context.Context, repo string, mr int, maxBytes int) ([]byte, bool, error) {
	base, err := p.mergeRequestBase(repo, mr)
	if err != nil {
		return nil, false, err
	}
	if maxBytes <= 0 || maxBytes > provider.MaxDiffBytes {
		maxBytes = provider.MaxDiffBytes
	}
	var buf bytes.Buffer
	truncated := false
	for page := 1; ; page++ {
		var diffs []struct {
			NewPath   string `json:"new_path"`
			OldPath   string `json:"old_path"`
			Diff      string `json:"diff"`
			TooLarge  bool   `json:"too_large"`
			Collapsed bool   `json:"collapsed"`
		}
		if err := p.getJSON(ctx, fmt.Sprintf("%s/diffs?page=%d&per_page=%d", base, page, changePageSize), &diffs); err != nil {
			return nil, false, fmt.Errorf("gitlab: diffs of %s!%d: %w", repo, mr, err)
		}
		for _, d := range diffs {
			if d.TooLarge || d.Collapsed {
				truncated = true
				continue
			}
			oldPath, newPath := d.OldPath, d.NewPath
			if oldPath == "" {
				oldPath = newPath
			}
			chunk := fmt.Sprintf("diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n%s", oldPath, newPath, oldPath, newPath, d.Diff)
			if !strings.HasSuffix(chunk, "\n") {
				chunk += "\n"
			}
			if buf.Len()+len(chunk) > maxBytes {
				room := maxBytes - buf.Len()
				if i := strings.LastIndexByte(chunk[:max(0, room)], '\n'); i >= 0 {
					buf.WriteString(chunk[:i+1])
				}
				return buf.Bytes(), true, nil
			}
			buf.WriteString(chunk)
		}
		if len(diffs) < changePageSize {
			break
		}
	}
	return buf.Bytes(), truncated, nil
}
