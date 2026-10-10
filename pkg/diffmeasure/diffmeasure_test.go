package diffmeasure

import (
	"reflect"
	"testing"
)

const diff = `diff --git a/pkg/a.go b/pkg/a.go
--- a/pkg/a.go
+++ b/pkg/a.go
@@ -1,3 +1,5 @@
 func a() {
-	return
+	if x {
+		return
+	}
 }
diff --git a/web/b.ts b/web/b.ts
--- /dev/null
+++ b/web/b.ts
@@ -0,0 +1,3 @@
+function b() {
+  if (x) {
+
+    return 1
+  }
+}
diff --git a/old.txt b/old.txt
--- a/old.txt
+++ /dev/null
@@ -1 +0,0 @@
-        gone
Binary files a/logo.png and b/logo.png differ
diff --git a/logo.png b/logo.png
`

func TestIndentation_MeasuresEveryFile(t *testing.T) {
	got := Indentation([]byte(diff))
	want := []File{
		{Path: "pkg/a.go", Unit: DefaultIndentUnit, Added: 1 + 2 + 1, Removed: 1, MaxDepth: 2},
		{Path: "web/b.ts", Unit: 2, Added: 0 + 1 + 2 + 1 + 0, Removed: 0, MaxDepth: 2},
		{Path: "old.txt", Unit: DefaultIndentUnit, Added: 0, Removed: 2, MaxDepth: 0},
		{Path: "logo.png", Unit: DefaultIndentUnit},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Indentation =\n%+v\nwant\n%+v", got, want)
	}
}

func TestIndentation_EmptyDiffHasNoFiles(t *testing.T) {
	if got := Indentation(nil); len(got) != 0 {
		t.Fatalf("Indentation(nil) = %+v", got)
	}
}
