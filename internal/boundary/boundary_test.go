// Package boundary holds the repository-wide check that Ploeg names none of
// its consumers (ADR-0069).
package boundary

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// downstream matches the names of products that consume Ploeg. Ploeg knows
// its consumers only through its published contracts, so these names never
// appear outside dated records.
var downstream = regexp.MustCompile(`(?i)\b(vloer|unfold|unfoldhq)\b|webgrip/glide|(?:^|[^/\w])apps/ploeg/`)

// records are dated or append-only files that keep the words of their time.
var records = []string{
	"CHANGELOG.md",
	"PROVENANCE.md",
	"docs/adrs/",
	"docs/research/",
	"openspec/changes/",
	"pkg/store/migrations/",
	"internal/boundary/",
}

func root(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test")
		}
		dir = parent
	}
}

func isRecord(path string) bool {
	for _, prefix := range records {
		if path == prefix || strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func TestPloegNamesNoConsumer(t *testing.T) {
	base := root(t)
	err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, base+string(filepath.Separator)))
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", ".venv", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if isRecord(rel) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
			return nil
		}
		for number, line := range strings.Split(string(data), "\n") {
			if match := downstream.FindString(line); match != "" {
				t.Errorf("%s:%d names a consumer (%q); describe it as an operator consumer instead", rel, number+1, match)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
