// Package knowledge selects the part of one or more OKF bundles that bears on
// a Work Item and writes it as a knowledge pack: an OKF bundle of its own that
// a Run reads from disk, with an index that says why each concept is there.
// A harness needs no tool to use it: every agent can read files.
package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/ploeg-hq/ploeg/pkg/notes"
	"github.com/ploeg-hq/ploeg/pkg/okf"
)

// DefaultBudgetBytes bounds a pack's concept documents together. The pack is
// supporting evidence; it must not crowd the Work Item out of a context.
const DefaultBudgetBytes = 24000

// PitfallType is the concept type of a recorded way earlier work went wrong.
// A matching pitfall ranks ahead of other concepts with the same score.
const PitfallType = "Pitfall"

// Source is one bundle a pack may draw from.
type Source struct {
	// Name labels the source in the pack and its provenance, for example
	// "repo" for the repository's own bundle.
	Name   string
	Bundle okf.Bundle
	// Unreviewed marks Work Item Notes: what earlier Runs of the same work
	// wrote without review (ADR-0088, proposed). They are about this very
	// work, so every one is offered, newest first, after the reviewed
	// knowledge and under its own heading.
	Unreviewed bool
	// Level is the narrowest scope the source speaks for (LevelWorkItem,
	// LevelRepository, LevelClient or LevelTenant), which picks the budget
	// its concepts are selected under. LevelFromPath takes each concept's
	// level from the first segment of its path instead, as the Tenant's
	// graph names them. Empty shares the budget of Select.
	Level         string
	LevelFromPath bool
	// Cite names the version the source was read at, such as a graph commit
	// or the commit a repository was cloned at; the index cites it per item.
	Cite string
}

// The levels a pack is selected at, narrowest first (ADR-0124). Each has a
// budget of its own, so a wide level cannot crowd out what is known about
// this very work.
const (
	LevelWorkItem   = string(notes.LevelWorkItem)
	LevelRepository = string(notes.LevelRepository)
	LevelClient     = string(notes.LevelClient)
	LevelTenant     = string(notes.LevelTenant)
)

// Levels is the order levels are selected and listed in.
var Levels = []string{LevelWorkItem, LevelRepository, LevelClient, LevelTenant, ""}

// Budgets bounds each level's concept documents in bytes. A level without a
// budget selects nothing.
type Budgets map[string]int

// DefaultBudgets splits total across the levels: three eighths each to the
// Work Item and the repository, an eighth each to the Client and the
// Tenant. Zero is DefaultBudgetBytes.
func DefaultBudgets(total int) Budgets {
	if total <= 0 {
		total = DefaultBudgetBytes
	}
	wide := total / 8
	narrow := (total - 2*wide) / 2
	return Budgets{LevelWorkItem: narrow, LevelRepository: total - 2*wide - narrow, LevelClient: wide, LevelTenant: wide}
}

// Section is what one level of a pack used of its budget.
type Section struct {
	Level   string
	Budget  int
	Bytes   int
	Items   int
	Omitted int
}

// Query is what the pack is selected for.
type Query struct {
	Title       string
	Description string
	// Labels are the Work Item's labels; they match concept tags.
	Labels []string
}

// Selected is one concept in a pack and the reason it is there.
type Selected struct {
	Source  string
	Concept okf.Concept
	Score   float64
	// Reason names the matched terms, or the concept a link was followed
	// from.
	Reason string
	// Unreviewed is set for a Work Item Note.
	Unreviewed bool
	// Level is the level the concept was selected at; Cite names what it
	// was read from.
	Level string
	Cite  string
}

// Pack is the selection for one Query.
type Pack struct {
	Selected []Selected
	// Considered counts the concepts in every source.
	Considered int
	// Omitted counts matching concepts left out by the budget.
	Omitted int
	Bytes   int
	// Digests are each source's content digest, so a Run records exactly
	// which knowledge it was given.
	Digests map[string]string
	// Sections are each level's budget and use, narrowest first; one
	// unnamed section when every source shares Select's budget.
	Sections []Section
}

