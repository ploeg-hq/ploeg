package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/forgebroker"
	"github.com/ploeg-hq/ploeg/pkg/harness"
)

const (
	refusalTestRun       = "ploeg-0123456789ab"
	refusalTestRunToken  = "run-token-of-the-replayed-run"
	refusalTestForgeBot  = "agent-builder"
	refusalTestBotSecret = "bot-password"
	refusalTestPresented = "ploeg-isolated-presented-guess"
)

type capturedLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *capturedLog) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *capturedLog) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

func (c *capturedLog) linesWithMessage(t *testing.T, msg string) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(c.text()), "\n") {
		if raw == "" {
			continue
		}
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("log line %q is not JSON: %v", raw, err)
		}
		if line["msg"] == msg {
			lines = append(lines, line)
		}
	}
	return lines
}

func (c *capturedLog) refusals(t *testing.T) []map[string]any {
	t.Helper()
	return c.linesWithMessage(t, proxyRefusalMessage)
}

func newCapturedLog() (*capturedLog, *slog.Logger) {
	c := &capturedLog{}
	return c, slog.New(slog.NewJSONHandler(c, nil))
}

type auditedForgeProxy struct {
	*forgeTokenProxy
	watch *leakWatch
	logs  *capturedLog
}

func startAuditedForgeProxy(t *testing.T, forgeURL string, tokens forgeTokenSource) auditedForgeProxy {
	t.Helper()
	logs, log := newCapturedLog()
	watch := newLeakWatch()
	scope := leakScope{run: refusalTestRun, workItem: "7", log: log, watch: watch}
	p, err := startForgeTokenProxy(harness.RepoRef{ForgeURL: forgeURL, Owner: "webgrip", Name: "example"},
		tokens, forgeWriter, writerBranch, scope)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.close)
	return auditedForgeProxy{forgeTokenProxy: p, watch: watch, logs: logs}
}

func startAuditedKeyProxy(t *testing.T) (*llmKeyProxy, *capturedLog) {
	t.Helper()
	gw, _ := fakeGateway(t)
	logs, log := newCapturedLog()
	p, err := startLLMKeyProxy(gw.URL, leakTestModelKey, nil, proxyRun{trace: refusalTestRun, log: log})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.close)
	return p, logs
}

func sendToKeyProxy(t *testing.T, p *llmKeyProxy, path string, header http.Header) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, p.baseURL+path, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header = header
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func assertOneRefusal(t *testing.T, logs *capturedLog, proxy, method, path string, reason refusalReason) {
	t.Helper()
	lines := logs.refusals(t)
	if len(lines) != 1 {
		t.Fatalf("%d refusal lines, want exactly one:\n%s", len(lines), logs.text())
	}
	want := map[string]any{
		"level":  "WARN",
		"proxy":  proxy,
		"run":    refusalTestRun,
		"method": method,
		"path":   path,
		"reason": string(reason),
	}
	for key, value := range want {
		if lines[0][key] != value {
			t.Errorf("refusal line %s = %v, want %v (line %v)", key, lines[0][key], value, lines[0])
		}
	}
}

