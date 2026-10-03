package clickup

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// Parents reads the task's parent from the parent field of GET /task/{id},
// and the parent's name from GET /task/{parent} (ADR-0053). A parent whose
// name cannot be read is still reported, without a title.
func (p *Provider) Parents(ctx context.Context, externalID string) ([]provider.TrackerParent, error) {
	if !p.configured() {
		return nil, errors.New("clickup: no API credentials configured; cannot read relations")
	}
	var t struct {
		ID     string  `json:"id"`
		Parent *string `json:"parent"`
	}
	if err := p.do(ctx, http.MethodGet, "/task/"+url.PathEscape(externalID), nil, &t); err != nil {
		return nil, err
	}
	if t.ID == "" {
		return nil, fmt.Errorf("clickup: task %s not found", externalID)
	}
	if t.Parent == nil || *t.Parent == "" || *t.Parent == t.ID {
		return []provider.TrackerParent{}, nil
	}
	parent := provider.TrackerParent{ExternalID: *t.Parent}
	var named struct {
		Name string `json:"name"`
	}
	if err := p.do(ctx, http.MethodGet, "/task/"+url.PathEscape(*t.Parent), nil, &named); err != nil {
		p.log().Warn("clickup parent name not read", "task", externalID, "parent", *t.Parent, "err", err)
	} else {
		parent.Title = named.Name
	}
	return []provider.TrackerParent{parent}, nil
}
