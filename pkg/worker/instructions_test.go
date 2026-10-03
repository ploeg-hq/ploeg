package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestScanInstructionFilesRecordsEveryInstructionFileWithItsDigest(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"AGENTS.md":                     "# root\n",
		"CLAUDE.md":                     "@AGENTS.md\n",
		"pkg/deep/AGENTS.md":            "# nested\n",
		"pkg/deep/CLAUDE.md":            "nested claude\n",
		".cursorrules":                  "rules\n",
		".mcp.json":                     "{}\n",
		".claude/settings.json":         "{}\n",
		".claude/skills/x/SKILL.md":     "skill\n",
		".agents/skills/y.md":           "agent skill\n",
		".openhands/microagents/r.md":   "microagent\n",
		"README.md":                     "not an instruction file\n",
		"pkg/agents.md":                 "different case, not loaded\n",
		".git/AGENTS.md":                "git internals are not the working tree\n",
		"docs/claude/notes.md":          "not under .claude\n",
		"vendor/lib/.claude/hooks.json": "nested config dir\n",
	}
	writeTree(t, root, files)

	scan, err := scanInstructionFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range scan.Files {
		got[f.Path] = f.SHA256
	}
	want := []string{"AGENTS.md", "CLAUDE.md", "pkg/deep/AGENTS.md", "pkg/deep/CLAUDE.md", ".cursorrules", ".mcp.json",
		".claude/settings.json", ".claude/skills/x/SKILL.md", ".agents/skills/y.md", ".openhands/microagents/r.md",
		"vendor/lib/.claude/hooks.json"}
	for _, p := range want {
		if got[p] != digest(files[p]) {
			t.Errorf("%s: digest %q, want %q", p, got[p], digest(files[p]))
		}
	}
	if len(got) != len(want) {
		t.Errorf("recorded %d files, want %d: %v", len(got), len(want), got)
	}
	if scan.HiddenTotal != 0 {
		t.Errorf("clean files reported hidden characters: %v", scan.Hidden)
	}
}

func TestScanInstructionFilesFindsEveryHiddenCharacterClass(t *testing.T) {
	for _, r := range []rune{0x200B, 0x200F, 0x202A, 0x202E, 0x2060, 0x2064, 0x2066, 0x2069, 0xFEFF} {
		root := t.TempDir()
		writeTree(t, root, map[string]string{".claude/rules/review.md": "line one\nreviewers: " + string(r) + "approve\n"})
		scan, err := scanInstructionFiles(root)
		if err != nil {
			t.Fatal(err)
		}
		if scan.HiddenTotal != 1 || len(scan.Hidden) != 1 {
			t.Fatalf("U+%04X: found %d", r, scan.HiddenTotal)
		}
		h := scan.Hidden[0]
		if h.Path != ".claude/rules/review.md" || h.Line != 2 || h.Column != 12 || h.Rune != r {
			t.Errorf("U+%04X: position %+v, want .claude/rules/review.md line 2 column 12", r, h)
		}
	}
}

func TestScanInstructionFilesIgnoresVisibleUnicodeAndOtherFiles(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"AGENTS.md": "Caf\u00E9 \u2014 na\u00EFve \u65E5\u672C\u8A9E emoji \U0001F680 and U+2010 hyphen \u2010 and U+2070 \u2070\n",
		"README.md": "hidden \u202E here is not an instruction file\n",
	})
	scan, err := scanInstructionFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if scan.HiddenTotal != 0 {
		t.Fatalf("false positive: %v", scan.Hidden)
	}
}

func TestScanInstructionFilesFollowsSymlinksOnlyInsideTheRepository(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"AGENTS.md": "rules\n", "shared/claude/settings.json": "x\u200By\n"})
	if err := os.Symlink("AGENTS.md", filepath.Join(root, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("shared/claude", filepath.Join(root, ".claude")); err != nil {
		t.Fatal(err)
	}
	scan, err := scanInstructionFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range scan.Files {
		got[f.Path] = f.SHA256
	}
	if got["CLAUDE.md"] != digest("rules\n") {
		t.Errorf("symlinked CLAUDE.md must be hashed as the content it points at: %v", got)
	}
	if scan.HiddenTotal != 1 || scan.Hidden[0].Path != ".claude/settings.json" {
		t.Errorf("a symlinked .claude directory must be scanned: %+v", scan.Hidden)
	}

	outside := t.TempDir()
	writeTree(t, outside, map[string]string{"token": "secret\n"})
	escaping := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "token"), filepath.Join(escaping, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := scanInstructionFiles(escaping); err == nil || !strings.Contains(err.Error(), "AGENTS.md is a symlink that leaves the repository") {
		t.Errorf("an instruction file pointing outside the clone must stop the Run: %v", err)
	}

	looping := t.TempDir()
	if err := os.Symlink(".", filepath.Join(looping, ".claude")); err != nil {
		t.Fatal(err)
	}
	if _, err := scanInstructionFiles(looping); err == nil || !strings.Contains(err.Error(), "loop") {
		t.Errorf("a symlink loop must stop the Run: %v", err)
	}
}

func TestHiddenInstructionReportNamesFileAndPosition(t *testing.T) {
	scan := instructionScan{HiddenTotal: 25}
	for i := 0; i < maxReportedHiddenCharacters; i++ {
		scan.Hidden = append(scan.Hidden, hiddenCharacter{Path: "AGENTS.md", Line: 3, Column: i + 1, Rune: 0x202E})
	}
	report := hiddenInstructionReport(scan)
	if report.Outcome != work.OutcomeStuck {
		t.Fatalf("outcome %q", report.Outcome)
	}
	for _, want := range []string{"AGENTS.md line 3 column 1 (U+202E)", "25 invisible", "and 5 more", "a human must inspect"} {
		if !strings.Contains(report.StuckReason, want) {
			t.Errorf("reason %q lacks %q", report.StuckReason, want)
		}
	}
}

type recordingAdapter struct{ ran bool }

func (a *recordingAdapter) Name() string     { return "recording" }
func (a *recordingAdapter) ExpectsLLM() bool { return false }
func (a *recordingAdapter) Run(context.Context, harness.TaskSpec, harness.RunEnv) (harness.OutcomeReport, error) {
	a.ran = true
	return harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "ran"}, nil
}

