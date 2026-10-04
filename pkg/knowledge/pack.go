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
	"sort"
	"strings"
	"unicode"

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
}

// Select ranks every concept against q, adds the concepts the best matches
// link to, and keeps as many as fit budget bytes, best first. A concept with
// no matching term and no link from a selected concept is never included.
func Select(sources []Source, q Query, budget int) Pack {
	if budget <= 0 {
		budget = DefaultBudgetBytes
	}
	terms := queryTerms(q)
	labels := map[string]bool{}
	for _, l := range q.Labels {
		labels[strings.ToLower(strings.TrimSpace(l))] = true
	}
	pack := Pack{Digests: map[string]string{}}
	type key struct{ source, path string }
	byKey := map[key]okf.Concept{}
	scored := map[key]Selected{}
	for _, s := range sources {
		pack.Digests[s.Name] = Digest(s.Bundle)
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
	for _, s := range all {
		size := len(okf.Format(s.Concept))
		if pack.Bytes+size > budget {
			pack.Omitted++
			continue
		}
		pack.Bytes += size
		pack.Selected = append(pack.Selected, s)
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

// Index renders the pack's table of contents.
func (p Pack) Index() string {
	var b strings.Builder
	b.WriteString("# Knowledge pack\n\n")
	fmt.Fprintf(&b, "%d of %d concepts, %d bytes", len(p.Selected), p.Considered, p.Bytes)
	if p.Omitted > 0 {
		fmt.Fprintf(&b, "; %d more matched but did not fit", p.Omitted)
	}
	b.WriteString(".\n\n")
	for _, s := range p.Selected {
		fmt.Fprintf(&b, "- [%s](%s) (%s, %s): %s", okf.DisplayName(s.Concept), path.Join(s.Source, s.Concept.Path), s.Concept.Type, s.Source, s.Reason)
		if s.Concept.Description != "" {
			fmt.Fprintf(&b, ". %s", s.Concept.Description)
		}
		b.WriteString("\n")
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

// Paths returns each selected concept as "<source>/<path>", best first.
func (p Pack) Paths() []string {
	out := make([]string, len(p.Selected))
	for i, s := range p.Selected {
		out[i] = path.Join(s.Source, s.Concept.Path)
	}
	return out
}
