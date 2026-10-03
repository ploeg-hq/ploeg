package acp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

func profileEnv(t *testing.T) harness.RunEnv {
	t.Helper()
	return harness.RunEnv{
		RepoDir:    t.TempDir(),
		ScratchDir: t.TempDir(),
		LLM: harness.LLMEnv{
			APIKey:  "ploeg-isolated-canary",
			BaseURL: "http://127.0.0.1:43111/v1",
			Model:   "claude-sonnet-5",
			TraceID: "ploeg-abc123def456",
		},
	}
}

func envValue(t *testing.T, env []string, key string) string {
	t.Helper()
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v
		}
	}
	t.Fatalf("%s is not in the environment %v", key, env)
	return ""
}

func TestLookup_KnowsTheQwenCodeAndGooseProfiles(t *testing.T) {
	for name, argv := range map[string][]string{
		"qwen-code": {"qwen", "--acp", "--auth-type=openai"},
		"goose":     {"goose", "acp"},
		"openhands": {"openhands", "acp", "--override-with-envs"},
	} {
		p, err := Lookup(name, ProfileOverrides{})
		if err != nil {
			t.Fatalf("Lookup(%q): %v", name, err)
		}
		if !slices.Equal(p.Argv, argv) {
			t.Errorf("%s argv = %v, want %v", name, p.Argv, argv)
		}
		o, err := Lookup(name, ProfileOverrides{Entrypoint: "/opt/agent"})
		if err != nil || o.Argv[0] != "/opt/agent" || !slices.Equal(o.Argv[1:], argv[1:]) {
			t.Errorf("%s entrypoint override gave %v, %v", name, o.Argv, err)
		}
	}
}

func TestLookup_GooseRefusesAConfigDocumentItCannotRead(t *testing.T) {
	if _, err := Lookup("goose", ProfileOverrides{ConfigJSON: `{}`}); err == nil {
		t.Fatal("goose accepted a config document it would silently ignore")
	}
}

