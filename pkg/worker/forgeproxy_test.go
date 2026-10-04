package worker

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

type forgeSeen struct {
	mu       sync.Mutex
	requests []*http.Request
}

func (f *forgeSeen) last() *http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return nil
	}
	return f.requests[len(f.requests)-1]
}

func fakeForge(t *testing.T) (*httptest.Server, *forgeSeen) {
	t.Helper()
	seen := &forgeSeen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.mu.Lock()
		seen.requests = append(seen.requests, r.Clone(r.Context()))
		seen.mu.Unlock()
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

func startTestForgeProxy(t *testing.T, repo harness.RepoRef) *forgeTokenProxy {
	t.Helper()
	return startTestForgeProxyFor(t, repo, forgeWriter)
}

func startTestForgeProxyFor(t *testing.T, repo harness.RepoRef, access forgeAccess) *forgeTokenProxy {
	t.Helper()
	p, err := startForgeTokenProxy(repo, "real-forge-token", access, writerBranch, testLeakScope(newLeakWatch()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.close)
	return p
}

func send(t *testing.T, p *forgeTokenProxy, method, path string, header http.Header) int {
	t.Helper()
	return sendBody(t, p, method, path, header, bodyGitSendsFirst(method, path))
}

func bodyGitSendsFirst(method, path string) io.Reader {
	if method == http.MethodPost && strings.HasSuffix(path, "/git-receive-pack") {
		return strings.NewReader(flushPkt)
	}
	return nil
}

func sendBody(t *testing.T, p *forgeTokenProxy, method, path string, header http.Header, body io.Reader) int {
	t.Helper()
	req, err := http.NewRequest(method, p.baseURL+"/", body)
	if err != nil {
		t.Fatal(err)
	}
	requestPath, query, _ := strings.Cut(path, "?")
	req.URL.Opaque = requestPath
	req.URL.RawQuery = query
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestForgeProxyAuthenticatesGitForTheRunsRepository(t *testing.T) {
	forge, seen := fakeForge(t)
	p := startTestForgeProxy(t, harness.RepoRef{ForgeURL: forge.URL, Owner: "webgrip", Name: "example"})
	send(t, p, http.MethodGet, "/webgrip/example.git/info/refs?service=git-receive-pack",
		http.Header{"Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte("agent-builder:"+p.placeholder))}})
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("agent-builder:real-forge-token"))
	if got := seen.last(); got == nil || got.Header.Get("Authorization") != want {
		t.Fatalf("forge saw %v, want the real token as git basic auth", got)
	}
}

func TestForgeProxyAuthenticatesTheForgejoAPI(t *testing.T) {
	forge, seen := fakeForge(t)
	p := startTestForgeProxy(t, harness.RepoRef{ForgeURL: forge.URL, Owner: "webgrip", Name: "example"})
	send(t, p, http.MethodPost, "/api/v1/repos/webgrip/example/pulls", http.Header{"Authorization": {"token " + p.placeholder}})
	if got := seen.last(); got == nil || got.Header.Get("Authorization") != "token real-forge-token" {
		t.Fatalf("forge saw %v, want the real Forgejo token", got)
	}
}

func TestForgeProxyAuthenticatesTheGitLabAPI(t *testing.T) {
	forge, seen := fakeForge(t)
	p := startTestForgeProxy(t, harness.RepoRef{Forge: harness.ForgeGitLab, ForgeURL: forge.URL, Owner: "group/sub", Name: "example"})
	send(t, p, http.MethodPost, "/api/v4/projects/group%2Fsub%2Fexample/merge_requests", http.Header{"Private-Token": {p.placeholder}})
	got := seen.last()
	if got == nil || got.Header.Get("Private-Token") != "real-forge-token" || got.Header.Get("Authorization") != "" {
		t.Fatalf("forge saw %v, want only the real PRIVATE-TOKEN", got)
	}
	if got.URL.EscapedPath() != "/api/v4/projects/group%2Fsub%2Fexample/merge_requests" {
		t.Fatalf("forge saw path %q; the project path must stay encoded", got.URL.EscapedPath())
	}
}

func TestForgeProxyRefusesEverythingOutsideTheRunsRepository(t *testing.T) {
	forge, seen := fakeForge(t)
	p := startTestForgeProxy(t, harness.RepoRef{ForgeURL: forge.URL, Owner: "webgrip", Name: "example"})
	for _, path := range []string{
		"/webgrip/other.git/info/refs",
		"/webgrip/example-evil.git/info/refs",
		"/api/v1/repos/webgrip/example-evil/pulls",
		"/api/v1/repos/webgrip/other/pulls",
		"/api/v1/user",
		"/api/v1/admin/users",
		"/webgrip/example.git/../other.git/info/refs",
		"/api/v1/repos/webgrip/example/%2e%2e/other/pulls",
	} {
		if status := send(t, p, http.MethodGet, path, nil); status != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", path, status)
		}
	}
	if len(seen.requests) != 0 {
		t.Fatalf("an out-of-scope request reached the forge: %s", seen.requests[0].URL)
	}
}

func TestGitPushesThroughTheForgeProxy(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	forge, seen := fakeForge(t)
	repo := harness.RepoRef{ForgeURL: forge.URL, Owner: "webgrip", Name: "example"}
	p := startTestForgeProxy(t, repo)
	remote, err := plainURL(repo.ForgeURL, repo.Owner, repo.Name)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "ls-remote", remote)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, p.gitEnvironment()...)
	out, _ := cmd.CombinedOutput()
	got := seen.last()
	if got == nil {
		t.Fatalf("git never reached the forge through the proxy: %s", out)
	}
	if !strings.HasPrefix(got.URL.Path, "/webgrip/example.git/") {
		t.Fatalf("forge saw %s", got.URL.Path)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("agent-builder:real-forge-token"))
	if got.Header.Get("Authorization") != want {
		t.Fatalf("git reached the forge with %q, want the real token added by the proxy", got.Header.Get("Authorization"))
	}
	for _, kv := range p.gitEnvironment() {
		if strings.Contains(kv, "real-forge-token") {
			t.Fatalf("the harness git environment carries the token: %s", kv)
		}
	}
}

func (f *forgeSeen) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func assertRefusedUnforwarded(t *testing.T, p *forgeTokenProxy, seen *forgeSeen, method, path string) {
	t.Helper()
	before := seen.count()
	status := send(t, p, method, path, http.Header{"Authorization": {"token " + p.placeholder}})
	if status != http.StatusForbidden && status != http.StatusBadRequest {
		t.Errorf("%s %s: status %d, want a refusal", method, path, status)
	}
	if seen.count() != before {
		t.Errorf("%s %s reached the forge with %q", method, path, seen.last().Header.Get("Authorization"))
	}
}

func assertForwardedWithToken(t *testing.T, p *forgeTokenProxy, seen *forgeSeen, method, path, header, want string) {
	t.Helper()
	before := seen.count()
	send(t, p, method, path, http.Header{"Authorization": {"token " + p.placeholder}})
	if seen.count() != before+1 {
		t.Errorf("%s %s was not forwarded to the forge", method, path)
		return
	}
	if got := seen.last().Header.Get(header); got != want {
		t.Errorf("%s %s reached the forge with %s %q, want %q", method, path, header, got, want)
	}
}

func TestForgeProxyRefusesEncodedTraversalAndSeparators(t *testing.T) {
	forge, seen := fakeForge(t)
	p := startTestForgeProxy(t, harness.RepoRef{ForgeURL: forge.URL, Owner: "webgrip", Name: "example"})
	for _, path := range []string{
		"/api/v1/repos/webgrip/example/../other/pulls",
		"/api/v1/repos/webgrip/example/./pulls",
		"/api/v1/repos/webgrip/example/%2e./other/pulls",
		"/api/v1/repos/webgrip/example/.%2e/other/pulls",
		"/api/v1/repos/webgrip/example/%2E%2E/other/pulls",
		"/api/v1/repos/webgrip/example/%2e/pulls",
		"/api/v1/repos/webgrip/example/%252e%252e/other/pulls",
		"/api/v1/repos/webgrip/example/pulls/%2e%2e",
		"/api/v1/repos/webgrip/example%2f..%2fother/pulls",
		"/api/v1/repos/webgrip/example/..%2F..%2Fother/pulls",
		"/api/v1/repos/webgrip/example/..%5c..%5cother/pulls",
		"/api/v1/repos/webgrip/example/..%255c..%255cother/pulls",
		"/api/v1/repos/webgrip/example/..%252f..%252fother/pulls",
		"/api/v1/repos/webgrip%2fexample/pulls",
		"/api/v1/repos/webgrip/example//pulls",
		"/api/v1/repos/webgrip/example/pulls%00",
		"/webgrip/example.git/%2e%2e/other.git/info/refs?service=git-upload-pack",
		"/webgrip/example.git/%2e./other.git/git-receive-pack",
		"/webgrip/example.git/..%2fother.git/git-upload-pack",
	} {
		assertRefusedUnforwarded(t, p, seen, http.MethodGet, path)
		assertRefusedUnforwarded(t, p, seen, http.MethodPost, path)
	}
}

func TestForgeProxyRefusesForgejoEndpointsNoRoleNeeds(t *testing.T) {
	forge, seen := fakeForge(t)
	p := startTestForgeProxy(t, harness.RepoRef{ForgeURL: forge.URL, Owner: "webgrip", Name: "example"})
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/repos/webgrip/example/pulls/1/merge"},
		{http.MethodDelete, "/api/v1/repos/webgrip/example"},
		{http.MethodPatch, "/api/v1/repos/webgrip/example"},
		{http.MethodPut, "/api/v1/repos/webgrip/example/collaborators/mallory"},
		{http.MethodGet, "/api/v1/repos/webgrip/example/collaborators"},
		{http.MethodPost, "/api/v1/repos/webgrip/example/hooks"},
		{http.MethodGet, "/api/v1/repos/webgrip/example/hooks"},
		{http.MethodPost, "/api/v1/repos/webgrip/example/branch_protections"},
		{http.MethodDelete, "/api/v1/repos/webgrip/example/branch_protections/development"},
		{http.MethodPost, "/api/v1/repos/webgrip/example/keys"},
		{http.MethodGet, "/api/v1/repos/webgrip/example/keys"},
		{http.MethodDelete, "/api/v1/repos/webgrip/example/branches/development"},
		{http.MethodDelete, "/api/v1/repos/webgrip/example/issues/1/comments/2"},
		{http.MethodPost, "/api/v1/repos/webgrip/example/statuses/abc123"},
		{http.MethodPut, "/api/v1/repos/webgrip/example/contents/README.md"},
		{http.MethodPost, "/api/v1/repos/webgrip/example/releases"},
		{http.MethodGet, "/webgrip/example.git/info/refs"},
		{http.MethodGet, "/webgrip/example.git/HEAD"},
		{http.MethodGet, "/webgrip/example.git/info/refs?service=git-upload-archive"},
	} {
		assertRefusedUnforwarded(t, p, seen, c.method, c.path)
	}
}