// Select ranks every concept against q, adds the concepts the best matches
// link to, and keeps as many as fit budget bytes, best first. A concept with
// no matching term and no link from a selected concept is never included.
// Every source shares the one budget, whatever its level.
func Select(sources []Source, q Query, budget int) Pack {
	if budget <= 0 {
		budget = DefaultBudgetBytes
	}
	shared := make([]Source, len(sources))
	for i, s := range sources {
		s.Level, s.LevelFromPath = "", false
		shared[i] = s
	}
	return SelectWithin(shared, q, Budgets{"": budget})
}

// SelectWithin selects like Select, but each level under its own budget,
// narrowest level first. Within a level the best matches come first, a
// fresher concept ahead of an older one with the same score, then the Work
// Item Notes, newest first.
func SelectWithin(sources []Source, q Query, budgets Budgets) Pack {
	terms := queryTerms(q)
	labels := map[string]bool{}
	for _, l := range q.Labels {
		labels[strings.ToLower(strings.TrimSpace(l))] = true
	}
	pack := Pack{Digests: map[string]string{}}
	type key struct{ source, path string }
	byKey := map[key]okf.Concept{}
	origin := map[string]Source{}
	scored := map[key]Selected{}
	var noteItems []Selected
	for _, s := range sources {
		pack.Digests[s.Name] = Digest(s.Bundle)
		origin[s.Name] = s
		if s.Unreviewed {
			for _, c := range s.Bundle.Concepts {
				pack.Considered++
				noteItems = append(noteItems, Selected{Source: s.Name, Concept: c, Reason: noteReason(c), Unreviewed: true})
			}
			continue
		}
		for _, c := range s.Bundle.Concepts {
			pack.Considered++
			k := key{s.Name, c.Path}
			byKey[k] = c
			score, matched := scoreConcept(c, terms, labels)
			if score > 0 {
				scored[k] = Selected{Source: s.Name, Concept: c, Score: score, Reason: "matches " + strings.Join(matched, ", ")}
			}
		}
	}
	direct := make([]Selected, 0, len(scored))
	for _, s := range scored {
		direct = append(direct, s)
	}
	sortSelected(direct)
	for _, s := range direct {
		for _, l := range s.Concept.Links {
			k := key{s.Source, l}
			target, ok := byKey[k]
			if !ok {
				continue
			}
			linked := s.Score / 3
			if prior, seen := scored[k]; seen && prior.Score >= linked {
				continue
			}
			scored[k] = Selected{Source: s.Source, Concept: target, Score: linked, Reason: "linked from " + okf.DisplayName(s.Concept)}
		}
	}
	all := make([]Selected, 0, len(scored))
	for _, s := range scored {
		all = append(all, s)
	}
	sortSelected(all)
	sort.SliceStable(noteItems, func(i, j int) bool { return noteItems[i].Concept.Timestamp.After(noteItems[j].Concept.Timestamp) })
	all = append(all, noteItems...)
	byLevel := map[string][]Selected{}
	for _, s := range all {
		src := origin[s.Source]
		s.Level = src.Level
		if src.LevelFromPath {
			s.Level, _, _ = strings.Cut(s.Concept.Path, "/")
		}
		if level, ok := s.Concept.Extra["level"].(string); ok && slices.Contains(Levels[:len(Levels)-1], level) && src.Level != "" {
			s.Level = level
		}
		if s.Unreviewed && s.Level == "" && budgets[LevelWorkItem] > 0 {
			s.Level = LevelWorkItem
		}
		s.Cite = src.Cite
		if s.Unreviewed {
			if note, ok := s.Concept.Extra["note"].(string); ok && note != "" {
				s.Cite = "note " + note
			}
		}
		byLevel[s.Level] = append(byLevel[s.Level], s)
	}
	for _, level := range Levels {
		budget, ok := budgets[level]
		if !ok {
			pack.Omitted += len(byLevel[level])
			continue
		}
		sec := Section{Level: level, Budget: budget}
		for _, s := range byLevel[level] {
			size := len(okf.Format(s.Concept))
			if sec.Bytes+size > budget {
				sec.Omitted++
				continue
			}
			sec.Bytes += size
			sec.Items++
			pack.Selected = append(pack.Selected, s)
		}
		pack.Bytes += sec.Bytes
		pack.Omitted += sec.Omitted
		pack.Sections = append(pack.Sections, sec)
	}
	for level, list := range byLevel {
		if !slices.Contains(Levels, level) {
			pack.Omitted += len(list)
		}
	}
	return pack
}

