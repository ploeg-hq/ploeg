package acp

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

// Profile is how the ACP adapter launches one kind of agent: its command, the
// environment that points it at Ploeg's model gateway, and an optional config
// file. The registry is the switch in Lookup; `custom` is the escape hatch.
// docs/contracts/acp-profiles.md records each profile's upstream version and
// settings.
type Profile struct {
	Name string
	// Argv is the launch command. argv[0] may be overridden per team.
	Argv []string
	// Env translates Ploeg's neutral LLMEnv into the agent's native names.
	Env func(harness.LLMEnv) []string
	// Configure optionally writes an agent config file and returns the env
	// that points at it. ScratchDir is os.TempDir() and shared per process, so
	// every file it writes MUST be trace-id-suffixed. mode is the Run's
	// permission mode, for agents whose own approval setting must match it.
	Configure func(spec harness.TaskSpec, env harness.RunEnv, mode PermissionMode) ([]string, error)
}

// ProfileOverrides carries per-team Helm settings.
type ProfileOverrides struct {
	// Entrypoint replaces argv[0] (PLOEG_HARNESS_ENTRYPOINT), consistent with
	// the other three adapters.
	Entrypoint string
	// Argv replaces the whole command. Required for `custom`.
	Argv []string
	// ConfigJSON replaces a profile's generated config wholesale, so a
	// provider-block change does not need an image rebuild.
	ConfigJSON string
}

// Prepare resolves the launch command and environment for one run under the
// given permission mode.
func (p Profile) Prepare(spec harness.TaskSpec, env harness.RunEnv, mode PermissionMode) (argv []string, extraEnv []string, err error) {
	argv = append([]string{}, p.Argv...)
	if len(argv) == 0 {
		return nil, nil, fmt.Errorf("profile %q has no argv", p.Name)
	}
	if p.Env != nil {
		extraEnv = append(extraEnv, p.Env(env.LLM)...)
	}
	if p.Configure != nil {
		cfgEnv, cfgErr := p.Configure(spec, env, mode)
		if cfgErr != nil {
			return nil, nil, cfgErr
		}
		extraEnv = append(extraEnv, cfgEnv...)
	}
	return argv, extraEnv, nil
}

// Lookup resolves a profile by name, applying per-team overrides. An unknown
// name is an error, never a silent fallback: worker.NewAdapter calls this
// before Claim, so a misconfigured team fails at startup instead of leasing a
// ticket it cannot work.
func Lookup(name string, o ProfileOverrides) (Profile, error) {
	var p Profile
	switch normalise(name) {
	case "opencode", "":
		p = opencodeProfile(o.ConfigJSON)
	case "qwen_code":
		p = qwenCodeProfile(o.ConfigJSON)
	case "goose":
		if o.ConfigJSON != "" {
			return Profile{}, fmt.Errorf("acp profile \"goose\" is configured by environment only and takes no config JSON")
		}
		p = gooseProfile()
	case "openhands":
		if o.ConfigJSON != "" {
			return Profile{}, fmt.Errorf("acp profile \"openhands\" writes its own agent settings and takes no config JSON")
		}
		p = openhandsProfile()
	case "custom":
		if len(o.Argv) == 0 {
			return Profile{}, fmt.Errorf("acp profile \"custom\" requires an explicit argv")
		}
		p = Profile{Name: "custom", Argv: o.Argv, Env: openAICompatEnv}
	default:
		return Profile{}, fmt.Errorf("unknown acp profile %q (known: opencode, qwen-code, goose, openhands, custom)", name)
	}
	if len(o.Argv) > 0 {
		p.Argv = o.Argv
	}
	if o.Entrypoint != "" && len(p.Argv) > 0 {
		p.Argv[0] = o.Entrypoint
	}
	return p, nil
}

