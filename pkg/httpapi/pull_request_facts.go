package httpapi

import (
	"context"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/forgefacts"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

func (s *Server) recordPullRequestFacts(ctx context.Context, fp provider.ForgeProvider, ev provider.ForgeEvent) {
	switch ev.Kind {
	case provider.ForgeReviewSubmitted, provider.ForgePRMerged, provider.ForgePRClosed,
		provider.ForgePROpened, provider.ForgePRSynchronized, provider.ForgeMergeStateDirty:
	default:
		return
	}
	if ev.PR <= 0 || ev.Repo == "" {
		return
	}
	pr := forgefacts.PullRequest{Forge: fp.Name(), Repo: ev.Repo, Number: ev.PR, Branch: ev.Branch}
	rec := forgefacts.Facts(pr, ev.PullRequest, nil, time.Time{})
	if ev.Kind == provider.ForgeReviewSubmitted {
		rec.Review = &store.PullRequestReview{Reviewer: ev.Actor, State: string(ev.Review), HeadSHA: ev.PullRequest.HeadSHA}
	}
	recorded, err := s.Store.RecordPullRequestFacts(ctx, rec)
	if err != nil {
		s.Log.Error("pull request facts not recorded", "provider", fp.Name(), "kind", ev.Kind,
			"repo", ev.Repo, "pr", ev.PR, "err", err)
		return
	}
	if !recorded {
		s.Log.Debug("pull request facts skipped: not a Ploeg pull request", "provider", fp.Name(),
			"repo", ev.Repo, "pr", ev.PR, "branch", ev.Branch)
		if ev.Kind == provider.ForgePRMerged {
			s.captureMergedChange(ctx, fp, ev, false)
		}
		return
	}
	head := s.capturePullRequestFacts(ctx, fp, pr, ev.PullRequest)
	if ev.Kind == provider.ForgePRMerged {
		s.captureMergedChange(ctx, fp, ev, true)
	}
	s.capturePlayPipeline(ctx, fp, pr, head, ev.Kind == provider.ForgePRMerged || ev.Kind == provider.ForgePRClosed)
	if ev.Kind == provider.ForgePRMerged {
		s.publishMergedCard(ctx, fp, ev)
	}
}

const mergedChangeTimeout = 20 * time.Second

func (s *Server) captureMergedChange(ctx context.Context, fp provider.ForgeProvider, ev provider.ForgeEvent, play bool) {
	ctx, cancel := context.WithTimeout(ctx, mergedChangeTimeout)
	defer cancel()
	if reader, ok := fp.(provider.PullRequestChangeReader); ok {
		s.recordMergedChange(ctx, fp, reader, ev, play)
	}
	if !play {
		return
	}
	mended, err := s.Store.RecordMends(ctx, fp.Name(), ev.Repo, ev.PR)
	if err != nil {
		s.Log.Error("crack mends not recorded", "provider", fp.Name(), "repo", ev.Repo, "pr", ev.PR, "err", err)
		return
	}
	if mended > 0 {
		s.Log.Info("cracks mended by a merged fix", "provider", fp.Name(), "repo", ev.Repo, "pr", ev.PR, "cracks", mended)
	}
}

func (s *Server) recordMergedChange(ctx context.Context, fp provider.ForgeProvider, reader provider.PullRequestChangeReader, ev provider.ForgeEvent, play bool) {
	if !play {
		worth, err := s.Store.HasMergedPlays(ctx, fp.Name(), ev.Repo)
		if err != nil || !worth {
			return
		}
	}
	change, err := reader.PullRequestChange(ctx, ev.Repo, ev.PR)
	if err != nil {
		s.Log.Warn("merged pull request change not read; files and reverts not recorded", "provider", fp.Name(),
			"repo", ev.Repo, "pr", ev.PR, "err", err)
		return
	}
	if play {
		if _, err := s.Store.RecordPullRequestChange(ctx, store.PullRequestChange{Forge: fp.Name(), Repo: ev.Repo, Number: ev.PR,
			Labels: change.Labels, Files: change.Files, FilesTruncated: change.FilesTruncated, Lines: fileLines(change.Lines)}); err != nil {
			s.Log.Error("pull request files not recorded", "provider", fp.Name(), "repo", ev.Repo, "pr", ev.PR, "err", err)
		} else {
			s.recordPlayShape(ctx, fp, ev.Repo, ev.PR)
		}
	}
	claim := forgefacts.Reverts(ev.Repo, change)
	if len(claim.Numbers) == 0 && len(claim.SHAs) == 0 {
		return
	}
	reverted, err := s.Store.RecordRevert(ctx, store.Revert{Forge: fp.Name(), Repo: ev.Repo, Number: ev.PR,
		MergeCommitSHA: ev.PullRequest.MergeCommitSHA, MergedAt: ev.PullRequest.MergedAt, MergedBy: ev.PullRequest.MergedBy,
		Numbers: claim.Numbers, SHAs: claim.SHAs})
	if err != nil {
		s.Log.Error("revert not recorded", "provider", fp.Name(), "repo", ev.Repo, "pr", ev.PR, "err", err)
		return
	}
	if len(reverted) > 0 {
		s.Log.Info("revert of a Ploeg play recorded", "provider", fp.Name(), "repo", ev.Repo, "pr", ev.PR, "work_items", reverted)
	}
}

func (s *Server) capturePullRequestFacts(ctx context.Context, fp provider.ForgeProvider, pr forgefacts.PullRequest, facts provider.PullRequestFacts) string {
	read, err := fp.PullRequestFacts(ctx, pr.Repo, pr.Number)
	readOK := err == nil
	if err != nil {
		s.Log.Warn("pull request facts read failed; keeping the webhook's", "provider", fp.Name(),
			"repo", pr.Repo, "pr", pr.Number, "err", err)
	} else {
		facts = forgefacts.WithMissing(facts, read)
	}
	ci, err := forgefacts.CI(ctx, fp, pr.Repo, facts.HeadSHA)
	if err != nil {
		s.Log.Warn("commit status read failed; CI stays as recorded", "provider", fp.Name(),
			"repo", pr.Repo, "pr", pr.Number, "head", facts.HeadSHA, "err", err)
	}
	if !readOK && ci == nil {
		return facts.HeadSHA
	}
	if _, err := s.Store.RecordPullRequestFacts(ctx, forgefacts.Facts(pr, facts, ci, time.Now())); err != nil {
		s.Log.Error("pull request facts not recorded", "provider", fp.Name(),
			"repo", pr.Repo, "pr", pr.Number, "err", err)
	}
	return facts.HeadSHA
}

func fileLines(lines map[string]provider.FileLines) map[string]store.FileLines {
	out := make(map[string]store.FileLines, len(lines))
	for path, l := range lines {
		out[path] = store.FileLines{Additions: l.Additions, Deletions: l.Deletions}
	}
	return out
}
