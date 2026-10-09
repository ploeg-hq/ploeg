package worker

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
)

type gatewaySeen struct {
	mu            sync.Mutex
	authorization string
	apiKey        string
	litellmKey    string
	path          string
}

func fakeGateway(t *testing.T) (*httptest.Server, *gatewaySeen) {
	t.Helper()
	seen := &gatewaySeen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.mu.Lock()
		seen.authorization = r.Header.Get("Authorization")
		seen.apiKey = r.Header.Get("X-Api-Key")
		seen.litellmKey = r.Header.Get("X-Litellm-Api-Key")
		seen.path = r.URL.Path
		seen.mu.Unlock()
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

func TestKeyProxySwapsTheBearerPlaceholderForTheRealKey(t *testing.T) {
	gw, seen := fakeGateway(t)
	p, err := startLLMKeyProxy(gw.URL+"/v1", "sk-real-run-key", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	if !strings.HasPrefix(p.baseURL, "http://127.0.0.1:") || !strings.HasSuffix(p.baseURL, "/v1") {
		t.Fatalf("proxy base URL %q, want a loopback URL ending in the gateway path", p.baseURL)
	}
	req, _ := http.NewRequest(http.MethodPost, p.baseURL+"/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+p.placeholder)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if seen.authorization != "Bearer sk-real-run-key" {
		t.Fatalf("gateway saw Authorization %q, want the real key", seen.authorization)
	}
	if seen.path != "/v1/chat/completions" {
		t.Fatalf("gateway saw path %q", seen.path)
	}
}

func TestKeyProxySwapsAnAnthropicStyleKey(t *testing.T) {
	gw, seen := fakeGateway(t)
	p, err := startLLMKeyProxy(gw.URL, "sk-real-run-key", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	req, _ := http.NewRequest(http.MethodPost, p.baseURL+"/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("X-Api-Key", p.placeholder)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if seen.apiKey != "sk-real-run-key" || seen.authorization != "" {
		t.Fatalf("gateway saw x-api-key %q and Authorization %q, want only the real x-api-key", seen.apiKey, seen.authorization)
	}
}

func TestKeyProxyStreamsWithoutBuffering(t *testing.T) {
	release := make(chan struct{})
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer gw.Close()
	defer close(release)
	p, err := startLLMKeyProxy(gw.URL, "sk-real-run-key", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	req, _ := http.NewRequest(http.MethodGet, p.baseURL+"/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+p.placeholder)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line := make(chan string, 1)
	go func() {
		l, _ := bufio.NewReader(resp.Body).ReadString('\n')
		line <- l
	}()
	select {
	case l := <-line:
		if !strings.Contains(l, "first") {
			t.Fatalf("first streamed line = %q", l)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the proxy buffered a streaming response; tokens would arrive only at the end")
	}
}

func TestKeyProxyRefusesRequestsWithoutThePlaceholder(t *testing.T) {
	var forwarded atomic.Int32
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded.Add(1)
	}))
	defer gw.Close()
	p, err := startLLMKeyProxy(gw.URL, "sk-real-run-key", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	for name, header := range map[string]http.Header{
		"no credential":                 {},
		"wrong bearer":                  {"Authorization": {"Bearer ploeg-isolated-guess"}},
		"wrong x-api-key":               {"X-Api-Key": {"ploeg-isolated-guess"}},
		"placeholder without a scheme":  {"Authorization": {p.placeholder}},
		"placeholder as basic password": {"Authorization": {"Basic " + p.placeholder}},
		"placeholder prefix":            {"Authorization": {"Bearer " + p.placeholder[:len(p.placeholder)-1]}},
		"placeholder with a suffix":     {"X-Api-Key": {p.placeholder + "x"}},
		"placeholder in another header": {"Api-Key": {p.placeholder}},
		"the real key itself":           {"Authorization": {"Bearer sk-real-run-key"}},
	} {
		req, _ := http.NewRequest(http.MethodPost, p.baseURL+"/v1/chat/completions", strings.NewReader(`{}`))
		req.Header = header
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", name, resp.StatusCode)
		}
	}
	if n := forwarded.Load(); n != 0 {
		t.Fatalf("%d requests without the placeholder reached the gateway", n)
	}
	req, _ := http.NewRequest(http.MethodPost, p.baseURL+"/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "bearer "+p.placeholder)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || forwarded.Load() != 1 {
		t.Fatalf("the placeholder got status %d and %d forwards, want 200 and 1", resp.StatusCode, forwarded.Load())
	}
}

func TestModelObserverForwardsWithoutAPlaceholder(t *testing.T) {
	gw, seen := fakeGateway(t)
	p, err := startLLMObserver(gw.URL, harness.NewActivity())
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	req, _ := http.NewRequest(http.MethodPost, p.baseURL+"/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer sk-harness-held-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || seen.authorization != "Bearer sk-harness-held-key" {
		t.Fatalf("observer answered %d and forwarded %q, want 200 and the harness's own key", resp.StatusCode, seen.authorization)
	}
}

type envCapturingAdapter struct {
	seen   harness.RunEnv
	status int
	auth   string
}

func (a *envCapturingAdapter) Name() string     { return "capture" }
func (a *envCapturingAdapter) ExpectsLLM() bool { return true }
func (a *envCapturingAdapter) Run(_ context.Context, _ harness.TaskSpec, env harness.RunEnv) (harness.OutcomeReport, error) {
	a.seen = env
	req, _ := http.NewRequest(http.MethodPost, env.LLM.BaseURL+"/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+env.LLM.APIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return harness.OutcomeReport{}, err
	}
	resp.Body.Close()
	a.status = resp.StatusCode
	return harness.OutcomeReport{}, nil
}

func TestIsolatedRunNeverHandsTheKeyToTheHarness(t *testing.T) {
	gw, seen := fakeGateway(t)
	broker := &recordingBroker{key: "sk-real-run-key"}
	adapter := &envCapturingAdapter{}
	env := runEnv(t)
	env.LLM.BaseURL = gw.URL + "/v1"
	env.BaseEnv = append(env.BaseEnv, "LLM_BASE_URL="+gw.URL+"/v1")
	_, mintErr, runErr := runAgent(context.Background(), discardLog(), broker, adapter, testTaskSpec(), env,
		llmbroker.MintRequest{RunToken: "abc123def456ff"}, 0, KeyIsolationProxy)
	if mintErr != nil || runErr != nil {
		t.Fatalf("mint=%v run=%v", mintErr, runErr)
	}
	if adapter.seen.LLM.APIKey == "sk-real-run-key" {
		t.Fatal("the harness received the real per-run key")
	}
	for _, kv := range adapter.seen.BaseEnv {
		if strings.Contains(kv, "sk-real-run-key") {
			t.Fatalf("the harness environment carries the real key: %s", kv)
		}
		if strings.HasPrefix(kv, "LLM_BASE_URL=") && !strings.HasPrefix(kv, "LLM_BASE_URL=http://127.0.0.1:") {
			t.Fatalf("the harness still points at the gateway directly: %s", kv)
		}
	}
	if adapter.status != http.StatusOK || seen.authorization != "Bearer sk-real-run-key" {
		t.Fatalf("harness call status %d reached the gateway with %q, want the real key", adapter.status, seen.authorization)
	}
	if _, err := http.Get(adapter.seen.LLM.BaseURL + "/models"); err == nil {
		t.Fatal("the key proxy still answers after the Run ended")
	}
	if broker.revoked != 1 {
		t.Fatalf("revoked %d times, want 1", broker.revoked)
	}
}

func TestUnisolatedRunKeepsHandingTheKeyOver(t *testing.T) {
	adapter := &envCapturingAdapter{}
	gw, _ := fakeGateway(t)
	env := runEnv(t)
	env.LLM.BaseURL = gw.URL
	_, _, _ = runAgent(context.Background(), discardLog(), &recordingBroker{key: "sk-real-run-key"}, adapter,
		testTaskSpec(), env, llmbroker.MintRequest{RunToken: "abc123def456ff"}, 0, "")
	if adapter.seen.LLM.APIKey != "sk-real-run-key" {
		t.Fatalf("without isolation the harness key = %q, want the minted key", adapter.seen.LLM.APIKey)
	}
}

type silentModelCaller struct {
	stop chan struct{}
	auth chan string
}

func (silentModelCaller) Name() string     { return "silent" }
func (silentModelCaller) ExpectsLLM() bool { return true }

func (a silentModelCaller) Prepare(_ harness.TaskSpec, env harness.RunEnv) (harness.Invocation, error) {
	go func() {
		for {
			select {
			case <-a.stop:
				return
			case <-time.After(50 * time.Millisecond):
			}
			req, _ := http.NewRequest(http.MethodPost, env.LLM.BaseURL+"/chat/completions", strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Bearer "+env.LLM.APIKey)
			if resp, err := http.DefaultClient.Do(req); err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
			select {
			case a.auth <- env.LLM.APIKey:
			default:
			}
		}
	}()
	return harness.Invocation{Argv: []string{"/bin/sh", "-c", "sleep 1"}}, nil
}

func (silentModelCaller) ParseOutcome(harness.TaskSpec, harness.ExecResult) (harness.OutcomeReport, error) {
	return harness.OutcomeReport{}, nil
}

func TestSilentHarnessThatKeepsCallingTheModelIsNotIdle(t *testing.T) {
	gw, seen := fakeGateway(t)
	adapter := silentModelCaller{stop: make(chan struct{}), auth: make(chan string, 1)}
	defer close(adapter.stop)
	env := runEnv(t)
	env.LLM.BaseURL = gw.URL + "/v1"
	env.IdleTimeout = 300 * time.Millisecond
	env.Activity = harness.NewActivity()
	_, mintErr, runErr := runAgent(context.Background(), discardLog(), &recordingBroker{key: "sk-real-run-key"},
		harness.RunCommand(adapter), testTaskSpec(), env, llmbroker.MintRequest{RunToken: "abc123def456ff"}, 0, "")
	if mintErr != nil || runErr != nil {
		t.Fatalf("a harness calling the model without printing was stopped: mint=%v run=%v", mintErr, runErr)
	}
	if key := <-adapter.auth; key != "sk-real-run-key" {
		t.Fatalf("without isolation the harness key = %q, want the minted key", key)
	}
	seen.mu.Lock()
	defer seen.mu.Unlock()
	if seen.authorization != "Bearer sk-real-run-key" {
		t.Fatalf("the observing proxy changed the key: %q", seen.authorization)
	}
}

// ADR-0078: the gateway's MCP endpoint reads the key from x-litellm-api-key.
// The proxy accepts the placeholder there and forwards the real key in the
// same header, and never the placeholder or a second credential.
func TestKeyProxySwapsTheLiteLLMKeyHeaderForMCP(t *testing.T) {
	gw, seen := fakeGateway(t)
	p, err := startLLMKeyProxy(gw.URL+"/v1", "sk-real-run-key", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	req, _ := http.NewRequest(http.MethodPost, gatewayMCPURL(p.baseURL), strings.NewReader(`{}`))
	req.Header.Set("x-litellm-api-key", "Bearer "+p.placeholder)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("proxy refused the placeholder in x-litellm-api-key: HTTP %d", resp.StatusCode)
	}
	if seen.litellmKey != "Bearer sk-real-run-key" || seen.authorization != "" || seen.apiKey != "" {
		t.Fatalf("gateway saw x-litellm-api-key %q, Authorization %q, x-api-key %q; want only the real key in x-litellm-api-key",
			seen.litellmKey, seen.authorization, seen.apiKey)
	}
	if seen.path != "/mcp" {
		t.Fatalf("gateway saw path %q, want /mcp at the gateway root", seen.path)
	}

	req, _ = http.NewRequest(http.MethodPost, gatewayMCPURL(p.baseURL), strings.NewReader(`{}`))
	req.Header.Set("x-litellm-api-key", "Bearer sk-someone-elses-key")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a foreign key in x-litellm-api-key got HTTP %d, want 403", resp.StatusCode)
	}
}

func TestGatewayMCPURLIsTheGatewayRoot(t *testing.T) {
	for base, want := range map[string]string{
		"http://litellm.gateway.svc.cluster.local:4000/v1":  "http://litellm.gateway.svc.cluster.local:4000/mcp",
		"http://litellm.gateway.svc.cluster.local:4000/v1/": "http://litellm.gateway.svc.cluster.local:4000/mcp",
		"http://127.0.0.1:41234":                            "http://127.0.0.1:41234/mcp",
		"https://gw.example/litellm/v1":                     "https://gw.example/litellm/mcp",
	} {
		if got := gatewayMCPURL(base); got != want {
			t.Errorf("gatewayMCPURL(%q) = %q, want %q", base, got, want)
		}
	}
}

// Only a credential minted with MCP access groups gives the harness the
// gateway's MCP endpoint, and under isolation it points at the loopback proxy
// with the placeholder, never at the gateway with the real key.
func TestRunGetsTheGatewayMCPEndpointOnlyWithAGrant(t *testing.T) {
	gw, _ := fakeGateway(t)
	for _, tc := range []struct {
		name   string
		groups []string
	}{{"without a grant", nil}, {"with a grant", []string{"observability-read-orders"}}} {
		t.Run(tc.name, func(t *testing.T) {
			adapter := &envCapturingAdapter{}
			env := runEnv(t)
			env.LLM.BaseURL = gw.URL + "/v1"
			_, mintErr, runErr := runAgent(context.Background(), discardLog(), &recordingBroker{key: "sk-real-run-key", groups: tc.groups},
				adapter, testTaskSpec(), env, llmbroker.MintRequest{RunToken: "abc123def456ff"}, 0, KeyIsolationProxy)
			if mintErr != nil || runErr != nil {
				t.Fatalf("mint=%v run=%v", mintErr, runErr)
			}
			got := adapter.seen.LLM.MCPURL
			if tc.groups == nil {
				if got != "" {
					t.Fatalf("a Run without MCP access groups got MCP endpoint %q", got)
				}
				return
			}
			if !strings.HasPrefix(got, "http://127.0.0.1:") || !strings.HasSuffix(got, "/mcp") || strings.Contains(got, "/v1") {
				t.Fatalf("MCP endpoint %q, want the loopback proxy root plus /mcp", got)
			}
			if adapter.seen.LLM.APIKey == "sk-real-run-key" {
				t.Fatal("the harness received the real per-run key")
			}
		})
	}
}
