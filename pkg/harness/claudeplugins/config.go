// Package claudeplugins installs the Claude Code plugins a Team declares into
// a claude-code Run (ADR-0139): each plugin is fetched from a marketplace at a
// pinned commit, copied with only its skills, commands, agents and hooks, and
// every hook command is rewritten to run under a guard that hands it only the
// plugin's declared environment and never lets it block or stop the Run.
package claudeplugins

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/runidentity"
)

// SecretEnvPrefix is the prefix of the worker variables a declared plugin
// variable may be read from. The chart renders each Secret-backed plugin
// variable under it, so a declaration cannot reach any other variable of the
// worker, such as its bootstrap token or the forge token.
const SecretEnvPrefix = "PLOEG_CLAUDE_PLUGIN_ENV_"

// DefaultHookTimeout bounds one hook invocation when a plugin names none.
const DefaultHookTimeout = 10 * time.Second

// MaxHookTimeout is the longest a declaration may let one hook run.
const MaxHookTimeout = 60 * time.Second

// Plugin is one Claude Code plugin a Team declares.
type Plugin struct {
	// Name is the plugin's name in its marketplace.
	Name        string      `json:"name"`
	Marketplace Marketplace `json:"marketplace"`
	// Env is everything the plugin's hooks receive beyond a minimal
	// environment. Nothing else of the Run's environment reaches them.
	Env []EnvVar `json:"env,omitempty"`
	// HookTimeout bounds one hook invocation; zero is DefaultHookTimeout.
	HookTimeout Duration `json:"hookTimeout,omitempty"`
	// RunIdentity opts the plugin's hooks into the Run identity
	// (ADR-0149): a short-lived token ploegd signs for Audience, handed to
	// every hook as RunIdentityEnv. Nil hands them none.
	RunIdentity *RunIdentity `json:"runIdentity,omitempty"`
}

// RunIdentityEnv is the variable the guard sets to the Run identity of a
// plugin that opted in. A declaration may not set it.
const RunIdentityEnv = "AGENT_RUN_IDENTITY_TOKEN"

// RunIdentity is a plugin's opt-in to the Run identity.
type RunIdentity struct {
	// Audience is the one service the plugin reports to: the token's aud,
	// so a token for one service is refused by every other.
	Audience string `json:"audience"`
}

// Marketplace is a git repository holding .claude-plugin/marketplace.json,
// pinned to one commit.
type Marketplace struct {
	URL    string `json:"url"`
	Commit string `json:"commit"`
}

// EnvVar is one variable a plugin's hooks receive: a literal Value, or the
// value of the worker variable FromEnv names.
type EnvVar struct {
	Name    string `json:"name"`
	Value   string `json:"value,omitempty"`
	FromEnv string `json:"fromEnv,omitempty"`
}

// Duration is a time.Duration that decodes from a Go duration string.
type Duration time.Duration

