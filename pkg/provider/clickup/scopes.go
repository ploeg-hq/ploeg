package clickup

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// ClickUp's container kinds, from the Workspace down to the List a task
// lives in. Only a List is a routing Scope.
const (
	KindWorkspace = "workspace"
	KindSpace     = "space"
	KindFolder    = "folder"
	KindList      = "list"
)

type namedContainer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListScopes lists one level of the Workspace, Space, Folder and List
// hierarchy the token reaches (ADR-0196). A zero parent lists Workspaces; a
// Space lists its Folders and then its Lists outside any Folder.
func (p *Provider) ListScopes(ctx context.Context, parent provider.Scope) ([]provider.ScopeNode, error) {
	if !p.configured() {
		return nil, errors.New("clickup: no API credentials configured")
	}
	switch parent.Kind {
	case "":
		var out struct {
			Teams []namedContainer `json:"teams"`
		}
		if err := p.do(ctx, http.MethodGet, "/team", nil, &out); err != nil {
			return nil, err
		}
		return containers(KindWorkspace, out.Teams, false, true), nil
	case KindWorkspace:
		var out struct {
			Spaces []namedContainer `json:"spaces"`
		}
		if err := p.do(ctx, http.MethodGet, "/team/"+url.PathEscape(parent.ID)+"/space?archived=false", nil, &out); err != nil {
			return nil, err
		}
		return containers(KindSpace, out.Spaces, false, true), nil
	case KindSpace:
		var folders struct {
			Folders []namedContainer `json:"folders"`
		}
		if err := p.do(ctx, http.MethodGet, "/space/"+url.PathEscape(parent.ID)+"/folder?archived=false", nil, &folders); err != nil {
			return nil, err
		}
		var lists struct {
			Lists []namedContainer `json:"lists"`
		}
		if err := p.do(ctx, http.MethodGet, "/space/"+url.PathEscape(parent.ID)+"/list?archived=false", nil, &lists); err != nil {
			return nil, err
		}
		nodes := append(containers(KindFolder, folders.Folders, false, true), containers(KindList, lists.Lists, true, false)...)
		return nodes[:min(len(nodes), provider.MaxScopeChildren)], nil
	case KindFolder:
		var out struct {
			Lists []namedContainer `json:"lists"`
		}
		if err := p.do(ctx, http.MethodGet, "/folder/"+url.PathEscape(parent.ID)+"/list?archived=false", nil, &out); err != nil {
			return nil, err
		}
		return containers(KindList, out.Lists, true, false), nil
	case KindList:
		return []provider.ScopeNode{}, nil
	}
	return nil, provider.NotFound(fmt.Errorf("clickup: %q is not a container kind", parent.Kind))
}

func containers(kind string, in []namedContainer, bindable, browsable bool) []provider.ScopeNode {
	out := make([]provider.ScopeNode, 0, min(len(in), provider.MaxScopeChildren))
	for _, c := range in {
		if c.ID == "" {
			continue
		}
		if len(out) == provider.MaxScopeChildren {
			break
		}
		out = append(out, provider.ScopeNode{Scope: provider.Scope{Kind: kind, ID: c.ID, Name: c.Name}, Bindable: bindable, Browsable: browsable})
	}
	return out
}

// ListOpenItems reads one page of a List's open tasks, subtasks included,
// in ClickUp's page size of 100 (ADR-0196).
func (p *Provider) ListOpenItems(ctx context.Context, scopeID string, page int) (provider.ScopeItemPage, error) {
	if !p.configured() {
		return provider.ScopeItemPage{}, errors.New("clickup: no API credentials configured")
	}
	if page < 1 {
		page = 1
	}
	var out struct {
		Tasks    []task `json:"tasks"`
		LastPage *bool  `json:"last_page"`
	}
	path := "/list/" + url.PathEscape(scopeID) + "/task?archived=false&include_closed=false&subtasks=true&page=" + strconv.Itoa(page-1)
	if err := p.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return provider.ScopeItemPage{}, err
	}
	result := provider.ScopeItemPage{Items: make([]provider.ScopeItem, 0, len(out.Tasks))}
	for _, t := range out.Tasks {
		if t.ID == "" || t.Archived || t.closed() {
			continue
		}
		link := t.URL
		if link == "" {
			link = p.TaskURL(t.ID)
		}
		result.Items = append(result.Items, provider.ScopeItem{ID: t.ID, Title: t.Name, Status: t.Status.Status, URL: link, UpdatedAt: unixMillis(t.DateUpdated)})
	}
	if out.LastPage != nil && !*out.LastPage && len(out.Tasks) > 0 {
		result.NextPage = page + 1
	}
	return result, nil
}
