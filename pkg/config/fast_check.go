package config

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// FastCheck is the fast check one repository on one forge instance declares
// (ADR-0146). Forge is empty for the deployment's default forge.
type FastCheck struct {
	Forge    string
	Repo     string
	Commands []string
}

// FastChecks lists every repository route that declares a fastCheck, sorted
// by forge and repository. Validate has already refused a repository two
// routes give different commands.
func (f *File) FastChecks() []FastCheck {
	seen := map[[2]string]FastCheck{}
	for _, r := range f.ciRoutes() {
		if len(r.fastCheck) == 0 || r.repo == "" {
			continue
		}
		seen[ciRouteKey(r)] = FastCheck{Forge: r.forge, Repo: r.repo, Commands: slices.Clone(r.fastCheck)}
	}
	out := make([]FastCheck, 0, len(seen))
	for _, c := range seen {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Forge != out[j].Forge {
			return out[i].Forge < out[j].Forge
		}
		return out[i].Repo < out[j].Repo
	})
	return out
}

func (f *File) validateFastChecks() error {
	set := map[[2]string]ciRoute{}
	for _, r := range f.ciRoutes() {
		if len(r.fastCheck) == 0 {
			continue
		}
		if r.repo == "" {
			return fmt.Errorf("%s.fastCheck: needs the route's repo", r.where)
		}
		for i, c := range r.fastCheck {
			if strings.TrimSpace(c) == "" {
				return fmt.Errorf("%s.fastCheck[%d] is empty", r.where, i)
			}
			if strings.ContainsAny(c, "\n\r") {
				return fmt.Errorf("%s.fastCheck[%d] spans more than one line", r.where, i)
			}
		}
		key := ciRouteKey(r)
		if prev, dup := set[key]; dup && !slices.Equal(prev.fastCheck, r.fastCheck) {
			return fmt.Errorf("%s.fastCheck: %s declares a different fast check at %s", r.where, r.repo, prev.where)
		}
		set[key] = r
	}
	return nil
}
