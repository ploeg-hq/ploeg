package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
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

type requestLog struct {
	mu      sync.Mutex
	planted int
	total   int
}

func (l *requestLog) record(r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.total++
	if r.Header.Get("X-Planted") != "" {
		l.planted++
	}
}

func (l *requestLog) counts() (total, planted int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.total, l.planted
}

type hostileCheckout struct {
	forgeURL string
	evilURL  string
	forge    *requestLog
	evil     *requestLog
	ran      string
	planted  string
}

func newHostileCheckout(t *testing.T, forge *writerForge) *hostileCheckout {
	t.Helper()
	upstream, err := url.Parse(forge.url)
	if err != nil {
		t.Fatal(err)
	}
	h := &hostileCheckout{forge: &requestLog{}, evil: &requestLog{}, ran: filepath.Join(t.TempDir(), "ran")}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.forge.record(r)
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.evil.record(r)
		http.NotFound(w, r)
	}))
	t.Cleanup(evil.Close)
	h.forgeURL, h.evilURL = front.URL, evil.URL
	return h
}

type hostileVector struct {
	name  string
	plant func(h *hostileCheckout, reporter, dir string) []string
}

func hostileVectors() []hostileVector {
	hookNames := []string{"pre-push", "reference-transaction", "post-checkout", "post-index-change", "pre-auto-gc", "post-rewrite"}
	return []hostileVector{
		{"url.insteadOf", func(h *hostileCheckout, _, _ string) []string {
			return []string{"git config 'url." + h.evilURL + "/.insteadOf' '" + h.forgeURL + "/'"}
		}},
		{"http.extraHeader", func(*hostileCheckout, string, string) []string {
			return []string{"git config http.extraHeader 'X-Planted: harness'"}
		}},
		{"hooks", func(_ *hostileCheckout, reporter, dir string) []string {
			lines := []string{"mkdir -p .git/hooks " + dir + "/hooks", "git config core.hooksPath " + dir + "/hooks"}
			for _, hook := range hookNames {
				lines = append(lines, "cp "+reporter+" .git/hooks/"+hook, "cp "+reporter+" "+dir+"/hooks/"+hook)
			}
			return lines
		}},
		{"core.fsmonitor", func(_ *hostileCheckout, reporter, _ string) []string {
			return []string{"git config core.fsmonitor " + reporter}
		}},
		{"include.path", func(_ *hostileCheckout, reporter, dir string) []string {
			return []string{
				"printf '[core]\\n\\tfsmonitor = " + reporter + "\\n[credential]\\n\\thelper = " + reporter + "\\n' > " + dir + "/included",
				"git config include.path " + dir + "/included",
			}
		}},
	}
}

