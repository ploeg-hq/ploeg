package provider

import "context"

// PullRequestMergeability is a forge's answer to "does this pull request
// merge into its base branch" (ADR-0040), read together with the pull
// request's facts. Mergeable is nil when the forge has not computed it, and
// BaseBranch is empty when the forge did not say.
type PullRequestMergeability struct {
	Facts      PullRequestFacts
	Mergeable  *bool
	BaseBranch string
}

// MergeabilityReader is implemented by a ForgeProvider that reports whether a
// pull request merges cleanly. One call reads the pull request once and
// returns its facts too, so a caller needs no PullRequestFacts read as well.
// A forge without it leaves every merge state unknown.
type MergeabilityReader interface {
	PullRequestMergeability(ctx context.Context, repo string, pr int) (PullRequestMergeability, error)
}

// ReadPullRequest reads a pull request's facts and, when fp is a
// MergeabilityReader, its mergeability in the same read. ok is false when fp
// cannot report mergeability or the read failed.
func ReadPullRequest(ctx context.Context, fp ForgeProvider, repo string, pr int) (m PullRequestMergeability, ok bool, err error) {
	if reader, can := fp.(MergeabilityReader); can {
		m, err = reader.PullRequestMergeability(ctx, repo, pr)
		return m, err == nil, err
	}
	m.Facts, err = fp.PullRequestFacts(ctx, repo, pr)
	return m, false, err
}
