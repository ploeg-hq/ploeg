package vikunja

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// KindProject is Vikunja's one container kind. A project may hold child
// projects, and every project is a routing Scope.
const KindProject = "project"

const (
	projectsPerPage  = 200
	openItemsPerPage = 50
)

type project struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	ParentID   int64  `json:"parent_project_id"`
	IsArchived bool   `json:"is_archived"`
}

// ListScopes lists the projects the token reaches whose parent is parent,
// or the top-level projects for a zero parent (ADR-0196). Vikunja has no
// call for one project's children, so it reads every page of projects, at
// most MaxScopeChildren of them, and keeps one level.
func (p *Provider) ListScopes(ctx context.Context, parent provider.Scope) ([]provider.ScopeNode, error) {
	if !p.configured() {
		return nil, errors.New("vikunja: no API credentials configured")
	}
	var parentID int64
	switch parent.Kind {
	case "":
	case KindProject:
		id, err := strconv.ParseInt(parent.ID, 10, 64)
		if err != nil || id <= 0 {
			return nil, provider.NotFound(fmt.Errorf("vikunja: %q is not a project id", parent.ID))
		}
		parentID = id
	default:
		return nil, provider.NotFound(fmt.Errorf("vikunja: %q is not a container kind", parent.Kind))
	}
	projects, err := p.allProjects(ctx)
	if err != nil {
		return nil, err
	}
	hasChildren := map[int64]bool{}
	known := false
	for _, pr := range projects {
		hasChildren[pr.ParentID] = true
		known = known || pr.ID == parentID
	}
	if parentID != 0 && !known {
		return nil, provider.NotFound(fmt.Errorf("vikunja: project %d not found", parentID))
	}
	out := []provider.ScopeNode{}
	for _, pr := range projects {
		if pr.ParentID != parentID || pr.ID <= 0 {
			continue
		}
		out = append(out, provider.ScopeNode{
			Scope:     provider.Scope{Kind: KindProject, ID: strconv.FormatInt(pr.ID, 10), Name: pr.Title},
			Bindable:  true,
			Browsable: hasChildren[pr.ID],
		})
	}
	return out, nil
}

func (p *Provider) allProjects(ctx context.Context) ([]project, error) {
	var all []project
	for page := 1; len(all) < provider.MaxScopeChildren; page++ {
		var batch []project
		if err := p.do(ctx, http.MethodGet, "/projects?is_archived=false&per_page="+strconv.Itoa(projectsPerPage)+"&page="+strconv.Itoa(page), nil, &batch); err != nil {
			return nil, err
		}
		for _, pr := range batch {
			if !pr.IsArchived {
				all = append(all, pr)
			}
		}
		if len(batch) < projectsPerPage {
			break
		}
	}
	return all[:min(len(all), provider.MaxScopeChildren)], nil
}

// ListOpenItems reads one page of a project's tasks that are not done,
// fifty to a page (ADR-0196). It filters on the server and again here, so
// an instance that ignores the filter still returns only open tasks of
// that project, on a shorter page.
func (p *Provider) ListOpenItems(ctx context.Context, scopeID string, page int) (provider.ScopeItemPage, error) {
	if !p.configured() {
		return provider.ScopeItemPage{}, errors.New("vikunja: no API credentials configured")
	}
	projectID, err := strconv.ParseInt(scopeID, 10, 64)
	if err != nil || projectID <= 0 {
		return provider.ScopeItemPage{}, provider.NotFound(fmt.Errorf("vikunja: %q is not a project id", scopeID))
	}
	if page < 1 {
		page = 1
	}
	query := url.Values{}
	query.Set("filter", fmt.Sprintf("done = false && project = %d", projectID))
	query.Set("per_page", strconv.Itoa(openItemsPerPage))
	query.Set("page", strconv.Itoa(page))
	var tasks []struct {
		ID        int64  `json:"id"`
		Title     string `json:"title"`
		Done      bool   `json:"done"`
		ProjectID int64  `json:"project_id"`
		Updated   string `json:"updated"`
		Bucket    *struct {
			Title string `json:"title"`
		} `json:"bucket"`
	}
	if err := p.do(ctx, http.MethodGet, "/tasks/all?"+query.Encode(), nil, &tasks); err != nil {
		return provider.ScopeItemPage{}, err
	}
	result := provider.ScopeItemPage{Items: []provider.ScopeItem{}}
	for _, t := range tasks {
		if t.ID <= 0 || t.Done || t.ProjectID != projectID {
			continue
		}
		id := strconv.FormatInt(t.ID, 10)
		status := ""
		if t.Bucket != nil {
			status = t.Bucket.Title
		}
		result.Items = append(result.Items, provider.ScopeItem{ID: id, Title: t.Title, Status: status, URL: p.TaskURL(id), UpdatedAt: createdAt(t.Updated)})
	}
	if len(tasks) == openItemsPerPage {
		result.NextPage = page + 1
	}
	return result, nil
}
