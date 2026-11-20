package worker

import (
	"log/slog"
	"net/url"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

const (
	fallbackCommitName  = "ploeg-agent"
	fallbackCommitEmail = "agent@ploeg.invalid"
)

// commitIdentity is the git author and committer of the commits a Run makes:
// the account its forge credential pushes as (ADR-0206). The zero value is
// the neutral fallback.
type commitIdentity struct {
	name, email string
}

// claimedCommitIdentity is the identity ploegd read from the Run's forge,
// or the fallback, logged with why, when the claim carries none.
func claimedCommitIdentity(claimed *ClaimResponse, log *slog.Logger) commitIdentity {
	var c *work.GitIdentity
	if claimed != nil && claimed.Attribution != nil {
		c = claimed.Attribution.Committer
	}
	if c == nil || oneLine(c.Name) == "" || oneLine(c.Email) == "" {
		if log != nil {
			log.Warn("the claim carries no identity of the forge account; committing as the fallback",
				"name", fallbackCommitName, "email", fallbackCommitEmail)
		}
		return commitIdentity{}.resolved()
	}
	return commitIdentity{name: oneLine(c.Name), email: oneLine(c.Email)}
}

func (id commitIdentity) resolved() commitIdentity {
	if id.name == "" || id.email == "" {
		return commitIdentity{name: fallbackCommitName, email: fallbackCommitEmail}
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

// attributionTrailers are the trailers a Run's commits end with after
// Agent-Trace-Id (ADR-0206): the Run's id, the Work Item with its tracker
// link, and the person the Run acts for with how Ploeg knows them.
func attributionTrailers(a *work.Attribution, item work.WorkItem) []string {
	var out []string
	if a != nil && oneLine(a.Run) != "" {
		out = append(out, "Ploeg-Run: "+oneLine(a.Run))
	}
	workItem := work.Reference(item)
	if link := httpLink(item.URL); link != "" {
		workItem += " " + link
	}
	out = append(out, "Work-Item: "+workItem)
	if a != nil && oneLine(a.Person) != "" {
		person := oneLine(a.Person)
		if proof := oneLine(a.PersonProof); proof != "" {
			person += " (" + proof + ")"
		}
		out = append(out, "Ploeg-Acts-For: "+person)
	}
	return out
}

func oneLine(s string) string {
	return strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(s))
}

func httpLink(raw string) string {
	raw = oneLine(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || strings.ContainsAny(raw, " \t") {
		return ""
	}
	return raw
}