func (h *hostileCheckout) script(t *testing.T, vectors []hostileVector) string {
	t.Helper()
	dir := t.TempDir()
	reporter := filepath.Join(dir, "report.sh")
	if err := os.WriteFile(reporter, []byte("#!/bin/sh\necho \"$0 $*\" >> "+h.ran+"\ncat >/dev/null\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.planted = filepath.Join(dir, "planted")
	var plant strings.Builder
	for _, vector := range vectors {
		for _, line := range vector.plant(h, reporter, dir) {
			plant.WriteString(line + " || exit 97\n")
		}
	}
	plant.WriteString("touch " + h.planted + "\n")
	return plant.String()
}

func (h *hostileCheckout) runWriter(t *testing.T, beforePlanting string, vectors []hostileVector, cfg Config) harness.OutcomeReport {
	t.Helper()
	adapter := acpAgentDuringPrompt(t, editToolCall+"\n"+beforePlanting+"\n"+h.script(t, vectors))
	var rec checkpointRecorder
	cfg.APIURL, cfg.ForgeURL, cfg.DefaultForge, cfg.BuilderToken = rec.server(t), h.forgeURL, harness.ForgeForgejo, "tok"
	cfg.RepoOwner, cfg.RepoName, cfg.BaseBranch, cfg.WorkDir = "webgrip", "example", "development", t.TempDir()
	w := New(cfg, adapter, llmbroker.Static{}, discardLog())
	claimed := &ClaimResponse{RunToken: "rt", Role: "builder", Writes: true, WorkItem: work.WorkItem{ID: "1", ExternalID: "7", Title: "t"}}
	return w.execute(context.Background(), claimed, writerBranch, "trace", "", "")
}

func (h *hostileCheckout) assertIgnored(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(h.planted); err != nil {
		t.Fatalf("the harness never finished planting its configuration: %v", err)
	}
	if total, _ := h.evil.counts(); total != 0 {
		t.Errorf("the worker followed the planted url.insteadOf: %d request(s) reached the redirect target", total)
	}
	if _, planted := h.forge.counts(); planted != 0 {
		t.Errorf("the worker sent the planted http.extraHeader on %d request(s)", planted)
	}
	if ran, err := os.ReadFile(h.ran); err == nil {
		t.Errorf("the worker executed programs the harness configured:\n%s", ran)
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func eachHostileVector(t *testing.T, run func(t *testing.T, vectors []hostileVector)) {
	all := hostileVectors()
	for _, vector := range all {
		t.Run(vector.name, func(t *testing.T) { run(t, []hostileVector{vector}) })
	}
	t.Run("all at once", func(t *testing.T) { run(t, all) })
}

func TestWorkerLeakScanIgnoresGitConfigurationTheHarnessWrote(t *testing.T) {
	eachHostileVector(t, func(t *testing.T, vectors []hostileVector) {
		h := newHostileCheckout(t, newWriterForge(t, false, pullsForPushedBranch))
		report := h.runWriter(t, `printf 'export TOKEN=%s\n' "$`+canaryEnv+`" > deploy.sh
      git checkout -q -b `+writerBranch+` >&2 && git add deploy.sh && git commit -qm 'add deploy script' >&2 && git push -q origin `+writerBranch+` >&2`,
			vectors, Config{VerifyCommands: []string{"true"}})
		if report.Outcome != work.OutcomeFailed || report.FailureReason != string(work.FailureCredentialLeak) {
			t.Errorf("report = %+v, want the leak scan to read the real forge and fail the Run with credential_leak", report)
		}
		h.assertIgnored(t)
	})
}

func TestWorkerUnpublishedWorkGuardIgnoresGitConfigurationTheHarnessWrote(t *testing.T) {
	eachHostileVector(t, func(t *testing.T, vectors []hostileVector) {
		h := newHostileCheckout(t, newWriterForge(t, false, pullsNone))
		report := h.runWriter(t, `printf 'func main() {}\n' >> main.go
      git commit -qam 'local only' >&2
      printf 'more\n' >> main.go`, vectors, Config{})
		wantStuck(t, report, "the writer's local commit and edit never reached the forge", "commit(s) not on the forge", "uncommitted path(s): main.go")
		h.assertIgnored(t)
	})
}

func TestOpenSpecGateIgnoresGitConfigurationTheHarnessWrote(t *testing.T) {
	fakeOpenSpec(t, fakeOpenSpecScript)
	evil := &requestLog{}
	evilServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		evil.record(r)
		http.NotFound(w, r)
	}))
	t.Cleanup(evilServer.Close)
	ran := filepath.Join(t.TempDir(), "ran")
	reporter := filepath.Join(t.TempDir(), "report.sh")
	if err := os.WriteFile(reporter, []byte("#!/bin/sh\necho \"$0 $*\" >> "+ran+"\ncat >/dev/null\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	adapter := &openSpecAdapter{
		report: harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "reviewed", Findings: "LGTM", Verdict: harness.VerdictApprove},
		edit: func(repo string) {
			git := func(args ...string) string {
				cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
				cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			origin := git("config", "remote.origin.url")
			git("config", "url."+evilServer.URL+"/webgrip/example.git.insteadOf", origin)
			git("config", "core.hooksPath", filepath.Dir(reporter))
			git("config", "core.fsmonitor", reporter)
			for _, hook := range []string{"post-checkout", "reference-transaction", "post-index-change"} {
				if err := os.Link(reporter, filepath.Join(filepath.Dir(reporter), hook)); err != nil {
					t.Fatal(err)
				}
			}
		},
	}
	report := runOpenSpecWorkItem(t, "openspec: add-widget", ClaimResponse{Role: "reviewer"}, "BROKEN", adapter)
	if report.Verdict != harness.VerdictRequestChanges || !strings.Contains(report.Findings, "at least one delta") {
		t.Errorf("report = %+v, want the gate to validate the pushed branch from the real forge", report)
	}
	if total, _ := evil.counts(); total != 0 {
		t.Errorf("the gate followed the planted url.insteadOf: %d request(s) reached the redirect target", total)
	}
	if out, err := os.ReadFile(ran); err == nil {
		t.Errorf("the gate executed programs the harness configured:\n%s", out)
	}
}