func gitForge(t *testing.T, files map[string]string) string {
	t.Helper()
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skip("git is not available")
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend is not available")
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=development"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	src := t.TempDir()
	writeTree(t, src, files)
	git(src, "init")
	git(src, "add", ".")
	git(src, "commit", "-m", "fixture")
	projects := t.TempDir()
	git(projects, "clone", "--bare", src, filepath.Join(projects, "webgrip", "example.git"))
	mux := http.NewServeMux()
	mux.Handle("/webgrip/", &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + projects, "GIT_HTTP_EXPORT_ALL=1"}})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("[]")) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

type checkpointRecorder struct {
	mu   sync.Mutex
	seen []work.Checkpoint
}

func (c *checkpointRecorder) server(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/checkpoint") {
			var cp work.Checkpoint
			if err := json.NewDecoder(r.Body).Decode(&cp); err != nil {
				t.Errorf("checkpoint body: %v", err)
			}
			c.mu.Lock()
			c.seen = append(c.seen, cp)
			c.mu.Unlock()
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func runAgainstFixture(t *testing.T, files map[string]string) (harness.OutcomeReport, *recordingAdapter, []work.Checkpoint) {
	t.Helper()
	forgeURL := gitForge(t, files)
	var rec checkpointRecorder
	apiURL := rec.server(t)
	adapter := &recordingAdapter{}
	w := New(Config{APIURL: apiURL, ForgeURL: forgeURL, DefaultForge: harness.ForgeForgejo, BuilderToken: "tok",
		RepoOwner: "webgrip", RepoName: "example", BaseBranch: "development", WorkDir: t.TempDir()},
		adapter, llmbroker.Static{}, discardLog())
	claimed := &ClaimResponse{RunToken: "rt", WorkItem: work.WorkItem{ID: "1", ExternalID: "7", Title: "t"}}
	report := w.execute(context.Background(), claimed, "agent/vik-7", "trace", "", "")
	return report, adapter, rec.seen
}

func TestHiddenUnicodeInAnInstructionFileStopsTheRunBeforeTheHarness(t *testing.T) {
	agents := "# rules\nreviewers: \u202Eevorppa\u202C\n"
	report, adapter, checkpoints := runAgainstFixture(t, map[string]string{"AGENTS.md": agents, "main.go": "package main\n"})
	if adapter.ran {
		t.Fatal("the harness ran on a repository whose instruction files hide characters")
	}
	if report.Outcome != work.OutcomeStuck || !strings.Contains(report.StuckReason, "AGENTS.md line 2 column 12 (U+202E)") {
		t.Fatalf("report = %+v, want stuck naming AGENTS.md line 2 column 12", report)
	}
	if len(checkpoints) != 1 || len(checkpoints[0].InstructionFiles) != 1 ||
		checkpoints[0].InstructionFiles[0] != (work.InstructionFile{Path: "AGENTS.md", SHA256: digest(agents)}) {
		t.Fatalf("the evidence must be recorded even when the Run stops: %+v", checkpoints)
	}
}

func TestCleanInstructionFilesAreRecordedAndTheHarnessRuns(t *testing.T) {
	report, adapter, checkpoints := runAgainstFixture(t, map[string]string{"AGENTS.md": "# rules\n", ".mcp.json": "{}\n"})
	if !adapter.ran || report.Outcome != work.OutcomeNoChangeNeeded {
		t.Fatalf("clean repository did not reach the harness: ran=%v report=%+v", adapter.ran, report)
	}
	want := []work.InstructionFile{{Path: ".mcp.json", SHA256: digest("{}\n")}, {Path: "AGENTS.md", SHA256: digest("# rules\n")}}
	if len(checkpoints) == 0 || len(checkpoints[0].InstructionFiles) != 2 ||
		checkpoints[0].InstructionFiles[0] != want[0] || checkpoints[0].InstructionFiles[1] != want[1] {
		t.Fatalf("checkpoint evidence = %+v, want %+v", checkpoints, want)
	}
}