func TestForgeProxyForwardsWhatAForgejoWriterNeeds(t *testing.T) {
	forge, seen := fakeForge(t)
	p := startTestForgeProxy(t, harness.RepoRef{ForgeURL: forge.URL, Owner: "WebGrip", Name: "example"})
	git := "Basic " + base64.StdEncoding.EncodeToString([]byte("agent-builder:real-forge-token"))
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/webgrip/example.git/info/refs?service=git-upload-pack"},
		{http.MethodPost, "/webgrip/example.git/git-upload-pack"},
		{http.MethodGet, "/WebGrip/example.git/info/refs?service=git-receive-pack"},
		{http.MethodPost, "/webgrip/example.git/git-receive-pack"},
	} {
		assertForwardedWithToken(t, p, seen, c.method, c.path, "Authorization", git)
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/repos/webgrip/example"},
		{http.MethodPost, "/api/v1/repos/webgrip/example/pulls"},
		{http.MethodGet, "/api/v1/repos/webgrip/example/pulls?state=open"},
		{http.MethodGet, "/api/v1/repos/webgrip/example/pulls/7"},
		{http.MethodPatch, "/api/v1/repos/webgrip/example/pulls/7"},
		{http.MethodGet, "/api/v1/repos/webgrip/example/pulls/7/files"},
		{http.MethodGet, "/api/v1/repos/webgrip/example/issues/7/comments"},
		{http.MethodPost, "/api/v1/repos/webgrip/example/issues/7/comments"},
		{http.MethodGet, "/api/v1/repos/webgrip/example/commits/abc123/status"},
		{http.MethodGet, "/api/v1/repos/webgrip/example/commits/abc123/statuses"},
		{http.MethodGet, "/api/v1/repos/webgrip/example/statuses/abc123"},
	} {
		assertForwardedWithToken(t, p, seen, c.method, c.path, "Authorization", "token real-forge-token")
	}
}

