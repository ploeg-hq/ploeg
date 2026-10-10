package provider

import "context"

// PublishedPullRequest is what a forge reports about a pull request a
// trusted publisher says it opened, read with Ploeg's own forge credential
// (ADR 0027). Repositories are the forge's owner/name of each side.
type PublishedPullRequest struct {
	State          PullRequestState
	HeadSHA        string
	HeadRef        string
	HeadRepository string
	BaseRef        string
	BaseRepository string
	Author         string
}

// PublishedPullRequestReader is implemented by a ForgeProvider that can read
// both sides of a pull request. Ploeg accepts a published delivery only from
// a forge that implements it.
type PublishedPullRequestReader interface {
	PublishedPullRequest(ctx context.Context, repo string, pr int) (PublishedPullRequest, error)
}