// opencodeProfile is the flagship. opencode has been stable since v1.0.0
// (2025-10-31), is MIT, and speaks ACP natively — the facts that made
// homelab-cluster ADR-0051 possible.
//
// The exact provider-config key names are the one thing here that must be
// verified against a real binary; that is what the //go:build manual probe is
// for. Until then PLOEG_ACP_CONFIG_JSON overrides the whole document without an
// image rebuild, so a key-name surprise is a values edit rather than a release.
func opencodeProfile(configJSON string) Profile {
	return Profile{
		Name: "opencode",
		Argv: []string{"opencode", "acp"},
		Env:  openAICompatEnv,
		Configure: func(spec harness.TaskSpec, env harness.RunEnv, _ PermissionMode) ([]string, error) {
			doc := configJSON
			if doc == "" {
				var err error
				if doc, err = opencodeConfigDoc(env.LLM); err != nil {
					return nil, err
				}
			}
			path := filepath.Join(env.ScratchDir, "opencode-"+safeSuffix(spec.TraceID)+".json")
			if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
				return nil, fmt.Errorf("write opencode config: %w", err)
			}
			return []string{"OPENCODE_CONFIG=" + path}, nil
		},
	}
}

func opencodeConfigDoc(l harness.LLMEnv) (string, error) {
	model := l.Model
	if model == "" {
		model = "default"
	}
	cfg := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"provider": map[string]any{
			"litellm": map[string]any{
				"npm": "@ai-sdk/openai-compatible",
				"options": map[string]any{
					"baseURL": l.BaseURL,
					"apiKey":  "{env:LLM_API_KEY}",
				},
				"models": map[string]any{model: map[string]any{}},
			},
		},
		"model": "litellm/" + model,
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	return string(b), err
}

func qwenCodeProfile(configJSON string) Profile {
	return Profile{
		Name: "qwen-code",
		Argv: []string{"qwen", "--acp", "--auth-type=openai"},
		Env:  qwenCodeEnv,
		Configure: func(spec harness.TaskSpec, env harness.RunEnv, mode PermissionMode) ([]string, error) {
			home := filepath.Join(env.ScratchDir, "qwen-home-"+safeSuffix(spec.TraceID))
			if err := os.MkdirAll(home, 0o700); err != nil {
				return nil, fmt.Errorf("create qwen-code home: %w", err)
			}
			doc := configJSON
			if doc == "" {
				var err error
				if doc, err = qwenCodeSettingsDoc(env.LLM, mode); err != nil {
					return nil, err
				}
			}
			path := filepath.Join(env.ScratchDir, "qwen-code-"+safeSuffix(spec.TraceID)+".json")
			if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
				return nil, fmt.Errorf("write qwen-code settings: %w", err)
			}
			return []string{"QWEN_HOME=" + home, "QWEN_CODE_SYSTEM_SETTINGS_PATH=" + path}, nil
		},
	}
}

func qwenCodeEnv(l harness.LLMEnv) []string {
	out := openAICompatEnv(l)
	if l.Model != "" {
		out = append(out, "OPENAI_MODEL="+l.Model)
	}
	return append(out, "QWEN_USAGE_STATISTICS_ENABLED=false", "QWEN_CODE_NO_RELAUNCH=true")
}

