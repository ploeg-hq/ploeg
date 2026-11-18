package forgejo

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/oauthconn"
)

func apiRoot(inst provider.ConnectionInstance) string {
	if api := oauthconn.Trim(inst.APIURL); api != "" {
		return api
	}
	return oauthconn.Trim(inst.URL) + "/api/v1"
}

type grantRepo struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	Archived      bool   `json:"archived"`
	Mirror        bool   `json:"mirror"`
	DefaultBranch string `json:"default_branch"`
	Permissions   struct {
		Admin bool `json:"admin"`
		Push  bool `json:"push"`
		Pull  bool `json:"pull"`
	} `json:"permissions"`
}

func repoURL(inst provider.ConnectionInstance, fullName string) (string, error) {
	parts := strings.Split(fullName, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("forgejo: %q is not owner/name: %w", fullName, provider.ErrRepositoryNotFound)
	}
	return apiRoot(inst) + "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]), nil
}

// ProbeRepositoryGrant reports whether the consenting account may push to
// fullName. A repository the account cannot see is not found: Forgejo shows
// a private repository only to those who may read it.
func (Connector) ProbeRepositoryGrant(ctx context.Context, inst provider.ConnectionInstance, cred provider.ConnectionCredential, fullName string) (provider.RepositoryGrant, error) {
	u, err := repoURL(inst, fullName)
	if err != nil {
		return provider.RepositoryGrant{}, err
	}
	var repo grantRepo
	found, err := oauthconn.Lookup(ctx, u, "Bearer "+cred.Token, &repo)
	if err != nil {
		return provider.RepositoryGrant{}, err
	}
	if !found {
		return provider.RepositoryGrant{}, fmt.Errorf("forgejo: %s: %w", fullName, provider.ErrRepositoryNotFound)
	}
	var perms []string
	if repo.Permissions.Pull {
		perms = append(perms, "pull")
	}
	if repo.Permissions.Push {
		perms = append(perms, "push")
	}
	if repo.Permissions.Admin {
		perms = append(perms, "admin")
	}
	scoped := tokenWritesRepositories(inst)
	if !scoped {
		perms = append(perms, "token:read-only")
	}
	return provider.RepositoryGrant{ID: strconv.FormatInt(repo.ID, 10), FullName: repo.FullName, CanWrite: repo.Permissions.Push && scoped,
		Permissions: perms, DefaultBranch: repo.DefaultBranch}, nil
}

// tokenWritesRepositories reports whether the OAuth tokens of inst may
// write repositories. Forgejo grants an OAuth token the scopes its
// application asks for, and every scope when it asks for none.
func tokenWritesRepositories(inst provider.ConnectionInstance) bool {
	if inst.OAuth == nil || len(inst.OAuth.Scopes) == 0 {
		return true
	}
	for _, scope := range inst.OAuth.Scopes {
		switch strings.TrimSpace(scope) {
		case "all", "write:repository":
			return true
		}
	}
	return false
}

// InspectGrantedRepository reads whether fullName is archived or a mirror
// and whether AGENTS.md is on branch, with the connection's token.
func (Connector) InspectGrantedRepository(ctx context.Context, inst provider.ConnectionInstance, cred provider.ConnectionCredential, fullName, branch string) (provider.RepositoryState, error) {
	u, err := repoURL(inst, fullName)
	if err != nil {
		return provider.RepositoryState{}, err
	}
	var repo grantRepo
	found, err := oauthconn.Lookup(ctx, u, "Bearer "+cred.Token, &repo)
	if err != nil {
		return provider.RepositoryState{}, err
	}
	if !found {
		return provider.RepositoryState{}, fmt.Errorf("forgejo: %s: %w", fullName, provider.ErrRepositoryNotFound)
	}
	state := provider.RepositoryState{Archived: repo.Archived, Mirror: repo.Mirror, Branch: branch}
	if state.Branch == "" {
		state.Branch = repo.DefaultBranch
	}
	state.AgentsFile, err = oauthconn.Lookup(ctx, u+"/contents/AGENTS.md?ref="+url.QueryEscape(state.Branch), "Bearer "+cred.Token, nil)
	return state, err
}
