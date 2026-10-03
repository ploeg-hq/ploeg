package config

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/flow"
	"github.com/ploeg-hq/ploeg/pkg/gate"
)

type projectResolver struct {
	r      ScopeResolver
	byName map[string]string
}

func (pr *projectResolver) id(ctx context.Context, p Project, purpose string) (string, error) {
	if p.ID != "" {
		return p.ID, nil
	}
	if pr.byName == nil {
		if pr.r == nil {
			return "", fmt.Errorf("%s name project %q but no tracker client is configured to resolve it; set the tracker URL and token, or pin its id", purpose, p.Name)
		}
		byName, err := pr.r.ProjectsByName(ctx)
		if err != nil {
			return "", fmt.Errorf("resolving project names for %s: %w", purpose, err)
		}
		pr.byName = byName
	}
	id, ok := pr.byName[p.Name]
	if !ok {
		return "", fmt.Errorf("no tracker project named %q; available: %s", p.Name, strings.Join(sortedKeys(pr.byName), ", "))
	}
	return id, nil
}

func (f *File) validateStatusKinds() error {
	for _, tr := range f.trackerProjects() {
		where := map[string]string{}
		mapped := map[string]flow.Kinds{}
		for i, p := range tr.projects {
			if p.StatusKinds == nil {
				continue
			}
			at := fmt.Sprintf("trackers.%s.projects[%d]", tr.provider, i)
			if _, err := flow.NewKindMap(*p.StatusKinds); err != nil {
				return fmt.Errorf("%s.statusKinds: %w", at, err)
			}
			if prev, dup := mapped[p.label()]; dup && !reflect.DeepEqual(prev, *p.StatusKinds) {
				return fmt.Errorf("%s.statusKinds: project %q sets its status kinds differently at %s", at, p.label(), where[p.label()])
			}
			mapped[p.label()], where[p.label()] = *p.StatusKinds, at
		}
	}
	return nil
}

type trackerProjects struct {
	provider string
	projects []Project
}

func (f *File) trackerProjects() []trackerProjects {
	return []trackerProjects{{"vikunja", f.Trackers.Vikunja.Projects}, {"clickup", f.Trackers.Clickup.Projects}}
}

// FlowBoards resolves every project with gates or statusKinds to its
// tracker id and returns the status kinds of each board (ADR-0057). These
// are the boards whose status moves Ploeg records. A board without
// statusKinds gets an empty kind map, which applies flow.DefaultKind.
// Named projects resolve as in GateBoards.
func (f *File) FlowBoards(ctx context.Context, r ScopeResolver, log *slog.Logger) (flow.Boards, error) {
	boards := flow.Boards{}
	pr := &projectResolver{r: r}
	for _, tr := range f.trackerProjects() {
		for _, p := range tr.projects {
			if p.Gates == nil && p.StatusKinds == nil {
				continue
			}
			var kinds flow.Kinds
			if p.StatusKinds != nil {
				kinds = *p.StatusKinds
			}
			m, err := flow.NewKindMap(kinds)
			if err != nil {
				return nil, fmt.Errorf("project %q statusKinds: %w", p.label(), err)
			}
			id, err := pr.id(ctx, p, "statusKinds")
			if err != nil {
				return nil, err
			}
			if boards[tr.provider] == nil {
				boards[tr.provider] = map[string]flow.KindMap{}
			}
			if _, seen := boards[tr.provider][id]; seen && p.StatusKinds == nil {
				continue
			}
			boards[tr.provider][id] = m
			log.Info("status moves recorded", "provider", tr.provider, "project", p.label(), "id", id, "status_kinds", p.StatusKinds != nil)
		}
	}
	return boards, nil
}

// WorkingCalendars returns the working calendar of every Team that sets
// workingHours (ADR-0057). A Team absent from the result counts working
// time in flow.DefaultCalendar.
func (f *File) WorkingCalendars() (map[string]flow.Calendar, error) {
	out := map[string]flow.Calendar{}
	for _, name := range sortedTeamNames(f.Teams) {
		hours := f.Teams[name].WorkingHours
		if hours == nil {
			continue
		}
		c, err := flow.NewCalendar(*hours)
		if err != nil {
			return nil, fmt.Errorf("teams.%s.workingHours: %w", name, err)
		}
		out[name] = c
	}
	return out, nil
}

func gateMap(p Project) (gate.Map, error) {
	m, err := gate.NewMap(*p.Gates)
	if err != nil {
		return gate.Map{}, fmt.Errorf("project %q gates: %w", p.label(), err)
	}
	return m, nil
}
