package playkpi

import (
	"reflect"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/rarity"
)

func lines(path string, a, d int) rarity.FileLines {
	return rarity.FileLines{Path: path, Additions: &a, Deletions: &d}
}

func sizeMatcher(t *testing.T) rarity.Matcher {
	t.Helper()
	m, err := rarity.Rules{}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMeasure_TestRatioDocsLanguagesAndExcludedFiles(t *testing.T) {
	at := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)
	s := Measure(MeasureInput{
		Files: []rarity.FileLines{
			lines("pkg/store/card.go", 80, 20),
			lines("pkg/store/card_test.go", 40, 10),
			lines("web/src/card.spec.ts", 10, 0),
			lines("docs/concepts/run-cards.md", 30, 5),
			lines("README.md", 1, 1),
			lines("go.sum", 500, 400),
		},
		Diff: []byte(fileDiff("pkg/store/card.go", "+\tif x {", "+\t\ty()", "+\t}") + fileDiff("go.sum", "+\t\t\t\tdeep")),
		Size: sizeMatcher(t), Paths: DefaultMatcher, CapturedAt: at,
	})
	if s.Files != 5 || s.CountedLines == nil || *s.CountedLines != 197 || s.DocsTouched != 2 {
		t.Fatalf("shape = %+v; go.sum counts for nothing", s)
	}
	if s.TestLines == nil || *s.TestLines != 60 || s.TestRatio == nil || *s.TestRatio != 0.438 {
		t.Errorf("tests = %v, ratio = %v; want 60 test lines over 137 others", s.TestLines, s.TestRatio)
	}
	want := []Language{{"Go", 150}, {"Markdown", 37}, {"TypeScript", 10}}
	if !reflect.DeepEqual(s.Languages, want) {
		t.Errorf("languages = %+v; want %+v", s.Languages, want)
	}
	if s.Complexity == nil || s.Complexity.Added != 4 || s.Truncated || !s.CapturedAt.Equal(at) {
		t.Errorf("complexity = %+v, truncated = %v", s.Complexity, s.Truncated)
	}
}

func TestMeasure_UnknownLinesAndNoDiffStayNil(t *testing.T) {
	s := Measure(MeasureInput{Files: []rarity.FileLines{lines("a.go", 3, 1), {Path: "b_test.go"}}, Size: sizeMatcher(t), Paths: DefaultMatcher})
	if s.TestLines != nil || s.TestRatio != nil || s.CountedLines != nil || s.Complexity != nil || s.Files != 2 {
		t.Fatalf("shape = %+v; a file without line counts leaves the line figures unknown", s)
	}
	total := int64(40)
	truncated := Measure(MeasureInput{Files: []rarity.FileLines{lines("a.go", 3, 1)}, FilesTruncated: true, Total: &total,
		Diff: []byte{}, DiffTruncated: true, Size: sizeMatcher(t), Paths: DefaultMatcher})
	if !truncated.Truncated || truncated.TestLines != nil || truncated.CountedLines == nil || *truncated.CountedLines != 40 ||
		truncated.Complexity == nil || truncated.Complexity.Added != 0 {
		t.Errorf("shape = %+v", truncated)
	}
	onlyTests := Measure(MeasureInput{Files: []rarity.FileLines{lines("a_test.go", 3, 1)}, Size: sizeMatcher(t), Paths: DefaultMatcher})
	if onlyTests.TestRatio != nil || onlyTests.TestLines == nil || *onlyTests.TestLines != 4 {
		t.Errorf("shape = %+v; a ratio over zero other lines is unknown", onlyTests)
	}
}

func TestRules_DefaultsReplacementsAndRefusals(t *testing.T) {
	for _, tc := range []struct {
		path       string
		test, docs bool
	}{
		{"pkg/store/card_test.go", true, false},
		{"tests/e2e/run.py", true, false},
		{"src/__tests__/card.tsx", true, false},
		{"src/card.test.ts", true, false},
		{"src/main/java/CardTest.java", true, false},
		{"pkg/store/testdata/fixture.json", true, false},
		{"docs/index.md", false, true},
		{"services/engine/docs/adrs/0058.md", false, true},
		{"README", false, true},
		{"CHANGELOG.md", false, true},
		{"pkg/store/card.go", false, false},
		{"contest/main.go", false, false},
	} {
		if DefaultMatcher.Test(tc.path) != tc.test || DefaultMatcher.Doc(tc.path) != tc.docs {
			t.Errorf("%s: test %v, doc %v; want %v, %v", tc.path, DefaultMatcher.Test(tc.path), DefaultMatcher.Doc(tc.path), tc.test, tc.docs)
		}
	}
	m, err := Rules{TestPaths: []string{"qa/**"}, DocPaths: []string{}}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if !m.Test("qa/smoke.sh") || m.Test("a_test.go") || m.Doc("README.md") {
		t.Errorf("configured rules replace the defaults, and an empty list means none")
	}
	for _, bad := range []Rules{{TestPaths: []string{"a/**b"}}, {DocPaths: []string{"x", "x"}}, {TestPaths: []string{" spaced"}}} {
		if _, err := bad.Compile(); err == nil {
			t.Errorf("rules %+v compiled", bad)
		}
	}
}

func TestLanguageOf(t *testing.T) {
	for path, want := range map[string]string{
		"a/b.go": "Go", "x.TSX": "TypeScript", "Dockerfile": "Dockerfile", "views/a.blade.php": "Blade",
		"a.weird": ".weird", "Makefile": "Makefile", "LICENSE": "", ".gitignore": "",
	} {
		if got := LanguageOf(path); got != want {
			t.Errorf("LanguageOf(%q) = %q; want %q", path, got, want)
		}
	}
}

func TestShown_TrimsToThree(t *testing.T) {
	s := Shape{Languages: []Language{{"a", 4}, {"b", 3}, {"c", 2}, {"d", 1}},
		Complexity: &Complexity{Hotspots: []Hotspot{{"a", 4}, {"b", 3}, {"c", 2}, {"d", 1}}}}
	shown := s.Shown()
	if len(shown.Languages) != 3 || len(shown.Complexity.Hotspots) != 3 || len(s.Complexity.Hotspots) != 4 {
		t.Fatalf("shown = %+v; the stored shape keeps every hotspot", shown)
	}
}
