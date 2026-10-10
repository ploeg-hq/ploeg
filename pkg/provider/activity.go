package provider

import (
	"context"
	"time"
)

// Bounds on what one activity, CI or diff read returns (ADR-0058). A read
// that reaches a bound reports itself truncated.
const (
	// MaxActivityEvents is how many events a PullRequestActivity holds.
	MaxActivityEvents = 500
	// MaxActivityCommits is how many commits a PullRequestActivity counts.
	MaxActivityCommits = 250
	// MaxCIRuns is how many CI runs (pipelines) a PullRequestCI holds.
	MaxCIRuns = 30
	// MaxCIRunsWithJobs is how many of the newest runs, or head commits,
	// have their jobs read.
	MaxCIRunsWithJobs = 10
	// MaxCIJobs is how many jobs one CIRun holds.
	MaxCIJobs = 100
	// MaxDiffBytes is how much of a pull request's unified diff a
	// PullRequestDiffReader returns.
	MaxDiffBytes = 1 << 20
)

// ActivityKind is one kind of event on a pull request's conversation.
type ActivityKind string

const (
	// ActivityComment is a comment on the pull request's conversation.
	ActivityComment ActivityKind = "comment"
	// ActivityReviewComment is an inline comment on the diff.
	ActivityReviewComment ActivityKind = "review_comment"
	// ActivityReview is a submitted review; ActivityEvent.State is its
	// verdict and HeadSHA the commit it reviewed.
	ActivityReview ActivityKind = "review"
	// ActivityPush is a push to the head branch; HeadSHA is the head after
	// it.
	ActivityPush ActivityKind = "push"
	// ActivityForcePush is a push that rewrote the head branch.
	ActivityForcePush ActivityKind = "force_push"
	// ActivityReady is the pull request leaving draft (or WIP).
	ActivityReady ActivityKind = "ready"
	// ActivityDraft is the pull request being marked draft (or WIP).
	ActivityDraft ActivityKind = "draft"
)

// ActivityEvent is one event on a pull request: who, what and when. Ploeg
// never keeps the text of a comment or review. Actor is empty when the forge
// did not say.
type ActivityEvent struct {
	Kind    ActivityKind
	Actor   string
	At      time.Time
	State   ForgeReviewState
	HeadSHA string
}

// PullRequestActivity is what a forge reports about the conversation and
// commits of one pull request (ADR-0058). Events are oldest first and hold
// at most MaxActivityEvents; EventsTruncated says there were more. Commits
// counts at most MaxActivityCommits commits, and FirstCommitAt is the
// earliest author date among them. ForcePushesKnown is false when the forge
// does not report force pushes, so their count is unknown rather than zero.
type PullRequestActivity struct {
	Events           []ActivityEvent
	EventsTruncated  bool
	Commits          int
	CommitsTruncated bool
	FirstCommitAt    *time.Time
	ForcePushesKnown bool
}

// PullRequestActivityReader is implemented by a ForgeProvider that can read
// a pull request's comments, reviews, pushes, draft changes and commits. A
// forge without it records only the reviews its webhooks report.
type PullRequestActivityReader interface {
	PullRequestActivity(ctx context.Context, repo string, pr int) (PullRequestActivity, error)
}

// CIStatus is the outcome of a CI run or job.
type CIStatus string

const (
	CISuccess   CIStatus = "success"
	CIFailure   CIStatus = "failure"
	CIError     CIStatus = "error"
	CICancelled CIStatus = "cancelled"
	CISkipped   CIStatus = "skipped"
	CIPending   CIStatus = "pending"
	CIRunning   CIStatus = "running"
)

// Terminal reports whether s is a final outcome.
func (s CIStatus) Terminal() bool {
	switch s {
	case CISuccess, CIFailure, CIError, CICancelled, CISkipped:
		return true
	}
	return false
}

// CIJob is one attempt of one CI job or check. Name is the job or check
// name as the forge shows it. QueuedSeconds is how long the attempt waited
// for a runner before it started, nil when the forge did not say. Attempt
// counts from 1; a rerun of the same job on the same run is attempt 2.
type CIJob struct {
	Name          string
	Status        CIStatus
	StartedAt     *time.Time
	CompletedAt   *time.Time
	QueuedSeconds *int64
	Attempt       int
}

// CIRun is one CI run (a Forgejo Actions run, a GitLab pipeline, or the
// checks one head commit reported without either) on one head commit.
// Workflow names the workflow file, empty when the forge has one pipeline per
// commit. StartedAt and CompletedAt describe its latest attempt.
type CIRun struct {
	ID          string
	SHA         string
	Workflow    string
	Status      CIStatus
	CreatedAt   *time.Time
	StartedAt   *time.Time
	CompletedAt *time.Time
	Jobs        []CIJob
}

// CI sources: where a PullRequestCI's runs came from.
const (
	CISourceActions   = "actions"
	CISourceStatuses  = "statuses"
	CISourcePipelines = "pipelines"
)

// PullRequestCI is every CI run a forge reports for one pull request, across
// its head commits, newest first, at most MaxCIRuns. Truncated says there
// were more runs, or runs whose jobs were not read. Source is one of the
// CISource constants.
type PullRequestCI struct {
	Runs      []CIRun
	Truncated bool
	Source    string
}

// CIHistoryReader is implemented by a ForgeProvider that can read the CI
// runs of a pull request with their timings (ADR-0058). branch is the head
// branch when known, and heads the head commits Ploeg saw; both narrow runs
// the forge does not tie to the pull request itself. A forge without it
// records no CI runs.
type CIHistoryReader interface {
	PullRequestCI(ctx context.Context, repo string, pr int, branch string, heads []string) (PullRequestCI, error)
}

// PullRequestDiffReader is implemented by a ForgeProvider that can read a
// pull request's unified diff. It returns at most maxBytes, cut at a line,
// and truncated when the diff was longer or the forge left files out.
type PullRequestDiffReader interface {
	PullRequestDiff(ctx context.Context, repo string, pr int, maxBytes int) (diff []byte, truncated bool, err error)
}
