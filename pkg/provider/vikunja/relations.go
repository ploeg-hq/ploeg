package vikunja

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

const maxParents = 20

// Parents reads the task's parent tasks from the "parenttask" relations of
// GET /tasks/{id} (ADR-0053).
func (p *Provider) Parents(ctx context.Context, externalID string) ([]provider.TrackerParent, error) {
	if !p.configured() {
		return nil, errors.New("vikunja: no API credentials configured; cannot read relations")
	}
	var task struct {
		ID           int64 `json:"id"`
		RelatedTasks map[string][]struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
		} `json:"related_tasks"`
	}
	if err := p.do(ctx, http.MethodGet, "/tasks/"+url.PathEscape(externalID), nil, &task); err != nil {
		return nil, err
	}
	if task.ID == 0 {
		return nil, fmt.Errorf("vikunja: task %s not found", externalID)
	}
	out := []provider.TrackerParent{}
	for _, parent := range task.RelatedTasks["parenttask"] {
		if parent.ID <= 0 || len(out) == maxParents {
			continue
		}
		out = append(out, provider.TrackerParent{ExternalID: fmt.Sprint(parent.ID), Title: parent.Title})
	}
	return out, nil
}
