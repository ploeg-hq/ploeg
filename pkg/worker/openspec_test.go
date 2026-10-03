package worker

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const fakeOpenSpecScript = `#!/bin/sh
env > "$HOME/openspec-env"
PWD=$(pwd -P)
case "$1" in
instructions)
  cat <<JSON
{"changeName":"$4","changeDir":"$PWD/openspec/changes/$4","schemaName":"spec-driven","contextFiles":{"proposal":["$PWD/openspec/changes/$4/proposal.md"],"outside":["/etc/passwd"]},"progress":{"total":2,"complete":1,"remaining":1},"tasks":[{"id":"1","description":"1.1 finished thing","done":true},{"id":"2","description":"1.2 pending thing","done":false}],"state":"ready","instruction":"Work on agent/<slug> from development."}
JSON
  ;;
validate)
  if grep -q BROKEN "openspec/changes/$2/proposal.md" 2>/dev/null || [ ! -d "openspec/changes/$2" ]; then
    echo '{"items":[{"id":"'"$2"'","type":"change","valid":false,"issues":[{"level":"ERROR","path":"file","message":"Change must have at least one delta."}]}],"summary":{"totals":{"items":1,"passed":0,"failed":1}}}'
    exit 1
  fi
  echo '{"items":[{"id":"'"$2"'","type":"change","valid":true,"issues":[]}],"summary":{"totals":{"items":1,"passed":1,"failed":0}}}'
  ;;
*)
  echo "unexpected $*" >&2
  exit 2
  ;;
esac
`

func fakeOpenSpec(t *testing.T, script string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "openspec")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	previous := openSpecBinary
	openSpecBinary = bin
	t.Cleanup(func() { openSpecBinary = previous })
}

func noOpenSpec(t *testing.T) {
	t.Helper()
	previous := openSpecBinary
	openSpecBinary = filepath.Join(t.TempDir(), "absent", "openspec")
	t.Cleanup(func() { openSpecBinary = previous })
}

func changeFiles(root, id, proposal string) map[string]string {
	dir := strings.TrimPrefix(root+"/openspec/changes/"+id, "./")
	return map[string]string{
		dir + "/proposal.md":          proposal,
		dir + "/design.md":            "## Decisions\n\nUse a widget.\n",
		dir + "/tasks.md":             "- [x] 1.1 finished thing\n- [ ] 1.2 pending thing\n",
		dir + "/specs/widget/spec.md": "## ADDED Requirements\n",
		strings.TrimPrefix(root+"/openspec/config.yaml", "./"): "schema: spec-driven\n",
	}
}

