package provider

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

type ExecutionItem struct {
	Item work.WorkItem
	Open bool
}

type ExecutionReader interface {
	TrackerAPIBaseURL() string
	FetchExecutionItem(context.Context, string) (ExecutionItem, error)
}

type ForgeRepositoryLocator interface {
	RepositoryURL(owner, repository string) (string, error)
}

func RepositoryURL(base, owner, repository string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", errors.New("invalid forge repository endpoint")
	}
	for _, segment := range strings.Split(owner+"/"+repository, "/") {
		if segment == "" || segment == "." || segment == ".." || strings.ContainsAny(segment, "/\\?#%\x00") {
			return "", errors.New("invalid repository coordinate")
		}
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + owner + "/" + strings.TrimSuffix(repository, ".git") + ".git"
	return u.String(), nil
}

// RepositoryState is what a forge reports about a repository that decides
// whether agent work may target it (ADR-0038's readiness gate).
type RepositoryState struct {
	Archived bool
	Mirror   bool
	// Branch is the branch that was inspected: the requested base branch, or
	// the repository's default branch when none was requested.
	Branch string
	// AgentsFile reports an AGENTS.md at the root of Branch.
	AgentsFile bool
}

// RepositoryInspector is implemented by a ForgeProvider that can read
// RepositoryState. A repository the forge does not show is an error.
type RepositoryInspector interface {
	InspectRepository(ctx context.Context, owner, repository, branch string) (RepositoryState, error)
}