func TestForgeProxyGivesAReaderOnlyReadsAndComments(t *testing.T) {
	forge, seen := fakeForge(t)
	p := startTestForgeProxyFor(t, harness.RepoRef{ForgeURL: forge.URL, Owner: "webgrip", Name: "example"}, forgeReader)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/webgrip/example.git/info/refs?service=git-receive-pack"},
		{http.MethodPost, "/webgrip/example.git/git-receive-pack"},
		{http.MethodPost, "/api/v1/repos/webgrip/example/pulls"},
		{http.MethodPatch, "/api/v1/repos/webgrip/example/pulls/7"},
		{http.MethodPost, "/api/v1/repos/webgrip/example/pulls/7/merge"},
	} {
		assertRefusedUnforwarded(t, p, seen, c.method, c.path)
	}
	assertForwardedWithToken(t, p, seen, http.MethodPost, "/webgrip/example.git/git-upload-pack", "Authorization",
		"Basic "+base64.StdEncoding.EncodeToString([]byte("agent-builder:real-forge-token")))
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/repos/webgrip/example/pulls/7"},
		{http.MethodPost, "/api/v1/repos/webgrip/example/issues/7/comments"},
	} {
		assertForwardedWithToken(t, p, seen, c.method, c.path, "Authorization", "token real-forge-token")
	}
}

