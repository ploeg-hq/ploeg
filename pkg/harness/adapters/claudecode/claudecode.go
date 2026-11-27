// Package claudecode adapts the Claude Code CLI (backlog #62): a headless
// `claude -p` run with a JSON result envelope mapped into OutcomeReport
// usage (cost, tokens, session id). The run outcome itself still comes from
// the orchestrator's forge poll — Claude does not know whether its PR landed.
package claudecode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

const (
	// DefaultBin is the Claude Code CLI binary; override via
	// PLOEG_HARNESS_ENTRYPOINT for images that bake it elsewhere.
	DefaultBin = "claude"
	// DefaultPermissionMode: the worker pod is a disposable, credential-
	// scoped sandbox (design §6) — prompting is impossible in headless mode,
	// so permissions are bypassed unless the Team chooses another mode
	// (ADR-0208).
	DefaultPermissionMode = "bypassPermissions"
	// DontAsk is Claude Code's mode that refuses every tool call its
	// permission rules do not allow, instead of prompting (ADR-0208).
	DontAsk = "dontAsk"
	// TargetHooksDisabled is the --settings value that stops a target
	// repository's hooks from running. Command-line settings outrank the
	// repository's .claude/settings.json (Ploeg ADR-0030).
	TargetHooksDisabled = `{"disableAllHooks":true}`
	// AdvisorDisabled keeps the server-side advisor tool off in every Run.
	// LiteLLM prices advisor tokens at the executor model's rate and the
	// key's model scope does not cover the advisor model (Ploeg ADR-0039).
	AdvisorDisabled = "CLAUDE_CODE_DISABLE_ADVISOR_TOOL=1"
	// UserSettingsOnly is the --setting-sources value of a Run that loads
	// Team plugins: the clone's .claude/settings.json and
	// settings.local.json, and with them their hooks, are not read, while
	// the plugins' guarded hooks run (Ploeg ADR-0139).
	UserSettingsOnly = "user"
	// RepositoryInstructions makes Claude Code read CLAUDE.md from the
	// clone, passed with --add-dir, when project settings are not loaded.
	RepositoryInstructions = "CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1"
)

// ModelAliasEnv names the variables Claude Code resolves its model aliases
// through. Each is pinned to the Run's model, so a subagent or background
// task that asks for haiku, sonnet, opus or fable stays on the model the
// Run's key is scoped to (Ploeg ADR-0039).
var ModelAliasEnv = []string{
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"ANTHROPIC_DEFAULT_FABLE_MODEL",
}

// PermissionModes are the modes Claude Code's --permission-mode accepts.
var PermissionModes = []string{"default", "acceptEdits", "plan", "auto", DontAsk, DefaultPermissionMode}

// Rules are a Team's own Claude Code permission rules (ADR-0208), in Claude
// Code's rule syntax. Deny wins over allow in every mode.
type Rules struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

type Adapter struct {
	Bin            string // empty = DefaultBin
	PermissionMode string // empty = DefaultPermissionMode
	Rules          Rules
}

func New(bin, permissionMode string) *Adapter {
	return &Adapter{Bin: bin, PermissionMode: permissionMode}
}

func (a *Adapter) Name() string     { return "claude-code" }
func (a *Adapter) ExpectsLLM() bool { return true }

