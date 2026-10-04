package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContextInspect(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{"docs/a.md": "# A\n", "../escape": "x"} {
		f, _ := zw.Create(name)
		_, _ = f.Write([]byte(body))
	}
	_ = zw.Close()
	bad := filepath.Join(dir, "bad.zip")
	good := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(bad, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(good, []byte("# Notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{"context", "inspect", good}, &out); err != nil || !strings.Contains(out.String(), "notes.md: text/markdown, 1 files") {
		t.Errorf("inspect notes.md: %v\n%s", err, out.String())
	}
	if err := run([]string{"context", "inspect", bad}, &out); err == nil || !strings.Contains(err.Error(), "path_traversal") {
		t.Errorf("inspect bad.zip: %v", err)
	}
}
