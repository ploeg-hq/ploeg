package httpapi

import (
	"github.com/ploeg-hq/ploeg/pkg/store"
)

// RunSelections resolves what each Role may run and call from the chart's
// Harness configuration and the managed inference policies (ADR-0098,
// ADR-0103). The Shift engine stores the result on every pending Run.
type RunSelections struct {
	Harnesses TeamHarnesses
	LLM       *LLMControl
	// Runtime is the Harness a runtime Team's Role runs on (ADR-0198);
	// its Runs store it, so only that Harness's pool claims them.
	Runtime func(team, role string) (string, bool)
}

// RoleSelection is the Harness keys an admission may give the Role, the
// models its policy lists, and both defaults.
func (r RunSelections) RoleSelection(team, role string) store.RoleSelection {
	var sel store.RoleSelection
	if r.Runtime != nil {
		if key, ok := r.Runtime(team, role); ok {
			sel.DefaultHarness, sel.Harnesses = key, []string{key}
			r.addModels(&sel, team, role)
			return sel
		}
	}
	if h, ok := r.Harnesses[team][role]; ok {
		sel.DefaultHarness = h.DefaultKey()
		switch {
		case h.RoutineCarried():
			sel.RoutineCarried = []string{h.DefaultKey()}
		case h.ClientCarried():
			sel.ClientCarried = []string{h.DefaultKey()}
		}
		for _, a := range h.Allowlist() {
			if key := harnessKeyOf(a.OperatorRoleHarness); h.Admits(key) {
				sel.Harnesses = append(sel.Harnesses, key)
			}
		}
	}
	r.addModels(&sel, team, role)
	return sel
}

func (r RunSelections) addModels(sel *store.RoleSelection, team, role string) {
	if r.LLM != nil {
		if p, ok := r.LLM.Policy(team, role); ok {
			sel.Models = append([]string{}, p.Models...)
			sel.DefaultModel = p.Default()
		}
	}
}
