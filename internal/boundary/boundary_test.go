// Package boundary holds the repository-wide check that Ploeg names none of
// its consumers (ADR-0069).
package boundary

import (
	"bytes"
	"fmt"
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
	mentions, err := consumerMentions(root(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, mention := range mentions {
		t.Errorf("%s; describe it as an operator consumer instead", mention)
	}
}

func TestAGitFileNamingTheSuperprojectIsNotAMention(t *testing.T) {
	base := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(base, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(".git", "gitdir: /src/unfold/.git/modules/apps/ploeg\n")
	write("README.md", "Ploeg serves any operator consumer.\n")

	mentions, err := consumerMentions(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(mentions) != 0 {
		t.Fatalf("a submodule's .git file was scanned: %v", mentions)
	}

	write("README.md", "Ploeg serves Unfold.\n")
	mentions, err = consumerMentions(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(mentions) != 1 {
		t.Fatalf("mentions = %v, want the README line", mentions)
	}
}

func consumerMentions(base string) ([]string, error) {
	var mentions []string
	err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, base+string(filepath.Separator)))
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "node_modules", ".venv", "vendor":
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
				mentions = append(mentions, fmt.Sprintf("%s:%d names a consumer (%q)", rel, number+1, match))
			}
		}
		return nil
	})
	return mentions, err
}
