package worker

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
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
)

const (
	zeroID  = "0000000000000000000000000000000000000000"
	oldID   = "1111111111111111111111111111111111111111"
	newID   = "2222222222222222222222222222222222222222"
	packTag = "PACK-the-rest-of-the-stream"
)

func pktLine(payload string) string {
	return fmt.Sprintf("%04x%s", len(payload)+pktLengthSize, payload)
}

func pushBody(commands ...string) string {
	var b strings.Builder
	for i, c := range commands {
		if i == 0 {
			c += "\x00report-status side-band-64k agent=git/2.43.0"
		}
		b.WriteString(pktLine(c + "\n"))
	}
	return b.String() + flushPkt + packTag
}

type bodyForge struct {
	mu     sync.Mutex
	bodies []string
}

func (f *bodyForge) received() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.bodies...)
}

func bodyRecordingForge(t *testing.T) (*httptest.Server, *bodyForge) {
	t.Helper()
	seen := &bodyForge{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen.mu.Lock()
		seen.bodies = append(seen.bodies, string(body))
		seen.mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

func pushThrough(t *testing.T, p *forgeTokenProxy, body io.Reader, header http.Header) int {
	t.Helper()
	if header == nil {
		header = http.Header{}
	}
	header.Set("Authorization", basicAuth("agent-builder", p.placeholder))
	return sendBody(t, p, http.MethodPost, "/webgrip/example.git/git-receive-pack", header, body)
}

func TestForgeProxyForwardsAPushToTheRunBranchByteForByte(t *testing.T) {
	forge, seen := bodyRecordingForge(t)
	p := startTestForgeProxy(t, harness.RepoRef{ForgeURL: forge.URL, Owner: "webgrip", Name: "example"})
	for name, body := range map[string]string{
		"update":           pushBody(oldID + " " + newID + " refs/heads/" + writerBranch),
		"create":           pushBody(zeroID + " " + newID + " refs/heads/" + writerBranch),
		"auth probe":       flushPkt,
		"shallow then ref": pktLine("shallow "+oldID) + pushBody(oldID+" "+newID+" refs/heads/"+writerBranch),
	} {
		before := len(seen.received())
		if status := pushThrough(t, p, strings.NewReader(body), nil); status != http.StatusOK {
			t.Errorf("%s: status %d, want the push forwarded", name, status)
			continue
		}
		if got := seen.received(); len(got) != before+1 || got[before] != body {
			t.Errorf("%s: the forge did not receive the push body unchanged", name)
		}
	}
}

func TestForgeProxyRefusesPushToTheBaseBranch(t *testing.T) {
	forge, seen := bodyRecordingForge(t)
	p := startTestForgeProxy(t, harness.RepoRef{ForgeURL: forge.URL, Owner: "webgrip", Name: "example"})
	run := oldID + " " + newID + " refs/heads/" + writerBranch
	for name, body := range map[string]string{
		"the base branch":                  pushBody(oldID + " " + newID + " refs/heads/development"),
		"main":                             pushBody(oldID + " " + newID + " refs/heads/main"),
		"the run branch, then the base":    pushBody(run, oldID+" "+newID+" refs/heads/development"),
		"the base, then the run branch":    pushBody(oldID+" "+newID+" refs/heads/development", run),
		"a tag":                            pushBody(zeroID + " " + newID + " refs/tags/v1.0.0"),
		"a sibling branch":                 pushBody(zeroID + " " + newID + " refs/heads/" + writerBranch + "-2"),
		"a branch the run branch prefixes": pushBody(zeroID + " " + newID + " refs/heads/" + writerBranch + "/nested"),
		"deleting the run branch":          pushBody(oldID + " " + zeroID + " refs/heads/" + writerBranch),
		"the run branch name unqualified":  pushBody(oldID + " " + newID + " " + writerBranch),
		"a notes ref":                      pushBody(oldID + " " + newID + " refs/notes/commits"),
	} {
		if status := pushThrough(t, p, strings.NewReader(body), nil); status != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", name, status)
		}
	}
	if got := seen.received(); len(got) != 0 {
		t.Fatalf("a fenced push reached the forge: %q", got[0])
	}
}

func TestForgeProxyRefusesAnUnparseablePush(t *testing.T) {
	forge, seen := bodyRecordingForge(t)
	p := startTestForgeProxy(t, harness.RepoRef{ForgeURL: forge.URL, Owner: "webgrip", Name: "example"})
	run := "refs/heads/" + writerBranch
	for name, body := range map[string]string{
		"empty":                  "",
		"no flush":               pktLine(oldID + " " + newID + " " + run + "\n"),
		"truncated line":         "00ff" + oldID,
		"bad length":             "zzzz" + oldID,
		"reserved length":        "0002",
		"oversized length":       "fff1" + strings.Repeat("a", 0xfff1),
		"short object id":        pushBody("1111 " + newID + " " + run),
		"uppercase object id":    pushBody(strings.ToUpper("abcdef1111111111111111111111111111111111") + " " + newID + " " + run),
		"mixed hash lengths":     pushBody(oldID + strings.Repeat("1", 24) + " " + newID + " " + run),
		"extra field":            pushBody(oldID + " " + newID + " " + run + " extra"),
		"push certificate":       pktLine("push-cert\x00report-status\n") + flushPkt,
		"capabilities on line 2": pushBody(oldID+" "+newID+" "+run, oldID+" "+newID+" "+run+"\x00report-status"),
		"raw packfile":           packTag,
	} {
		if status := pushThrough(t, p, strings.NewReader(body), nil); status != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", name, status)
		}
	}
	if got := seen.received(); len(got) != 0 {
		t.Fatalf("an unparseable push reached the forge: %q", got[0])
	}
}

