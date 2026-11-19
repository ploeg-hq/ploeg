package openhands

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

const hostileHooks = `{"SessionStart":[{"matcher":"*","hooks":[{"type":"command","command":"touch /tmp/ploeg-hooks-ran"}]}]}` + "\n"

func TestSetAside_MovesTheShippedHooksFileOutAndRestoresItByteForByte(t *testing.T) {
	repo := gitRepository(t, map[string]string{RepositoryHooksPath: hostileHooks, ".openhands/agents/reviewer.md": "# reviewer\n"})
	if err := os.Chmod(filepath.Join(repo, RepositoryHooksPath), 0o640); err != nil {
		t.Fatal(err)
	}

	hooks, err := SetAsideRepositoryHooks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if hooks.Shipped() != RepositoryHooksPath {
		t.Errorf("Shipped() = %q, want %q", hooks.Shipped(), RepositoryHooksPath)
	}
	if _, err := os.Lstat(filepath.Join(repo, RepositoryHooksPath)); !os.IsNotExist(err) {
		t.Fatalf("the hooks file is still in the clone while the harness runs: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".openhands", "agents", "reviewer.md")); err != nil {
		t.Errorf("setting the hooks aside took the rest of .openhands with it: %v", err)
	}
	git(t, repo, "add", "--all")
	git(t, repo, "commit", "--all", "--allow-empty", "-qm", "the agent's commit")
	if got := git(t, repo, "show", "HEAD:"+RepositoryHooksPath); got+"\n" != hostileHooks {
		t.Errorf("the agent's git add --all and commit -a changed the hooks file in the commit: %q", got)
	}

	written, err := hooks.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 0 {
		t.Errorf("Restore reported agent-written hooks %q on a Run that wrote none", written)
	}
	got, err := os.ReadFile(filepath.Join(repo, RepositoryHooksPath))
	if err != nil || string(got) != hostileHooks {
		t.Fatalf("the restored hooks file = %q (%v), want the shipped bytes", got, err)
	}
	if info, _ := os.Stat(filepath.Join(repo, RepositoryHooksPath)); info.Mode().Perm() != 0o640 {
		t.Errorf("the restored hooks file has mode %v, want 0640", info.Mode().Perm())
	}
}

func TestSetAside_RemovesAHooksFileTheAgentWritesBeforeTheNextSessionAndAtTheEnd(t *testing.T) {
	for name, shipped := range map[string]map[string]string{
		"a repository without hooks": {"main.go": "package main\n"},
		"a repository with hooks":    {RepositoryHooksPath: hostileHooks},
	} {
		t.Run(name, func(t *testing.T) {
			repo := gitRepository(t, shipped)
			hooks, err := SetAsideRepositoryHooks(repo)
			if err != nil {
				t.Fatal(err)
			}
			plant(t, repo, `{"SessionStart":[{"hooks":[{"type":"command","command":"touch planted"}]}]}`)
			removed, err := hooks.BeforeSession()
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(removed, []string{RepositoryHooksPath}) {
				t.Errorf("BeforeSession removed %q, want the agent's hooks file", removed)
			}
			if _, err := os.Lstat(filepath.Join(repo, RepositoryHooksPath)); !os.IsNotExist(err) {
				t.Fatalf("a session would start with the agent's hooks file in the clone: %v", err)
			}
			plant(t, repo, `{"Stop":[]}`)

			written, err := hooks.Restore()
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(written, []string{RepositoryHooksPath}) {
				t.Errorf("Restore reported %q, want the agent's hooks file", written)
			}
			got, err := os.ReadFile(filepath.Join(repo, RepositoryHooksPath))
			want, isShipped := shipped[RepositoryHooksPath]
			switch {
			case isShipped && (err != nil || string(got) != want):
				t.Errorf("the restored hooks file = %q (%v), want the shipped bytes", got, err)
			case !isShipped && !os.IsNotExist(err):
				t.Errorf("the agent's hooks file survived the Run: %q (%v)", got, err)
			}
		})
	}
}

func TestSetAside_NeverWritesThroughALinkTheAgentLeft(t *testing.T) {
	repo := gitRepository(t, map[string]string{RepositoryHooksPath: hostileHooks})
	outside := t.TempDir()
	hooks, err := SetAsideRepositoryHooks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(repo, RepositoryHooksDir)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, RepositoryHooksDir)); err != nil {
		t.Fatal(err)
	}

	written, err := hooks.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(written, RepositoryHooksDir) {
		t.Errorf("Restore reported %q, want the agent's .openhands link", written)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("Restore wrote through the agent's link to %s: %v", outside, entries)
	}
	if got, err := os.ReadFile(filepath.Join(repo, RepositoryHooksPath)); err != nil || string(got) != hostileHooks {
		t.Errorf("the restored hooks file = %q (%v), want the shipped bytes", got, err)
	}
}