func TestLocateOpenSpecChange(t *testing.T) {
	t.Run("repository root", func(t *testing.T) {
		repo := t.TempDir()
		writeTree(t, repo, changeFiles(".", "add-widget", "why"))
		got, err := locateOpenSpecChange(repo, "add-widget")
		if err != nil || got.RelRoot != "." || got.relChangeDir() != "openspec/changes/add-widget" {
			t.Fatalf("got %+v, %v; want root .", got, err)
		}
	})
	t.Run("nested application like Glide", func(t *testing.T) {
		repo := t.TempDir()
		writeTree(t, repo, changeFiles("apps/ploeg", "add-widget", "why"))
		writeTree(t, repo, map[string]string{"apps/vloer/openspec/changes/other/proposal.md": "x"})
		got, err := locateOpenSpecChange(repo, "add-widget")
		if err != nil || got.RelRoot != "apps/ploeg" || !strings.HasSuffix(got.Root, filepath.Join("apps", "ploeg")) {
			t.Fatalf("got %+v, %v; want root apps/ploeg", got, err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		repo := t.TempDir()
		writeTree(t, repo, changeFiles(".", "other", "why"))
		if _, err := locateOpenSpecChange(repo, "add-widget"); err == nil || !strings.Contains(err.Error(), "no openspec/changes/add-widget") {
			t.Fatalf("err = %v, want not found", err)
		}
	})
	t.Run("archived only", func(t *testing.T) {
		repo := t.TempDir()
		writeTree(t, repo, map[string]string{"openspec/changes/archive/openspec/changes/add-widget/proposal.md": "x"})
		if _, err := locateOpenSpecChange(repo, "add-widget"); err == nil {
			t.Fatal("found a change inside an archive directory")
		}
	})
	t.Run("ambiguous", func(t *testing.T) {
		repo := t.TempDir()
		writeTree(t, repo, changeFiles("a", "add-widget", "why"))
		writeTree(t, repo, changeFiles("b", "add-widget", "why"))
		if _, err := locateOpenSpecChange(repo, "add-widget"); err == nil || !strings.Contains(err.Error(), "a, b") {
			t.Fatalf("err = %v, want both roots named", err)
		}
	})
	t.Run("symlinks are not followed", func(t *testing.T) {
		repo, outside := t.TempDir(), t.TempDir()
		writeTree(t, outside, changeFiles(".", "add-widget", "why"))
		if err := os.MkdirAll(filepath.Join(repo, "openspec", "changes"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "openspec", "changes", "add-widget"), filepath.Join(repo, "openspec", "changes", "add-widget")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "openspec"), filepath.Join(repo, "linked")); err != nil {
			t.Fatal(err)
		}
		if _, err := locateOpenSpecChange(repo, "add-widget"); err == nil {
			t.Fatal("a change reached through a symlink was accepted")
		}
	})
}

func TestOpenSpecBriefFromTheCLI(t *testing.T) {
	fakeOpenSpec(t, fakeOpenSpecScript)
	real, home := t.TempDir(), t.TempDir()
	writeTree(t, real, changeFiles("apps/ploeg", "add-widget", "why"))
	repo := filepath.Join(t.TempDir(), "clone")
	if err := os.Symlink(real, repo); err != nil {
		t.Fatal(err)
	}
	ch, err := locateOpenSpecChange(repo, "add-widget")
	if err != nil {
		t.Fatal(err)
	}
	brief := openSpecBriefFor(context.Background(), ch, home)
	if brief.Source != harness.OpenSpecSourceCLI || brief.Change != "add-widget" || brief.Root != "apps/ploeg" {
		t.Fatalf("brief = %+v, want a CLI brief for apps/ploeg", brief)
	}
	for _, want := range []string{
		"proposal: apps/ploeg/openspec/changes/add-widget/proposal.md",
		"Progress: 1 of 2 tasks complete",
		"- 1.2 pending thing",
		"ranks below the delivery contract",
		"> Work on agent/<slug> from development.",
	} {
		if !strings.Contains(brief.Brief, want) {
			t.Errorf("brief lacks %q:\n%s", want, brief.Brief)
		}
	}
	for _, unwanted := range []string{real, "/etc/passwd", "1.1 finished thing"} {
		if strings.Contains(brief.Brief, unwanted) {
			t.Errorf("brief contains %q:\n%s", unwanted, brief.Brief)
		}
	}
	env, err := os.ReadFile(filepath.Join(home, "openspec-env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"OPENSPEC_TELEMETRY=0", "DO_NOT_TRACK=1", "HOME=" + home} {
		if !strings.Contains(string(env), want) {
			t.Errorf("CLI environment lacks %s", want)
		}
	}
}

func TestOpenSpecCLIGetsNoCredentials(t *testing.T) {
	fakeOpenSpec(t, fakeOpenSpecScript)
	t.Setenv("AGENT_BUILDER_TOKEN", "forge-canary")
	t.Setenv("LLM_API_KEY", "model-canary")
	repo, home := t.TempDir(), t.TempDir()
	writeTree(t, repo, changeFiles(".", "add-widget", "why"))
	if gate := validateOpenSpecChange(context.Background(), repo, "add-widget", home); !gate.Passed {
		t.Fatalf("gate = %+v, want passed", gate)
	}
	env, err := os.ReadFile(filepath.Join(home, "openspec-env"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(env), "canary") {
		t.Fatalf("the openspec CLI received a credential:\n%s", env)
	}
}

func TestOpenSpecBriefFallsBackToTheChangeFiles(t *testing.T) {
	for name, setup := range map[string]func(*testing.T){
		"CLI absent": noOpenSpec,
		"CLI fails":  func(t *testing.T) { fakeOpenSpec(t, "#!/bin/sh\necho boom >&2\nexit 3\n") },
	} {
		t.Run(name, func(t *testing.T) {
			setup(t)
			repo := t.TempDir()
			writeTree(t, repo, changeFiles(".", "add-widget", "## Why\n\nWidgets are missing.\n"))
			ch, err := locateOpenSpecChange(repo, "add-widget")
			if err != nil {
				t.Fatal(err)
			}
			brief := openSpecBriefFor(context.Background(), ch, t.TempDir())
			if brief.Source != harness.OpenSpecSourceFiles {
				t.Fatalf("source = %q, want files", brief.Source)
			}
			for _, want := range []string{"Widgets are missing.", "Use a widget.", "- [ ] 1.2 pending thing",
				"openspec/changes/add-widget/specs/widget/spec.md", "unavailable"} {
				if !strings.Contains(brief.Brief, want) {
					t.Errorf("brief lacks %q:\n%s", want, brief.Brief)
				}
			}
		})
	}
}

func TestOpenSpecBriefIsCapped(t *testing.T) {
	noOpenSpec(t)
	repo := t.TempDir()
	writeTree(t, repo, changeFiles(".", "add-widget", strings.Repeat("é", maxOpenSpecBriefBytes)))
	ch, err := locateOpenSpecChange(repo, "add-widget")
	if err != nil {
		t.Fatal(err)
	}
	brief := openSpecBriefFor(context.Background(), ch, t.TempDir())
	if len(brief.Brief) > maxOpenSpecBriefBytes+100 || !strings.Contains(brief.Brief, "_(truncated)_") {
		t.Fatalf("brief is %d bytes, want it capped near %d", len(brief.Brief), maxOpenSpecBriefBytes)
	}
}

func TestValidateOpenSpecChange(t *testing.T) {
	t.Run("passes", func(t *testing.T) {
		fakeOpenSpec(t, fakeOpenSpecScript)
		repo := t.TempDir()
		writeTree(t, repo, changeFiles(".", "add-widget", "fine"))
		gate := validateOpenSpecChange(context.Background(), repo, "add-widget", t.TempDir())
		if !gate.Ran || !gate.Passed || !strings.Contains(gate.Output, "--strict") {
			t.Fatalf("gate = %+v, want passed", gate)
		}
	})
	t.Run("fails with issues", func(t *testing.T) {
		fakeOpenSpec(t, fakeOpenSpecScript)
		repo := t.TempDir()
		writeTree(t, repo, changeFiles(".", "add-widget", "BROKEN"))
		gate := validateOpenSpecChange(context.Background(), repo, "add-widget", t.TempDir())
		if !gate.Ran || gate.Passed || !strings.Contains(gate.Output, "ERROR file: Change must have at least one delta.") {
			t.Fatalf("gate = %+v, want failed with the issue", gate)
		}
	})
	t.Run("exit zero but invalid", func(t *testing.T) {
		fakeOpenSpec(t, `#!/bin/sh
echo '{"items":[{"id":"add-widget","valid":false,"issues":[]}],"summary":{"totals":{"items":1,"failed":1}}}'
`)
		gate := validateOpenSpecChange(context.Background(), t.TempDir(), "add-widget", t.TempDir())
		if !gate.Ran || gate.Passed {
			t.Fatalf("gate = %+v, want failed", gate)
		}
	})
	t.Run("CLI absent", func(t *testing.T) {
		noOpenSpec(t)
		gate := validateOpenSpecChange(context.Background(), t.TempDir(), "add-widget", t.TempDir())
		if gate.Ran || gate.Passed || !strings.Contains(gate.Output, "not on the worker's PATH") {
			t.Fatalf("gate = %+v, want not run", gate)
		}
	})
	t.Run("unreadable output", func(t *testing.T) {
		fakeOpenSpec(t, "#!/bin/sh\necho 'Validating...'\n")
		gate := validateOpenSpecChange(context.Background(), t.TempDir(), "add-widget", t.TempDir())
		if gate.Ran || gate.Passed || !strings.Contains(gate.Output, "no readable result") {
			t.Fatalf("gate = %+v, want not run", gate)
		}
	})
}

func TestApplyOpenSpecGate(t *testing.T) {
	pr := "https://forge.example/webgrip/example/pulls/3"
	writer := harness.OutcomeReport{Outcome: work.OutcomePROpened, Summary: "opened", Links: []string{pr}}
	reader := harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "reviewed", Findings: "looks good", Verdict: harness.VerdictApprove}
	passed := openSpecGate{Ran: true, Passed: true, Output: "`openspec validate x` passed"}
	failed := openSpecGate{Ran: true, Output: "`openspec validate x` failed:\n\n- ERROR file: no deltas"}
	notRun := openSpecGate{Output: "the openspec CLI is not on the worker's PATH"}

	if got := applyOpenSpecGate(writer, "x", passed, true); got.Outcome != work.OutcomePROpened || !strings.Contains(got.Summary, "passed") {
		t.Errorf("passing writer = %+v", got)
	}
	if got := applyOpenSpecGate(writer, "x", failed, true); got.Outcome != work.OutcomeStuck ||
		!strings.Contains(got.StuckReason, "no deltas") || len(got.Links) != 1 || got.Links[0] != pr {
		t.Errorf("failing writer = %+v, want stuck with the output and the pull request", got)
	}
	if got := applyOpenSpecGate(writer, "x", notRun, true); got.Outcome != work.OutcomeStuck || !strings.Contains(got.StuckReason, "not on the worker's PATH") {
		t.Errorf("writer without CLI = %+v, want stuck", got)
	}
	if got := applyOpenSpecGate(reader, "x", passed, false); got.Verdict != harness.VerdictApprove || got.Findings != "looks good" {
		t.Errorf("passing reader = %+v, want its verdict and findings kept", got)
	}
	got := applyOpenSpecGate(reader, "x", failed, false)
	if got.Outcome != work.OutcomeNoChangeNeeded || got.Verdict != harness.VerdictRequestChanges ||
		!strings.HasPrefix(got.Findings, "## OpenSpec validation (run by Ploeg)") || !strings.HasSuffix(got.Findings, "looks good") {
		t.Errorf("failing reader = %+v, want request_changes with the output first", got)
	}
	if got := applyOpenSpecGate(reader, "x", notRun, false); got.Outcome != work.OutcomeStuck || got.Verdict != "" {
		t.Errorf("reader without CLI = %+v, want stuck without a verdict", got)
	}
}

func TestOpenSpecGateApplies(t *testing.T) {
	for _, c := range []struct {
		name                   string
		outcome                work.Outcome
		writes, onReviewBranch bool
		want                   bool
	}{
		{"writer opened", work.OutcomePROpened, true, false, true},
		{"writer updated", work.OutcomePRUpdated, true, false, true},
		{"writer stuck", work.OutcomeStuck, true, false, false},
		{"writer no change", work.OutcomeNoChangeNeeded, true, false, false},
		{"reader on branch", work.OutcomeNoChangeNeeded, false, true, true},
		{"reader before any writer", work.OutcomeNoChangeNeeded, false, false, false},
		{"planner", work.OutcomeFollowUpCreated, false, false, false},
	} {
		if got := openSpecGateApplies(harness.OutcomeReport{Outcome: c.outcome}, c.writes, c.onReviewBranch); got != c.want {
			t.Errorf("%s: applies = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestComposePromptCarriesTheOpenSpecBrief(t *testing.T) {
	spec := testTaskSpec()
	spec.Repo.BaseBranch = "development"
	spec.OpenSpec = &harness.OpenSpecBrief{Change: "add-widget", Root: "apps/ploeg", Source: harness.OpenSpecSourceCLI, Brief: "Pending tasks:\n- 1.2 pending thing"}

	writer := ComposePrompt(spec, true, "", false)
	for _, want := range []string{"## OpenSpec change add-widget", "the directory apps/ploeg",
		"apps/ploeg/openspec/changes/add-widget/tasks.md", "openspec validate add-widget --type change --strict",
		"contract wins", "### Brief (from the openspec CLI)", "- 1.2 pending thing"} {
		if !strings.Contains(writer, want) {
			t.Errorf("writer prompt lacks %q", want)
		}
	}
	if strings.Index(writer, "## OpenSpec change") > strings.Index(writer, "## Delivery contract") {
		t.Error("the brief must come before the delivery contract so the contract is read last")
	}

	reader := ComposePrompt(spec, false, "", true)
	for _, want := range []string{"git diff development...HEAD -- apps/ploeg/openspec/changes/add-widget",
		"replaced by request_changes", "### Brief (from the openspec CLI)"} {
		if !strings.Contains(reader, want) {
			t.Errorf("reader prompt lacks %q", want)
		}
	}

	planner := ComposePlannerPrompt(spec)
	if !strings.Contains(planner, `"openspec: add-widget"`) {
		t.Error("planner prompt does not ask created Work Items to name the change")
	}

	spec.OpenSpec = nil
	if strings.Contains(ComposePrompt(spec, true, "", false), "OpenSpec") {
		t.Error("a Work Item without a change got an OpenSpec section")
	}
}

type openSpecForge struct {
	url string
}

func newOpenSpecForge(t *testing.T, base map[string]string, branch string, branchFiles map[string]string) openSpecForge {
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
	writeTree(t, src, base)
	git(src, "init")
	git(src, "add", ".")
	git(src, "commit", "-m", "base")
	git(src, "checkout", "-b", branch)
	writeTree(t, src, branchFiles)
	git(src, "add", ".")
	git(src, "commit", "-m", "work")
	git(src, "checkout", "development")
	projects := t.TempDir()
	git(projects, "clone", "--bare", src, filepath.Join(projects, "webgrip", "example.git"))

	mux := http.NewServeMux()
	mux.Handle("/webgrip/", &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + projects, "GIT_HTTP_EXPORT_ALL=1"}})
	var srvURL string
	mux.HandleFunc("/api/v1/repos/webgrip/example/pulls", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `[{"html_url":%q,"head":{"ref":%q},"base":{"ref":"development"}}]`, srvURL+"/webgrip/example/pulls/3", branch)
	})
	srv := httptest.NewServer(mux)
	srvURL = srv.URL
	t.Cleanup(srv.Close)
	return openSpecForge{url: srv.URL}
}

type openSpecAdapter struct {
	ran    bool
	spec   harness.TaskSpec
	prompt string
	report harness.OutcomeReport
	edit   func(repoDir string)
}

func (a *openSpecAdapter) Name() string     { return "recording" }
func (a *openSpecAdapter) ExpectsLLM() bool { return false }
func (a *openSpecAdapter) Run(_ context.Context, spec harness.TaskSpec, env harness.RunEnv) (harness.OutcomeReport, error) {
	a.ran, a.spec, a.prompt = true, spec, env.Prompt
	if a.edit != nil {
		a.edit(env.RepoDir)
	}
	return a.report, nil
}

func runOpenSpecWorkItem(t *testing.T, description string, claimed ClaimResponse, branchProposal string, adapter *openSpecAdapter) harness.OutcomeReport {
	t.Helper()
	const branch = "agent/vik-7"
	forge := newOpenSpecForge(t, changeFiles("apps/ploeg", "add-widget", "## Why\n\nWidgets.\n"), branch,
		map[string]string{"apps/ploeg/openspec/changes/add-widget/proposal.md": branchProposal})
	var rec checkpointRecorder
	w := New(Config{APIURL: rec.server(t), ForgeURL: forge.url, DefaultForge: harness.ForgeForgejo, BuilderToken: "tok",
		ForgeTokenAccess: ForgeTokenReadOnly, RepoOwner: "webgrip", RepoName: "example", BaseBranch: "development",
		WorkDir: t.TempDir()}, adapter, llmbroker.Static{}, discardLog())
	claimed.RunToken = "rt"
	claimed.WorkItem = work.WorkItem{ID: "1", ExternalID: "7", Title: "t", Description: description}
	return w.execute(context.Background(), &claimed, branch, "trace", "", "")
}

func TestOpenSpecWorkItem_WriterBriefedAndGated(t *testing.T) {
	writer := ClaimResponse{Role: "builder", Writes: true}
	t.Run("valid branch hands off", func(t *testing.T) {
		fakeOpenSpec(t, fakeOpenSpecScript)
		adapter := &openSpecAdapter{}
		report := runOpenSpecWorkItem(t, "<p>Build it.</p><p>openspec: add-widget</p>", writer, "## Why\n\nWidgets, done.\n", adapter)
		if !adapter.ran || adapter.spec.OpenSpec == nil || adapter.spec.OpenSpec.Root != "apps/ploeg" ||
			adapter.spec.OpenSpec.Source != harness.OpenSpecSourceCLI {
			t.Fatalf("harness spec = %+v, want an OpenSpec brief for apps/ploeg", adapter.spec.OpenSpec)
		}
		if !strings.Contains(adapter.prompt, "## OpenSpec change add-widget") {
			t.Error("the writer prompt carries no OpenSpec section")
		}
		if report.Outcome != work.OutcomePRUpdated || !strings.Contains(report.Summary, "passed") {
			t.Fatalf("report = %+v, want pr_updated noting the passed gate", report)
		}
	})
	t.Run("pushed branch fails the gate", func(t *testing.T) {
		fakeOpenSpec(t, fakeOpenSpecScript)
		report := runOpenSpecWorkItem(t, "openspec: add-widget", writer, "BROKEN", &openSpecAdapter{})
		if report.Outcome != work.OutcomeStuck || !strings.Contains(report.StuckReason, "at least one delta") ||
			len(report.Links) != 1 || !strings.HasSuffix(report.Links[0], "/pulls/3") {
			t.Fatalf("report = %+v, want stuck with the validation output and the pull request", report)
		}
	})
	t.Run("a valid fix left in the working tree is not delivered", func(t *testing.T) {
		fakeOpenSpec(t, fakeOpenSpecScript)
		adapter := &openSpecAdapter{edit: func(repo string) {
			writeTree(t, repo, map[string]string{"apps/ploeg/openspec/changes/add-widget/proposal.md": "fixed locally, never pushed"})
		}}
		report := runOpenSpecWorkItem(t, "openspec: add-widget", writer, "BROKEN", adapter)
		if report.Outcome != work.OutcomeStuck || !strings.Contains(report.StuckReason, "add-widget/proposal.md") ||
			len(report.Links) != 1 || !strings.HasSuffix(report.Links[0], "/pulls/3") {
			t.Fatalf("report = %+v, want stuck naming the unpushed proposal, with the pull request", report)
		}
	})
	t.Run("no CLI in the image", func(t *testing.T) {
		noOpenSpec(t)
		adapter := &openSpecAdapter{}
		report := runOpenSpecWorkItem(t, "openspec: add-widget", writer, "fine", adapter)
		if adapter.spec.OpenSpec == nil || adapter.spec.OpenSpec.Source != harness.OpenSpecSourceFiles ||
			!strings.Contains(adapter.spec.OpenSpec.Brief, "Widgets.") {
			t.Fatalf("brief = %+v, want one read from the change's files", adapter.spec.OpenSpec)
		}
		if report.Outcome != work.OutcomeStuck || !strings.Contains(report.StuckReason, "not on the worker's PATH") {
			t.Fatalf("report = %+v, want stuck because the gate could not run", report)
		}
	})
}

func TestOpenSpecWorkItem_ReviewerVerdictFollowsTheGate(t *testing.T) {
	reader := ClaimResponse{Role: "reviewer"}
	approve := harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "reviewed", Findings: "LGTM", Verdict: harness.VerdictApprove}

	fakeOpenSpec(t, fakeOpenSpecScript)
	report := runOpenSpecWorkItem(t, "openspec: add-widget", reader, "BROKEN", &openSpecAdapter{report: approve})
	if report.Verdict != harness.VerdictRequestChanges || !strings.Contains(report.Findings, "at least one delta") ||
		!strings.HasSuffix(report.Findings, "LGTM") {
		t.Fatalf("report = %+v, want request_changes with the validation output ahead of the findings", report)
	}

	report = runOpenSpecWorkItem(t, "openspec: add-widget", reader, "fine", &openSpecAdapter{report: approve})
	if report.Verdict != harness.VerdictApprove || report.Findings != "LGTM" {
		t.Fatalf("report = %+v, want the approval kept", report)
	}
}

