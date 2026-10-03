package worker

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/harness/adapters/acp"
	"github.com/ploeg-hq/ploeg/pkg/harness/adapters/claudecode"
	"github.com/ploeg-hq/ploeg/pkg/harness/adapters/execbin"
	"github.com/ploeg-hq/ploeg/pkg/harness/adapters/openhands"
)

// HarnessConfig selects and configures the harness adapter for this worker
// (per-team via the Helm harness block → PLOEG_HARNESS* env).
type HarnessConfig struct {
	Name           string   // PLOEG_HARNESS: openhands (default) | exec | claude-code | acp
	Entrypoint     string   // PLOEG_HARNESS_ENTRYPOINT: binary override (adapter default when empty)
	Args           []string // PLOEG_HARNESS_ARGS (JSON array): exec adapter argv template
	OutcomeFile    string   // PLOEG_OUTCOME_FILE: exec adapter outcome path override
	PermissionMode string   // PLOEG_CLAUDE_PERMISSION_MODE: claude-code only
	ACP            ACPConfig
}

// ACPConfig configures the acp harness. Grouped rather than flattened into
// HarnessConfig because it carries six knobs where the other adapters carry
// one each — and because every one of them is a `PLOEG_ACP_*` env var, so the
// grouping mirrors the wire.
//
// NOTE: "ACP" here is Zed's Agent CLIENT Protocol (editor ↔ local coding
// agent, stdio JSON-RPC). It is unrelated to IBM's former Agent COMMUNICATION
// Protocol, which merged into A2A in 2025 and is archived. See ADR-0007.
type ACPConfig struct {
	Profile        string        // PLOEG_ACP_PROFILE: opencode (default) | custom
	Argv           []string      // PLOEG_ACP_ARGV (JSON array): whole command; required for profile=custom
	ConfigJSON     string        // PLOEG_ACP_CONFIG_JSON: replaces a profile's generated agent config
	PermissionMode string        // PLOEG_ACP_PERMISSION_MODE: allow_always (default) | allow_read_only | deny_all
	PromptTimeout  time.Duration // PLOEG_ACP_PROMPT_TIMEOUT: 0 = adapter default (45m)
	IdleTimeout    time.Duration // PLOEG_ACP_IDLE_TIMEOUT: 0 = adapter default (10m)
}

// HarnessBinary names the program the configured harness executes, resolved
// the same way the adapter resolves argv[0].
func HarnessBinary(hc HarnessConfig) (string, error) {
	switch hc.Name {
	case "", "openhands":
		return firstNonEmpty(hc.Entrypoint, openhands.DefaultEntrypoint), nil
	case "claude-code":
		return firstNonEmpty(hc.Entrypoint, claudecode.DefaultBin), nil
	case "exec":
		if len(hc.Args) == 0 {
			return "", fmt.Errorf("exec harness requires a non-empty args template (PLOEG_HARNESS_ARGS)")
		}
		return hc.Args[0], nil
	case "acp":
		p, err := acp.Lookup(hc.ACP.Profile, acp.ProfileOverrides{Entrypoint: hc.Entrypoint, Argv: hc.ACP.Argv, ConfigJSON: hc.ACP.ConfigJSON})
		if err != nil {
			return "", err
		}
		if len(p.Argv) == 0 {
			return "", fmt.Errorf("acp profile %q has no argv", p.Name)
		}
		return p.Argv[0], nil
	default:
		return "", fmt.Errorf("unknown harness %q (known: openhands, exec, claude-code, acp)", hc.Name)
	}
}

// CheckHarnessBinary fails before a claim when the harness program cannot be
// executed. A path relative to the clone, or one built from a task
// placeholder, cannot be checked before the clone exists and is accepted.
func CheckHarnessBinary(hc HarnessConfig, lookPath func(string) (string, error)) error {
	bin, err := HarnessBinary(hc)
	if err != nil {
		return err
	}
	if bin == "" {
		return fmt.Errorf("harness %q has no program to execute", hc.Name)
	}
	if strings.Contains(bin, execbin.PlaceholderTaskSpec) || strings.Contains(bin, execbin.PlaceholderTaskFile) || (strings.ContainsRune(bin, '/') && !filepath.IsAbs(bin)) {
		return nil
	}
	if _, err := lookPath(bin); err != nil {
		return fmt.Errorf("harness %q program %q is not executable in this image: %w", firstNonEmpty(hc.Name, "openhands"), bin, err)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// NewAdapter is the explicit adapter registry — a switch, not init-magic,
// matching the provider SPI's compile-time philosophy. An unknown name
// fails at worker startup, before anything is claimed.
func NewAdapter(hc HarnessConfig) (harness.Adapter, error) {
	switch hc.Name {
	case "", "openhands":
		return harness.RunCommand(openhands.New(hc.Entrypoint)), nil
	case "exec":
		a, err := execbin.New(hc.Args, hc.OutcomeFile)
		if err != nil {
			return nil, err
		}
		return harness.RunCommand(a), nil
	case "claude-code":
		return harness.RunCommand(claudecode.New(hc.Entrypoint, hc.PermissionMode)), nil
	case "acp":
		// Session-protocol adapter: implements harness.Adapter directly, so no
		// RunCommand lift. Profile resolution and permission-mode validation
		// both happen here, which is what keeps a misconfigured team failing at
		// startup rather than after it has leased a ticket it cannot work.
		mode, ok := acp.ParsePermissionMode(hc.ACP.PermissionMode)
		if !ok {
			return nil, fmt.Errorf("unknown acp permission mode %q (known: allow_always, allow_read_only, deny_all)", hc.ACP.PermissionMode)
		}
		return acp.New(hc.ACP.Profile, acp.ProfileOverrides{
			Entrypoint: hc.Entrypoint,
			Argv:       hc.ACP.Argv,
			ConfigJSON: hc.ACP.ConfigJSON,
		}, acp.Options{
			PermissionMode: mode,
			PromptTimeout:  hc.ACP.PromptTimeout,
			IdleTimeout:    hc.ACP.IdleTimeout,
		})
	default:
		return nil, fmt.Errorf("unknown harness %q (known: openhands, exec, claude-code, acp)", hc.Name)
	}
}

// SkillDirectories names where the configured harness discovers user-level
// skills, relative to the Run's HOME. Every Run also gets
// skills.CanonicalDir, which the Agent Skills clients that follow the
// cross-client convention read, and the prompt names each installed skill by
// path for a harness that discovers none.
func SkillDirectories(hc HarnessConfig) []string {
	switch hc.Name {
	case "", "openhands":
		return []string{".openhands/skills/installed"}
	case "claude-code":
		return []string{".claude/skills"}
	default:
		return nil
	}
}
