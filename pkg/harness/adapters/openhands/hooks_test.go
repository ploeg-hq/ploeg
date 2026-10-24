package openhands

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

func TestPrepare_RefusesARepositoryThatShipsHooks(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".openhands"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".openhands", "hooks.json"), []byte(`{"SessionStart":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	_, err := New("").Prepare(harness.TaskSpec{}, harness.RunEnv{RepoDir: repo, ScratchDir: scratch, Prompt: "p"})
	if !errors.Is(err, ErrRepositoryHooks) {
		t.Fatalf("Prepare on a repository with %s = %v, want ErrRepositoryHooks", RepositoryHooksPath, err)
	}
	if _, statErr := os.Stat(filepath.Join(scratch, "task.md")); !os.IsNotExist(statErr) {
		t.Errorf("Prepare wrote the task file before refusing: %v", statErr)
	}
}

func TestRefuseRepositoryHooks_FollowsOpenHandsLookup(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "hooks.json")
	if err := os.WriteFile(outside, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		plant   func(t *testing.T, repo string)
		refuses bool
	}{
		{"no .openhands directory", func(*testing.T, string) {}, false},
		{".openhands without hooks", func(t *testing.T, repo string) { mkdir(t, repo, ".openhands") }, false},
		{"a hooks file", func(t *testing.T, repo string) {
			mkdir(t, repo, ".openhands")
			write(t, filepath.Join(repo, ".openhands", "hooks.json"))
		}, true},
		{"a hooks symlink to a file outside the clone", func(t *testing.T, repo string) {
			mkdir(t, repo, ".openhands")
			symlink(t, outside, filepath.Join(repo, ".openhands", "hooks.json"))
		}, true},
		{"a .openhands symlink to a directory with hooks", func(t *testing.T, repo string) {
			symlink(t, filepath.Dir(outside), filepath.Join(repo, ".openhands"))
		}, true},
		{"a dangling hooks symlink", func(t *testing.T, repo string) {
			mkdir(t, repo, ".openhands")
			symlink(t, filepath.Join(repo, "missing"), filepath.Join(repo, ".openhands", "hooks.json"))
		}, false},
		{".openhands is a file", func(t *testing.T, repo string) { write(t, filepath.Join(repo, ".openhands")) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			tc.plant(t, repo)
			err := RefuseRepositoryHooks(repo)
			if got := errors.Is(err, ErrRepositoryHooks); got != tc.refuses {
				t.Errorf("refused = %v (%v), want %v", got, err, tc.refuses)
			}
		})
	}
	if err := RefuseRepositoryHooks(""); err != nil {
		t.Errorf("a Run without a clone was refused: %v", err)
	}
}

func mkdir(t *testing.T, repo, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(repo, name), 0o755); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}
