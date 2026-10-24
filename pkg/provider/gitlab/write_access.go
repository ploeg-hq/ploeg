package gitlab

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

const developerAccess = 30

// HasWriteAccess reads the login's membership of repo, inherited ones
// included, and counts Developer access or more as write access. GitLab's
// member search is fuzzy, so only an exact username match counts.
func (p *Provider) HasWriteAccess(ctx context.Context, repo, login string) (bool, error) {
	if err := validRepo(repo); err != nil {
		return false, err
	}
	target := fmt.Sprintf("%s/api/v4/projects/%s/members/all?query=%s&per_page=100", strings.TrimRight(p.BaseURL, "/"),
		url.PathEscape(repo), url.QueryEscape(login))
	var members []struct {
		Username    string `json:"username"`
		AccessLevel int    `json:"access_level"`
		State       string `json:"state"`
	}
	if err := p.getJSON(ctx, target, &members); err != nil {
		return false, fmt.Errorf("gitlab: members of %s matching %s: %w", repo, login, err)
	}
	for _, m := range members {
		if strings.EqualFold(m.Username, login) && m.AccessLevel >= developerAccess && (m.State == "" || m.State == "active") {
			return true, nil
		}
	}
	return false, nil
}