func TestProfile_QwenCodeUsesTheGatewayAndAgentsMD(t *testing.T) {
	p, err := Lookup("qwen-code", ProfileOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	env := profileEnv(t)
	_, extra, err := p.Prepare(testSpec(), env, PermissionAllowAll)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"OPENAI_API_KEY":                env.LLM.APIKey,
		"OPENAI_BASE_URL":               env.LLM.BaseURL,
		"OPENAI_MODEL":                  env.LLM.Model,
		"QWEN_USAGE_STATISTICS_ENABLED": "false",
		"QWEN_CODE_NO_RELAUNCH":         "true",
	} {
		if got := envValue(t, extra, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	home := envValue(t, extra, "QWEN_HOME")
	if fi, err := os.Stat(home); err != nil || !fi.IsDir() || !strings.Contains(home, "ploeg-abc123def456") {
		t.Errorf("QWEN_HOME %q is not a trace-scoped directory: %v", home, err)
	}
	path := envValue(t, extra, "QWEN_CODE_SYSTEM_SETTINGS_PATH")
	if !strings.Contains(path, "ploeg-abc123def456") {
		t.Errorf("settings path %q is not trace-scoped", path)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("settings file %q: %v", path, err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), env.LLM.APIKey) {
		t.Fatal("the model key was written into the settings file")
	}
	var doc struct {
		Security struct {
			Auth struct {
				SelectedType string `json:"selectedType"`
			} `json:"auth"`
		} `json:"security"`
		Context struct {
			FileName []string `json:"fileName"`
		} `json:"context"`
		Tools struct {
			ApprovalMode string `json:"approvalMode"`
			WebSearch    struct {
				Enabled bool `json:"enabled"`
			} `json:"webSearch"`
		} `json:"tools"`
		General struct {
			EnableAutoUpdate *bool `json:"enableAutoUpdate"`
		} `json:"general"`
		Privacy struct {
			UsageStatisticsEnabled *bool `json:"usageStatisticsEnabled"`
		} `json:"privacy"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("settings are not JSON: %v", err)
	}
	if doc.Security.Auth.SelectedType != "openai" {
		t.Errorf("auth type = %q, want openai", doc.Security.Auth.SelectedType)
	}
	if !slices.Equal(doc.Context.FileName, []string{"AGENTS.md"}) {
		t.Errorf("context files = %v, want [AGENTS.md]", doc.Context.FileName)
	}
	if doc.Tools.WebSearch.Enabled || doc.General.EnableAutoUpdate == nil || *doc.General.EnableAutoUpdate ||
		doc.Privacy.UsageStatisticsEnabled == nil || *doc.Privacy.UsageStatisticsEnabled {
		t.Errorf("settings leave a call outside the gateway enabled: %s", body)
	}
}

func TestProfile_AgentApprovalFollowsThePermissionMode(t *testing.T) {
	for _, tc := range []struct {
		mode        PermissionMode
		qwen, goose string
	}{
		{PermissionAllowAll, "yolo", "auto"},
		{PermissionReadOnly, "default", "approve"},
		{PermissionDenyAll, "default", "approve"},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			env := profileEnv(t)
			q, _ := Lookup("qwen-code", ProfileOverrides{})
			_, extra, err := q.Prepare(testSpec(), env, tc.mode)
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(envValue(t, extra, "QWEN_CODE_SYSTEM_SETTINGS_PATH"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), `"approvalMode": "`+tc.qwen+`"`) {
				t.Errorf("qwen-code settings under %s do not set approvalMode %q: %s", tc.mode, tc.qwen, body)
			}
			g, _ := Lookup("goose", ProfileOverrides{})
			_, extra, err = g.Prepare(testSpec(), env, tc.mode)
			if err != nil {
				t.Fatal(err)
			}
			if got := envValue(t, extra, "GOOSE_MODE"); got != tc.goose {
				t.Errorf("GOOSE_MODE under %s = %q, want %q", tc.mode, got, tc.goose)
			}
		})
	}
}

func TestProfile_QwenCodeConfigOverrideReplacesTheSettings(t *testing.T) {
	p, err := Lookup("qwen-code", ProfileOverrides{ConfigJSON: `{"hand":"written"}`})
	if err != nil {
		t.Fatal(err)
	}
	_, extra, err := p.Prepare(testSpec(), profileEnv(t), PermissionAllowAll)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(envValue(t, extra, "QWEN_CODE_SYSTEM_SETTINGS_PATH")); string(b) != `{"hand":"written"}` {
		t.Errorf("override ignored; settings = %s", b)
	}
}

func TestProfile_GooseUsesTheLiteLLMProviderThroughEnvironmentOnly(t *testing.T) {
	p, err := Lookup("goose", ProfileOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	env := profileEnv(t)
	_, extra, err := p.Prepare(testSpec(), env, PermissionAllowAll)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"GOOSE_PROVIDER":               "litellm",
		"GOOSE_MODEL":                  env.LLM.Model,
		"LITELLM_HOST":                 "http://127.0.0.1:43111",
		"LITELLM_BASE_PATH":            "v1/chat/completions",
		"LITELLM_API_KEY":              env.LLM.APIKey,
		"GOOSE_DISABLE_KEYRING":        "1",
		"GOOSE_TELEMETRY_OFF":          "1",
		"GOOSE_DISABLE_SESSION_NAMING": "true",
		"CONTEXT_FILE_NAMES":           `["AGENTS.md"]`,
	} {
		if got := envValue(t, extra, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	root := envValue(t, extra, "GOOSE_PATH_ROOT")
	if !filepath.IsAbs(root) || !strings.Contains(root, "ploeg-abc123def456") {
		t.Errorf("GOOSE_PATH_ROOT %q is not an absolute trace-scoped path", root)
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		t.Errorf("GOOSE_PATH_ROOT %q was not created: %v", root, err)
	}
	entries, _ := os.ReadDir(env.ScratchDir)
	for _, e := range entries {
		if !e.IsDir() {
			t.Errorf("goose wrote %s; it is configured by environment only", e.Name())
		}
	}
}

func TestSplitGatewayURL(t *testing.T) {
	for _, tc := range []struct{ in, host, path string }{
		{"http://litellm.ai.svc:4000/v1", "http://litellm.ai.svc:4000", "v1/chat/completions"},
		{"http://127.0.0.1:5555/gateway/v1/", "http://127.0.0.1:5555", "gateway/v1/chat/completions"},
		{"https://gateway.example", "https://gateway.example", "chat/completions"},
	} {
		host, path, err := splitGatewayURL(tc.in)
		if err != nil || host != tc.host || path != tc.path {
			t.Errorf("splitGatewayURL(%q) = %q, %q, %v; want %q, %q", tc.in, host, path, err, tc.host, tc.path)
		}
	}
	for _, bad := range []string{"", "litellm:4000/v1", "/v1"} {
		if _, _, err := splitGatewayURL(bad); err == nil {
			t.Errorf("splitGatewayURL(%q) accepted a relative URL", bad)
		}
	}
}

func TestProfile_OpenHandsReadsTheGatewayFromEnvironmentWithTheProxyPrefix(t *testing.T) {
	p, err := Lookup("openhands", ProfileOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	_, env, err := p.Prepare(harness.TaskSpec{TraceID: "ploeg-abc123def456"}, harness.RunEnv{
		ScratchDir: t.TempDir(),
		LLM:        harness.LLMEnv{APIKey: "sk-run", BaseURL: "http://127.0.0.1:40777/v1", Model: "deepseek-chat"},
	}, PermissionAllowAll)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"LLM_API_KEY":               "sk-run",
		"LITELLM_PROXY_API_KEY":     "sk-run",
		"LLM_BASE_URL":              "http://127.0.0.1:40777/v1",
		"LLM_MODEL":                 "litellm_proxy/deepseek-chat",
		"OPENHANDS_SUPPRESS_BANNER": "1",
	} {
		if got := envValue(t, env, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if _, err := Lookup("openhands", ProfileOverrides{ConfigJSON: `{}`}); err == nil {
		t.Fatal("openhands accepted a config document it would silently ignore")
	}
}

func TestProfile_OpenHandsWritesKeylessAgentSettingsSoSessionNewIsAuthorized(t *testing.T) {
	p, err := Lookup("openhands", ProfileOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	env := profileEnv(t)
	_, extra, err := p.Prepare(testSpec(), env, PermissionAllowAll)
	if err != nil {
		t.Fatal(err)
	}
	dir := envValue(t, extra, "OPENHANDS_PERSISTENCE_DIR")
	if !filepath.IsAbs(dir) || !strings.HasPrefix(dir, env.ScratchDir) || !strings.Contains(dir, "ploeg-abc123def456") {
		t.Errorf("OPENHANDS_PERSISTENCE_DIR %q is not a trace-scoped directory in scratch", dir)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("persistence directory %q: %v", dir, err)
	}
	path := filepath.Join(dir, "agent_settings.json")
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("agent settings %q: %v", path, err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), env.LLM.APIKey) || strings.Contains(string(body), "api_key") {
		t.Fatalf("the model key was written into the agent settings: %s", body)
	}
	var doc struct {
		LLM map[string]any `json:"llm"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("agent settings are not JSON: %v", err)
	}
	for key, want := range map[string]string{
		"model":    "litellm_proxy/" + env.LLM.Model,
		"base_url": env.LLM.BaseURL,
		"usage_id": "agent",
	} {
		if got := doc.LLM[key]; got != want {
			t.Errorf("llm.%s = %v, want %q", key, got, want)
		}
	}
	if got := envValue(t, extra, "LITELLM_PROXY_API_KEY"); got != env.LLM.APIKey {
		t.Errorf("LITELLM_PROXY_API_KEY = %q, want the Run's key %q", got, env.LLM.APIKey)
	}

	other := testSpec()
	other.TraceID = "ploeg-other"
	_, extra2, err := p.Prepare(other, env, PermissionAllowAll)
	if err != nil {
		t.Fatal(err)
	}
	if envValue(t, extra2, "OPENHANDS_PERSISTENCE_DIR") == dir {
		t.Error("two Runs sharing a scratch directory got the same OpenHands persistence directory")
	}
}

func TestProfile_OpenHandsRefusesARunWithoutAModel(t *testing.T) {
	p, err := Lookup("openhands", ProfileOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	env := profileEnv(t)
	env.LLM.Model = ""
	if _, _, err := p.Prepare(testSpec(), env, PermissionAllowAll); err == nil {
		t.Fatal("openhands started without a model; its agent settings would be invalid")
	}
}
