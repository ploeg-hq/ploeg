package rarity

import (
	"fmt"
	"path"
	"strings"
)

// DefaultSensitivePaths are the paths formula 2026.1 counts as sensitive
// ground on a Work Target that sets no sensitivePaths.
var DefaultSensitivePaths = []string{
	"**/migrations/**",
	"**/*.sql",
	"**/schema*.json",
	"**/*.proto",
	"**/openapi*.{yml,yaml,json}",
	"Dockerfile",
	"**/helm/**",
	"**/.github/workflows/**",
	"**/.forgejo/workflows/**",
}

// DefaultSizeExclude are the paths formula 2026.1 leaves out of counted
// lines on a Work Target that sets no sizeExclude: lockfiles, generated
// files and vendored code.
var DefaultSizeExclude = []string{
	"package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lock", "bun.lockb",
	"go.sum", "go.work.sum", "Cargo.lock", "poetry.lock", "Pipfile.lock", "uv.lock", "composer.lock",
	"Gemfile.lock", "mise.lock", "flake.lock", "packages.lock.json",
	"*.pb.go", "*_pb2.py", "*.gen.go", "*_generated.go", "zz_generated*", "*.min.js", "*.min.css", "*.map", "*.snap",
	"**/__snapshots__/**", "**/generated/**",
	"**/vendor/**", "**/node_modules/**", "**/third_party/**", "**/dist/**",
}

// ModuleRoots are the top-level directories whose children are the modules,
// as in apps/ploeg or packages/ui.
var ModuleRoots = []string{"apps", "packages", "services", "libs", "modules", "crates", "components", "plugins", "projects"}

// MaxPatterns is how many patterns one list of Rules may hold, and
// MaxPatternLength how long one pattern may be.
const (
	MaxPatterns      = 100
	MaxPatternLength = 256
)

// Rules are one Work Target's rarity path rules. A nil SensitivePaths uses
// DefaultSensitivePaths and a nil SizeExclude uses DefaultSizeExclude; an
// empty, non-nil list means none. AttentionPaths are always counted as
// sensitive on top.
type Rules struct {
	SensitivePaths []string
	AttentionPaths []string
	SizeExclude    []string
}

// Matcher answers the path questions of one set of Rules.
type Matcher struct {
	sensitive []Pattern
	exclude   []Pattern
}

// Compile validates r and returns its Matcher.
func (r Rules) Compile() (Matcher, error) {
	var m Matcher
	sensitive := r.SensitivePaths
	if sensitive == nil {
		sensitive = DefaultSensitivePaths
	}
	exclude := r.SizeExclude
	if exclude == nil {
		exclude = DefaultSizeExclude
	}
	var err error
	if m.sensitive, err = compileAll("sensitivePaths", sensitive); err != nil {
		return Matcher{}, err
	}
	attention, err := compileAll("attentionPaths", r.AttentionPaths)
	if err != nil {
		return Matcher{}, err
	}
	m.sensitive = append(m.sensitive, attention...)
	if m.exclude, err = compileAll("sizeExclude", exclude); err != nil {
		return Matcher{}, err
	}
	return m, nil
}