func TestOpenSpecWorkItem_DirectiveProblemsStopBeforeTheHarness(t *testing.T) {
	fakeOpenSpec(t, fakeOpenSpecScript)
	for description, want := range map[string]string{
		"openspec: ../../etc":                   "kebab-case",
		"openspec: add-widget\nopenspec: other": "two OpenSpec changes",
		"openspec: remove-widget":               "no openspec/changes/remove-widget",
	} {
		adapter := &openSpecAdapter{}
		report := runOpenSpecWorkItem(t, description, ClaimResponse{Role: "builder", Writes: true}, "fine", adapter)
		if adapter.ran || report.Outcome != work.OutcomeStuck || !strings.Contains(report.StuckReason, want) {
			t.Errorf("%q: ran=%v report=%+v, want stuck before the harness naming %q", description, adapter.ran, report, want)
		}
	}
}

func TestWorkItemWithoutOpenSpecIsUnchanged(t *testing.T) {
	fakeOpenSpec(t, "#!/bin/sh\necho called >&2\nexit 9\n")
	adapter := &openSpecAdapter{}
	report := runOpenSpecWorkItem(t, "Build it.", ClaimResponse{Role: "builder", Writes: true}, "BROKEN", adapter)
	if adapter.spec.OpenSpec != nil || report.Outcome != work.OutcomePRUpdated {
		t.Fatalf("spec=%+v report=%+v, want no brief and no gate", adapter.spec.OpenSpec, report)
	}
}
