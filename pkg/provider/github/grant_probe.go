package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/oauthconn"
)

func (Connector) installationToken(ctx context.Context, inst provider.ConnectionInstance, cred provider.ConnectionCredential) (string, func(), error) {
	auth, err := appAuth(cred.AppID, cred.PrivateKey)
	if err != nil {
		return "", nil, err
	}
	var token struct {
		Token string `json:"token"`
	}
	if err := oauthconn.Do(ctx, http.MethodPost, apiBase(inst)+"/app/installations/"+url.PathEscape(cred.InstallationID)+"/access_tokens", auth, nil, &token); err != nil {
		return "", nil, err
	}
	revoke := func() {
		_ = oauthconn.Do(context.WithoutCancel(ctx), http.MethodDelete, apiBase(inst)+"/installation/token", "Bearer "+token.Token, nil, nil)
	}
	return token.Token, revoke, nil
}

func repoPath(fullName string) (string, error) {
	parts := strings.Split(fullName, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("github: %q is not owner/name: %w", fullName, provider.ErrRepositoryNotFound)
	}
	return url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]), nil
}

// ProbeRepositoryGrant reports whether the installation was granted
// fullName and whether its permissions let a Run push and open pull
// requests. GitHub shows an installation token only the repositories the
// installation was granted, so a repository it does not show is not granted.
func (c Connector) ProbeRepositoryGrant(ctx context.Context, inst provider.ConnectionInstance, cred provider.ConnectionCredential, fullName string) (provider.RepositoryGrant, error) {
	path, err := repoPath(fullName)
	if err != nil {
		return provider.RepositoryGrant{}, err
	}
	auth, err := appAuth(cred.AppID, cred.PrivateKey)
	if err != nil {
		return provider.RepositoryGrant{}, err
	}
	var installation struct {
		Permissions map[string]string `json:"permissions"`
	}
	if err := oauthconn.GetJSON(ctx, apiBase(inst)+"/app/installations/"+url.PathEscape(cred.InstallationID), auth, &installation); err != nil {
		return provider.RepositoryGrant{}, err
	}
	token, revoke, err := c.installationToken(ctx, inst, cred)
	if err != nil {
		return provider.RepositoryGrant{}, err
	}
	defer revoke()
	var repo struct {
		ID            int64  `json:"id"`
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
	}
	found, err := oauthconn.Lookup(ctx, apiBase(inst)+"/repos/"+path, "Bearer "+token, &repo)
	if err != nil {
		return provider.RepositoryGrant{}, err
	}
	if !found {
		return provider.RepositoryGrant{}, fmt.Errorf("github: the installation does not reach %s: %w", fullName, provider.ErrRepositoryNotGranted)
	}
	perms := make([]string, 0, len(installation.Permissions))
	for k, v := range installation.Permissions {
		perms = append(perms, k+":"+v)
	}
	sort.Strings(perms)
	canWrite := installation.Permissions["contents"] == "write" && installation.Permissions["pull_requests"] == "write"
	return provider.RepositoryGrant{ID: strconv.FormatInt(repo.ID, 10), FullName: repo.FullName, CanWrite: canWrite,
		Permissions: perms, DefaultBranch: repo.DefaultBranch}, nil
}

// InspectGrantedRepository reads whether fullName is archived or a mirror
// and whether AGENTS.md is on branch, through an installation token.
func (c Connector) InspectGrantedRepository(ctx context.Context, inst provider.ConnectionInstance, cred provider.ConnectionCredential, fullName, branch string) (provider.RepositoryState, error) {
	path, err := repoPath(fullName)
	if err != nil {
		return provider.RepositoryState{}, err
	}
	token, revoke, err := c.installationToken(ctx, inst, cred)
	if err != nil {
		return provider.RepositoryState{}, err
	}
	defer revoke()
	var repo struct {
		Archived      bool    `json:"archived"`
		MirrorURL     *string `json:"mirror_url"`
		DefaultBranch string  `json:"default_branch"`
	}
	found, err := oauthconn.Lookup(ctx, apiBase(inst)+"/repos/"+path, "Bearer "+token, &repo)
	if err != nil {
		return provider.RepositoryState{}, err
	}
	if !found {
		return provider.RepositoryState{}, fmt.Errorf("github: %s: %w", fullName, provider.ErrRepositoryNotGranted)
	}
	state := provider.RepositoryState{Archived: repo.Archived, Mirror: repo.MirrorURL != nil && *repo.MirrorURL != "", Branch: branch}
	if state.Branch == "" {
		state.Branch = repo.DefaultBranch
	}
	state.AgentsFile, err = oauthconn.Lookup(ctx, apiBase(inst)+"/repos/"+path+"/contents/AGENTS.md?ref="+url.QueryEscape(state.Branch), "Bearer "+token, nil)
	return state, err
}

// ConnectionAPIURL is the REST API root of a runtime GitHub connection's
// instance, for minting its per-Run tokens.
func ConnectionAPIURL(inst provider.ConnectionInstance) string { return apiBase(inst) }