// UnmarshalJSON decodes a Go duration string such as "15s".
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("want a duration string such as \"15s\"")
	}
	if s == "" {
		*d = 0
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// MarshalJSON encodes the duration as a Go duration string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// Timeout is the bound on one hook invocation of p.
func (p Plugin) Timeout() time.Duration {
	if p.HookTimeout <= 0 {
		return DefaultHookTimeout
	}
	return time.Duration(p.HookTimeout)
}

var (
	pluginNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	commitPattern     = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	envNamePattern    = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
)

var reservedEnvPrefixes = []string{
	"ANTHROPIC_", "CLAUDE", "PLOEG_", "LLM_", "LITELLM_", "OPENAI_", "OTEL_",
	"GIT_", "AGENT_BUILDER", "FORGE_", "DOCKER_", "XDG_", "LD_", "DYLD_",
}

var reservedEnvNames = map[string]bool{
	"PATH": true, "HOME": true, "TMPDIR": true, "SHELL": true, "USER": true,
	"LANG": true, "LC_ALL": true, "TZ": true, "TERM": true, "PWD": true,
	"NODE_OPTIONS": true, "PYTHONPATH": true, "PYTHONSTARTUP": true,
	"BASH_ENV": true, "ENV": true, "IFS": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true, "ALL_PROXY": true,
	"http_proxy": true, "https_proxy": true, "no_proxy": true, "all_proxy": true,
	RunIdentityEnv: true,
}

// ReservedEnvName reports why name may not be declared for a plugin, or "".
func ReservedEnvName(name string) string {
	if reservedEnvNames[name] {
		return "is set by Ploeg or changes how every program runs"
	}
	for _, p := range reservedEnvPrefixes {
		if strings.HasPrefix(name, p) {
			return "starts with " + p + ", which Ploeg, the harness or the model gateway reads"
		}
	}
	return ""
}

// Parse decodes PLOEG_CLAUDE_PLUGINS and resolves each FromEnv through
// lookup. Empty input declares no plugins. Any malformed declaration is an
// error, so a misconfigured Team fails at worker start-up, before a claim.
func Parse(raw string, lookup func(string) (string, bool)) ([]Plugin, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var plugins []Plugin
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&plugins); err != nil {
		return nil, fmt.Errorf("PLOEG_CLAUDE_PLUGINS: %w", err)
	}
	seen := map[string]bool{}
	for i := range plugins {
		p := &plugins[i]
		if err := validate(*p); err != nil {
			return nil, fmt.Errorf("PLOEG_CLAUDE_PLUGINS: plugin %q: %w", p.Name, err)
		}
		if seen[p.Name] {
			return nil, fmt.Errorf("PLOEG_CLAUDE_PLUGINS: plugin %q is declared twice", p.Name)
		}
		seen[p.Name] = true
		for j := range p.Env {
			e := &p.Env[j]
			if e.FromEnv == "" {
				continue
			}
			v, ok := lookup(e.FromEnv)
			if !ok {
				return nil, fmt.Errorf("PLOEG_CLAUDE_PLUGINS: plugin %q: variable %s reads %s, which is not set", p.Name, e.Name, e.FromEnv)
			}
			e.Value, e.FromEnv = v, ""
		}
	}
	return plugins, nil
}

func validate(p Plugin) error {
	if len(p.Name) > 64 || !pluginNamePattern.MatchString(p.Name) {
		return fmt.Errorf("name must be 1 to 64 lowercase letters, digits and single hyphens")
	}
	if err := validateURL(p.Marketplace.URL); err != nil {
		return err
	}
	if !commitPattern.MatchString(p.Marketplace.Commit) {
		return fmt.Errorf("marketplace.commit %q must be a full commit id (40 or 64 lowercase hex characters), never a branch or tag", p.Marketplace.Commit)
	}
	if p.HookTimeout < 0 || time.Duration(p.HookTimeout) > MaxHookTimeout {
		return fmt.Errorf("hookTimeout must be between 0 and %s", MaxHookTimeout)
	}
	if p.RunIdentity != nil {
		if why := runidentity.ValidAudience(p.RunIdentity.Audience); why != "" {
			return fmt.Errorf("runIdentity.audience %q %s", p.RunIdentity.Audience, why)
		}
	}
	names := map[string]bool{}
	for _, e := range p.Env {
		if !envNamePattern.MatchString(e.Name) {
			return fmt.Errorf("env name %q must be upper-case letters, digits and underscores", e.Name)
		}
		if why := ReservedEnvName(e.Name); why != "" {
			return fmt.Errorf("env name %s %s", e.Name, why)
		}
		if names[e.Name] {
			return fmt.Errorf("env name %s is declared twice", e.Name)
		}
		names[e.Name] = true
		if e.FromEnv != "" && e.Value != "" {
			return fmt.Errorf("env %s sets both value and fromEnv", e.Name)
		}
		if e.FromEnv != "" && (!strings.HasPrefix(e.FromEnv, SecretEnvPrefix) || !envNamePattern.MatchString(e.FromEnv)) {
			return fmt.Errorf("env %s: fromEnv %q must name a %s* variable", e.Name, e.FromEnv, SecretEnvPrefix)
		}
	}
	return nil
}

func validateURL(raw string) error {
	if strings.HasPrefix(raw, "/") {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("marketplace.url %q must be an https URL or an absolute path", raw)
	}
	if u.User != nil {
		return fmt.Errorf("marketplace.url must not carry credentials")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("marketplace.url must not carry a query or fragment")
	}
	return nil
}
