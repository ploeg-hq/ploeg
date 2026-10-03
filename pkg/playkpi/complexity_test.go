package playkpi

import (
	"reflect"
	"strings"
	"testing"
)

func diffOf(files ...string) []byte { return []byte(strings.Join(files, "")) }

func fileDiff(path string, body ...string) string {
	return "diff --git a/" + path + " b/" + path + "\nindex 1111111..2222222 100644\n--- a/" + path + "\n+++ b/" + path +
		"\n@@ -1,3 +1,6 @@\n" + strings.Join(body, "\n") + "\n"
}

func TestComplexity_TabsCountOneLevelEach(t *testing.T) {
	c := MeasureComplexity(diffOf(fileDiff("main.go",
		" func main() {",
		"+\tif ok {",
		"+\t\tfor {",
		"+\t\t\trun()",
		"+\t\t}",
		"+\t}",
		"+",
		" }")), nil)
	if c.Added != 1+2+3+2+1 || c.Removed != 0 || c.MaxDepth != 3 || c.Net != 9 || c.Method != ComplexityMethod {
		t.Fatalf("complexity = %+v", c)
	}
	if !reflect.DeepEqual(c.Hotspots, []Hotspot{{"main.go", 9}}) {
		t.Errorf("hotspots = %+v", c.Hotspots)
	}
}

func TestComplexity_SpacesAreNormalisedByTheDetectedUnit(t *testing.T) {
	four := MeasureComplexity(diffOf(fileDiff("app.py",
		" def f():",
		"+    if x:",
		"+        return 1",
		"+    return 2")), nil)
	two := MeasureComplexity(diffOf(fileDiff("app.ts",
		" function f() {",
		"+  if (x) {",
		"+    return 1",
		"+  }",
		"+  return 2")), nil)
	if four.Added != 1+2+1 || four.MaxDepth != 2 {
		t.Errorf("four-space unit: %+v", four)
	}
	if two.Added != 1+2+1+1 || two.MaxDepth != 2 {
		t.Errorf("two-space unit: %+v", two)
	}
}

func TestComplexity_AnAlignedContinuationDoesNotHalveTheUnit(t *testing.T) {
	c := MeasureComplexity(diffOf(fileDiff("app.py",
		" def f():",
		"+    if x:",
		"+        call(a,",
		"+              b)",
		"+        return 1",
		"+    return 2")), nil)
	if c.MaxDepth != 3 || c.Added != 1+2+3+2+1 {
		t.Fatalf("complexity = %+v; the unit stays 4 when most steps are 4", c)
	}
}

func TestComplexity_LeavesExcludedFilesOut(t *testing.T) {
	c := MeasureComplexity(diffOf(
		fileDiff("go.sum", "+    deep line"),
		fileDiff("pkg/a.go", "+\tx := 1")), func(p string) bool { return p == "go.sum" })
	if c.Added != 1 || len(c.Hotspots) != 1 || c.Hotspots[0].Path != "pkg/a.go" {
		t.Fatalf("complexity = %+v", c)
	}
}

func TestComplexity_ATruncatedDiffCountsWhatArrived(t *testing.T) {
	full := diffOf(fileDiff("a.go", "+\tone", "+\t\ttwo"), fileDiff("b.go", "+\tthree"))
	cut := full[:strings.Index(string(full), "diff --git a/b.go")+len("diff --git a/b.go b/b.go\n--- a/b.go\n")]
	c := MeasureComplexity(cut, nil)
	if c.Added != 3 || len(c.Hotspots) != 1 {
		t.Fatalf("complexity = %+v; the cut file counts nothing, the whole one counts", c)
	}
}

func TestComplexity_ADeletedOnlyDiffHasNegativeNet(t *testing.T) {
	diff := "diff --git a/old.go b/old.go\ndeleted file mode 100644\nindex 1111111..0000000\n--- a/old.go\n+++ /dev/null\n" +
		"@@ -1,4 +0,0 @@\n-func old() {\n-\tif x {\n-\t\ty()\n-\t}\n"
	c := MeasureComplexity([]byte(diff), nil)
	if c.Added != 0 || c.Removed != 0+1+2+1 || c.Net != -4 || c.MaxDepth != 0 || len(c.Hotspots) != 0 {
		t.Fatalf("complexity = %+v", c)
	}
	excluded := MeasureComplexity([]byte(diff), func(p string) bool { return p == "old.go" })
	if excluded.Removed != 0 {
		t.Errorf("a deleted file is named by its old path: %+v", excluded)
	}
}

func TestComplexity_BinaryAndHeaderLinesCountNothing(t *testing.T) {
	diff := "diff --git a/logo.png b/logo.png\nnew file mode 100644\nindex 0000000..1111111\nBinary files /dev/null and b/logo.png differ\n" +
		fileDiff("x.go", "+++counter", "---decrement")
	c := MeasureComplexity([]byte(diff), nil)
	if c.Added != 0 || c.Removed != 0 || len(c.Hotspots) != 0 {
		t.Fatalf("complexity = %+v", c)
	}
}

func TestIndentUnit(t *testing.T) {
	for _, tc := range []struct {
		lines []string
		want  int
	}{
		{[]string{"a", "  b", "    c", "  d"}, 2},
		{[]string{"a", "    b", "        c"}, 4},
		{[]string{"a", "   b", "      c"}, 3},
		{[]string{"            only"}, DefaultIndentUnit},
		{[]string{"\tx", "\t\ty"}, DefaultIndentUnit},
		{[]string{"x", "  y"}, 2},
	} {
		if got := indentUnit(tc.lines); got != tc.want {
			t.Errorf("indentUnit(%q) = %d; want %d", tc.lines, got, tc.want)
		}
	}
}