func TestSetAside_FollowsOpenHandsLookup(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "hooks.json")
	if err := os.WriteFile(outside, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		plant   func(t *testing.T, repo string)
		shipped string
		refuses bool
	}{
		{name: "no .openhands directory", plant: func(*testing.T, string) {}},
		{name: ".openhands without hooks", plant: func(t *testing.T, repo string) { mkdir(t, repo, ".openhands") }},
		{name: "a hooks file", plant: func(t *testing.T, repo string) {
			mkdir(t, repo, ".openhands")
			write(t, filepath.Join(repo, ".openhands", "hooks.json"))
		}, shipped: RepositoryHooksPath},
		{name: "a hooks symlink to a file outside the clone", plant: func(t *testing.T, repo string) {
			mkdir(t, repo, ".openhands")
			symlink(t, outside, filepath.Join(repo, ".openhands", "hooks.json"))
		}, shipped: RepositoryHooksPath},
		{name: "a .openhands symlink to a directory with hooks", plant: func(t *testing.T, repo string) {
			symlink(t, filepath.Dir(outside), filepath.Join(repo, ".openhands"))
		}, shipped: RepositoryHooksDir},
		{name: "a dangling hooks symlink", plant: func(t *testing.T, repo string) {
			mkdir(t, repo, ".openhands")
			symlink(t, filepath.Join(repo, "missing"), filepath.Join(repo, ".openhands", "hooks.json"))
		}, shipped: RepositoryHooksPath},
		{name: ".openhands is a file", plant: func(t *testing.T, repo string) { write(t, filepath.Join(repo, ".openhands")) }},
		{name: "a hooks directory", plant: func(t *testing.T, repo string) { mkdir(t, repo, ".openhands/hooks.json") }, refuses: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			tc.plant(t, repo)
			before := snapshot(t, repo)
			hooks, err := SetAsideRepositoryHooks(repo)
			if got := errors.Is(err, ErrRepositoryHooks); got != tc.refuses {
				t.Fatalf("refused = %v (%v), want %v", got, err, tc.refuses)
			}
			if tc.refuses {
				return
			}
			if hooks.Shipped() != tc.shipped {
				t.Errorf("Shipped() = %q, want %q", hooks.Shipped(), tc.shipped)
			}
			if _, err := os.Stat(filepath.Join(repo, RepositoryHooksPath)); err == nil {
				t.Errorf("OpenHands would still find %s while the harness runs", RepositoryHooksPath)
			}
			if _, err := hooks.Restore(); err != nil {
				t.Fatal(err)
			}
			if after := snapshot(t, repo); after != before {
				t.Errorf("the clone after Restore:\n%s\nwant as shipped:\n%s", after, before)
			}
		})
	}
	if hooks, err := SetAsideRepositoryHooks(""); err != nil || hooks.Shipped() != "" {
		t.Errorf("a Run without a clone: %v, %q", err, hooks.Shipped())
	}
}

func TestRunner_RunsTheHarnessWithoutTheHooksFileAndPutsItBack(t *testing.T) {
	repo := gitRepository(t, map[string]string{RepositoryHooksPath: hostileHooks})
	seen := filepath.Join(t.TempDir(), "seen")
	bin := filepath.Join(t.TempDir(), "entrypoint.sh")
	script := "#!/bin/sh\nif [ -e " + RepositoryHooksPath + " ]; then echo present > " + seen + "; else echo absent > " + seen + "; fi\n" +
		"printf '{}' > " + RepositoryHooksPath + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := NewRunner(bin).Run(context.Background(), harness.TaskSpec{TraceID: "t1"},
		harness.RunEnv{RepoDir: repo, ScratchDir: t.TempDir(), Prompt: "p", BaseEnv: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(seen); strings.TrimSpace(string(got)) != "absent" {
		t.Errorf("the harness saw the repository's hooks file: %q", got)
	}
	if got, err := os.ReadFile(filepath.Join(repo, RepositoryHooksPath)); err != nil || string(got) != hostileHooks {
		t.Errorf("after the Run the hooks file = %q (%v), want the shipped bytes", got, err)
	}
}

func gitRepository(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	repo := t.TempDir()
	for name, body := range files {
		path := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "init", "-q")
	git(t, repo, "add", "--all")
	git(t, repo, "commit", "-qm", "shipped")
	return repo
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func plant(t *testing.T, repo, body string) {
	t.Helper()
	mkdir(t, repo, RepositoryHooksDir)
	if err := os.WriteFile(filepath.Join(repo, RepositoryHooksPath), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func snapshot(t *testing.T, repo string) string {
	t.Helper()
	var b bytes.Buffer
	err := filepath.Walk(repo, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(repo, path)
		b.WriteString(rel + " " + info.Mode().String())
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, _ := os.Readlink(path)
			b.WriteString(" -> " + target)
		case info.Mode().IsRegular():
			body, _ := os.ReadFile(path)
			b.WriteString(" " + string(body))
		}
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
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
