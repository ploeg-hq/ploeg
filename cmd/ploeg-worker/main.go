// ploeg-worker runs as the main container of an executor-spawned Job
// (docs/contracts/executor.md): it claims one work item from ploegd, drives
// a headless harness run via the adapter selected by PLOEG_HARNESS
// (design §5), and reports an OutcomeReport before exit. Empty-handed claim
// = exit 0 (backlog #49); ploeg owns retries via lease expiry, never the Job.
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/harness/claudeplugins"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/worker"
)

var version = "0.0.0-dev"

func main() {
	if len(os.Args) >= 3 && os.Args[1] == claudeplugins.GuardSubcommand {
		os.Exit(claudeplugins.Guard(os.Args[2], os.Stdin, os.Stdout, os.Stderr, os.Environ()))
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// `ploeg-worker install <dst>`: self-copy out of the distroless ploegd
	// image (no shell, so an initContainer cannot `cp`).
	if len(os.Args) >= 3 && os.Args[1] == "install" {
		if err := installSelf(os.Args[2]); err != nil {
			log.Error("install failed", "dst", os.Args[2], "err", err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) >= 3 && os.Args[1] == "harness-serve" {
		if err := runHarnessServe(os.Args[2]); err != nil {
			log.Error("harness launcher exiting", "socket", os.Args[2], "err", err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) >= 2 && os.Args[1] == "sandbox-launch" {
		if err := runSandboxLaunch(log); err != nil {
			log.Error("sandbox launcher exiting", "err", err)
			os.Exit(1)
		}
		return
	}

	if err := run(log); err != nil {
		log.Error("ploeg-worker exiting", "err", err)
		os.Exit(1)
	}
}

func installSelf(dst string) error {
	src, err := os.Open("/proc/self/exe")
	if err != nil {
		return err
	}
	defer src.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func run(log *slog.Logger) error {
	if err := worker.ConcealFromHarness(); err != nil {
		return err
	}
	if err := rejectAdministrativeEnvironment(); err != nil {
		return err
	}
	// Boot-required and parsed strictly. Discarding this error turned a typo
	// or a dropped chart value into budget 0, and budget 0 mints an UNCAPPED
	// key rather than a useless one. Failing here costs no attempt and
	// strands no lease, which is the same reason AGENT_BUILDER_TOKEN is
	// required below: it is true for every possible target.
	budgetRaw := os.Getenv("LITELLM_KEY_BUDGET")
	budget, err := strconv.ParseFloat(budgetRaw, 64)
	if err != nil {
		return fmt.Errorf("LITELLM_KEY_BUDGET is not a number: %q", budgetRaw)
	}
	model := os.Getenv("LLM_MODEL")
	cfg := worker.Config{
		APIURL:         requireEnv("PLOEG_API_URL"),
		BootstrapToken: os.Getenv("PLOEG_WORKER_BOOTSTRAP_TOKEN"),
		WorkerID:       envOr("PLOEG_WORKER_ID", os.Getenv("POD_UID")),
		Team:           requireEnv("PLOEG_TEAM"),
		Version:        version,
		// This pod's slot in a Round. NOT boot-required: unset is the
		// pre-Shift claim over queued work items, which is exactly what a
		// team without a plan keeps doing.
		Role: os.Getenv("PLOEG_ROLE"),
		// The repository is a property of the work item, resolved at ingest and
		// delivered on the claim (R11). These are only the fallback, so they
		// are NOT boot-required: requiring them would crash-loop a pod that was
		// about to be handed a perfectly good target. A run that ends up with
		// no target at all reports stuck after claiming (worker.resolveTarget)
		// rather than exiting and stranding the lease for the sweeper.
		RepoOwner:    os.Getenv("REPO_OWNER"),
		RepoName:     os.Getenv("REPO_NAME"),
		BaseBranch:   envOr("PLOEG_BASE_BRANCH", ""),
		TargetSource: envOr("PLOEG_TARGET_SOURCE", ""),
		ForgeURL:     trimSlash(requireEnv("FORGE_URL")),
		DefaultForge: envOr("PLOEG_TARGET_FORGE", harness.ForgeForgejo),
		// The credential is true for EVERY possible target, so it stays
		// boot-required: failing here costs no attempt and strands no lease.
		BuilderToken: requireEnv("AGENT_BUILDER_TOKEN"),
		WorkDir:      envOr("WORK_DIR", "/mnt/ci-shared"),

		LLMBaseURL:          os.Getenv("LLM_BASE_URL"),
		LLMKeyIsolation:     os.Getenv("PLOEG_LLM_KEY_ISOLATION"),
		ForgeTokenIsolation: os.Getenv("PLOEG_FORGE_TOKEN_ISOLATION"),
		LLMModel:            model,
		LLMModels:           worker.ModelList(model),
		KeyBudget:           budget,
		KeyTTL:              durationOr("LITELLM_KEY_DURATION", 4*time.Hour),
	}

	if cfg.LLMKeyIsolation != "" && cfg.LLMKeyIsolation != worker.KeyIsolationProxy {
		return fmt.Errorf("PLOEG_LLM_KEY_ISOLATION must be empty or %q, got %q", worker.KeyIsolationProxy, cfg.LLMKeyIsolation)
	}
	if cfg.ForgeTokenIsolation != "" && cfg.ForgeTokenIsolation != worker.ForgeTokenIsolationProxy {
		return fmt.Errorf("PLOEG_FORGE_TOKEN_ISOLATION must be empty or %q, got %q", worker.ForgeTokenIsolationProxy, cfg.ForgeTokenIsolation)
	}
	if cfg.IsolationRequested() {
		if err := worker.ShedPtraceCapability(); err != nil {
			return err
		}
	}
	if cfg.ForgeTokenAccess, err = forgeTokenAccess(); err != nil {
		return err
	}

	// The harness seam: fail fast on a misconfigured adapter BEFORE claiming.
	hc := worker.HarnessConfig{
		Name:           envOr("PLOEG_HARNESS", "openhands"),
		Entrypoint:     os.Getenv("PLOEG_HARNESS_ENTRYPOINT"),
		OutcomeFile:    os.Getenv("PLOEG_OUTCOME_FILE"),
		PermissionMode: os.Getenv("PLOEG_CLAUDE_PERMISSION_MODE"),
		ACP: worker.ACPConfig{
			Profile:        os.Getenv("PLOEG_ACP_PROFILE"),
			ConfigJSON:     os.Getenv("PLOEG_ACP_CONFIG_JSON"),
			PermissionMode: os.Getenv("PLOEG_ACP_PERMISSION_MODE"),
		},
		ClaudeEnvironment: worker.ClaudeEnvironmentConfig{
			EnvironmentID: os.Getenv("PLOEG_CLAUDE_ENVIRONMENT_ID"),
			ConfigDir:     os.Getenv("PLOEG_CLAUDE_CONFIG_DIR"),
			RefreshToken:  os.Getenv("CLAUDE_CODE_OAUTH_REFRESH_TOKEN"),
			Scopes:        os.Getenv("CLAUDE_CODE_OAUTH_SCOPES"),
		},
	}
	if raw := os.Getenv("PLOEG_HARNESS_ARGS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &hc.Args); err != nil {
			return fmt.Errorf("PLOEG_HARNESS_ARGS must be a JSON string array: %w", err)
		}
	}
	if raw := os.Getenv("PLOEG_ACP_ARGV"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &hc.ACP.Argv); err != nil {
			return fmt.Errorf("PLOEG_ACP_ARGV must be a JSON string array: %w", err)
		}
	}
	if hc.ACP.PromptTimeout, err = durationEnv("PLOEG_ACP_PROMPT_TIMEOUT"); err != nil {
		return err
	}
	if hc.ACP.IdleTimeout, err = durationEnv("PLOEG_ACP_IDLE_TIMEOUT"); err != nil {
		return err
	}
	if hc.ClaudeEnvironment.PollEvery, err = durationEnv("PLOEG_CLAUDE_POLL_INTERVAL"); err != nil {
		return err
	}
	if cfg.HarnessTimeout, err = boundEnv("PLOEG_HARNESS_TIMEOUT", defaultHarnessTimeout); err != nil {
		return err
	}
	if cfg.HarnessIdleTimeout, err = boundEnv("PLOEG_HARNESS_IDLE_TIMEOUT", defaultHarnessIdleTimeout); err != nil {
		return err
	}
	adapter, err := worker.NewAdapter(hc)
	if err != nil {
		return err
	}
	side, err := connectHarnessSide(log, hc)
	if err != nil {
		return err
	}
	defer side.close()
	lookPath := worker.LookPathIn(context.Background(), side.spawner, side.environ)
	if err := worker.CheckHarnessBinary(hc, lookPath); err != nil {
		return err
	}
	cfg.HarnessVersion = worker.HarnessVersion(hc, lookPath, worker.SpawnedVersionProbe(side.spawner, side.environ, side.scratch(cfg.WorkDir)))
	cfg.SkillDirs = worker.SkillDirectories(hc)
	cfg.KnowledgeDirs = filepath.SplitList(os.Getenv("PLOEG_KNOWLEDGE_DIRS"))
	cfg.RepoKnowledgeDir = os.Getenv("PLOEG_REPO_KNOWLEDGE_DIR")
	cfg.KnowledgeOutbox = os.Getenv("PLOEG_KNOWLEDGE_OUTBOX")
	if cfg.Transcript, err = switchEnv("PLOEG_RUN_TRANSCRIPTS", true); err != nil {
		return err
	}
	if cfg.TranscriptThoughts, err = switchEnv("PLOEG_RUN_TRANSCRIPT_THOUGHTS", true); err != nil {
		return err
	}
	if cfg.HarnessOutputInLog, err = switchEnv("PLOEG_HARNESS_OUTPUT_IN_LOG", false); err != nil {
		return err
	}
	if v := os.Getenv("PLOEG_KNOWLEDGE_BUDGET_BYTES"); v != "" {
		if cfg.KnowledgeBudget, err = strconv.Atoi(v); err != nil || cfg.KnowledgeBudget < 0 {
			return fmt.Errorf("PLOEG_KNOWLEDGE_BUDGET_BYTES: want a non-negative byte count, got %q", v)
		}
	}
	if cfg.Toolchains, err = worker.ParseToolchains(os.Getenv("PLOEG_TOOLCHAINS")); err != nil {
		return err
	}
	if err := worker.CheckToolchains(cfg.Toolchains, func(dir string) error { return side.spawner.CheckDir(context.Background(), dir) }); err != nil {
		return err
	}
	if cfg.Telemetry, err = worker.ParseTelemetry(os.Getenv("PLOEG_TELEMETRY")); err != nil {
		return err
	}
	if cfg.ClaudePlugins, err = claudeplugins.Parse(os.Getenv("PLOEG_CLAUDE_PLUGINS"), os.LookupEnv); err != nil {
		return err
	}
	if len(cfg.ClaudePlugins) > 0 {
		if cfg.PluginHookGuard, err = side.executable(); err != nil {
			return fmt.Errorf("PLOEG_CLAUDE_PLUGINS: cannot locate the worker binary for the hook guard: %w", err)
		}
	}
	if cfg.VerifyCommands, err = worker.ParseVerifyCommands(os.Getenv("PLOEG_VERIFY_COMMANDS")); err != nil {
		return err
	}
	if cfg.VerifyTimeout, err = boundEnv("PLOEG_VERIFY_TIMEOUT", worker.DefaultVerifyTimeout); err != nil {
		return err
	}

	var broker llmbroker.Broker
	switch envOr("PLOEG_LLM_CREDENTIAL_MODE", "managed") {
	case "managed":
		if cfg.BootstrapToken == "" || cfg.WorkerID == "" {
			return fmt.Errorf("managed worker requires bootstrap capability and worker identity")
		}
	case "static-compatibility":
		broker = llmbroker.Static{Key: os.Getenv("LLM_API_KEY")}
	default:
		return fmt.Errorf("unknown worker credential mode")
	}

	nodeName := os.Getenv("NODE_NAME")
	podUID := os.Getenv("POD_UID")
	log.Info("ploeg-worker starting", "version", version, "team", cfg.Team, "harness", hc.Name, "node", nodeName, "pod_uid", podUID)
	warnWhenCredentialIsolationOff(log, cfg, hc)
	logTelemetryReach(log, cfg, hc)
	logClaudePluginReach(log, cfg, hc)

	// A worker pod is killed for reasons that have nothing to do with the
	// agent: an eviction, a drain, a Job deadline, a node going away. Without
	// this the process died on the default disposition — no outcome, no
	// revoked credential, no released Lease — and the item sat active until
	// the sweeper reclaimed it a full TTL later, having charged the Round an
	// attempt for infrastructure's mistake. The signal is not the failure;
	// staying silent about it was.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	w := worker.New(cfg, adapter, broker, log)
	w.Spawner, w.HarnessEnviron = side.spawner, side.environ
	return w.RunContext(ctx)
}

// logClaudePluginReach says which declared Claude Code plugins this harness
// loads, or why it loads none of them.
func logClaudePluginReach(log *slog.Logger, cfg worker.Config, hc worker.HarnessConfig) {
	if len(cfg.ClaudePlugins) == 0 {
		return
	}
	names := make([]string, 0, len(cfg.ClaudePlugins))
	for _, p := range cfg.ClaudePlugins {
		names = append(names, p.Name+"@"+p.Marketplace.Commit[:12])
	}
	if reason := worker.ClaudePluginsWithheldReason(hc.Name); reason != "" {
		log.Warn("Claude Code plugins are declared but withheld from this harness",
			"harness", hc.Name, "plugins", strings.Join(names, ","), "reason", reason)
		return
	}
	log.Info("Claude Code plugins load into every Run", "harness", hc.Name, "plugins", strings.Join(names, ","))
}

func warnWhenCredentialIsolationOff(log *slog.Logger, cfg worker.Config, hc worker.HarnessConfig) {
	if cfg.LLMKeyIsolation == worker.KeyIsolationProxy && cfg.ForgeTokenIsolation == worker.ForgeTokenIsolationProxy {
		return
	}
	harnessName := hc.Name
	if hc.Name == "acp" {
		harnessName = "acp/" + cmp.Or(hc.ACP.Profile, "opencode")
	}
	log.Warn("credential isolation is off: the harness can read the Run's model key or forge token",
		"harness", harnessName,
		"reason", envOr("PLOEG_CREDENTIAL_ISOLATION_OFF_REASON", "disabled"),
		"llm_key_isolation", cfg.LLMKeyIsolation,
		"forge_token_isolation", cfg.ForgeTokenIsolation)
}

// logTelemetryReach says what a telemetry setting reaches for this harness,
// so a harness that exports nothing natively is visible in the pod log
// instead of looking like a broken collector.
func logTelemetryReach(log *slog.Logger, cfg worker.Config, hc worker.HarnessConfig) {
	if cfg.Telemetry == nil {
		return
	}
	profile := ""
	if hc.Name == "acp" {
		profile = cmp.Or(hc.ACP.Profile, "opencode")
	}
	native := worker.TelemetrySupport(hc.Name, profile, cfg.Telemetry.AllowHarnesses)
	if len(native) == 0 {
		log.Warn("telemetry is configured but withheld from this harness",
			"harness", hc.Name, "profile", profile, "endpoint", cfg.Telemetry.Endpoint,
			"reason", worker.TelemetryWithheldReason(hc.Name, profile))
		return
	}
	log.Info("telemetry exported by the harness",
		"harness", hc.Name, "profile", profile, "endpoint", cfg.Telemetry.Endpoint,
		"requested", strings.Join(cfg.Telemetry.Signals, ","), "native", strings.Join(native, ","))
}

func rejectAdministrativeEnvironment() error {
	for _, key := range []string{"LITELLM_MASTER_KEY", "LITELLM_ADMIN_URL", "PLOEG_WORKER_SIGNING_KEY", "PLOEG_WORKER_BOOTSTRAPS", "PLOEG_FORGEJO_ADMIN_TOKEN", "PLOEG_FORGEJO_BOT_PASSWORD", "PLOEG_DATABASE_URL", "KUBECONFIG"} {
		if os.Getenv(key) != "" {
			return fmt.Errorf("administrative configuration %s must not enter worker containers", key)
		}
	}
	return nil
}

func forgeTokenAccess() (string, error) {
	v := os.Getenv("PLOEG_FORGE_TOKEN_ACCESS")
	switch v {
	case "", worker.ForgeTokenReadOnly, worker.ForgeTokenReadWrite:
		return v, nil
	}
	return "", fmt.Errorf("PLOEG_FORGE_TOKEN_ACCESS must be %q or %q, got %q",
		worker.ForgeTokenReadOnly, worker.ForgeTokenReadWrite, v)
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "%s is required\n", key)
		os.Exit(1)
	}
	return v
}

// switchEnv reads an on/off setting; empty is def.
func switchEnv(key string, def bool) (bool, error) {
	switch os.Getenv(key) {
	case "":
		return def, nil
	case "on", "true":
		return true, nil
	case "off", "false":
		return false, nil
	}
	return false, fmt.Errorf("%s must be on or off, got %q", key, os.Getenv(key))
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

const (
	defaultHarnessTimeout     = 100 * time.Minute
	defaultHarnessIdleTimeout = 15 * time.Minute
)

func boundEnv(key string, def time.Duration) (time.Duration, error) {
	if os.Getenv(key) == "" {
		return def, nil
	}
	d, err := durationEnv(key)
	if err == nil && d < 0 {
		err = fmt.Errorf("%s must not be negative", key)
	}
	return d, err
}

func durationOr(key string, def time.Duration) time.Duration {
	if d, err := durationEnv(key); err == nil && d != 0 {
		return d
	}
	return def
}

// durationEnv parses an optional duration and reports a typo instead of
// swallowing it. Unlike durationOr, a bad value here is fatal: these are
// watchdog timeouts, and one that silently reverts to its default fires at the
// wrong moment and reads as an agent bug rather than a config error. Zero means
// unset — the adapter's own default applies.
func durationEnv(key string) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s must be a Go duration such as 45m or 90s: %w", key, err)
	}
	return d, nil
}
