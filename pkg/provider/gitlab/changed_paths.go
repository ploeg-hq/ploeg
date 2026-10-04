package gitlab

import (
	"context"
	"fmt"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// ChangedPaths lists the paths a merge request changes from
// GET /projects/:id/merge_requests/:iid/diffs, page by page up to
// provider.MaxChangedPaths entries (VIK-1698).
func (p *Provider) ChangedPaths(ctx context.Context, repo string, mr int) (provider.ChangedPaths, error) {
	base, err := p.mergeRequestBase(repo, mr)
	if err != nil {
		return provider.ChangedPaths{}, err
	}
	out := provider.ChangedPaths{Paths: []provider.ChangedPath{}}
	for page := 1; ; page++ {
		var diffs []struct {
			NewPath     string `json:"new_path"`
			OldPath     string `json:"old_path"`
			NewFile     bool   `json:"new_file"`
			RenamedFile bool   `json:"renamed_file"`
			DeletedFile bool   `json:"deleted_file"`
		}
		if err := p.getJSON(ctx, fmt.Sprintf("%s/diffs?page=%d&per_page=%d", base, page, changePageSize), &diffs); err != nil {
			return provider.ChangedPaths{}, fmt.Errorf("gitlab: diffs of %s!%d: %w", repo, mr, err)
		}
		for _, d := range diffs {
			path := provider.ChangedPath{Path: d.NewPath, Status: provider.PathModified}
			switch {
			case d.NewFile:
				path.Status = provider.PathAdded
			case d.DeletedFile:
				path.Status = provider.PathDeleted
				if d.OldPath != "" {
					path.Path = d.OldPath
				}
			case d.RenamedFile:
				path.Status, path.PreviousPath = provider.PathRenamed, d.OldPath
			}
			if !provider.AppendChangedPath(&out, path) {
				return out, nil
			}
		}
		if len(diffs) < changePageSize {
			return out, nil
		}
	}
}
