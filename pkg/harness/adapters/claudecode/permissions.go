package claudecode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

// ValidPermissionMode reports whether mode is one Claude Code accepts. Empty
// is the adapter's default.
func ValidPermissionMode(mode string) bool {
	return mode == "" || slices.Contains(PermissionModes, mode)
}

// ValidateRules refuses a rule Claude Code could not read as one rule.
func ValidateRules(r Rules) error {
	for _, list := range []struct {
		kind  string
		rules []string
	}{{"allow", r.Allow}, {"deny", r.Deny}} {
		kind := list.kind
		for i, rule := range list.rules {
			if strings.TrimSpace(rule) == "" {
				return fmt.Errorf("claude-code permission rule %s[%d] is empty", kind, i)
			}
			if strings.ContainsAny(rule, "\n\r") {
				return fmt.Errorf("claude-code permission rule %s[%d] spans more than one line", kind, i)
			}
		}
	}
	return nil
}

// asks reports whether a call outside the allow rules is refused or asked
// about rather than run. Only bypassPermissions runs it.
func asks(mode string) bool { return mode != DefaultPermissionMode }

type cliSettings struct {
	DisableAllHooks bool         `json:"disableAllHooks,omitempty"`
	Permissions     *permissions `json:"permissions,omitempty"`
}

type permissions struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

// settingsFor is the --settings JSON of a Run. A mode that asks gets the
// rules the delivery contract needs (contractRules) and the Team's own; the
// Team's deny rules apply in every mode. Nothing is returned when there is
// nothing to set.
func settingsFor(mode string, team Rules, env harness.RunEnv, disableHooks bool) (string, error) {
	s := cliSettings{DisableAllHooks: disableHooks}
	var p permissions
	if asks(mode) {
		p.Allow = append(contractRules(env), team.Allow...)
	}
	p.Deny = team.Deny
	if len(p.Allow) > 0 || len(p.Deny) > 0 {
		s.Permissions = &p
	}
	if !s.DisableAllHooks && s.Permissions == nil {
		return "", nil
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return "", fmt.Errorf("claude-code: encode settings: %w", err)
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// contractRules allow what Ploeg's delivery contract tells every Run to do,
// so a mode that refuses unlisted calls cannot fail the contract itself
// (ADR-0208). Every Run writes its drop box and notes in the scratch
// directory, prints with printf and runs the scripts its prompt names; a
// writer also edits its
// clone, commits and pushes with git and opens its pull request over the
// forge's HTTP API; each MCP server Ploeg configured for the Run is usable.
// Writes to Claude Code's protected paths stay refused.
func contractRules(env harness.RunEnv) []string {
	var rules []string
	for _, dir := range pathForms(env.ScratchDir) {
		rules = append(rules, "Edit(/"+dir+"/**)")
	}
	rules = append(rules, "Bash(printf *)")
	if env.Writes {
		for _, dir := range pathForms(env.RepoDir) {
			rules = append(rules, "Edit(/"+dir+"/**)")
		}
		rules = append(rules, "Bash(git *)", "Bash(curl *)")
	}
	for _, s := range env.Scripts {
		if s.Path != "" {
			rules = append(rules, "Bash("+s.Path+")", "Bash(sh "+s.Path+")")
		}
		if s.Env != "" {
			rules = append(rules, scriptVariableRules(s.Env)...)
		}
	}
	if env.LLM.MCPURL != "" && env.LLM.APIKey != "" {
		rules = append(rules, mcpRule(MCPServerName))
	}
	if env.LLM.APIKey != "" {
		for _, s := range env.LLM.MCPServers {
			rules = append(rules, mcpRule(s.Name))
		}
	}
	return rules
}

// scriptVariableRules allow a script run through the variable that names it.
// The quoted form comes first: Claude Code 2.1.332 refuses "$VAR" when
// Bash($VAR) is listed before Bash("$VAR"), and allows both forms in this
// order (measured, docs/research/2026-11-27-claude-code-dont-ask.md).
func scriptVariableRules(name string) []string {
	return []string{`Bash("$` + name + `")`, "Bash($" + name + ")", `Bash(sh "$` + name + `")`}
}

// pathForms is dir as an absolute path and, when it differs, with its
// symbolic links resolved, because Claude Code matches a file's resolved path.
func pathForms(dir string) []string {
	if dir == "" {
		return nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil
	}
	forms := []string{abs}
	if real, err := filepath.EvalSymlinks(abs); err == nil && real != abs {
		forms = append(forms, real)
	}
	return forms
}

// mcpRule allows every tool of one MCP server. Claude Code turns each
// character outside letters, digits, '_' and '-' of a server name into '_'.
func mcpRule(server string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		default:
			return '_'
		}
	}, server)
	return "mcp__" + name + "__*"
}

// pathsNote tells an agent whose shell redirects to a variable are refused
// the absolute path of each file Ploeg names by a variable: the drop box and
// every PLOEG_ variable whose value lies in the scratch directory.
func pathsNote(mode string, env harness.RunEnv, outcomePath string) string {
	paths := map[string]string{harness.DropBoxEnv: outcomePath}
	scratch := pathForms(env.ScratchDir)
	for _, kv := range env.BaseEnv {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(name, "PLOEG_") || !filepath.IsAbs(value) {
			continue
		}
		for _, dir := range scratch {
			if value == dir || strings.HasPrefix(value, dir+string(filepath.Separator)) {
				paths[name] = value
			}
		}
	}
	names := make([]string, 0, len(paths))
	for name := range paths {
		names = append(names, name)
	}
	slices.Sort(names)
	var b strings.Builder
	fmt.Fprintf(&b, "\n## This Run's tool permissions\n\n"+
		"Claude Code runs this Run in %s mode: a tool call outside the Team's permission\n"+
		"rules is refused, not asked about. A shell redirect to a variable, such as\n"+
		"> \"$%s\", is refused, so use these paths as written, with your file tools\n"+
		"or in commands:\n\n", mode, harness.DropBoxEnv)
	for _, name := range names {
		fmt.Fprintf(&b, "- %s: %s\n", name, paths[name])
	}
	b.WriteString("\nWhen a call is refused, do not try it again in another form; name what you\n" +
		"could not do in your report.\n")
	return b.String()
}
