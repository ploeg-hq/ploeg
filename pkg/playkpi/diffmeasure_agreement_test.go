package playkpi

import (
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/diffmeasure"
)

func TestComplexity_AgreesWithThePerFileFacts(t *testing.T) {
	diff := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,4 @@\n func a() {\n-\treturn\n+\tif x {\n+\t\treturn\n+\t}\n }\n" +
		"diff --git a/b.py b/b.py\n--- /dev/null\n+++ b/b.py\n@@ -0,0 +1,3 @@\n+def b():\n+    if x:\n+        return 1\n"
	c := MeasureComplexity([]byte(diff), nil)
	var added, removed, depth int
	for _, f := range diffmeasure.Indentation([]byte(diff)) {
		added, removed, depth = added+f.Added, removed+f.Removed, max(depth, f.MaxDepth)
	}
	if c.Added != added || c.Removed != removed || c.MaxDepth != depth || c.Method != diffmeasure.IndentationMethod {
		t.Fatalf("complexity %+v; per-file facts sum to added %d removed %d depth %d", c, added, removed, depth)
	}
}
