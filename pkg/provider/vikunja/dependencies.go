package vikunja

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

var dependencyKinds = []struct {
	relation string
	kind     provider.DependencyKind
}{
	{"blocked", provider.DependencyBlockedBy},
	{"follows", provider.DependencyFollows},
}

// Dependencies reads the tasks a task waits for from the "blocked" and
// "follows" relations of GET /tasks/{id}, with each one's done flag
// (ADR-0123).
func (p *Provider) Dependencies(ctx context.Context, externalID string) ([]provider.TrackerDependency, error) {
	if !p.configured() {
		return nil, errors.New("vikunja: no API credentials configured; cannot read relations")
	}
	var task struct {
		ID           int64 `json:"id"`
		RelatedTasks map[string][]struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
			Done  bool   `json:"done"`
		} `json:"related_tasks"`
	}
	if err := p.do(ctx, http.MethodGet, "/tasks/"+url.PathEscape(externalID), nil, &task); err != nil {
		return nil, err
	}
	if task.ID == 0 {
		return nil, fmt.Errorf("vikunja: task %s not found", externalID)
	}
	out := []provider.TrackerDependency{}
	seen := map[int64]bool{}
	for _, k := range dependencyKinds {
		for _, other := range task.RelatedTasks[k.relation] {
			if other.ID <= 0 || other.ID == task.ID || seen[other.ID] || len(out) == provider.MaxTrackerDependencies {
				continue
			}
			seen[other.ID] = true
			out = append(out, provider.TrackerDependency{ExternalID: fmt.Sprint(other.ID), Title: other.Title, Kind: k.kind, Done: other.Done})
		}
	}
	return out, nil
}
