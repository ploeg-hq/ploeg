package gitlab

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
	return oauthconn.Trim(inst.URL) + "/api/v4"
}

type grantProject struct {
	ID                int64  `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
	Archived          bool   `json:"archived"`
	Mirror            bool   `json:"mirror"`
	DefaultBranch     string `json:"default_branch"`
	Permissions       struct {
		Project *struct {
			AccessLevel int `json:"access_level"`
		} `json:"project_access"`
		Group *struct {
			AccessLevel int `json:"access_level"`
		} `json:"group_access"`
	} `json:"permissions"`
}

func (p grantProject) accessLevel() int {
	level := 0
	if p.Permissions.Project != nil {
		level = p.Permissions.Project.AccessLevel
	}
	if p.Permissions.Group != nil && p.Permissions.Group.AccessLevel > level {
		level = p.Permissions.Group.AccessLevel
	}
	return level
}

func projectURL(inst provider.ConnectionInstance, fullName string) (string, error) {
	if strings.Count(fullName, "/") < 1 || strings.HasPrefix(fullName, "/") || strings.HasSuffix(fullName, "/") || strings.Contains(fullName, "//") {
		return "", fmt.Errorf("gitlab: %q is not group/project: %w", fullName, provider.ErrRepositoryNotFound)
	}
	return apiRoot(inst) + "/projects/" + url.PathEscape(fullName), nil
}

func (Connector) project(ctx context.Context, inst provider.ConnectionInstance, cred provider.ConnectionCredential, fullName string) (grantProject, string, error) {
	u, err := projectURL(inst, fullName)
	if err != nil {
		return grantProject{}, "", err
	}
	var p grantProject
	found, err := oauthconn.Lookup(ctx, u, "Bearer "+cred.Token, &p)
	if err != nil {
		return grantProject{}, "", err
	}
	if !found {
		return grantProject{}, "", fmt.Errorf("gitlab: %s: %w", fullName, provider.ErrRepositoryNotFound)
	}
	return p, u, nil
}

// ProbeRepositoryGrant reports whether the consenting account may push to
// fullName: Developer access or above.
func (c Connector) ProbeRepositoryGrant(ctx context.Context, inst provider.ConnectionInstance, cred provider.ConnectionCredential, fullName string) (provider.RepositoryGrant, error) {
	p, _, err := c.project(ctx, inst, cred, fullName)
	if err != nil {
		return provider.RepositoryGrant{}, err
	}
	level := p.accessLevel()
	return provider.RepositoryGrant{ID: strconv.FormatInt(p.ID, 10), FullName: p.PathWithNamespace, CanWrite: level >= developerAccess,
		Permissions: []string{"access_level:" + strconv.Itoa(level)}, DefaultBranch: p.DefaultBranch}, nil
}

// InspectGrantedRepository reads whether fullName is archived or a mirror
// and whether AGENTS.md is on branch, with the connection's token.
func (c Connector) InspectGrantedRepository(ctx context.Context, inst provider.ConnectionInstance, cred provider.ConnectionCredential, fullName, branch string) (provider.RepositoryState, error) {
	p, u, err := c.project(ctx, inst, cred, fullName)
	if err != nil {
		return provider.RepositoryState{}, err
	}
	state := provider.RepositoryState{Archived: p.Archived, Mirror: p.Mirror, Branch: branch}
	if state.Branch == "" {
		state.Branch = p.DefaultBranch
	}
	state.AgentsFile, err = oauthconn.Lookup(ctx, u+"/repository/files/AGENTS.md?ref="+url.QueryEscape(state.Branch), "Bearer "+cred.Token, nil)
	return state, err
}
