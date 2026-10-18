package knowledge

import (
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/okf"
)

// ADR-0124: each level is selected under its own budget, narrowest first, so
// wide knowledge cannot crowd out the Work Item's notes; notes come newest
// first and every entry cites what it was read from.
func TestSelectWithin_GivesEachLevelItsOwnBudgetNarrowestFirst(t *testing.T) {
	old := concept("old.md", "Fact", "Old note", "", nil, "store migration")
	old.Timestamp, old.Extra = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), map[string]any{"note": "ploeg-a/1"}
	fresh := concept("fresh.md", "Fact", "Fresh note", "", nil, "store migration")
	fresh.Timestamp, fresh.Extra = time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC), map[string]any{"note": "ploeg-b/2"}
	wide := okf.Bundle{Name: "graph"}
	for _, p := range []string{"tenant/a.md", "tenant/b.md", "tenant/c.md"} {
		wide.Concepts = append(wide.Concepts, concept(p, "Convention", "Store migration "+p, "store migration postgres", nil, strings.Repeat("store migration ", 40)))
	}
	sources := []Source{
		{Name: "graph", Bundle: wide, LevelFromPath: true, Cite: "graph commit c9"},
		{Name: "notes", Bundle: okf.Bundle{Name: "notes", Concepts: []okf.Concept{old, fresh}}, Unreviewed: true, Level: LevelWorkItem},
	}
	tenantBudget := len(okf.Format(wide.Concepts[0]))
	p := SelectWithin(sources, Query{Title: "store migration postgres"}, Budgets{LevelWorkItem: 10000, LevelTenant: tenantBudget})
	if len(p.Selected) != 3 || p.Selected[0].Concept.Path != "fresh.md" || p.Selected[1].Concept.Path != "old.md" || p.Selected[2].Level != LevelTenant {
		t.Fatalf("selected %v", paths(p))
	}
	if len(p.Sections) != 2 || p.Sections[1].Omitted != 2 || p.Omitted != 2 {
		t.Fatalf("sections %+v omitted %d", p.Sections, p.Omitted)
	}
	index := p.Index()
	for _, want := range []string{"## This Work Item", "## The Tenant", "note ploeg-b/2", "graph commit c9"} {
		if !strings.Contains(index, want) {
			t.Errorf("index lacks %q:\n%s", want, index)
		}
	}
	if strings.Index(index, "This Work Item") > strings.Index(index, "The Tenant") {
		t.Error("the Tenant is listed before the Work Item")
	}
	if b := DefaultBudgets(24000); b[LevelWorkItem] != 9000 || b[LevelRepository] != 9000 || b[LevelClient] != 3000 || b[LevelTenant] != 3000 {
		t.Errorf("default budgets %v", b)
	}
}

// ADR-0122 with ADR-0124: a note is selected under the budget of the level
// it is read at, so a repository note never spends the Work Item's budget.
func TestSelectWithin_RoutesANoteByItsLevel(t *testing.T) {
	repo := concept("repo-note.md", "Convention", "Repository note", "", nil, "store migration")
	repo.Extra = map[string]any{"note": "person/1", "level": LevelRepository}
	own := concept("own-note.md", "Fact", "Own note", "", nil, "store migration")
	own.Extra = map[string]any{"note": "ploeg-a/1", "level": LevelWorkItem}
	p := SelectWithin([]Source{{Name: "notes", Bundle: okf.Bundle{Name: "notes", Concepts: []okf.Concept{repo, own}}, Unreviewed: true, Level: LevelWorkItem}},
		Query{Title: "x"}, DefaultBudgets(0))
	levels := map[string]string{}
	for _, s := range p.Selected {
		levels[s.Concept.Path] = s.Level
	}
	if levels["repo-note.md"] != LevelRepository || levels["own-note.md"] != LevelWorkItem {
		t.Fatalf("levels %v", levels)
	}
	if LevelWorkItem != "work-item" {
		t.Fatalf("the Work Item level is spelled %q, not the notes contract's work-item", LevelWorkItem)
	}
	index := p.Index()
	if !strings.Contains(index, "## This repository") || !strings.Contains(index, "a note on this repository") || strings.Contains(index, "without review") {
		t.Errorf("index:\n%s", index)
	}
}
