package config

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// CI is how ploegd treats the forge's CI on a route's pull requests
// (ADR-0119).
type CI struct {
	// WaitTimeout is how long a Shift waits for the forge's CI on its pull
	// request's head before a person is asked, as a Go duration such as
	// "60m". Empty = 60 minutes.
	WaitTimeout string `yaml:"waitTimeout"`
}

// CIWait is the CI wait timeout of one repository on one forge instance.
// Forge is empty for the deployment's default forge.
type CIWait struct {
	Forge   string
	Repo    string
	Timeout time.Duration
}

func (c *CI) waitTimeout() (time.Duration, bool, error) {
	if c == nil || strings.TrimSpace(c.WaitTimeout) == "" {
		return 0, false, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(c.WaitTimeout))
	if err != nil {
		return 0, false, fmt.Errorf("ci.waitTimeout %q: %w", c.WaitTimeout, err)
	}
	if d <= 0 {
		return 0, false, fmt.Errorf("ci.waitTimeout %q: must be positive", c.WaitTimeout)
	}
	return d, true, nil
}

type ciRoute struct {
	where string
	forge string
	repo  string
	ci    *CI
}

func (f *File) ciRoutes() []ciRoute {
	var out []ciRoute
	for _, key := range sortedTargetKeys(f.Targets) {
		t := f.Targets[key]
		out = append(out, ciRoute{where: "targets." + key, forge: t.Forge, repo: t.Repo, ci: t.CI})
	}
	for _, tr := range f.trackerProjects() {
		for i, p := range tr.projects {
			out = append(out, ciRoute{where: fmt.Sprintf("trackers.%s.projects[%d]", tr.provider, i), forge: p.Forge, repo: p.Repo, ci: p.CI})
		}
	}
	return out
}

// CIWaits lists every repository route that sets ci.waitTimeout, sorted by
// forge and repository. Validate has already refused a repository two routes
// give different timeouts.
func (f *File) CIWaits() []CIWait {
	seen := map[[2]string]CIWait{}
	for _, r := range f.ciRoutes() {
		d, ok, err := r.ci.waitTimeout()
		if err != nil || !ok || r.repo == "" {
			continue
		}
		seen[ciRouteKey(r)] = CIWait{Forge: r.forge, Repo: r.repo, Timeout: d}
	}
	out := make([]CIWait, 0, len(seen))
	for _, w := range seen {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Forge != out[j].Forge {
			return out[i].Forge < out[j].Forge
		}
		return out[i].Repo < out[j].Repo
	})
	return out
}

func ciRouteKey(r ciRoute) [2]string {
	return [2]string{strings.ToLower(strings.TrimSpace(r.forge)), strings.ToLower(strings.TrimSpace(r.repo))}
}

func (f *File) validateCI() error {
	set := map[[2]string]ciRoute{}
	for _, r := range f.ciRoutes() {
		d, ok, err := r.ci.waitTimeout()
		if err != nil {
			return fmt.Errorf("%s.%w", r.where, err)
		}
		if !ok {
			continue
		}
		if r.repo == "" {
			return fmt.Errorf("%s.ci.waitTimeout: needs the route's repo", r.where)
		}
		key := ciRouteKey(r)
		if prev, dup := set[key]; dup {
			if pd, _, _ := prev.ci.waitTimeout(); pd != d {
				return fmt.Errorf("%s.ci.waitTimeout: %s is %s here and %s at %s", r.where, r.repo, d, pd, prev.where)
			}
		}
		set[key] = r
	}
	return nil
}
