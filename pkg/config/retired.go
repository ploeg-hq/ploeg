package config

import (
	"fmt"

	"go.yaml.in/yaml/v3"
)

// RetiredKeys lists, in file order, every configuration key this file sets
// that no longer does anything (ADR-0080): the Run card keys `cards`, a
// target's or project's `cardStyle`, `release`, `rarity` and `cardShape`, a
// team's `cards` and `workingHours`, and a project's `statusKinds`. They are
// accepted for one release so an upgrade does not fail to boot, and the
// next release refuses them as unknown keys.
func (f *File) RetiredKeys() []string {
	var out []string
	add := func(path string, n yaml.Node) {
		if n.Kind != 0 {
			out = append(out, path)
		}
	}
	add("cards", f.Cards)
	for _, key := range sortedTargetKeys(f.Targets) {
		t := f.Targets[key]
		at := "targets." + key
		add(at+".cardStyle", t.CardStyle)
		add(at+".release", t.Release)
		add(at+".rarity", t.Rarity)
		add(at+".cardShape", t.CardShape)
	}
	for _, tr := range f.trackerProjects() {
		for i, p := range tr.projects {
			at := fmt.Sprintf("trackers.%s.projects[%d]", tr.provider, i)
			add(at+".cardStyle", p.CardStyle)
			add(at+".release", p.Release)
			add(at+".rarity", p.Rarity)
			add(at+".cardShape", p.CardShape)
			add(at+".statusKinds", p.StatusKinds)
		}
	}
	for _, name := range sortedTeamNames(f.Teams) {
		t := f.Teams[name]
		add("teams."+name+".cards", t.Cards)
		add("teams."+name+".workingHours", t.WorkingHours)
	}
	return out
}