func (a *Adapter) Prepare(spec harness.TaskSpec, env harness.RunEnv) (harness.Invocation, error) {
	bin := a.Bin
	if bin == "" {
		bin = DefaultBin
	}
	mode := a.PermissionMode
	if mode == "" {
		mode = DefaultPermissionMode
	}

	extraEnv := []string{AdvisorDisabled}
	if env.LLM.APIKey != "" {
		extraEnv = append(extraEnv, "ANTHROPIC_API_KEY="+env.LLM.APIKey)
	}
	if env.LLM.BaseURL != "" {
		// The Anthropic SDK appends /v1/... itself; LiteLLM's Anthropic
		// passthrough therefore wants the proxy root, not the /v1 OpenAI base.
		extraEnv = append(extraEnv, "ANTHROPIC_BASE_URL="+strings.TrimSuffix(env.LLM.BaseURL, "/v1"))
	}
	if env.LLM.Model != "" {
		extraEnv = append(extraEnv, "ANTHROPIC_MODEL="+env.LLM.Model)
		for _, alias := range ModelAliasEnv {
			extraEnv = append(extraEnv, alias+"="+env.LLM.Model)
		}
	}

	// The drop box (ADR-0018). Claude Code's result envelope carries usage and
	// prose, nothing structured about the review — so a reading Run returns its
	// findings and verdict the same way it does on every other harness, through
	// the file PLOEG_OUTCOME_FILE names.
	outcomePath := harness.DropBoxPath(env.ScratchDir, spec.TraceID)
	_ = os.Remove(outcomePath) // never inherit a previous run's report

	prompt := env.Prompt
	if asks(mode) {
		prompt += pathsNote(mode, env, outcomePath)
	}
	argv := []string{bin, "-p", prompt,
		"--output-format", "json",
		"--permission-mode", mode,
	}
	var addDirs []string
	if len(env.PluginDirs) > 0 {
		addDirs = append(addDirs, env.RepoDir)
	}
	if asks(mode) && env.ScratchDir != "" {
		addDirs = append(addDirs, env.ScratchDir)
	}
	settings, err := settingsFor(mode, a.Rules, env, len(env.PluginDirs) == 0)
	if err != nil {
		return harness.Invocation{}, err
	}
	if settings != "" {
		argv = append(argv, "--settings", settings)
	}
	if len(env.PluginDirs) > 0 {
		argv = append(argv, "--setting-sources", UserSettingsOnly)
	}
	if len(addDirs) > 0 {
		argv = append(argv, "--add-dir")
		argv = append(argv, addDirs...)
	}
	for _, dir := range env.PluginDirs {
		argv = append(argv, "--plugin-dir", dir)
	}
	if len(env.PluginDirs) > 0 {
		extraEnv = append(extraEnv, RepositoryInstructions)
	}
	argv = append(argv, "--strict-mcp-config")
	// --strict-mcp-config stays: the gateway's server is the only one the Run
	// loads, never the target repository's .mcp.json (ADR-0030, ADR-0078).
	if (env.LLM.MCPURL != "" || len(env.LLM.MCPServers) > 0) && env.LLM.APIKey != "" {
		path, err := writeMCPConfig(env.ScratchDir, env.LLM, env.LLM.APIKey)
		if err != nil {
			return harness.Invocation{}, err
		}
		argv = append(argv, "--mcp-config", path)
	}

	return harness.Invocation{
		Argv:          argv,
		ExtraEnv:      append(extraEnv, harness.DropBoxEnv+"="+outcomePath),
		OutcomeFile:   outcomePath,
		CaptureStdout: true, // the JSON result envelope arrives on stdout
	}, nil
}

// MCPServerName is the model gateway's MCP server (ADR-0078); a registered
// server reached through the credential gateway never takes this name.
const MCPServerName = "litellm"

// mcpConfig is Claude Code's --mcp-config file shape.
type mcpConfig struct {
	MCPServers map[string]mcpServer `json:"mcpServers"`
}

type mcpServer struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// writeMCPConfig writes the Run's MCP configuration under the scratch
// directory, outside the clone, readable only by the Run's user. The key is
// the one the harness already holds (a placeholder under key isolation), so
// the file grants nothing the Run's environment does not.
func writeMCPConfig(scratchDir string, llm harness.LLMEnv, key string) (string, error) {
	cfg := mcpConfig{MCPServers: map[string]mcpServer{}}
	if llm.MCPURL != "" {
		cfg.MCPServers[MCPServerName] = mcpServer{Type: "http", URL: llm.MCPURL, Headers: map[string]string{"x-litellm-api-key": "Bearer " + key}}
	}
	for _, s := range llm.MCPServers {
		if s.Name == MCPServerName {
			return "", fmt.Errorf("claude-code: MCP server name %q is reserved for the model gateway", s.Name)
		}
		cfg.MCPServers[s.Name] = mcpServer{Type: "http", URL: s.URL, Headers: map[string]string{"Authorization": "Bearer " + key}}
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("claude-code: encode MCP config: %w", err)
	}
	path := filepath.Join(scratchDir, "mcp-config.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return "", fmt.Errorf("claude-code: write MCP config: %w", err)
	}
	return path, nil
}

