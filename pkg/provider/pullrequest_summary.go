package provider

import "context"

// PullRequestSummary is what a forge lists about one pull request: enough
// for Ploeg to decide whether a Team explains it and to brief that Team
// (ADR-0106). Empty fields are facts the forge did not report.
type PullRequestSummary struct {
	Number int
	Title  string
	// Body is the description as its author wrote it, unrendered.
	Body string
	// Author is the forge login that opened the pull request.
	Author string
	Draft  bool
	// HeadBranch is the branch under review in the repository itself, and
	// HeadSHA its current commit.
	HeadBranch string
	HeadSHA    string
	BaseBranch string
	// URL is where a person reads the pull request.
	URL string
}

// PullRequestLister is implemented by a ForgeProvider that can list a
// repository's open pull requests and read one of them with its title and
// description (ADR-0106). A pull request from a fork is not the repository's
// own and is never listed.
type PullRequestLister interface {
	ListOpenPullRequests(ctx context.Context, repo string) ([]PullRequestSummary, error)
	PullRequestSummary(ctx context.Context, repo string, pr int) (PullRequestSummary, error)
}