func TestForgeProxyLogsEachRefusalWithItsReason(t *testing.T) {
	const apiPulls = "/api/v1/repos/webgrip/example/pulls"
	const receivePack = "/webgrip/example.git/git-receive-pack"
	placeholderHeader := func(p auditedForgeProxy) http.Header {
		return http.Header{"Authorization": {"token " + p.placeholder}}
	}
	gitHeader := func(p auditedForgeProxy) http.Header {
		return http.Header{"Authorization": {basicAuth(refusalTestForgeBot, p.placeholder)}}
	}
	for _, c := range []struct {
		reason      refusalReason
		method      string
		path        string
		loggedPath  string
		header      func(auditedForgeProxy) http.Header
		body        string
		beforeSends func(auditedForgeProxy)
	}{
		{reason: refusalMissingPlaceholder, method: http.MethodPost, path: apiPulls,
			header: func(auditedForgeProxy) http.Header { return nil }},
		{reason: refusalWrongPlaceholder, method: http.MethodPost, path: apiPulls,
			header: func(auditedForgeProxy) http.Header {
				return http.Header{"Authorization": {"token " + refusalTestPresented}}
			}},
		{reason: refusalUnsafePath, method: http.MethodGet, path: "/api/v1/repos/webgrip/example/%2e%2e/other/pulls",
			header: placeholderHeader},
		{reason: refusalOutsideRepository, method: http.MethodGet, path: "/api/v1/repos/webgrip/other/pulls?state=open",
			loggedPath: "/api/v1/repos/webgrip/other/pulls", header: placeholderHeader},
		{reason: refusalForbiddenEndpoint, method: http.MethodPost, path: "/api/v1/repos/webgrip/example/pulls/3/merge",
			header: placeholderHeader},
		{reason: refusalPushToOtherBranch, method: http.MethodPost, path: receivePack, header: gitHeader,
			body: pushBody(oldID + " " + newID + " refs/heads/main")},
		{reason: refusalBranchDeletion, method: http.MethodPost, path: receivePack, header: gitHeader,
			body: pushBody(oldID + " " + zeroID + " refs/heads/" + writerBranch)},
		{reason: refusalUninspectablePush, method: http.MethodPost, path: receivePack, header: gitHeader,
			body: packTag},
		{reason: refusalUninspectableBody, method: http.MethodPost, path: apiPulls,
			header: func(p auditedForgeProxy) http.Header {
				return http.Header{"Authorization": {"token " + p.placeholder}, "Content-Encoding": {"gzip"}}
			}, body: "{}"},
		{reason: refusalCredentialInRequest, method: http.MethodPost, path: apiPulls, header: placeholderHeader,
			body: `{"body":"real-forge-token"}`},
		{reason: refusalWritesStoppedByLeak, method: http.MethodPost, path: apiPulls, header: placeholderHeader,
			body: "{}", beforeSends: func(p auditedForgeProxy) {
				p.watch.record(credentialLeak{kind: credentialCanary, where: "an earlier request"})
			}},
	} {
		t.Run(string(c.reason), func(t *testing.T) {
			forge, seen := fakeForge(t)
			p := startAuditedForgeProxy(t, forge.URL, staticForgeToken("real-forge-token"))
			if c.beforeSends != nil {
				c.beforeSends(p)
			}
			if status := sendBody(t, p.forgeTokenProxy, c.method, c.path, c.header(p), strings.NewReader(c.body)); status != http.StatusForbidden {
				t.Fatalf("status %d, want 403", status)
			}
			if seen.count() != 0 {
				t.Fatal("the refused request reached the forge")
			}
			logged := c.loggedPath
			if logged == "" {
				logged = c.path
			}
			assertOneRefusal(t, p.logs, forgeProxyName, c.method, logged, c.reason)
		})
	}
}

func TestForgeProxyLogsNoRefusalForAnAllowedRequestOrTheGitChallenge(t *testing.T) {
	forge, seen := fakeForge(t)
	p := startAuditedForgeProxy(t, forge.URL, staticForgeToken("real-forge-token"))
	sendBody(t, p.forgeTokenProxy, http.MethodPost, "/api/v1/repos/webgrip/example/pulls",
		http.Header{"Authorization": {"token " + p.placeholder}}, strings.NewReader(`{"title":"t"}`))
	if status := send(t, p.forgeTokenProxy, http.MethodGet, "/webgrip/example.git/info/refs?service=git-upload-pack", nil); status != http.StatusUnauthorized {
		t.Fatalf("git without a credential: status %d, want the 401 challenge", status)
	}
	if seen.count() != 1 {
		t.Fatalf("the allowed request reached the forge %d times, want once", seen.count())
	}
	if lines := p.logs.refusals(t); len(lines) != 0 {
		t.Fatalf("an allowed request and the git challenge logged refusals: %v", lines)
	}
}

