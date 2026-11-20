package httpapi

import (
	"encoding/json"
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
)

func TestPostSessionOpensAGitLabMergeRequestThroughTheGateway(t *testing.T) {
	for _, tool := range []string{"bash", "curl", "jq", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not available", tool)
		}
	}
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skip("git is not available")
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend is not available")
	}
	work := t.TempDir()
	projects := filepath.Join(work, "forge")
	bare := filepath.Join(projects, "group", "sub", "fixture.git")
	gitIn(t, "", "init", "-q", "--bare", bare)
	gitIn(t, bare, "config", "http.receivepack", "true")
	workspace := filepath.Join(work, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("# fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, workspace, "init", "-q")
	gitIn(t, workspace, "add", ".")
	gitIn(t, workspace, "commit", "-q", "-m", "change")

	const session = "sk-ant-cc-post-session-token-0123456789"
	var (
		mu      sync.Mutex
		created map[string]any
		listed  string
		report  map[string]string
		auths   []string
	)
	git := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + projects, "GIT_HTTP_EXPORT_ALL=1"}}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		const project = "/api/v4/projects/group%2Fsub%2Ffixture/merge_requests"
		switch {
		case r.URL.Path == "/api/v1/claude-sessions/run":
			_, _ = io.WriteString(w, `{"repository":"group/sub/fixture","branch":"agent/vik-7","baseBranch":"main","title":"Add a health check","reference":"VIK-7","dialect":"gitlab"}`)
		case r.URL.Path == "/api/v1/claude-sessions/report":
			mu.Lock()
			_ = json.NewDecoder(r.Body).Decode(&report)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.EscapedPath() == project:
			mu.Lock()
			listed = r.URL.Query().Get("state") + " " + r.URL.Query().Get("source_branch")
			mu.Unlock()
			_, _ = io.WriteString(w, "[]")
		case r.Method == http.MethodPost && r.URL.EscapedPath() == project:
			mu.Lock()
			_ = json.NewDecoder(r.Body).Decode(&created)
			mu.Unlock()
			_, _ = io.WriteString(w, `{"iid":3,"web_url":"https://gitlab.example/group/sub/fixture/-/merge_requests/3"}`)
		case strings.HasPrefix(r.URL.Path, "/group/sub/fixture.git/"):
			git.ServeHTTP(w, r)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.EscapedPath(), http.StatusNotFound)
		}
	}))
	defer gateway.Close()

	cmd := exec.Command(filepath.Join(repoRoot(t), "ops/claude-runner/post-session"))
	cmd.Dir = work
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + work, "PLOEG_URL=" + gateway.URL, "PLOEG_CREDENTIAL_GATEWAY=on",
		"CLAUDE_CODE_SESSION_ACCESS_TOKEN=" + session, "CLAUDE_RUNNER_WORKSPACE_PATHS=" + workspace, "CLAUDE_RUNNER_EXIT_REASON=completed"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("post-session failed: %v: %s", err, out)
	}

	if head, err := exec.Command("git", "-C", bare, "rev-parse", "--verify", "refs/heads/agent/vik-7").Output(); err != nil || len(head) == 0 {
		t.Fatalf("the Run branch was not pushed: %v: %s", err, out)
	}
	mu.Lock()
	defer mu.Unlock()
	if listed != "opened agent/vik-7" {
		t.Fatalf("open merge requests were looked up with %q", listed)
	}
	if created["source_branch"] != "agent/vik-7" || created["target_branch"] != "main" || created["title"] != "Add a health check" || created["description"] != "VIK-7" {
		t.Fatalf("merge request created as %v: %s", created, out)
	}
	if report["pullRequest"] != "https://gitlab.example/group/sub/fixture/-/merge_requests/3" || report["exitReason"] != "completed" || report["pushedCommit"] == "" {
		t.Fatalf("report = %v", report)
	}
	for _, auth := range auths {
		if auth != "" && auth != "Bearer "+session {
			t.Fatalf("the gateway saw a credential other than the session token: %q", auth)
		}
	}
}
