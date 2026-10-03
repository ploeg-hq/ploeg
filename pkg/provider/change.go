package provider

import "context"

// MaxChangedFiles is how many changed file paths a PullRequestChange holds.
// A pull request that touched more is reported with FilesTruncated.
const MaxChangedFiles = 300

// MaxChangeCommits is how many commit messages a PullRequestChange holds.
const MaxChangeCommits = 250

// PullRequestChange is what a forge reports about the content of one pull
// request (ADR-0052): its title, description and labels, the paths it
// changed (a renamed file contributes its old and new path) and the messages
// of its commits. Files holds at most MaxChangedFiles paths and Commits at
// most MaxChangeCommits messages. Lines holds the lines each file added
// and removed, for the files the forge counted (ADR-0056); a renamed file's
// old path counts zero.
type PullRequestChange struct {
	Title          string
	Body           string
	Labels         []string
	Files          []string
	FilesTruncated bool
	Lines          map[string]FileLines
	Commits        []string
}

// FileLines is how many lines one changed file added and removed.
type FileLines struct {
	Additions int
	Deletions int
}

// PullRequestChangeReader is implemented by a ForgeProvider that can read a
// pull request's changed files and commits. A forge without it records no
// files, so its plays are never crack candidates and its reverts go
// undetected.
type PullRequestChangeReader interface {
	PullRequestChange(ctx context.Context, repo string, pr int) (PullRequestChange, error)
}
