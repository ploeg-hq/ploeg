// Package okf reads and writes Open Knowledge Format (OKF) v0.1 bundles: a
// directory of markdown files, one concept per file, each opening with YAML
// frontmatter whose only required field is type. A concept's identity is its
// path inside the bundle, and markdown links between concepts make the bundle
// a graph. Spec: https://github.com/GoogleCloudPlatform/knowledge-catalog/tree/main/okf
package okf

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// The reserved filenames of OKF v0.1. Neither is a concept.
const (
	IndexFile = "index.md"
	LogFile   = "log.md"
)

// Concept is one OKF document.
type Concept struct {
	// Path is the concept's identity: slash-separated, relative to the bundle
	// root, ending in ".md".
	Path        string
	Type        string
	Title       string
	Description string
	Resource    string
	Tags        []string
	Timestamp   time.Time
	// Extra holds the producer's own frontmatter fields, kept so a concept
	// survives a round trip unchanged.
	Extra map[string]any
	Body  string
	// Links are the bundle paths of the concepts the body links to, sorted and
	// without duplicates. Links that leave the bundle are not included.
	Links []string
}

// Bundle is a parsed OKF directory.
type Bundle struct {
	Name     string
	Concepts []Concept
}

// Problem is a file that is not a conforming OKF concept.
type Problem struct {
	Path   string
	Reason string
}

func (p Problem) String() string { return p.Path + ": " + p.Reason }

var errNoFrontmatter = errors.New("no YAML frontmatter")

// Parse reads one concept. p is its bundle path.
func Parse(p string, data []byte) (Concept, error) {
	front, body, err := splitFrontmatter(data)
	if err != nil {
		return Concept{}, err
	}
	fields := map[string]any{}
	if err := yaml.Unmarshal(front, &fields); err != nil {
		return Concept{}, fmt.Errorf("frontmatter: %w", err)
	}
	c := Concept{Path: p, Body: body, Extra: map[string]any{}}
	for k, v := range fields {
		switch k {
		case "type":
			c.Type = scalar(v)
		case "title":
			c.Title = scalar(v)
		case "description":
			c.Description = scalar(v)
		case "resource":
			c.Resource = scalar(v)
		case "tags":
			c.Tags = stringList(v)
		case "timestamp":
			c.Timestamp = timestamp(v)
		default:
			c.Extra[k] = v
		}
	}
	if strings.TrimSpace(c.Type) == "" {
		return Concept{}, errors.New("frontmatter has no type")
	}
	c.Links = Links(p, body)
	return c, nil
}

func splitFrontmatter(data []byte) (front []byte, body string, err error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return nil, "", errNoFrontmatter
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		if strings.HasSuffix(rest, "\n---") {
			return []byte(strings.TrimSuffix(rest, "\n---")), "", nil
		}
		return nil, "", errors.New("frontmatter is not closed")
	}
	return []byte(rest[:end]), strings.TrimLeft(rest[end+len("\n---\n"):], "\n"), nil
}

func scalar(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case time.Time:
		return t.UTC().Format(time.RFC3339)
	default:
		return fmt.Sprint(t)
	}
}

func stringList(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s := strings.TrimSpace(scalar(e)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		var out []string
		for _, s := range strings.Split(t, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func timestamp(v any) time.Time {
	switch t := v.(type) {
	case time.Time:
		return t.UTC()
	case string:
		for _, layout := range []string{time.RFC3339, "2006-01-02"} {
			if ts, err := time.Parse(layout, t); err == nil {
				return ts.UTC()
			}
		}
	}
	return time.Time{}
}

var markdownLink = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// Links returns the bundle paths a concept at bundle path from links to. An
// absolute link ("/tables/orders.md") is taken from the bundle root, a
// relative one from the linking file's directory.
func Links(from, body string) []string {
	seen := map[string]bool{}
	for _, m := range markdownLink.FindAllStringSubmatch(body, -1) {
		target := m[1]
		if i := strings.IndexAny(target, "#?"); i >= 0 {
			target = target[:i]
		}
		if target == "" || strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") || !strings.HasSuffix(target, ".md") {
			continue
		}
		var p string
		if strings.HasPrefix(target, "/") {
			p = path.Clean(strings.TrimPrefix(target, "/"))
		} else {
			p = path.Clean(path.Join(path.Dir(from), target))
		}
		if p == ".." || strings.HasPrefix(p, "../") || p == from {
			continue
		}
		seen[p] = true
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Format renders a concept as an OKF document.
func Format(c Concept) []byte {
	var b bytes.Buffer
	b.WriteString("---\n")
	writeField(&b, "type", c.Type)
	if c.Title != "" {
		writeField(&b, "title", c.Title)
	}
	if c.Description != "" {
		writeField(&b, "description", c.Description)
	}
	if c.Resource != "" {
		writeField(&b, "resource", c.Resource)
	}
	if len(c.Tags) > 0 {
		writeField(&b, "tags", c.Tags)
	}
	if !c.Timestamp.IsZero() {
		fmt.Fprintf(&b, "timestamp: %s\n", c.Timestamp.UTC().Format(time.RFC3339))
	}
	keys := make([]string, 0, len(c.Extra))
	for k := range c.Extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		writeField(&b, k, c.Extra[k])
	}
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(c.Body))
	b.WriteString("\n")
	return b.Bytes()
}

func writeField(b *bytes.Buffer, key string, value any) {
	if list, ok := value.([]string); ok {
		quoted := make([]string, len(list))
		for i, s := range list {
			quoted[i] = yamlScalar(s)
		}
		fmt.Fprintf(b, "%s: [%s]\n", key, strings.Join(quoted, ", "))
		return
	}
	out, err := yaml.Marshal(map[string]any{key: value})
	if err != nil {
		fmt.Fprintf(b, "%s: %q\n", key, fmt.Sprint(value))
		return
	}
	b.Write(out)
}

func yamlScalar(s string) string {
	out, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Sprintf("%q", s)
	}
	return strings.TrimSpace(string(out))
}

// ReadBundle parses every concept in fsys. Files that are not conforming
// concepts are reported as problems and left out; reserved files and hidden
// directories are skipped.
func ReadBundle(name string, fsys fs.FS) (Bundle, []Problem, error) {
	b := Bundle{Name: name}
	var problems []Problem
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != "." && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".md") || d.Name() == IndexFile || d.Name() == LogFile {
			return nil
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		c, perr := Parse(p, data)
		if perr != nil {
			problems = append(problems, Problem{Path: p, Reason: perr.Error()})
			return nil
		}
		b.Concepts = append(b.Concepts, c)
		return nil
	})
	if err != nil {
		return Bundle{}, nil, err
	}
	sort.Slice(b.Concepts, func(i, j int) bool { return b.Concepts[i].Path < b.Concepts[j].Path })
	return b, problems, nil
}

