package httpapi

import (
	"context"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/forgefacts"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

const (
	pipelineCaptureTimeout = 30 * time.Second
	pipelineRecapture      = 10 * time.Minute
	pipelineConcurrency    = 4
)

func (s *Server) pipelineSlot() chan struct{} {
	s.pipelineOnce.Do(func() { s.pipelineSlots = make(chan struct{}, pipelineConcurrency) })
	return s.pipelineSlots
}

func pipelineKey(fp provider.ForgeProvider, pr forgefacts.PullRequest) store.PullRequestKey {
	return store.PullRequestKey{Forge: fp.Name(), Repo: pr.Repo, Number: pr.Number}
}

func (s *Server) capturePlayPipeline(ctx context.Context, fp provider.ForgeProvider, pr forgefacts.PullRequest, head string, final bool) {
	key := pipelineKey(fp, pr)
	activity, canActivity := fp.(provider.PullRequestActivityReader)
	history, canCI := fp.(provider.CIHistoryReader)
	now := time.Now()
	due := final
	if !due && (canActivity || canCI) {
		var err error
		if due, err = s.Store.PullRequestCaptureDue(ctx, key, now.Add(-pipelineRecapture)); err != nil {
			s.Log.Warn("pull request activity check failed", "provider", fp.Name(), "repo", pr.Repo, "pr", pr.Number, "err", err)
		}
	}
	if !due || (!canActivity && !canCI) {
		return
	}
	s.pipelineWork.Add(1)
	go func() {
		defer s.pipelineWork.Done()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pipelineCaptureTimeout)
		defer cancel()
		slots := s.pipelineSlot()
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		case <-ctx.Done():
			s.Log.Warn("pull request activity not read: too many reads at once", "provider", fp.Name(), "repo", pr.Repo, "pr", pr.Number)
			return
		}
		s.readPlayPipeline(ctx, fp, activity, history, pr, head)
	}()
}

func (s *Server) readPlayPipeline(ctx context.Context, fp provider.ForgeProvider, activityReader provider.PullRequestActivityReader,
	history provider.CIHistoryReader, pr forgefacts.PullRequest, head string) {
	key := pipelineKey(fp, pr)
	var activity *store.PullRequestActivity
	var heads []string
	if activityReader != nil {
		read, err := activityReader.PullRequestActivity(ctx, pr.Repo, pr.Number)
		if err != nil {
			s.Log.Warn("pull request activity not read; Ploeg keeps what is recorded", "provider", fp.Name(),
				"repo", pr.Repo, "pr", pr.Number, "err", err)
		} else {
			activity = storeActivity(read)
			for _, e := range read.Events {
				if (e.Kind == provider.ActivityPush || e.Kind == provider.ActivityForcePush) && e.HeadSHA != "" {
					heads = append(heads, e.HeadSHA)
				}
			}
		}
	}
	if head != "" {
		heads = append(heads, head)
	}
	var ci *store.PullRequestCIRuns
	if history != nil {
		read, err := history.PullRequestCI(ctx, pr.Repo, pr.Number, pr.Branch, heads)
		if err != nil {
			s.Log.Warn("pull request CI runs not read; Ploeg keeps what is recorded", "provider", fp.Name(),
				"repo", pr.Repo, "pr", pr.Number, "err", err)
		} else {
			ci = storeCIRuns(read)
		}
	}
	if _, err := s.Store.RecordPullRequestPipeline(ctx, key, activity, ci, time.Now()); err != nil {
		s.Log.Error("pull request activity and CI runs not recorded", "provider", fp.Name(), "repo", pr.Repo, "pr", pr.Number, "err", err)
	}
}

func storeActivity(a provider.PullRequestActivity) *store.PullRequestActivity {
	out := &store.PullRequestActivity{Truncated: a.EventsTruncated, Commits: a.Commits, CommitsTruncated: a.CommitsTruncated,
		FirstCommitAt: a.FirstCommitAt, ForcePushesKnown: a.ForcePushesKnown, Events: make([]store.PullRequestEvent, 0, len(a.Events))}
	for _, e := range a.Events {
		out.Events = append(out.Events, store.PullRequestEvent{Kind: string(e.Kind), Actor: e.Actor, At: e.At, State: string(e.State), HeadSHA: e.HeadSHA})
	}
	return out
}

func storeCIRuns(c provider.PullRequestCI) *store.PullRequestCIRuns {
	out := &store.PullRequestCIRuns{Source: c.Source, Truncated: c.Truncated, Runs: make([]store.PullRequestCIRun, 0, len(c.Runs))}
	for _, r := range c.Runs {
		run := store.PullRequestCIRun{Key: r.ID, HeadSHA: r.SHA, Workflow: r.Workflow, Status: string(r.Status),
			CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, CompletedAt: r.CompletedAt, Jobs: make([]store.CIJob, 0, len(r.Jobs))}
		for _, j := range r.Jobs {
			run.Jobs = append(run.Jobs, store.CIJob{Name: j.Name, Status: string(j.Status), StartedAt: j.StartedAt, CompletedAt: j.CompletedAt,
				QueuedSeconds: j.QueuedSeconds, Attempt: j.Attempt})
		}
		out.Runs = append(out.Runs, run)
	}
	return out
}

func (s *Server) recordPlayIndentation(ctx context.Context, fp provider.ForgeProvider, repo string, number int) {
	reader, ok := fp.(provider.PullRequestDiffReader)
	if !ok {
		return
	}
	diff, _, err := reader.PullRequestDiff(ctx, repo, number, provider.MaxDiffBytes)
	if err != nil {
		s.Log.Warn("merged pull request diff not read; file indentation stays unknown", "provider", fp.Name(), "repo", repo, "pr", number, "err", err)
		return
	}
	if _, err := s.Store.RecordPullRequestIndentation(ctx, store.PullRequestKey{Forge: fp.Name(), Repo: repo, Number: number}, diff); err != nil {
		s.Log.Error("pull request file indentation not recorded", "provider", fp.Name(), "repo", repo, "pr", number, "err", err)
	}
}
