package provider

import "context"

// PullRequestCloser is implemented by a ForgeProvider that can find the open
// pull requests whose head is a branch of the repository itself and close
// one without deleting that branch (ADR-0105). A pull request from a fork
// with the same branch name is not the repository's own and is never listed.
type PullRequestCloser interface {
	OpenPullRequests(ctx context.Context, repo, branch string) ([]int, error)
	ClosePullRequest(ctx context.Context, repo string, pr int) error
}