func TestProxyRefusalLinesCarryNoCredentialOrPlaceholder(t *testing.T) {
	forge, _ := fakeForge(t)
	p := startAuditedForgeProxy(t, forge.URL, staticForgeToken("real-forge-token"))
	p.watch.guard(credentialCanary, leakTestCanary)
	for _, r := range []struct {
		method, path string
		header       http.Header
	}{
		{http.MethodGet, "/api/v1/repos/webgrip/" + p.placeholder + "/pulls?token=" + p.placeholder, http.Header{"Authorization": {"token " + p.placeholder}}},
		{http.MethodGet, "/api/v1/repos/webgrip/real-forge-token/pulls?token=real-forge-token", http.Header{"Authorization": {"token real-forge-token"}}},
		{http.MethodGet, "/webgrip/" + leakTestCanary + ".git/info/refs", http.Header{"Authorization": {"token " + refusalTestPresented}}},
		{http.MethodPost, "/api/v1/repos/webgrip/example/pulls", http.Header{"Authorization": {basicAuth(refusalTestForgeBot, refusalTestPresented)}}},
		{p.placeholder, "/api/v1/repos/webgrip/example/pulls", http.Header{"Private-Token": {p.placeholder}}},
	} {
		sendBody(t, p.forgeTokenProxy, r.method, r.path, r.header, strings.NewReader(`{"body":"real-forge-token"}`))
	}

	m, modelLogs := startAuditedKeyProxy(t)
	for _, header := range []http.Header{
		{"Authorization": {"Bearer " + leakTestModelKey}},
		{"X-Api-Key": {refusalTestPresented}},
	} {
		sendToKeyProxy(t, m, "/v1/"+m.placeholder+"/"+leakTestModelKey+"?key="+m.placeholder, header)
	}

	if n := len(p.logs.refusals(t)); n != 5 {
		t.Fatalf("%d forge refusal lines, want 5:\n%s", n, p.logs.text())
	}
	if n := len(modelLogs.refusals(t)); n != 2 {
		t.Fatalf("%d model refusal lines, want 2:\n%s", n, modelLogs.text())
	}
	logged := p.logs.text() + modelLogs.text()
	for _, secret := range []string{p.placeholder, "real-forge-token", leakTestCanary, refusalTestPresented, m.placeholder, leakTestModelKey} {
		if strings.Contains(logged, secret) {
			t.Errorf("a refusal line carries %q:\n%s", secret, logged)
		}
	}
	if !strings.Contains(logged, redactedInLog) {
		t.Errorf("no path or method was redacted:\n%s", logged)
	}
	if strings.Contains(logged, "?") {
		t.Errorf("a refusal line carries a query string:\n%s", logged)
	}
}

func TestKeyProxyLogsEachRefusalWithItsReason(t *testing.T) {
	for _, c := range []struct {
		name   string
		reason refusalReason
		header http.Header
	}{
		{"no credential", refusalMissingPlaceholder, http.Header{}},
		{"wrong bearer", refusalWrongPlaceholder, http.Header{"Authorization": {"Bearer " + refusalTestPresented}}},
		{"wrong x-api-key", refusalWrongPlaceholder, http.Header{"X-Api-Key": {refusalTestPresented}}},
		{"wrong litellm key", refusalWrongPlaceholder, http.Header{liteLLMKeyHeader: {"Bearer " + refusalTestPresented}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, logs := startAuditedKeyProxy(t)
			if status := sendToKeyProxy(t, p, "/v1/chat/completions?stream=true", c.header); status != http.StatusForbidden {
				t.Fatalf("status %d, want 403", status)
			}
			assertOneRefusal(t, logs, modelProxyName, http.MethodPost, "/v1/chat/completions", c.reason)
		})
	}
}

func TestKeyProxyLogsNoRefusalForTheRunsPlaceholder(t *testing.T) {
	p, logs := startAuditedKeyProxy(t)
	if status := sendToKeyProxy(t, p, "/v1/chat/completions", http.Header{"Authorization": {"Bearer " + p.placeholder}}); status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
	}
	if lines := logs.refusals(t); len(lines) != 0 {
		t.Fatalf("the Run's own placeholder logged refusals: %v", lines)
	}
}

type revocableForgejo struct {
	mu      sync.Mutex
	live    map[string]string
	nextID  int
	revoked []string
	used    []string
}