func sortSelected(list []Selected) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Score != list[j].Score {
			return list[i].Score > list[j].Score
		}
		pi, pj := list[i].Concept.Type == PitfallType, list[j].Concept.Type == PitfallType
		if pi != pj {
			return pi
		}
		if ti, tj := list[i].Concept.Timestamp, list[j].Concept.Timestamp; !ti.Equal(tj) {
			return ti.After(tj)
		}
		if list[i].Source != list[j].Source {
			return list[i].Source < list[j].Source
		}
		return list[i].Concept.Path < list[j].Concept.Path
	})
}

var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an and are as at be but by can do does for from has have how if in into is it its
		of on or that the their then there this to was we what when where which who why will with without you your
		de het een en van in op te dat die is zijn voor met niet als er aan bij ook naar om dan wat worden wordt
		add fix make use new update change should must need needs work item`) {
		stopwords[w] = true
	}
}

// Terms returns the lower-cased words of text that carry meaning, with a
// crude plural stem, in first-seen order.
func Terms(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_'
	}) {
		w = strings.Trim(w, "-_")
		if len(w) < 3 || stopwords[w] {
			continue
		}
		w = stem(w)
		if !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

func stem(w string) string {
	switch {
	case strings.HasSuffix(w, "ies") && len(w) > 4:
		return w[:len(w)-3] + "y"
	case strings.HasSuffix(w, "ses") || strings.HasSuffix(w, "ss"):
		return w
	case strings.HasSuffix(w, "s") && len(w) > 3:
		return w[:len(w)-1]
	}
	return w
}

func queryTerms(q Query) []string {
	return Terms(q.Title + "\n" + q.Description + "\n" + strings.Join(q.Labels, " "))
}

func scoreConcept(c okf.Concept, terms []string, labels map[string]bool) (float64, []string) {
	fields := []struct {
		weight float64
		terms  map[string]bool
	}{
		{4, termSet(okf.DisplayName(c) + " " + strings.ReplaceAll(c.Path, "/", " "))},
		{2, termSet(c.Description)},
		{2, termSet(strings.Join(c.Tags, " ") + " " + c.Type)},
		{1, termSet(c.Body)},
	}
	var score float64
	var matched []string
	for _, t := range terms {
		hit := false
		for _, f := range fields {
			if f.terms[t] {
				score += f.weight
				hit = true
			}
		}
		if hit {
			matched = append(matched, t)
		}
	}
	for _, tag := range c.Tags {
		if labels[strings.ToLower(tag)] {
			score += 3
			matched = append(matched, "label "+tag)
		}
	}
	if len(matched) < 2 && score < 4 {
		return 0, nil
	}
	return score, matched
}

func termSet(text string) map[string]bool {
	out := map[string]bool{}
	for _, t := range Terms(text) {
		out[t] = true
	}
	return out
}

// Digest is a content hash of a bundle's concepts: equal digests mean the
// same knowledge.
func Digest(b okf.Bundle) string {
	h := sha256.New()
	concepts := append([]okf.Concept(nil), b.Concepts...)
	sort.Slice(concepts, func(i, j int) bool { return concepts[i].Path < concepts[j].Path })
	for _, c := range concepts {
		fmt.Fprintf(h, "%s\x00", c.Path)
		h.Write(okf.Format(c))
		h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))[:16]
}

// Write writes the pack to dir: each concept under its source's directory,
// and an index.md that lists them best first with the reason for each.
// It returns that index.
func (p Pack) Write(dir string) (string, error) {
	bySource := map[string][]okf.Concept{}
	for _, s := range p.Selected {
		bySource[s.Source] = append(bySource[s.Source], s.Concept)
	}
	for source, concepts := range bySource {
		if err := okf.Write(filepath.Join(dir, source), "Knowledge from "+source, concepts); err != nil {
			return "", err
		}
	}
	index := p.Index()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return index, os.WriteFile(filepath.Join(dir, okf.IndexFile), []byte(index), 0o644)
}

// Index renders the pack's table of contents. A pack selected by level
// lists each level under its own heading, narrowest first, with what its
// budget held; every entry cites what it was read from.
func (p Pack) Index() string {
	var b strings.Builder
	b.WriteString("# Knowledge pack\n\n")
	fmt.Fprintf(&b, "%d of %d concepts, %d bytes", len(p.Selected), p.Considered, p.Bytes)
	if p.Omitted > 0 {
		fmt.Fprintf(&b, "; %d more matched but did not fit", p.Omitted)
	}
	b.WriteString(".\n\n")
	if p.byLevel() {
		b.WriteString("Narrowest first: what is known about this Work Item, then its repository, its Client and the Tenant. Each level has a budget of its own.\n")
		for _, sec := range p.Sections {
			if sec.Items == 0 {
				continue
			}
			fmt.Fprintf(&b, "\n## %s (%d of %d bytes", levelTitle(sec.Level), sec.Bytes, sec.Budget)
			if sec.Omitted > 0 {
				fmt.Fprintf(&b, ", %d left out", sec.Omitted)
			}
			b.WriteString(")\n\n")
			p.writeLevel(&b, sec.Level)
		}
	} else {
		p.writeLevel(&b, "")
	}
	sources := make([]string, 0, len(p.Digests))
	for name := range p.Digests {
		sources = append(sources, name)
	}
	sort.Strings(sources)
	b.WriteString("\nSources: ")
	for i, name := range sources {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s %s", name, p.Digests[name])
	}
	b.WriteString("\n")
	return b.String()
}

func (p Pack) byLevel() bool {
	for _, sec := range p.Sections {
		if sec.Level != "" {
			return true
		}
	}
	return false
}

// noteReason says why a note is in the pack: it is read at its level.
func noteReason(c okf.Concept) string {
	switch c.Extra["level"] {
	case LevelRepository:
		return "a note on this repository"
	case LevelWorkItem, nil:
		return "a note on this Work Item"
	}
	return "a note"
}

func levelTitle(level string) string {
	switch level {
	case LevelWorkItem:
		return "This Work Item"
	case LevelRepository:
		return "This repository"
	case LevelClient:
		return "This Client"
	case LevelTenant:
		return "The Tenant"
	}
	return "Other knowledge"
}

func (p Pack) writeLevel(b *strings.Builder, level string) {
	notes := 0
	for _, s := range p.Selected {
		if s.Level != level {
			continue
		}
		if s.Unreviewed {
			notes++
			continue
		}
		writeEntry(b, s)
	}
	if notes == 0 {
		return
	}
	b.WriteString("\n### Notes\n\n")
	b.WriteString("Runs, people and Ploeg wrote these about this work, newest first. Each\nsays who wrote it and whether its text is trusted; untrusted text is quoted\nas data. Check each against the code before you rely on it.\n\n")
	for _, s := range p.Selected {
		if s.Level == level && s.Unreviewed {
			writeEntry(b, s)
		}
	}
}

func writeEntry(b *strings.Builder, s Selected) {
	cite := s.Source
	if s.Cite != "" {
		cite = s.Cite
	}
	fmt.Fprintf(b, "- [%s](%s) (%s, %s): %s", okf.DisplayName(s.Concept), path.Join(s.Source, s.Concept.Path), s.Concept.Type, cite, s.Reason)
	if s.Concept.Description != "" {
		fmt.Fprintf(b, ". %s", s.Concept.Description)
	}
	b.WriteString("\n")
}

// Paths returns each selected concept as "<source>/<path>", best first.
func (p Pack) Paths() []string {
	out := make([]string, len(p.Selected))
	for i, s := range p.Selected {
		out[i] = path.Join(s.Source, s.Concept.Path)
	}
	return out
}