func compileAll(field string, patterns []string) ([]Pattern, error) {
	if len(patterns) > MaxPatterns {
		return nil, fmt.Errorf("%s: at most %d patterns, got %d", field, MaxPatterns, len(patterns))
	}
	seen := map[string]bool{}
	out := make([]Pattern, 0, len(patterns))
	for _, raw := range patterns {
		if seen[raw] {
			return nil, fmt.Errorf("%s: pattern %q is listed twice", field, raw)
		}
		seen[raw] = true
		p, err := Compile(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", field, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// Sensitive reports whether file is sensitive ground.
func (m Matcher) Sensitive(file string) bool { return matchAny(m.sensitive, file) }

// Excluded reports whether file is left out of counted lines.
func (m Matcher) Excluded(file string) bool { return matchAny(m.exclude, file) }

func matchAny(patterns []Pattern, file string) bool {
	for _, p := range patterns {
		if p.Match(file) {
			return true
		}
	}
	return false
}

// Pattern is a compiled path pattern. A pattern without a slash matches the
// file name at any depth; any other pattern is anchored at the repository
// root. ** spans any number of directories, including none; *, ? and [...]
// work within one path segment as in path.Match; {a,b} lists alternatives.
// A trailing slash matches everything below that directory.
type Pattern struct {
	alternatives [][]string
}

// Compile parses one pattern.
func Compile(raw string) (Pattern, error) {
	if strings.TrimSpace(raw) != raw || raw == "" || len(raw) > MaxPatternLength {
		return Pattern{}, fmt.Errorf("pattern %q must be 1 to %d characters without surrounding space", raw, MaxPatternLength)
	}
	expanded, err := expandBraces(raw)
	if err != nil {
		return Pattern{}, err
	}
	var p Pattern
	for _, alt := range expanded {
		alt = strings.TrimPrefix(alt, "/")
		if strings.HasSuffix(alt, "/") {
			alt += "**"
		}
		if !strings.Contains(alt, "/") {
			alt = "**/" + alt
		}
		segments := strings.Split(alt, "/")
		for _, s := range segments {
			if s == "" {
				return Pattern{}, fmt.Errorf("pattern %q has an empty path segment", raw)
			}
			if s == "**" {
				continue
			}
			if strings.Contains(s, "**") {
				return Pattern{}, fmt.Errorf("pattern %q: ** must be a whole path segment", raw)
			}
			if _, err := path.Match(s, ""); err != nil {
				return Pattern{}, fmt.Errorf("pattern %q: %w", raw, err)
			}
		}
		p.alternatives = append(p.alternatives, segments)
	}
	return p, nil
}

const maxBraceAlternatives = 64

func expandBraces(raw string) ([]string, error) {
	open := strings.IndexByte(raw, '{')
	if open < 0 {
		if strings.ContainsRune(raw, '}') {
			return nil, fmt.Errorf("pattern %q has an unmatched }", raw)
		}
		return []string{raw}, nil
	}
	end := strings.IndexByte(raw[open:], '}')
	if end < 0 {
		return nil, fmt.Errorf("pattern %q has an unmatched {", raw)
	}
	end += open
	inner := raw[open+1 : end]
	if strings.ContainsRune(inner, '{') {
		return nil, fmt.Errorf("pattern %q nests braces", raw)
	}
	rest, err := expandBraces(raw[end+1:])
	if err != nil {
		return nil, err
	}
	var out []string
	for _, choice := range strings.Split(inner, ",") {
		for _, tail := range rest {
			out = append(out, raw[:open]+choice+tail)
			if len(out) > maxBraceAlternatives {
				return nil, fmt.Errorf("pattern %q expands to more than %d alternatives", raw, maxBraceAlternatives)
			}
		}
	}
	return out, nil
}

// Match reports whether the repository path file matches p.
func (p Pattern) Match(file string) bool {
	parts := strings.Split(strings.TrimPrefix(file, "/"), "/")
	for _, alt := range p.alternatives {
		if matchSegments(alt, parts) {
			return true
		}
	}
	return false
}

func matchSegments(pattern, parts []string) bool {
	if len(pattern) == 0 {
		return len(parts) == 0
	}
	if pattern[0] == "**" {
		for i := 0; i <= len(parts); i++ {
			if matchSegments(pattern[1:], parts[i:]) {
				return true
			}
		}
		return false
	}
	if len(parts) == 0 {
		return false
	}
	ok, err := path.Match(pattern[0], parts[0])
	return err == nil && ok && matchSegments(pattern[1:], parts[1:])
}

// Module is the module a repository path belongs to: its top-level
// directory, or the first two directories under one of ModuleRoots. A file
// at the root belongs to the module ".".
func Module(file string) string {
	parts := strings.Split(strings.TrimPrefix(file, "/"), "/")
	if len(parts) == 1 {
		return "."
	}
	if len(parts) > 2 {
		for _, root := range ModuleRoots {
			if parts[0] == root {
				return parts[0] + "/" + parts[1]
			}
		}
	}
	return parts[0]
}

// FileLines is one changed file and the lines it added and removed. A nil
// count is one the forge did not report.
type FileLines struct {
	Path      string
	Additions *int
	Deletions *int
}

// CountedLines adds the lines files added and removed without the files m
// leaves out of size (ADR-0056). total is the whole diff size of the pull
// request, or nil. When files is truncated, the excluded lines are taken off
// total; when a file was not counted, total stands in only if no file is
// excluded. It returns nil when the count is unknown.
func (m Matcher) CountedLines(files []FileLines, total *int64, truncated bool) *int64 {
	var counted, excluded int64
	complete, anyExcluded := true, false
	for _, f := range files {
		skip := m.Excluded(f.Path)
		anyExcluded = anyExcluded || skip
		if f.Additions == nil || f.Deletions == nil {
			complete = false
			continue
		}
		if skip {
			excluded += int64(*f.Additions + *f.Deletions)
		} else {
			counted += int64(*f.Additions + *f.Deletions)
		}
	}
	switch {
	case truncated && total != nil:
		n := max(0, *total-excluded)
		return &n
	case complete && !truncated:
		return &counted
	case !anyExcluded && !truncated && total != nil:
		n := *total
		return &n
	default:
		return nil
	}
}