const revocableTokensPath = "/api/v1/users/" + refusalTestForgeBot + "/tokens"

func (f *revocableForgejo) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.HasPrefix(r.URL.Path, revocableTokensPath) {
		f.serveTokens(w, r)
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "token ")
	f.used = append(f.used, token)
	if _, ok := f.live[token]; !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	_, _ = w.Write([]byte(`[]`))
}

func (f *revocableForgejo) serveTokens(w http.ResponseWriter, r *http.Request) {
	if user, secret, ok := r.BasicAuth(); !ok || user != refusalTestForgeBot || secret != refusalTestBotSecret {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodPost:
		var body struct {
			Name         string           `json:"name"`
			Repositories []map[string]any `json:"repositories"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.nextID++
		token := "minted-per-run-forge-token-" + strings.Repeat("x", f.nextID)
		f.live[token] = body.Name
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": f.nextID, "name": body.Name, "sha1": token, "repositories": body.Repositories})
	case http.MethodDelete:
		name := strings.TrimPrefix(r.URL.Path, revocableTokensPath+"/")
		for token, tokenName := range f.live {
			if tokenName == name {
				delete(f.live, token)
				f.revoked = append(f.revoked, name)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *revocableForgejo) tokensUsed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.used...)
}

func (f *revocableForgejo) revokedNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.revoked...)
}

func TestARevokedPerRunTokenIsRefusedByTheForgeAndTheProxyPassesTheRefusalOn(t *testing.T) {
	fake := &revocableForgejo{live: map[string]string{}}
	forge := httptest.NewServer(http.HandlerFunc(fake.serve))
	defer forge.Close()
	broker := &forgebroker.Forgejo{BaseURL: forge.URL, Bot: refusalTestForgeBot, Password: refusalTestBotSecret}
	cred, err := broker.Mint(context.Background(), forgebroker.MintRequest{RunToken: refusalTestRunToken, Owner: "webgrip", Repo: "example"})
	if err != nil {
		t.Fatal(err)
	}
	p := startAuditedForgeProxy(t, forge.URL, staticForgeToken(cred.Token))
	harnessRequest := http.Header{"Authorization": {"token " + p.placeholder}}
	const pulls = "/api/v1/repos/webgrip/example/pulls"

	if status := send(t, p.forgeTokenProxy, http.MethodGet, pulls, harnessRequest); status != http.StatusOK {
		t.Fatalf("before revocation: status %d, want 200", status)
	}
	if err := broker.Revoke(context.Background(), cred); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if revoked := fake.revokedNames(); len(revoked) != 1 || revoked[0] != cred.Name {
		t.Fatalf("the forge revoked %v, want only %s", revoked, cred.Name)
	}
	if status := send(t, p.forgeTokenProxy, http.MethodGet, pulls, harnessRequest); status != http.StatusUnauthorized {
		t.Fatalf("replay after revocation: status %d, want the forge's 401", status)
	}
	if status := send(t, p.forgeTokenProxy, http.MethodGet, pulls, http.Header{"Authorization": {"token " + cred.Token}}); status != http.StatusForbidden {
		t.Fatalf("the revoked token presented to the proxy: status %d, want the proxy's 403", status)
	}

	if used := fake.tokensUsed(); len(used) != 2 || used[0] != cred.Token || used[1] != cred.Token {
		t.Fatalf("the forge saw %d API requests, want two, both with the minted token", len(used))
	}
	rejected := p.logs.linesWithMessage(t, forgeRejectedCredentialLine)
	if len(rejected) != 1 || rejected[0]["status"] != float64(http.StatusUnauthorized) || rejected[0]["path"] != pulls ||
		rejected[0]["run"] != refusalTestRun || rejected[0]["level"] != "WARN" {
		t.Fatalf("forge-rejected lines %v, want one WARN line for the replay", rejected)
	}
	assertOneRefusal(t, p.logs, forgeProxyName, http.MethodGet, pulls, refusalWrongPlaceholder)
	if strings.Contains(p.logs.text(), cred.Token) {
		t.Fatalf("the worker log carries the revoked token:\n%s", p.logs.text())
	}
}
