package worker

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const (
	leakTestModelKey = "sk-real-run-key-0123456789"
	leakTestCanary   = "ghp_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789"
)

func testLeakScope(watch *leakWatch) leakScope {
	return leakScope{run: "ploeg-0123456789ab", workItem: "7", log: discardLog(), watch: watch}
}

type leakProxy struct {
	*forgeTokenProxy
	watch *leakWatch
	logs  *bytes.Buffer
}

func startLeakWatchedForgeProxy(t *testing.T, forgeURL string) leakProxy {
	t.Helper()
	watch := newLeakWatch()
	watch.guard(credentialModelKey, leakTestModelKey)
	watch.guard(credentialCanary, leakTestCanary)
	var logs bytes.Buffer
	scope := testLeakScope(watch)
	scope.log = slog.New(slog.NewTextHandler(&logs, nil))
	p, err := startForgeTokenProxy(harness.RepoRef{ForgeURL: forgeURL, Owner: "webgrip", Name: "example"},
		"real-forge-token", forgeWriter, writerBranch, scope)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.close)
	return leakProxy{forgeTokenProxy: p, watch: watch, logs: &logs}
}

func (p leakProxy) post(t *testing.T, path, body string) int {
	t.Helper()
	return sendBody(t, p.forgeTokenProxy, http.MethodPost, path, http.Header{"Authorization": {"token " + p.placeholder}}, strings.NewReader(body))
}

func TestForgeProxyRefusesAnAPIRequestCarryingACredential(t *testing.T) {
	for _, c := range []struct {
		credential, value, path, body string
	}{
		{"model key", leakTestModelKey, "/api/v1/repos/webgrip/example/pulls", `{"title":"t","body":"key: ` + leakTestModelKey + `"}`},
		{"forge token", "real-forge-token", "/api/v1/repos/webgrip/example/issues/3/comments", `{"body":"token real-forge-token"}`},
		{"canary credential", leakTestCanary, "/api/v1/repos/webgrip/example/pulls", `{"title":"` + leakTestCanary + `"}`},
		{"canary credential", leakTestCanary, "/api/v1/repos/webgrip/example/issues/3/comments?body=" + leakTestCanary, `{}`},
	} {
		t.Run(c.credential, func(t *testing.T) {
			forge, seen := fakeForge(t)
			p := startLeakWatchedForgeProxy(t, forge.URL)
			if status := p.post(t, c.path, c.body); status != http.StatusForbidden {
				t.Fatalf("status %d, want 403", status)
			}
			if seen.count() != 0 {
				t.Fatal("the leaking request reached the forge")
			}
			leak, leaked := p.watch.leak()
			if !leaked || string(leak.kind) != c.credential {
				t.Fatalf("recorded leak %+v (%v), want the %s", leak, leaked, c.credential)
			}
			logged := p.logs.String()
			if n := strings.Count(logged, "level=ERROR"); n != 1 {
				t.Fatalf("%d ERROR lines, want exactly one:\n%s", n, logged)
			}
			for _, want := range []string{"run=ploeg-0123456789ab", "route=\"the forge API request POST ", "credential=\"" + c.credential + "\""} {
				if !strings.Contains(logged, want) {
					t.Errorf("the ERROR line does not name %s:\n%s", want, logged)
				}
			}
			if strings.Contains(logged, c.value) {
				t.Fatalf("the ERROR line carries the credential itself:\n%s", logged)
			}
		})
	}
}

func TestForgeProxyStopsForgeWritesAfterALeak(t *testing.T) {
	forge, seen := fakeForge(t)
	p := startLeakWatchedForgeProxy(t, forge.URL)
	if status := p.post(t, "/api/v1/repos/webgrip/example/pulls", leakTestCanary); status != http.StatusForbidden {
		t.Fatalf("leaking request: status %d, want 403", status)
	}
	if status := p.post(t, "/api/v1/repos/webgrip/example/issues/3/comments", `{"body":"all clean"}`); status != http.StatusForbidden {
		t.Errorf("a clean comment after the leak: status %d, want 403", status)
	}
	push := pushBody(oldID + " " + newID + " refs/heads/" + writerBranch)
	if status := pushThrough(t, p.forgeTokenProxy, strings.NewReader(push), nil); status != http.StatusForbidden {
		t.Errorf("a push to the run branch after the leak: status %d, want 403", status)
	}
	if seen.count() != 0 {
		t.Fatalf("%d writes reached the forge after the leak", seen.count())
	}
	send(t, p.forgeTokenProxy, http.MethodGet, "/api/v1/repos/webgrip/example/pulls/3", http.Header{"Authorization": {"token " + p.placeholder}})
	if seen.count() != 1 {
		t.Fatal("a read after the leak was refused; only writes stop")
	}
}

func TestForgeProxyForwardsACleanPullRequest(t *testing.T) {
	forge, seen := fakeForge(t)
	p := startLeakWatchedForgeProxy(t, forge.URL)
	body := `{"title":"Fix the parser","body":"Refs VIK-7. Uses ` + p.placeholder + ` nowhere; ghp_short and sk-real are not credentials.","head":"` + writerBranch + `"}`
	p.post(t, "/api/v1/repos/webgrip/example/pulls", body)
	if seen.count() != 1 {
		t.Fatal("a clean pull request was not forwarded")
	}
	if _, leaked := p.watch.leak(); leaked || p.logs.Len() != 0 {
		t.Fatalf("a clean pull request counted as a leak:\n%s", p.logs.String())
	}
}

