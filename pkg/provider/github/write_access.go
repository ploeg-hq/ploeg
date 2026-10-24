package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/githubapi"
)

// HasWriteAccess reads the login's permission on repo from GitHub's
// collaborator permission endpoint, where maintain reads as write and triage
// as read. A login GitHub does not know has none.
func (p *Provider) HasWriteAccess(ctx context.Context, repo, login string) (bool, error) {
	base, owner, name, err := p.repoBase(repo)
	if err != nil {
		return false, err
	}
	var body struct {
		Permission string `json:"permission"`
	}
	err = p.call(ctx, http.MethodGet, base+"/collaborators/"+url.PathEscape(login)+"/permission", owner, name, nil, &body)
	if githubapi.StatusOf(err) == http.StatusNotFound {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("github: permission of %s on %s: %w", login, repo, err)
	}
	switch strings.ToLower(body.Permission) {
	case "write", "admin":
		return true, nil
	}
	return false, nil
}
