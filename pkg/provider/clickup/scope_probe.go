package clickup

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// readOnlyPermissions are the List permission levels that cannot set a
// task's status.
var readOnlyPermissions = []string{"read", "comment"}

// ProbeScope reads one List with the connection's token (ADR-0186): its name
// and statuses through GET /list/{id}, whether the token may change tasks in
// it from the List's permission_level, and who can see it through
// GET /list/{id}/member. ClickUp answers 404 for an unknown List and 401 or
// 403 for one the token cannot reach.
func (p *Provider) ProbeScope(ctx context.Context, listID string) (provider.ScopeProbe, error) {
	if !p.configured() {
		return provider.ScopeProbe{}, errors.New("clickup: no API credentials configured; cannot probe lists")
	}
	probe := provider.ScopeProbe{ID: listID}
	list, err := p.readList(ctx, listID)
	var status *apiError
	switch {
	case errors.As(err, &status) && status.status == http.StatusNotFound:
		return probe, provider.ErrScopeNotFound
	case errors.As(err, &status) && (status.status == http.StatusUnauthorized || status.status == http.StatusForbidden):
		return probe, nil
	case err != nil:
		return probe, err
	case list.ID == "":
		return probe, provider.ErrScopeNotFound
	}
	probe.ID, probe.Name, probe.CanRead = string(list.ID), list.Name, true
	probe.CanWrite = !slices.Contains(readOnlyPermissions, strings.ToLower(strings.TrimSpace(list.PermissionLevel)))
	probe.Statuses = make([]string, 0, len(list.Statuses))
	for _, s := range list.Statuses {
		probe.Statuses = append(probe.Statuses, s.Status)
	}
	probe.Members = p.listIdentities(ctx, listID)
	return probe, nil
}

func (p *Provider) listIdentities(ctx context.Context, listID string) []string {
	members, err := p.readMembers(ctx, listID)
	if err != nil {
		return nil
	}
	out := []string{}
	for _, m := range members {
		for _, id := range []string{m.Username, m.Email} {
			if id != "" && !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	return out
}

type namedLocation struct {
	ID   flexID `json:"id"`
	Name string `json:"name"`
}

// ScopeIDByName finds the List of the Workspace that carries name, in every
// Space, folderless or inside a Folder. Two Lists of that name are
// ErrScopeAmbiguous, since guessing would bind the wrong board.
func (p *Provider) ScopeIDByName(ctx context.Context, name string) (string, error) {
	if !p.configured() || p.Workspace == "" {
		return "", errors.New("clickup: resolving a List name needs API credentials and the Workspace id")
	}
	c := p.scopeCache()
	if id, ok := lookup(c, c.names, name); ok {
		return id, nil
	}
	id, err := p.resolveListName(ctx, name)
	if err == nil {
		keep(c, c.names, name, id, nameCacheTTL)
	}
	return id, err
}

func (p *Provider) resolveListName(ctx context.Context, name string) (string, error) {
	var spaces struct {
		Spaces []namedLocation `json:"spaces"`
	}
	if err := p.do(ctx, http.MethodGet, "/team/"+url.PathEscape(p.Workspace)+"/space?archived=false", nil, &spaces); err != nil {
		return "", err
	}
	var found []string
	for _, space := range spaces.Spaces {
		var folderless struct {
			Lists []namedLocation `json:"lists"`
		}
		if err := p.do(ctx, http.MethodGet, "/space/"+url.PathEscape(string(space.ID))+"/list?archived=false", nil, &folderless); err != nil {
			return "", err
		}
		var folders struct {
			Folders []struct {
				Lists []namedLocation `json:"lists"`
			} `json:"folders"`
		}
		if err := p.do(ctx, http.MethodGet, "/space/"+url.PathEscape(string(space.ID))+"/folder?archived=false", nil, &folders); err != nil {
			return "", err
		}
		lists := folderless.Lists
		for _, f := range folders.Folders {
			lists = append(lists, f.Lists...)
		}
		for _, l := range lists {
			if l.Name == name && !slices.Contains(found, string(l.ID)) {
				found = append(found, string(l.ID))
			}
		}
	}
	switch len(found) {
	case 0:
		return "", provider.ErrScopeNotFound
	case 1:
		return found[0], nil
	default:
		return "", fmt.Errorf("clickup: %d Lists are named %q: %w", len(found), name, provider.ErrScopeAmbiguous)
	}
}
