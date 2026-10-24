package forgejo

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// HasWriteAccess reads the login's permission on repo from Forgejo's
// collaborator permission endpoint. Forgejo answers it only to a token that
// administers the repository or to the login itself; any other answer is an
// error, so the caller refuses rather than guesses.
func (p *Provider) HasWriteAccess(ctx context.Context, repo, login string) (bool, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return false, fmt.Errorf("forgejo: repo %q must be owner/name", repo)
	}
	target := fmt.Sprintf("%s/api/v1/repos/%s/%s/collaborators/%s/permission", strings.TrimRight(p.BaseURL, "/"),
		url.PathEscape(owner), url.PathEscape(name), url.PathEscape(login))
	var body struct {
		Permission string `json:"permission"`
	}
	status, err := p.get(ctx, target, &body)
	if err != nil {
		return false, err
	}
	switch status {
	case http.StatusOK:
		return writePermission(body.Permission), nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("forgejo: permission of %s on %s: HTTP %d", login, repo, status)
	}
}

func writePermission(permission string) bool {
	switch strings.ToLower(strings.TrimSpace(permission)) {
	case "write", "admin", "owner":
		return true
	}
	return false
}
