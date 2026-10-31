package clickup

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// Dependencies reads the tasks a task is waiting on from the dependencies
// field of GET /task/{id}: an entry whose task_id is this task names the
// task it depends_on. Entries where another task waits on this one are not
// dependencies of it. Each waited-on task is read once more for its name and
// whether its status is closed or done (ADR-0123).
func (p *Provider) Dependencies(ctx context.Context, externalID string) ([]provider.TrackerDependency, error) {
	if !p.configured() {
		return nil, errors.New("clickup: no API credentials configured; cannot read dependencies")
	}
	var t struct {
		ID           string `json:"id"`
		Dependencies []struct {
			TaskID    string `json:"task_id"`
			DependsOn string `json:"depends_on"`
		} `json:"dependencies"`
	}
	if err := p.do(ctx, http.MethodGet, "/task/"+url.PathEscape(externalID), nil, &t); err != nil {
		return nil, err
	}
	if t.ID == "" {
		return nil, fmt.Errorf("clickup: task %s not found", externalID)
	}
	out := []provider.TrackerDependency{}
	seen := map[string]bool{}
	for _, d := range t.Dependencies {
		if d.TaskID != t.ID || d.DependsOn == "" || d.DependsOn == t.ID || seen[d.DependsOn] || len(out) == provider.MaxTrackerDependencies {
			continue
		}
		seen[d.DependsOn] = true
		other, err := p.readTask(ctx, d.DependsOn)
		if err != nil {
			return nil, fmt.Errorf("clickup: read dependency %s of %s: %w", d.DependsOn, externalID, err)
		}
		out = append(out, provider.TrackerDependency{ExternalID: other.ID, Title: other.Name, Kind: provider.DependencyBlockedBy, Done: other.closed()})
	}
	return out, nil
}
