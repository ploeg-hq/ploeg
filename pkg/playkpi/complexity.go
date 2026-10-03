package playkpi

import (
	"bufio"
	"bytes"
	"sort"
	"strings"
)

// ComplexityMethod names the complexity measure. Any change to how a line's
// level, the indent unit or the totals are computed changes it.
const ComplexityMethod = "indentation/2026.1"

// DefaultIndentUnit is the indent unit of a file whose diff shows no
// indentation step between 2 and 8 spaces.
const DefaultIndentUnit = 4

// Hotspot is one file's added indentation complexity.
type Hotspot struct {
	Path  string `json:"path"`
	Added int    `json:"added"`
}

// Complexity is the indentation complexity of a change (Hindle, Godfrey and
// Holt 2008): each added or removed line counts its logical indentation
// level, a tab per level or the file's indent unit of spaces per level.
// Added and Removed sum the levels of the added and removed lines, Net is
// their difference, MaxDepth is the deepest added line and Hotspots are the
// files with the most added complexity, most first.
type Complexity struct {
	Method   string    `json:"method"`
	Added    int       `json:"added"`
	Removed  int       `json:"removed"`
	Net      int       `json:"net"`
	MaxDepth int       `json:"maxDepth"`
	Hotspots []Hotspot `json:"hotspots"`
}

// StoredHotspots is how many hotspots a measured play keeps, so a card can
// merge its plays' hotspots.
const StoredHotspots = 10

type diffFile struct {
	path           string
	added, removed []string
	sequence       []string
}

// MeasureComplexity reads a unified diff and returns the indentation
// complexity of every file excluded does not leave out. A binary file or a
// file without hunks counts nothing.
func MeasureComplexity(diff []byte, excluded func(path string) bool) Complexity {
	c := Complexity{Method: ComplexityMethod, Hotspots: []Hotspot{}}
	for _, f := range parseDiff(diff) {
		if f.path == "" || (excluded != nil && excluded(f.path)) {
			continue
		}
		unit := indentUnit(f.sequence)
		added := 0
		for _, line := range f.added {
			level, blank := indentLevel(line, unit)
			if blank {
				continue
			}
			added += level
			c.MaxDepth = max(c.MaxDepth, level)
		}
		for _, line := range f.removed {
			if level, blank := indentLevel(line, unit); !blank {
				c.Removed += level
			}
		}
		c.Added += added
		if added > 0 {
			c.Hotspots = append(c.Hotspots, Hotspot{Path: f.path, Added: added})
		}
	}
	c.Net = c.Added - c.Removed
	sortHotspots(c.Hotspots)
	if len(c.Hotspots) > StoredHotspots {
		c.Hotspots = c.Hotspots[:StoredHotspots]
	}
	return c
}

func sortHotspots(h []Hotspot) {
	sort.Slice(h, func(i, j int) bool {
		if h[i].Added != h[j].Added {
			return h[i].Added > h[j].Added
		}
		return h[i].Path < h[j].Path
	})
}

func parseDiff(diff []byte) []*diffFile {
	var files []*diffFile
	var cur *diffFile
	inHunk := false
	var oldPath string
	scanner := bufio.NewScanner(bytes.NewReader(diff))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		switch {
		case strings.HasPrefix(line, "diff --git "):
			cur = &diffFile{path: gitHeaderPath(line)}
			files = append(files, cur)
			inHunk, oldPath = false, ""
			continue
		case cur == nil:
			continue
		case !inHunk && strings.HasPrefix(line, "--- "):
			oldPath = diffPath(line[4:])
			continue
		case !inHunk && strings.HasPrefix(line, "+++ "):
			if p := diffPath(line[4:]); p != "" {
				cur.path = p
			} else if oldPath != "" {
				cur.path = oldPath
			}
			continue
		case strings.HasPrefix(line, "@@"):
			inHunk = true
			continue
		case !inHunk:
			continue
		}
		switch {
		case strings.HasPrefix(line, "+"):
			cur.added = append(cur.added, line[1:])
			cur.sequence = append(cur.sequence, line[1:])
		case strings.HasPrefix(line, "-"):
			cur.removed = append(cur.removed, line[1:])
		case strings.HasPrefix(line, " "):
			cur.sequence = append(cur.sequence, line[1:])
		}
	}
	return files
}

func gitHeaderPath(line string) string {
	rest := strings.TrimPrefix(line, "diff --git ")
	if i := strings.LastIndex(rest, " b/"); i >= 0 {
		return strings.Trim(rest[i+3:], `"`)
	}
	return ""
}

func diffPath(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\t'); i >= 0 {
		s = s[:i]
	}
	s = strings.Trim(s, `"`)
	if s == "/dev/null" {
		return ""
	}
	for _, prefix := range []string{"a/", "b/"} {
		if strings.HasPrefix(s, prefix) {
			return s[len(prefix):]
		}
	}
	return s
}

func leading(line string) (tabs, spaces int, blank bool) {
	for _, r := range line {
		switch r {
		case '\t':
			if spaces == 0 {
				tabs++
			} else {
				spaces += DefaultIndentUnit
			}
		case ' ':
			spaces++
		default:
			return tabs, spaces, false
		}
	}
	return tabs, spaces, true
}

func indentLevel(line string, unit int) (int, bool) {
	tabs, spaces, blank := leading(line)
	if blank {
		return 0, true
	}
	return tabs + spaces/unit, false
}

func indentUnit(lines []string) int {
	steps := map[int]int{}
	previous := -1
	smallest := 0
	for _, line := range lines {
		tabs, spaces, blank := leading(line)
		if blank || tabs > 0 {
			continue
		}
		if spaces > 0 && (smallest == 0 || spaces < smallest) {
			smallest = spaces
		}
		if previous >= 0 {
			step := spaces - previous
			if step < 0 {
				step = -step
			}
			if step >= 2 && step <= 8 {
				steps[step]++
			}
		}
		previous = spaces
	}
	best, count := 0, 0
	for step, n := range steps {
		if n > count || (n == count && step < best) {
			best, count = step, n
		}
	}
	switch {
	case best > 0:
		return best
	case smallest >= 2 && smallest <= 8:
		return smallest
	default:
		return DefaultIndentUnit
	}
}