func TestForgeProxyInspectsAGzippedPush(t *testing.T) {
	forge, seen := bodyRecordingForge(t)
	p := startTestForgeProxy(t, harness.RepoRef{ForgeURL: forge.URL, Owner: "webgrip", Name: "example"})
	gzipped := func(s string) io.Reader {
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		_, _ = io.WriteString(zw, s)
		_ = zw.Close()
		return &b
	}
	encoded := http.Header{"Content-Encoding": {"gzip"}}
	base := pushBody(oldID + " " + newID + " refs/heads/development")
	if status := pushThrough(t, p, gzipped(base), encoded.Clone()); status != http.StatusForbidden {
		t.Errorf("a gzipped push to the base branch: status %d, want 403", status)
	}
	run := pushBody(oldID + " " + newID + " refs/heads/" + writerBranch)
	if status := pushThrough(t, p, gzipped(run), encoded.Clone()); status != http.StatusOK {
		t.Fatalf("a gzipped push to the run branch: status %d, want it forwarded", status)
	}
	if got := seen.received(); len(got) != 1 || got[0] != run {
		t.Fatalf("the forge received %q, want the inflated push", got)
	}
	if status := pushThrough(t, p, strings.NewReader(run), http.Header{"Content-Encoding": {"br"}}); status != http.StatusForbidden {
		t.Errorf("an encoding the fence cannot read: status %d, want 403", status)
	}
}