// ReadDir parses the bundle in directory dir, named after the directory.
func ReadDir(dir string) (Bundle, []Problem, error) {
	return ReadBundle(filepath.Base(filepath.Clean(dir)), os.DirFS(dir))
}

// Dangling returns, per concept path, the links that name no concept in the
// bundle.
func (b Bundle) Dangling() map[string][]string {
	known := make(map[string]bool, len(b.Concepts))
	for _, c := range b.Concepts {
		known[c.Path] = true
	}
	out := map[string][]string{}
	for _, c := range b.Concepts {
		for _, l := range c.Links {
			if !known[l] {
				out[c.Path] = append(out[c.Path], l)
			}
		}
	}
	return out
}

// ValidPath refuses a bundle path that could leave the bundle or that names a
// reserved file.
func ValidPath(p string) error {
	clean := path.Clean(p)
	if p == "" || clean != p || path.IsAbs(p) || clean == ".." || strings.HasPrefix(clean, "../") ||
		!strings.HasSuffix(p, ".md") || path.Base(p) == IndexFile || path.Base(p) == LogFile {
		return fmt.Errorf("okf: %q is not a concept path", p)
	}
	return nil
}

// Write writes concepts into dir at their bundle paths, plus an index.md in
// every directory on the way to a concept, so a reader can descend from the
// root index without listing directories.
func Write(dir, title string, concepts []Concept) error {
	members := map[string][]Concept{".": nil}
	for _, c := range concepts {
		if err := ValidPath(c.Path); err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(c.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, Format(c), 0o644); err != nil {
			return err
		}
		parent := path.Dir(c.Path)
		members[parent] = append(members[parent], c)
		for d := path.Dir(parent); parent != "."; parent, d = d, path.Dir(d) {
			if _, ok := members[d]; !ok {
				members[d] = nil
			}
		}
	}
	for d, list := range members {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(d)), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(d), IndexFile), indexFor(d, title, list, children(members, d)), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func children(members map[string][]Concept, parent string) []string {
	var out []string
	for d := range members {
		if d != "." && d != parent && path.Dir(d) == parent {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

func indexFor(dir, title string, list []Concept, subdirs []string) []byte {
	var b strings.Builder
	heading := title
	if dir != "." {
		heading = dir
	}
	fmt.Fprintf(&b, "# %s\n\n", heading)
	sort.Slice(list, func(i, j int) bool { return list[i].Path < list[j].Path })
	for _, c := range list {
		fmt.Fprintf(&b, "- [%s](%s) (%s)", DisplayName(c), path.Base(c.Path), c.Type)
		if c.Description != "" {
			fmt.Fprintf(&b, ": %s", c.Description)
		}
		b.WriteString("\n")
	}
	for _, d := range subdirs {
		fmt.Fprintf(&b, "- [%s/](%s/%s)\n", path.Base(d), path.Base(d), IndexFile)
	}
	return []byte(b.String())
}

// DisplayName is the concept's title, or its file name without ".md".
func DisplayName(c Concept) string {
	if c.Title != "" {
		return c.Title
	}
	return strings.TrimSuffix(path.Base(c.Path), ".md")
}