func TestLeakWatchFindsACredentialSplitAcrossReads(t *testing.T) {
	watch := newLeakWatch()
	watch.guard(credentialCanary, leakTestCanary)
	stream := strings.Repeat("x", 64<<10-10) + leakTestCanary + strings.Repeat("y", 100)
	kind, found, err := watch.findInStream(iotest.HalfReader(strings.NewReader(stream)))
	if err != nil || !found || kind != credentialCanary {
		t.Fatalf("findInStream = %q, %v, %v; want the canary found across a read boundary", kind, found, err)
	}
	if _, found, _ := watch.findInStream(strings.NewReader(stream[:len(stream)-130])); found {
		t.Fatal("found a credential in a stream that carries only part of it")
	}
}

func TestLeakWatchIgnoresValuesTooShortToBeCredentials(t *testing.T) {
	watch := newLeakWatch()
	watch.guard(credentialForgeToken, "tok")
	watch.guard(credentialModelKey, "")
	if _, found := watch.find([]byte("a token and stock")); found {
		t.Fatal("a three-letter test token matched ordinary text")
	}
}

func TestCanaryCredentialLooksLikeAGitHubToken(t *testing.T) {
	shape := regexp.MustCompile(`^ghp_[A-Za-z0-9]{36}$`)
	first, err := newCanaryCredential()
	if err != nil {
		t.Fatal(err)
	}
	second, _ := newCanaryCredential()
	if !shape.MatchString(first) || first == second {
		t.Fatalf("canaries %q and %q, want two different GitHub-token-shaped values", first, second)
	}
}

func TestWriterThatCommitsTheCanaryFailsWithACredentialLeak(t *testing.T) {
	forge := newWriterForge(t, false, pullsForPushedBranch)
	adapter := acpAgentDuringPrompt(t, editToolCall+`
      printf 'export TOKEN=%s\n' "$`+canaryEnv+`" > deploy.sh
      git checkout -q -b `+writerBranch+` >&2 && git add deploy.sh && git commit -qm 'add deploy script' >&2 && git push -q origin `+writerBranch+` >&2`)
	report := runWriterAgainst(t, forge, adapter)
	if report.Outcome != work.OutcomeFailed || report.FailureReason != string(work.FailureCredentialLeak) {
		t.Fatalf("report = %+v, want failed with credential_leak", report)
	}
	if len(report.Links) != 0 || !strings.Contains(report.Summary, "canary credential") || !strings.Contains(report.Summary, writerBranch) {
		t.Fatalf("report = %+v, want no links and a summary naming the canary and the branch", report)
	}
	if !forge.hasBranch() {
		t.Fatal("the leaking commits were removed from the forge; they stay for forensics")
	}
}

func TestWriterThatPutsTheCanaryInACommitMessageFailsWithACredentialLeak(t *testing.T) {
	forge := newWriterForge(t, false, pullsForPushedBranch)
	adapter := acpAgentDuringPrompt(t, editToolCall+`
      printf 'func main() {}\n' >> main.go
      git checkout -q -b `+writerBranch+` >&2 && git commit -qam "add main with $`+canaryEnv+`" >&2 && git push -q origin `+writerBranch+` >&2`)
	report := runWriterAgainst(t, forge, adapter)
	if report.FailureReason != string(work.FailureCredentialLeak) {
		t.Fatalf("report = %+v, want credential_leak", report)
	}
}

func TestCleanWriterThatPushedIsNotAFalsePositive(t *testing.T) {
	forge := newWriterForge(t, false, pullsForPushedBranch)
	adapter := acpAgentDuringPrompt(t, editToolCall+`
      test -n "$`+canaryEnv+`" || exit 9
      printf 'func main() {}\n' >> main.go
      git checkout -q -b `+writerBranch+` >&2 && git commit -qam 'add main' >&2 && git push -q origin `+writerBranch+` >&2`)
	report := runWriterAgainst(t, forge, adapter)
	if report.Outcome != work.OutcomePROpened || report.FailureReason != "" {
		t.Fatalf("report = %+v, want pr_opened with no failure reason", report)
	}
}

func TestLeakFoundByTheProxyFailsTheRunEvenWithoutALeakingCommit(t *testing.T) {
	watch := newLeakWatch()
	watch.record(credentialLeak{kind: credentialModelKey, where: "the forge API request POST /api/v1/repos/webgrip/example/pulls"})
	report := guardCredentialLeaks(context.Background(),
		harness.OutcomeReport{Outcome: work.OutcomePROpened, Summary: "done", Links: []string{"https://forge/pulls/3"}},
		testLeakScope(watch), pushedCommitScan{dir: t.TempDir(), cloneURL: "http://127.0.0.1:1/webgrip/example.git", branch: writerBranch}, true)
	if report.Outcome != work.OutcomeFailed || report.FailureReason != string(work.FailureCredentialLeak) || len(report.Links) != 0 {
		t.Fatalf("report = %+v, want failed with credential_leak and no links", report)
	}
}

func TestUnreadablePushedCommitsBlockADeliveryClaim(t *testing.T) {
	report := guardCredentialLeaks(context.Background(),
		harness.OutcomeReport{Outcome: work.OutcomePROpened, Summary: "done", Links: []string{"https://forge/pulls/3"}},
		testLeakScope(newLeakWatch()), pushedCommitScan{dir: t.TempDir(), cloneURL: "http://127.0.0.1:1/webgrip/example.git", branch: writerBranch}, true)
	if report.Outcome != work.OutcomeStuck || !strings.Contains(report.StuckReason, "credential leak scan") {
		t.Fatalf("report = %+v, want stuck: an unchecked branch is not ready for review", report)
	}
}

func TestToolchainsCannotOverrideTheCanary(t *testing.T) {
	if _, err := ParseToolchains(`[{"name":"go","path":["/a"],"env":{"` + canaryEnv + `":"x"}}]`); err == nil {
		t.Fatal("a toolchain set the canary variable")
	}
}
