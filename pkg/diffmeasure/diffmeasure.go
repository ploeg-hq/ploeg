// Package diffmeasure takes raw per-file measurements from a unified diff,
// so Ploeg can keep them as delivery facts without keeping the diff
// (ADR-0079).
package diffmeasure

import (
	"bufio"
	"bytes"
	"strings"
)

// IndentationMethod names the indentation measure. Any change to how a
// line's level, the indent unit or the sums are computed changes it.
const IndentationMethod = "indentation/2026.1"

// DefaultIndentUnit is the indent unit of a file whose diff shows no
// indentation step between 2 and 8 spaces.
const DefaultIndentUnit = 4

// File is the indentation of one file's added and removed lines (Hindle,
// Godfrey and Holt 2008): each non-blank line counts its logical
// indentation level, a tab per level or Unit spaces per level. Added and
// Removed sum the levels, MaxDepth is the deepest added line. A binary file
// or a file without hunks has every sum zero.
type File struct {
	Path     string
	Unit     int
	Added    int
	Removed  int
	MaxDepth int
}

type diffFile struct {
	path           string
	added, removed []string
	sequence       []string
}

// Indentation measures every file of diff, in the order the diff lists
// them. A file whose path the diff does not name is left out.
func Indentation(diff []byte) []File {
	var out []File
	for _, f := range parse(diff) {
		if f.path == "" {
			continue
		}
		m := File{Path: f.path, Unit: indentUnit(f.sequence)}
		for _, line := range f.added {
			level, blank := indentLevel(line, m.Unit)
			if blank {
				continue
			}
			m.Added += level
			m.MaxDepth = max(m.MaxDepth, level)
		}
		for _, line := range f.removed {
			if level, blank := indentLevel(line, m.Unit); !blank {
				m.Removed += level
			}
		}
		out = append(out, m)
	}
	return out
}

func parse(diff []byte) []*diffFile {
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
