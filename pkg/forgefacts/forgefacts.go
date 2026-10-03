// Package forgefacts turns what a forge reported about a pull request into
// the facts pkg/store keeps (ADR-0045, ADR-0046). The forge webhook handler
// and the review poller both record through it, so the two paths store the
// same shape.
package forgefacts

import (
	"context"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

// PullRequest names one pull request on one forge, and the Work Item it
// belongs to when the caller knows it (zero otherwise).
type PullRequest struct {
	Forge      string
	Repo       string
	Number     int
	Branch     string
	WorkItemID int64
}

// Facts converts what the forge reported, and the commit status read at its
// head when there is one, into the store's record.
func Facts(pr PullRequest, f provider.PullRequestFacts, ci *provider.CommitStatus, capturedAt time.Time) store.PullRequestFacts {
	rec := store.PullRequestFacts{
		Forge: pr.Forge, Repo: pr.Repo, Number: pr.Number, Branch: pr.Branch, WorkItemID: pr.WorkItemID,
		State: string(f.State), HeadSHA: f.HeadSHA, MergeCommitSHA: f.MergeCommitSHA,
		MergedAt: f.MergedAt, MergedBy: f.MergedBy, ClosedAt: f.ClosedAt,
		Additions: f.Additions, Deletions: f.Deletions, ChangedFiles: f.ChangedFiles,
		OpenedAt: f.OpenedAt, Author: f.Author, Draft: f.Draft,
	}
	if ci != nil {
		checks := make([]store.PullRequestCheck, 0, len(ci.Checks))
		for _, c := range ci.Checks {
			checks = append(checks, store.PullRequestCheck{Context: c.Context, State: string(c.State)})
		}
		rec.CI = &store.PullRequestCI{State: string(ci.State), Checks: checks, HeadSHA: ci.SHA, CapturedAt: capturedAt}
	}
	return rec
}

// WithMissing keeps every fact f has and takes the rest from read.
func WithMissing(f, read provider.PullRequestFacts) provider.PullRequestFacts {
	if f.State == "" {
		f.State = read.State
	}
	if f.HeadSHA == "" {
		f.HeadSHA = read.HeadSHA
	}
	if f.MergeCommitSHA == "" {
		f.MergeCommitSHA = read.MergeCommitSHA
	}
	if f.MergedAt == nil {
		f.MergedAt = read.MergedAt
	}
	if f.MergedBy == "" {
		f.MergedBy = read.MergedBy
	}
	if f.ClosedAt == nil {
		f.ClosedAt = read.ClosedAt
	}
	if f.Additions == nil {
		f.Additions = read.Additions
	}
	if f.Deletions == nil {
		f.Deletions = read.Deletions
	}
	if f.ChangedFiles == nil {
		f.ChangedFiles = read.ChangedFiles
	}
	if f.OpenedAt == nil {
		f.OpenedAt = read.OpenedAt
	}
	if f.Author == "" {
		f.Author = read.Author
	}
	if f.Draft == nil {
		f.Draft = read.Draft
	}
	return f
}

// CI reads the combined commit status at sha. It returns nil, and no error,
// when fp cannot read statuses or the commit has no check.
func CI(ctx context.Context, fp provider.ForgeProvider, repo, sha string) (*provider.CommitStatus, error) {
	status, ok, err := provider.ReadCommitStatus(ctx, fp, repo, sha)
	if err != nil || !ok {
		return nil, err
	}
	return &status, nil
}