func TestForgeProxyRefusesMergeAndDelete(t *testing.T) {
	for _, c := range []struct {
		name    string
		repo    harness.RepoRef
		refused []struct{ method, path string }
	}{
		{"forgejo", harness.RepoRef{Owner: "webgrip", Name: "example"}, []struct{ method, path string }{
			{http.MethodPost, "/api/v1/repos/webgrip/example/pulls/7/merge"},
			{http.MethodDelete, "/api/v1/repos/webgrip/example/pulls/7/merge"},
			{http.MethodDelete, "/api/v1/repos/webgrip/example"},
			{http.MethodDelete, "/api/v1/repos/webgrip/example/pulls/7"},
			{http.MethodDelete, "/api/v1/repos/webgrip/example/issues/7"},
			{http.MethodDelete, "/api/v1/repos/webgrip/example/issues/comments/9"},
			{http.MethodDelete, "/api/v1/repos/webgrip/example/branches/development"},
			{http.MethodDelete, "/api/v1/repos/webgrip/example/branches/" + writerBranch},
			{http.MethodDelete, "/api/v1/repos/webgrip/example/tags/v1.0.0"},
			{http.MethodPatch, "/api/v1/repos/webgrip/example"},
			{http.MethodPost, "/api/v1/repos/webgrip/example/branch_protections"},
			{http.MethodPost, "/api/v1/repos/webgrip/example/hooks"},
			{http.MethodPut, "/api/v1/repos/webgrip/example/collaborators/mallory"},
		}},
		{"gitlab", harness.RepoRef{Forge: harness.ForgeGitLab, Owner: "group/sub", Name: "example"}, []struct{ method, path string }{
			{http.MethodPut, "/api/v4/projects/group%2Fsub%2Fexample/merge_requests/3/merge"},
			{http.MethodPost, "/api/v4/projects/group%2Fsub%2Fexample/merge_requests/3/merge"},
			{http.MethodDelete, "/api/v4/projects/group%2Fsub%2Fexample"},
			{http.MethodDelete, "/api/v4/projects/group%2Fsub%2Fexample/merge_requests/3"},
			{http.MethodDelete, "/api/v4/projects/group%2Fsub%2Fexample/repository/branches/development"},
			{http.MethodDelete, "/api/v4/projects/group%2Fsub%2Fexample/merge_requests/3/notes/9"},
			{http.MethodPut, "/api/v4/projects/group%2Fsub%2Fexample"},
			{http.MethodPost, "/api/v4/projects/group%2Fsub%2Fexample/protected_branches"},
			{http.MethodPost, "/api/v4/projects/group%2Fsub%2Fexample/hooks"},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			forge, seen := fakeForge(t)
			c.repo.ForgeURL = forge.URL
			p := startTestForgeProxy(t, c.repo)
			for _, r := range c.refused {
				assertRefusedUnforwarded(t, p, seen, r.method, r.path)
			}
		})
	}
}

func TestGitCanPushOnlyTheRunBranchThroughTheProxy(t *testing.T) {
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skip("git is not available")
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend is not available")
	}
	projects := t.TempDir()
	bare := filepath.Join(projects, "webgrip", "example.git")
	gitIn(t, "", "init", "--bare", bare)
	gitIn(t, bare, "config", "http.receivepack", "true")
	forge := httptest.NewServer(&cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + projects, "GIT_HTTP_EXPORT_ALL=1"}})
	t.Cleanup(forge.Close)
	repo := harness.RepoRef{ForgeURL: forge.URL, Owner: "webgrip", Name: "example"}
	p := startTestForgeProxy(t, repo)
	remote, err := plainURL(repo.ForgeURL, repo.Owner, repo.Name)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	writeTree(t, work, map[string]string{"main.go": "package main\n"})
	gitIn(t, work, "init")
	gitIn(t, work, "add", ".")
	gitIn(t, work, "commit", "-m", "work")
	push := func(refspec string) error {
		cmd := exec.Command("git", "push", remote, refspec)
		cmd.Dir = work
		cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, p.gitEnvironment()...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, out)
		}
		return nil
	}
	if err := push("HEAD:refs/heads/" + writerBranch); err != nil {
		t.Fatalf("pushing the run branch through the proxy: %v", err)
	}
	if got := gitIn(t, bare, "rev-parse", "refs/heads/"+writerBranch); got != gitIn(t, work, "rev-parse", "HEAD") {
		t.Fatalf("the forge's run branch is at %s", got)
	}
	if err := push("HEAD:refs/heads/development"); err == nil {
		t.Fatal("git pushed the base branch through the proxy")
	}
	if err := push(":refs/heads/" + writerBranch); err == nil {
		t.Fatal("git deleted the run branch through the proxy")
	}
	if out, _ := exec.Command("git", "-C", bare, "show-ref").CombinedOutput(); strings.Contains(string(out), "refs/heads/development") {
		t.Fatalf("the base branch reached the forge:\n%s", out)
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=development"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