func TestForgeProxyScopesTheGitLabAPIToTheEncodedProject(t *testing.T) {
	forge, seen := fakeForge(t)
	p := startTestForgeProxy(t, harness.RepoRef{Forge: harness.ForgeGitLab, ForgeURL: forge.URL, Owner: "group/sub", Name: "example"})
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v4/projects/group%2Fsub%2Fexample"},
		{http.MethodGet, "/api/v4/projects/group%2fsub%2fexample/merge_requests?source_branch=x"},
		{http.MethodPut, "/api/v4/projects/group%2Fsub%2Fexample/merge_requests/3"},
		{http.MethodPost, "/api/v4/projects/group%2Fsub%2Fexample/merge_requests/3/notes"},
		{http.MethodGet, "/api/v4/projects/group%2Fsub%2Fexample/repository/commits/abc123/statuses"},
	} {
		assertForwardedWithToken(t, p, seen, c.method, c.path, "Private-Token", "real-forge-token")
	}
	assertForwardedWithToken(t, p, seen, http.MethodPost, "/group/sub/example.git/git-receive-pack", "Authorization",
		"Basic "+base64.StdEncoding.EncodeToString([]byte("agent-builder:real-forge-token")))
	for _, c := range []struct{ method, path string }{
		{http.MethodPut, "/api/v4/projects/group%2Fsub%2Fexample/merge_requests/3/merge"},
		{http.MethodDelete, "/api/v4/projects/group%2Fsub%2Fexample"},
		{http.MethodPut, "/api/v4/projects/group%2Fsub%2Fexample"},
		{http.MethodPost, "/api/v4/projects/group%2Fsub%2Fexample/hooks"},
		{http.MethodPost, "/api/v4/projects/group%2Fsub%2Fexample/members"},
		{http.MethodPost, "/api/v4/projects/group%2Fsub%2Fexample/protected_branches"},
		{http.MethodPost, "/api/v4/projects/group%2Fsub%2Fexample/deploy_keys"},
		{http.MethodGet, "/api/v4/projects/group%2Fsub%2Fother/merge_requests"},
		{http.MethodGet, "/api/v4/projects/group%2Fsub%2Fexample%2F..%2Fother/merge_requests"},
		{http.MethodGet, "/api/v4/projects/group%2Fsub%2F%2e%2e%2Fexample/merge_requests"},
		{http.MethodGet, "/api/v4/projects/group%252Fsub%252Fexample/merge_requests"},
		{http.MethodGet, "/api/v4/projects/group/sub/example/merge_requests"},
		{http.MethodGet, "/api/v4/projects/group%2Fsub%2Fexample/merge_requests/%2e%2e/3/merge"},
		{http.MethodGet, "/group%2Fsub/example.git/info/refs?service=git-upload-pack"},
	} {
		assertRefusedUnforwarded(t, p, seen, c.method, c.path)
	}
}

