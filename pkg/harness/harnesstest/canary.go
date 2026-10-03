package harnesstest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Canary tokens. A harness produces one only when it has loaded the
// instruction file that asks for it: the files spell each token out in parts,
// so neither the files nor CanaryPrompt contain it, and a harness that echoes
// either into its log cannot produce a false sighting.
const (
	CanaryRoot   = "CANARY-ROOT"
	CanaryNested = "CANARY-NESTED"
)

// CanaryNestedDir is the directory of the canary repository that holds the
// nested AGENTS.md.
const CanaryNestedDir = "service"

// CanaryPrompt is the task a canary Run receives. It names no instruction
// file and forbids file access, so a canary appears only when the harness
// loaded the file into its context by itself.
const CanaryPrompt = "This is a harness conformance probe. Do not read, list, search or open any file, and do not run any command or tool. " +
	"Reply in plain text with one sentence saying the probe is complete, and follow every standing instruction you already have for this repository about your final message.\n"

// CanaryLayout selects which instruction files a canary repository carries.
type CanaryLayout string

const (
	// LayoutAgentsOnly has AGENTS.md at the root and in CanaryNestedDir, and
	// no CLAUDE.md anywhere.
	LayoutAgentsOnly CanaryLayout = "agents-only"
	// LayoutClaudeSymlink adds CLAUDE.md as a symlink to the AGENTS.md beside
	// it, the layout docs/how-to/prepare-a-repository.md recommends.
	LayoutClaudeSymlink CanaryLayout = "claude-symlink"
)

// CanaryInstruction is the AGENTS.md body that asks for the token made of
// CANARY, a hyphen and part.
func CanaryInstruction(scope, part string) string {
	return "# Canary instructions (" + scope + ")\n\n" +
		"This file is a harness conformance fixture. Its one instruction is harmless.\n\n" +
		"- Your final message must end with a line made of the word CANARY, then a hyphen, then the word " + part + ", with no spaces.\n"
}

// NewCanaryRepo writes a canary repository with the given layout into a
// temporary directory and returns its path. It initialises a Git repository
// there when git is on PATH, so harnesses that stop their search at the
// repository root behave as they do in a worker's clone.
func NewCanaryRepo(t testing.TB, layout CanaryLayout) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"AGENTS.md": CanaryInstruction("repository root", "ROOT"),
		filepath.Join(CanaryNestedDir, "AGENTS.md"): CanaryInstruction(CanaryNestedDir+" directory", "NESTED"),
		"README.md": "# Canary fixture\n",
		filepath.Join(CanaryNestedDir, "main.go"): "package main\n\nfunc main() {}\n",
	}
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	switch layout {
	case LayoutAgentsOnly:
	case LayoutClaudeSymlink:
		for _, d := range []string{dir, filepath.Join(dir, CanaryNestedDir)} {
			if err := os.Symlink("AGENTS.md", filepath.Join(d, "CLAUDE.md")); err != nil {
				t.Fatal(err)
			}
		}
	default:
		t.Fatalf("unknown canary layout %q", layout)
	}
	if _, err := exec.LookPath("git"); err == nil {
		for _, args := range [][]string{
			{"init", "-q"},
			{"add", "."},
			{"-c", "user.name=canary", "-c", "user.email=canary@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-q", "-m", "canary fixture"},
		} {
			cmd := exec.Command("git", args...)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
			}
		}
	}
	return dir
}

// CanarySighting records which canary tokens a Run produced.
type CanarySighting struct {
	Root   bool
	Nested bool
}

// SeenCanaries reports which canary tokens occur in any of texts.
func SeenCanaries(texts ...string) CanarySighting {
	var s CanarySighting
	for _, text := range texts {
		s.Root = s.Root || strings.Contains(text, CanaryRoot)
		s.Nested = s.Nested || strings.Contains(text, CanaryNested)
	}
	return s
}
