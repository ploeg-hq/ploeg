package playkpi

import (
	"fmt"
	"math"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/rarity"
)

// DefaultTestPaths are the paths a Work Target that sets no testPaths
// counts as tests.
var DefaultTestPaths = []string{
	"**/test/**", "**/tests/**", "**/__tests__/**", "**/spec/**", "**/testdata/**",
	"*_test.go", "test_*.py", "*_test.py",
	"*.test.{js,jsx,ts,tsx,mjs,cjs}", "*.spec.{js,jsx,ts,tsx,mjs,cjs}",
	"*Test.java", "*Tests.java", "*Test.kt", "*Test.php", "*Test.cs", "*Tests.cs", "*_spec.rb", "*_test.exs",
}

// DefaultDocPaths are the paths a Work Target that sets no docPaths counts
// as documentation.
var DefaultDocPaths = []string{
	"**/docs/**", "**/doc/**", "*.md", "*.mdx", "*.rst", "*.adoc", "README*", "CHANGELOG*",
}

// Rules are one Work Target's change-shape path rules (ADR-0058). A nil
// list uses the defaults; an empty, non-nil list means none.
type Rules struct {
	TestPaths []string
	DocPaths  []string
}

// Matcher answers the change-shape path questions of one set of Rules.
type Matcher struct {
	tests []rarity.Pattern
	docs  []rarity.Pattern
}

// Compile validates r and returns its Matcher. The patterns follow the
// rarity path syntax (rarity.Compile).
func (r Rules) Compile() (Matcher, error) {
	tests, docs := r.TestPaths, r.DocPaths
	if tests == nil {
		tests = DefaultTestPaths
	}
	if docs == nil {
		docs = DefaultDocPaths
	}
	var m Matcher
	var err error
	if m.tests, err = compilePatterns("testPaths", tests); err != nil {
		return Matcher{}, err
	}
	if m.docs, err = compilePatterns("docPaths", docs); err != nil {
		return Matcher{}, err
	}
	return m, nil
}