func TestForgeProxyRefusesRequestsWithoutThePlaceholder(t *testing.T) {
	for _, c := range []struct {
		name string
		repo harness.RepoRef
		api  string
	}{
		{"forgejo", harness.RepoRef{Owner: "webgrip", Name: "example"}, "/api/v1/repos/webgrip/example/pulls"},
		{"gitlab", harness.RepoRef{Forge: harness.ForgeGitLab, Owner: "group/sub", Name: "example"}, "/api/v4/projects/group%2Fsub%2Fexample/merge_requests"},
	} {
		t.Run(c.name, func(t *testing.T) {
			forge, seen := fakeForge(t)
			c.repo.ForgeURL = forge.URL
			p := startTestForgeProxy(t, c.repo)
			git := "/" + c.repo.ProjectPath() + ".git/git-receive-pack"
			for name, header := range map[string]http.Header{
				"wrong token":                {"Authorization": {"token ploeg-isolated-guess"}},
				"wrong private token":        {"Private-Token": {"ploeg-isolated-guess"}},
				"wrong basic password":       {"Authorization": {basicAuth("agent-builder", "ploeg-isolated-guess")}},
				"placeholder as basic user":  {"Authorization": {basicAuth(p.placeholder, "")}},
				"placeholder prefix":         {"Authorization": {"token " + p.placeholder[:len(p.placeholder)-1]}},
				"placeholder without scheme": {"Authorization": {p.placeholder}},
				"undecodable basic":          {"Authorization": {"Basic " + p.placeholder}},
				"the real token itself":      {"Authorization": {"token real-forge-token"}},
				"real token as basic":        {"Authorization": {basicAuth("agent-builder", "real-forge-token")}},
				"two wrong credentials":      {"Authorization": {"token ploeg-isolated-guess"}, "Private-Token": {"x"}},
			} {
				for _, path := range []string{git, c.api} {
					if status := send(t, p, http.MethodPost, path, header); status != http.StatusForbidden {
						t.Errorf("%s on %s: status %d, want 403", name, path, status)
					}
				}
			}
			if status := send(t, p, http.MethodPost, c.api, nil); status != http.StatusForbidden {
				t.Errorf("an API call with no credential: status %d, want 403", status)
			}
			if status := send(t, p, http.MethodPost, git, nil); status != http.StatusUnauthorized {
				t.Errorf("git with no credential: status %d, want the 401 challenge git answers with the placeholder", status)
			}
			if n := seen.count(); n != 0 {
				t.Fatalf("%d requests without the placeholder reached the forge", n)
			}
			for _, header := range []http.Header{
				{"Authorization": {basicAuth("agent-builder", p.placeholder)}},
				{"Authorization": {"token " + p.placeholder}},
				{"Authorization": {"Bearer " + p.placeholder}},
				{"Private-Token": {p.placeholder}},
			} {
				before := seen.count()
				send(t, p, http.MethodPost, git, header)
				send(t, p, http.MethodPost, c.api, header)
				if seen.count() != before+2 {
					t.Errorf("%v: the Run's placeholder was not forwarded", header)
				}
			}
		})
	}
}

func TestForgeProxyGitEnvironmentCarriesThePlaceholderAsTheBasicPassword(t *testing.T) {
	forge, _ := fakeForge(t)
	p := startTestForgeProxy(t, harness.RepoRef{ForgeURL: forge.URL, Owner: "webgrip", Name: "example"})
	want := "GIT_CONFIG_KEY_0=url.http://agent-builder:" + p.placeholder + "@" + strings.TrimPrefix(p.baseURL, "http://") + "/.insteadOf"
	if !slices.Contains(p.gitEnvironment(), want) {
		t.Fatalf("git environment %v, want %s", p.gitEnvironment(), want)
	}
}

func basicAuth(user, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password))
}
