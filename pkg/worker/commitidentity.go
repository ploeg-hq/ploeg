package worker

import "strings"

const (
	defaultCommitName  = "agent-builder"
	defaultCommitEmail = "agent-builder@ploeg.invalid"
)

// commitIdentity is the git author and committer of the commits a Run makes.
// A part left empty commits as its default.
type commitIdentity struct {
	name, email string
}

// claimedCommitIdentity is the identity the claim's forge configures
// (ADR-0016).
func claimedCommitIdentity(claimed *ClaimResponse) commitIdentity {
	if claimed == nil {
		return commitIdentity{}.resolved()
	}
	return commitIdentity{name: claimed.CommitName, email: claimed.CommitEmail}.resolved()
}

func (id commitIdentity) resolved() commitIdentity {
	id.name, id.email = strings.TrimSpace(id.name), strings.TrimSpace(id.email)
	if id.name == "" {
		id.name = defaultCommitName
	}
	if id.email == "" {
		id.email = defaultCommitEmail
	}
	return id
}

func (id commitIdentity) gitConfig() []string {
	id = id.resolved()
	return []string{"-c", "user.name=" + id.name, "-c", "user.email=" + id.email}
}

func (id commitIdentity) environ() []string {
	id = id.resolved()
	return []string{"GIT_AUTHOR_NAME=" + id.name, "GIT_AUTHOR_EMAIL=" + id.email,
		"GIT_COMMITTER_NAME=" + id.name, "GIT_COMMITTER_EMAIL=" + id.email}
}