func compilePatterns(field string, raw []string) ([]rarity.Pattern, error) {
	if len(raw) > rarity.MaxPatterns {
		return nil, fmt.Errorf("%s: at most %d patterns, got %d", field, rarity.MaxPatterns, len(raw))
	}
	seen := map[string]bool{}
	out := make([]rarity.Pattern, 0, len(raw))
	for _, s := range raw {
		if seen[s] {
			return nil, fmt.Errorf("%s: pattern %q is listed twice", field, s)
		}
		seen[s] = true
		p, err := rarity.Compile(s)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", field, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// DefaultMatcher is the Matcher of the default rules.
var DefaultMatcher = func() Matcher {
	m, err := Rules{}.Compile()
	if err != nil {
		panic(err)
	}
	return m
}()

// Test reports whether file is a test.
func (m Matcher) Test(file string) bool { return matchAny(m.tests, file) }

// Doc reports whether file is documentation.
func (m Matcher) Doc(file string) bool { return matchAny(m.docs, file) }

func matchAny(patterns []rarity.Pattern, file string) bool {
	for _, p := range patterns {
		if p.Match(file) {
			return true
		}
	}
	return false
}

// Language is one language a change touched and the lines it added and
// removed in it.
type Language struct {
	Name  string `json:"name"`
	Lines int64  `json:"lines"`
}

// Bounds on what a play's and a card's Shape show.
const (
	ShownHotspots   = 3
	ShownLanguages  = 3
	StoredLanguages = 20
)

// Shape is how large, how tested and how complex a merged play's change was
// (ADR-0058). Every count leaves out the files the Work Target leaves out of
// size (lockfiles, generated and vendored code, ADR-0056). Files counts the
// changed files that remain and CountedLines their lines added and removed.
// TestLines counts the lines of test files and TestRatio divides them by the
// other counted lines; both are nil when a file's lines are unknown, and
// TestRatio also when no other line changed. DocsTouched counts the
// documentation files. Languages names the languages by file extension,
// most lines first. Complexity is nil when the diff was not read. Truncated
// says the file list or the diff reached its bound, so the figures are a
// lower bound.
type Shape struct {
	Complexity   *Complexity `json:"complexity"`
	Files        int         `json:"files"`
	CountedLines *int64      `json:"countedLines"`
	TestLines    *int64      `json:"testLines"`
	TestRatio    *float64    `json:"testRatio"`
	DocsTouched  int         `json:"docsTouched"`
	Languages    []Language  `json:"languages"`
	Truncated    bool        `json:"truncated"`
	CapturedAt   time.Time   `json:"capturedAt"`
}

// MeasureInput is what measuring a merged play takes. Files are its changed
// files with their lines, Total its whole diff size or nil. Diff is its
// unified diff, nil when it was not read.
type MeasureInput struct {
	Files          []rarity.FileLines
	FilesTruncated bool
	Total          *int64
	Diff           []byte
	DiffTruncated  bool
	Size           rarity.Matcher
	Paths          Matcher
	CapturedAt     time.Time
}

// Measure computes a play's Shape. It keeps up to StoredHotspots hotspots
// and StoredLanguages languages so a card can combine plays; Shown trims
// them for a play's card.
func Measure(in MeasureInput) Shape {
	s := Shape{Languages: []Language{}, Truncated: in.FilesTruncated || in.DiffTruncated, CapturedAt: in.CapturedAt.UTC()}
	s.CountedLines = in.Size.CountedLines(in.Files, in.Total, in.FilesTruncated)
	var tests int64
	known := !in.FilesTruncated
	languages := map[string]int64{}
	for _, f := range in.Files {
		if in.Size.Excluded(f.Path) {
			continue
		}
		s.Files++
		if in.Paths.Doc(f.Path) {
			s.DocsTouched++
		}
		if f.Additions == nil || f.Deletions == nil {
			known = false
			continue
		}
		lines := int64(*f.Additions + *f.Deletions)
		if in.Paths.Test(f.Path) {
			tests += lines
		}
		if name := LanguageOf(f.Path); name != "" && lines > 0 {
			languages[name] += lines
		}
	}
	if known && s.CountedLines != nil {
		s.TestLines = &tests
		if rest := *s.CountedLines - tests; rest > 0 {
			ratio := math.Round(float64(tests)/float64(rest)*1000) / 1000
			s.TestRatio = &ratio
		}
	}
	s.Languages = topLanguages(languages, StoredLanguages)
	if in.Diff != nil {
		c := MeasureComplexity(in.Diff, in.Size.Excluded)
		s.Complexity = &c
	}
	return s
}

func topLanguages(lines map[string]int64, n int) []Language {
	out := make([]Language, 0, len(lines))
	for name, l := range lines {
		out = append(out, Language{Name: name, Lines: l})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Lines != out[j].Lines {
			return out[i].Lines > out[j].Lines
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// Shown is s trimmed to ShownHotspots hotspots and ShownLanguages
// languages.
func (s Shape) Shown() Shape {
	if len(s.Languages) > ShownLanguages {
		s.Languages = s.Languages[:ShownLanguages]
	}
	if s.Complexity != nil {
		c := *s.Complexity
		if len(c.Hotspots) > ShownHotspots {
			c.Hotspots = c.Hotspots[:ShownHotspots]
		}
		s.Complexity = &c
	}
	return s
}

var languageByExtension = map[string]string{
	".go": "Go", ".ts": "TypeScript", ".tsx": "TypeScript", ".mts": "TypeScript", ".cts": "TypeScript",
	".js": "JavaScript", ".jsx": "JavaScript", ".mjs": "JavaScript", ".cjs": "JavaScript",
	".py": "Python", ".rb": "Ruby", ".java": "Java", ".kt": "Kotlin", ".kts": "Kotlin", ".scala": "Scala",
	".cs": "C#", ".fs": "F#", ".php": "PHP", ".rs": "Rust", ".c": "C", ".h": "C",
	".cc": "C++", ".cpp": "C++", ".cxx": "C++", ".hpp": "C++", ".swift": "Swift", ".m": "Objective-C",
	".dart": "Dart", ".ex": "Elixir", ".exs": "Elixir", ".erl": "Erlang", ".hs": "Haskell", ".lua": "Lua",
	".r": "R", ".jl": "Julia", ".clj": "Clojure", ".ml": "OCaml", ".zig": "Zig", ".nix": "Nix",
	".sh": "Shell", ".bash": "Shell", ".zsh": "Shell", ".ps1": "PowerShell", ".sql": "SQL",
	".html": "HTML", ".htm": "HTML", ".css": "CSS", ".scss": "SCSS", ".sass": "SCSS", ".less": "Less",
	".vue": "Vue", ".svelte": "Svelte", ".astro": "Astro", ".tf": "HCL", ".hcl": "HCL", ".proto": "Protocol Buffers",
	".graphql": "GraphQL", ".gql": "GraphQL", ".md": "Markdown", ".mdx": "Markdown", ".rst": "reStructuredText",
	".adoc": "AsciiDoc", ".yaml": "YAML", ".yml": "YAML", ".json": "JSON", ".toml": "TOML", ".xml": "XML",
	".gradle": "Gradle", ".twig": "Twig", ".blade.php": "Blade",
}

var languageByName = map[string]string{
	"Dockerfile": "Dockerfile", "Makefile": "Makefile", "Containerfile": "Dockerfile", "Justfile": "Just",
}

// LanguageOf names the language of file from its extension or name, the
// lowercased extension itself when it is not in the table, and "" for a
// file without one.
func LanguageOf(file string) string {
	base := path.Base(file)
	if name, ok := languageByName[base]; ok {
		return name
	}
	lower := strings.ToLower(base)
	if strings.HasSuffix(lower, ".blade.php") {
		return languageByExtension[".blade.php"]
	}
	ext := path.Ext(lower)
	if ext == "" || ext == lower {
		return ""
	}
	if name, ok := languageByExtension[ext]; ok {
		return name
	}
	return ext
}