// resultEnvelope is the subset of Claude Code's --output-format json
// envelope we consume.
type resultEnvelope struct {
	Type          string  `json:"type"`
	Subtype       string  `json:"subtype"`
	IsError       bool    `json:"is_error"`
	Result        string  `json:"result"`
	SessionID     string  `json:"session_id"`
	TotalCostUSD  float64 `json:"total_cost_usd"`
	NumTurns      *int64  `json:"num_turns"`
	DurationMs    *int64  `json:"duration_ms"`
	DurationAPIMs *int64  `json:"duration_api_ms"`
	Usage         struct {
		InputTokens              int64  `json:"input_tokens"`
		OutputTokens             int64  `json:"output_tokens"`
		CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
		CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
	ModelUsage map[string]envelopeModelUsage `json:"modelUsage"`
}

type envelopeModelUsage struct {
	InputTokens              *int64   `json:"inputTokens"`
	OutputTokens             *int64   `json:"outputTokens"`
	CacheReadInputTokens     *int64   `json:"cacheReadInputTokens"`
	CacheCreationInputTokens *int64   `json:"cacheCreationInputTokens"`
	CostUSD                  *float64 `json:"costUSD"`
	ContextWindow            *int64   `json:"contextWindow"`
}

const maxModelUsageEntries = 32

func (env resultEnvelope) usage() *harness.Usage {
	u := &harness.Usage{
		InputTokens:              env.Usage.InputTokens,
		OutputTokens:             env.Usage.OutputTokens,
		CostUSD:                  env.TotalCostUSD,
		SessionID:                env.SessionID,
		CacheReadInputTokens:     env.Usage.CacheReadInputTokens,
		CacheCreationInputTokens: env.Usage.CacheCreationInputTokens,
		Turns:                    env.NumTurns,
		DurationMs:               env.DurationMs,
		APIDurationMs:            env.DurationAPIMs,
	}
	models := make([]string, 0, len(env.ModelUsage))
	for model := range env.ModelUsage {
		if model != "" {
			models = append(models, model)
		}
	}
	sort.Strings(models)
	if len(models) > maxModelUsageEntries {
		models = models[:maxModelUsageEntries]
	}
	for _, model := range models {
		m := env.ModelUsage[model]
		if u.ModelUsage == nil {
			u.ModelUsage = map[string]harness.ModelUsage{}
		}
		u.ModelUsage[model] = harness.ModelUsage{
			InputTokens:              m.InputTokens,
			OutputTokens:             m.OutputTokens,
			CacheReadInputTokens:     m.CacheReadInputTokens,
			CacheCreationInputTokens: m.CacheCreationInputTokens,
			CostUSD:                  m.CostUSD,
			ContextWindowTokens:      m.ContextWindow,
		}
	}
	return u
}

// ParseOutcome combines the two channels Claude Code gives us.
//
// The stdout envelope owns usage (cost, tokens, session id) and asserts no
// outcome — the forge poll and the orchestrator's exit-code heuristics stay
// authoritative for whether a PR landed, because Claude does not know. The
// drop box owns whatever the agent chose to report about itself, which for a
// reading Run is the review and the verdict.
//
// The two channels are independent. A malformed envelope costs the Run its
// usage, which stays nil (unknown) rather than zero, but never anything the
// agent reported in the drop box: a writer's problem and solution, a reader's
// findings and verdict, or proposed Work Items. Usage comes only from the
// envelope; a usage figure the agent wrote into the drop box is discarded.
//
// When the drop box carries a report, an undecodable envelope is not returned
// as an error, because harness.RunCommand answers a parse error by discarding
// the whole report. The cost is diagnostic: that envelope failure is not
// logged as a parse warning, and shows only as the Run's missing usage. With
// no report in the drop box the decode error is returned as before.
func (a *Adapter) ParseOutcome(_ harness.TaskSpec, res harness.ExecResult) (harness.OutcomeReport, error) {
	box, err := harness.ReadDropBox(res.OutcomeFile)
	if err != nil {
		return harness.OutcomeReport{}, err
	}
	box.Usage = nil
	box = harness.MergeDropBox(harness.OutcomeReport{}, box)
	if len(res.Stdout) == 0 {
		return box, nil
	}
	var env resultEnvelope
	if err := json.Unmarshal(res.Stdout, &env); err != nil {
		if agentReported(box) {
			return box, nil
		}
		return box, fmt.Errorf("decode claude result envelope: %w", err)
	}
	if env.Type != "result" {
		return box, nil
	}
	return harness.MergeDropBox(harness.OutcomeReport{Usage: env.usage()}, box), nil
}

func agentReported(box harness.OutcomeReport) bool {
	return box.Outcome != "" || box.Verdict != "" ||
		strings.TrimSpace(box.Findings) != "" ||
		strings.TrimSpace(box.Problem) != "" ||
		strings.TrimSpace(box.Solution) != "" ||
		len(box.CreatedWorkItems) > 0 || len(box.FindingItems) > 0 ||
		len(box.FindingAnswers) > 0 || len(box.Questions) > 0 || len(box.QuestionAnswers) > 0
}