func qwenCodeSettingsDoc(l harness.LLMEnv, mode PermissionMode) (string, error) {
	approval := "default"
	if mode == PermissionAllowAll {
		approval = "yolo"
	}
	cfg := map[string]any{
		"security": map[string]any{"auth": map[string]any{"selectedType": "openai"}},
		"model":    map[string]any{"name": l.Model},
		"context":  map[string]any{"fileName": []string{"AGENTS.md"}},
		"tools": map[string]any{
			"approvalMode": approval,
			"webSearch":    map[string]any{"enabled": false},
		},
		"general": map[string]any{"enableAutoUpdate": false},
		"privacy": map[string]any{"usageStatisticsEnabled": false},
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	return string(b), err
}

func gooseProfile() Profile {
	return Profile{
		Name: "goose",
		Argv: []string{"goose", "acp"},
		Configure: func(spec harness.TaskSpec, env harness.RunEnv, mode PermissionMode) ([]string, error) {
			host, basePath, err := splitGatewayURL(env.LLM.BaseURL)
			if err != nil {
				return nil, err
			}
			root := filepath.Join(env.ScratchDir, "goose-"+safeSuffix(spec.TraceID))
			if err := os.MkdirAll(root, 0o700); err != nil {
				return nil, fmt.Errorf("create goose root: %w", err)
			}
			gooseMode := "approve"
			if mode == PermissionAllowAll {
				gooseMode = "auto"
			}
			out := []string{
				"GOOSE_PROVIDER=litellm",
				"GOOSE_MODEL=" + env.LLM.Model,
				"LITELLM_HOST=" + host,
				"LITELLM_BASE_PATH=" + basePath,
				"LITELLM_API_KEY=" + env.LLM.APIKey,
				"GOOSE_PATH_ROOT=" + root,
				"GOOSE_DISABLE_KEYRING=1",
				"GOOSE_MODE=" + gooseMode,
				"GOOSE_TELEMETRY_OFF=1",
				"GOOSE_DISABLE_SESSION_NAMING=true",
				`CONTEXT_FILE_NAMES=["AGENTS.md"]`,
			}
			if env.LLM.TraceID != "" {
				out = append(out, "LLM_TRACE_ID="+env.LLM.TraceID)
			}
			return out, nil
		},
	}
}

// openhandsProfile runs the OpenHands CLI (MIT) as an ACP agent.
// docs/contracts/acp-profiles.md records why it writes agent_settings.json.
func openhandsProfile() Profile {
	return Profile{
		Name: "openhands",
		Argv: []string{"openhands", "acp", "--override-with-envs"},
		Env: func(l harness.LLMEnv) []string {
			out := []string{"OPENHANDS_SUPPRESS_BANNER=1"}
			if l.APIKey != "" {
				out = append(out, "LLM_API_KEY="+l.APIKey, "LITELLM_PROXY_API_KEY="+l.APIKey)
			}
			if l.BaseURL != "" {
				out = append(out, "LLM_BASE_URL="+l.BaseURL)
			}
			if l.Model != "" {
				out = append(out, "LLM_MODEL="+openhandsModel(l.Model))
			}
			if l.TraceID != "" {
				out = append(out, "LLM_TRACE_ID="+l.TraceID)
			}
			return out
		},
		Configure: func(spec harness.TaskSpec, env harness.RunEnv, _ PermissionMode) ([]string, error) {
			if env.LLM.Model == "" {
				return nil, fmt.Errorf("acp profile \"openhands\" needs a model for its agent settings")
			}
			dir := filepath.Join(env.ScratchDir, "openhands-"+safeSuffix(spec.TraceID))
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, fmt.Errorf("create openhands persistence directory: %w", err)
			}
			doc, err := openhandsAgentSettingsDoc(env.LLM)
			if err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(dir, "agent_settings.json"), []byte(doc), 0o600); err != nil {
				return nil, fmt.Errorf("write openhands agent settings: %w", err)
			}
			return []string{"OPENHANDS_PERSISTENCE_DIR=" + dir}, nil
		},
	}
}

func openhandsModel(model string) string {
	return "litellm_proxy/" + model
}

func openhandsAgentSettingsDoc(l harness.LLMEnv) (string, error) {
	llm := map[string]any{
		"model":    openhandsModel(l.Model),
		"usage_id": "agent",
	}
	if l.BaseURL != "" {
		llm["base_url"] = l.BaseURL
	}
	b, err := json.MarshalIndent(map[string]any{"llm": llm}, "", "  ")
	return string(b), err
}

func splitGatewayURL(baseURL string) (host, chatPath string, err error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", "", fmt.Errorf("model gateway URL %q is not absolute", baseURL)
	}
	chatPath = strings.TrimPrefix(strings.TrimRight(u.Path, "/")+"/chat/completions", "/")
	return u.Scheme + "://" + u.Host, chatPath, nil
}

// openAICompatEnv is the lowest common denominator. Most agents read at least
// one of these, and the LLM_* names are already in BaseEnv for harnesses that
// read them directly.
func openAICompatEnv(l harness.LLMEnv) []string {
	var out []string
	if l.APIKey != "" {
		out = append(out, "OPENAI_API_KEY="+l.APIKey, "LLM_API_KEY="+l.APIKey)
	}
	if l.BaseURL != "" {
		out = append(out, "OPENAI_BASE_URL="+l.BaseURL, "LLM_BASE_URL="+l.BaseURL)
	}
	if l.Model != "" {
		out = append(out, "LLM_MODEL="+l.Model)
	}
	if l.TraceID != "" {
		out = append(out, "LLM_TRACE_ID="+l.TraceID)
	}
	return out
}

// safeSuffix keeps a trace id usable as a filename. ScratchDir is shared, so
// two concurrent runs must not collide on one config path.
func safeSuffix(s string) string {
	if s == "" {
		return "run"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, s)
}
