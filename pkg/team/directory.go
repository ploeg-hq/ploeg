package team

import (
	"slices"
	"strings"
	"sync"
)

// Team is one Team as the directory knows it.
type Team struct {
	ID         string
	Key        string
	Aliases    []string
	Source     string
	State      string
	Name       string
	NameSource NameSource
	Revision   int64
}

// Resolution is how a reference reached a Team.
type Resolution int

// Ways a reference resolves.
const (
	ByID Resolution = iota + 1
	ByKey
	ByAlias
)

// Directory resolves an id, a current key or an alias to a Team. It is
// swapped whole after the reconciler or a change, and safe for concurrent
// use.
type Directory struct {
	mu    sync.RWMutex
	byID  map[string]Team
	byRef map[string]string
	order []string
}

// NewDirectory returns a Directory holding teams.
func NewDirectory(teams []Team) *Directory {
	d := &Directory{}
	d.Set(teams)
	return d
}

// Set replaces every Team the directory holds.
func (d *Directory) Set(teams []Team) {
	byID := make(map[string]Team, len(teams))
	byRef := make(map[string]string, len(teams))
	order := make([]string, 0, len(teams))
	for _, t := range teams {
		t.Aliases = slices.Clone(t.Aliases)
		byID[t.ID] = t
		order = append(order, t.ID)
	}
	for _, staticFirst := range []bool{true, false} {
		for _, t := range teams {
			if (t.Source == SourceStatic) != staticFirst {
				continue
			}
			if _, taken := byRef[strings.ToLower(t.Key)]; !taken {
				byRef[strings.ToLower(t.Key)] = t.ID
			}
		}
	}
	for _, t := range teams {
		for _, a := range t.Aliases {
			if _, taken := byRef[strings.ToLower(a)]; !taken {
				byRef[strings.ToLower(a)] = t.ID
			}
		}
	}
	slices.SortFunc(order, func(a, b string) int { return strings.Compare(byID[a].Key, byID[b].Key) })
	d.mu.Lock()
	d.byID, d.byRef, d.order = byID, byRef, order
	d.mu.Unlock()
}

// Resolve finds the Team ref names: an id, a current key or an alias.
func (d *Directory) Resolve(ref string) (Team, Resolution, bool) {
	if d == nil || ref == "" {
		return Team{}, 0, false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if t, ok := d.byID[ref]; ok {
		return cloneTeam(t), ByID, true
	}
	id, ok := d.byRef[strings.ToLower(ref)]
	if !ok {
		return Team{}, 0, false
	}
	t := d.byID[id]
	if strings.EqualFold(t.Key, ref) {
		return cloneTeam(t), ByKey, true
	}
	return cloneTeam(t), ByAlias, true
}

// StaticKeys maps every static Team's lowercased key to its id.
func (d *Directory) StaticKeys() map[string]string {
	out := map[string]string{}
	for _, t := range d.All() {
		if t.Source == SourceStatic {
			out[strings.ToLower(t.Key)] = t.ID
		}
	}
	return out
}

// ID is the id of the Team ref names, or "" when none does.
func (d *Directory) ID(ref string) string {
	t, _, ok := d.Resolve(ref)
	if !ok {
		return ""
	}
	return t.ID
}

// All returns every Team, sorted by key.
func (d *Directory) All() []Team {
	if d == nil {
		return nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]Team, 0, len(d.order))
	for _, id := range d.order {
		out = append(out, cloneTeam(d.byID[id]))
	}
	return out
}

func cloneTeam(t Team) Team {
	t.Aliases = slices.Clone(t.Aliases)
	return t
}
