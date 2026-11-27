package claudecode

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

type decodedSettings struct {
	DisableAllHooks *bool `json:"disableAllHooks"`
	Permissions     *struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
	} `json:"permissions"`
}

func settingsOf(t *testing.T, argv []string) (decodedSettings, bool) {
	t.Helper()
	i := slices.Index(argv, "--settings")
	if i < 0 {
		return decodedSettings{}, false
	}
	if i+1 >= len(argv) {
		t.Fatalf("--settings has no value: %q", argv)
	}
	var s decodedSettings
	dec := json.NewDecoder(strings.NewReader(argv[i+1]))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		t.Fatalf("--settings %q is not one settings JSON document: %v", argv[i+1], err)
	}
	return s, true
}

func addedDirs(argv []string) []string {
	i := slices.Index(argv, "--add-dir")
	if i < 0 {
		return nil
	}
	var dirs []string
	for _, a := range argv[i+1:] {
		if strings.HasPrefix(a, "--") {
			break
		}
		dirs = append(dirs, a)
	}
	return dirs
}

func dontAskEnv(t *testing.T, writes bool) harness.RunEnv {
	t.Helper()
	env := testEnv()
	env.RepoDir = t.TempDir()
	env.ScratchDir = t.TempDir()
	env.Writes = writes
	env.Scripts = []harness.Script{{Env: "PLOEG_VERIFY_SCRIPT", Path: filepath.Join(env.ScratchDir, "ploeg-verify")}}
	return env
}

func allowRules(t *testing.T, a *Adapter, env harness.RunEnv) (decodedSettings, []string, harness.Invocation) {
	t.Helper()
	inv, err := a.Prepare(harness.TaskSpec{TraceID: "ploeg-1cd43e1dfd6c"}, env)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := settingsOf(t, inv.Argv)
	if !ok {
		t.Fatalf("a dontAsk Run got no --settings, so every call outside Claude Code's defaults is refused: %q", inv.Argv)
	}
	if s.Permissions == nil {
		t.Fatalf("a dontAsk Run got no permission rules, so it cannot edit, commit or report: %q", inv.Argv)
	}
	return s, s.Permissions.Allow, inv
}

func editRule(dir string) string { return "Edit(/" + dir + "/**)" }

func TestPrepare_ADontAskWriterIsAllowedWhatTheDeliveryContractNeeds(t *testing.T) {
	env := dontAskEnv(t, true)
	s, allow, inv := allowRules(t, New("", DontAsk), env)

	if !slices.Contains(inv.Argv, DontAsk) {
		t.Errorf("the mode did not reach claude: %q", inv.Argv)
	}
	if s.DisableAllHooks == nil || !*s.DisableAllHooks {
		t.Errorf("the permission rules replaced the hook guard: %+v", s)
	}
	verify := env.Scripts[0].Path
	for _, want := range []string{
		editRule(env.RepoDir),
		editRule(env.ScratchDir),
		"Bash(git *)",
		"Bash(curl *)",
		"Bash(" + verify + ")",
		`Bash("$PLOEG_VERIFY_SCRIPT")`,
		"Bash($PLOEG_VERIFY_SCRIPT)",
	} {
		if !slices.Contains(allow, want) {
			t.Errorf("a dontAsk writer is not allowed %s, which its delivery contract needs; allow = %q", want, allow)
		}
	}
	if !slices.Contains(addedDirs(inv.Argv), env.ScratchDir) {
		t.Errorf("the scratch directory is not a working directory, so the skills, knowledge and board the prompt names cannot be read: %q", inv.Argv)
	}
	for _, rule := range allow {
		if rule == "Bash" || rule == "Bash(*)" || rule == "Edit" || rule == "Write" || rule == "Read" {
			t.Errorf("the contract allows %s for every input, outside the clone and the scratch directory too", rule)
		}
	}
}

func TestPrepare_ADontAskReaderMayNotEditItsCloneOrRunGit(t *testing.T) {
	env := dontAskEnv(t, false)
	_, allow, _ := allowRules(t, New("", DontAsk), env)

	for _, never := range []string{editRule(env.RepoDir), "Bash(git *)", "Bash(curl *)"} {
		if slices.Contains(allow, never) {
			t.Errorf("a reading Run is allowed %s", never)
		}
	}
	for _, want := range []string{editRule(env.ScratchDir), `Bash("$PLOEG_VERIFY_SCRIPT")`} {
		if !slices.Contains(allow, want) {
			t.Errorf("a reading Run cannot %s, so it cannot report its findings or run its checks; allow = %q", want, allow)
		}
	}
}

func TestPrepare_TheTeamsRulesAreAddedAndItsDenyRulesKept(t *testing.T) {
	a := &Adapter{PermissionMode: DontAsk, Rules: Rules{Allow: []string{"Bash(go test *)"}, Deny: []string{"Bash(curl *)"}}}
	s, allow, _ := allowRules(t, a, dontAskEnv(t, true))

	if !slices.Contains(allow, "Bash(go test *)") {
		t.Errorf("the Team's allow rule is missing: %q", allow)
	}
	if !slices.Contains(s.Permissions.Deny, "Bash(curl *)") {
		t.Errorf("the Team's deny rule is missing, so it cannot narrow the contract's rules: %+v", s.Permissions)
	}
}

func TestPrepare_DontAskAllowsTheMCPServersPloegConfigured(t *testing.T) {
	env := dontAskEnv(t, false)
	env.LLM.MCPURL = "http://litellm.gateway.svc.cluster.local:4000/mcp"
	env.LLM.MCPServers = []harness.MCPServer{{Name: "issue.tracker", URL: "http://gateway/mcp/issue.tracker"}}
	_, allow, _ := allowRules(t, New("", DontAsk), env)

	for _, want := range []string{"mcp__litellm__*", "mcp__issue_tracker__*"} {
		if !slices.Contains(allow, want) {
			t.Errorf("an MCP server Ploeg configured for the Run is unusable under dontAsk: %s not in %q", want, allow)
		}
	}
}

func TestPrepare_ADontAskRunWithPluginsKeepsThePluginHooksAndGetsTheRules(t *testing.T) {
	env := dontAskEnv(t, true)
	env.PluginDirs = []string{filepath.Join(env.ScratchDir, "claude-plugins", "link")}
	s, allow, inv := allowRules(t, New("", DontAsk), env)

	if s.DisableAllHooks != nil {
		t.Errorf("the settings turn off the declared plugins' hooks (ADR-0139): %+v", s)
	}
	if !slices.Contains(allow, editRule(env.RepoDir)) {
		t.Errorf("a writer with plugins cannot edit its clone: %q", allow)
	}
	dirs := addedDirs(inv.Argv)
	if !slices.Contains(dirs, env.RepoDir) || !slices.Contains(dirs, env.ScratchDir) {
		t.Errorf("--add-dir = %q, want the clone and the scratch directory", dirs)
	}
	if i := slices.Index(inv.Argv, "--setting-sources"); i < 0 || inv.Argv[i+1] != UserSettingsOnly {
		t.Errorf("the clone's settings are read again: %q", inv.Argv)
	}
}

func TestPrepare_BypassPermissionsKeepsItsArgvAndAppliesOnlyTheTeamsDenyRules(t *testing.T) {
	env := dontAskEnv(t, true)
	inv, err := New("", "").Prepare(harness.TaskSpec{}, env)
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.Index(inv.Argv, "--settings"); i < 0 || inv.Argv[i+1] != TargetHooksDisabled {
		t.Errorf("a bypassPermissions Run's settings changed: %q", inv.Argv)
	}
	if slices.Contains(inv.Argv, "--add-dir") {
		t.Errorf("a bypassPermissions Run without plugins got a working directory: %q", inv.Argv)
	}

	a := &Adapter{Rules: Rules{Allow: []string{"Bash(go test *)"}, Deny: []string{"Bash(curl *)"}}}
	inv, err = a.Prepare(harness.TaskSpec{}, env)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := settingsOf(t, inv.Argv)
	if s.Permissions == nil || !slices.Equal(s.Permissions.Deny, []string{"Bash(curl *)"}) {
		t.Errorf("the Team's deny rule does not reach a bypassPermissions Run: %+v", s.Permissions)
	}
	if len(s.Permissions.Allow) > 0 {
		t.Errorf("a bypassPermissions Run got allow rules it does not use: %q", s.Permissions.Allow)
	}
}

func TestValidPermissionModeAndRules(t *testing.T) {
	for _, mode := range []string{"", DontAsk, DefaultPermissionMode, "acceptEdits"} {
		if !ValidPermissionMode(mode) {
			t.Errorf("ValidPermissionMode(%q) = false", mode)
		}
	}
	for _, mode := range []string{"dontask", "yolo", "allow_always"} {
		if ValidPermissionMode(mode) {
			t.Errorf("ValidPermissionMode(%q) = true", mode)
		}
	}
	if err := ValidateRules(Rules{Allow: []string{"Bash(go test *)"}}); err != nil {
		t.Errorf("a valid rule was refused: %v", err)
	}
	for _, r := range []Rules{{Allow: []string{" "}}, {Deny: []string{"Bash(a)\nBash(b)"}}} {
		if err := ValidateRules(r); err == nil {
			t.Errorf("ValidateRules(%+v) accepted it", r)
		}
	}
}

func TestPrepare_ADontAskRunIsToldTheAbsolutePathOfEachFileItWrites(t *testing.T) {
	env := dontAskEnv(t, false)
	notes := filepath.Join(env.ScratchDir, "notes.jsonl")
	env.BaseEnv = []string{"PLOEG_NOTES_FILE=" + notes, "PLOEG_FORGE_URL=http://forge", "HOME=" + filepath.Join(env.ScratchDir, "home")}
	_, allow, inv := allowRules(t, New("", DontAsk), env)

	prompt := inv.Argv[2]
	if !strings.HasPrefix(prompt, env.Prompt) {
		t.Fatalf("the Run's prompt was replaced: %q", prompt)
	}
	for _, want := range []string{harness.DropBoxEnv + ": " + inv.OutcomeFile, "PLOEG_NOTES_FILE: " + notes, "dontAsk"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt does not say %q, so the agent redirects to a variable and is refused:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "PLOEG_FORGE_URL") || strings.Contains(prompt, "HOME:") {
		t.Errorf("the paths note names a variable that is not a file in the scratch directory:\n%s", prompt)
	}
	if !slices.Contains(allow, "Bash(printf *)") {
		t.Errorf("printf, the prompt's way of appending a note, is refused: %q", allow)
	}

	inv, err := New("", "").Prepare(harness.TaskSpec{}, env)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Argv[2] != env.Prompt {
		t.Errorf("a bypassPermissions Run's prompt changed: %q", inv.Argv[2])
	}
}

func TestPrepare_TheQuotedScriptVariableRuleComesBeforeTheBareOne(t *testing.T) {
	_, allow, _ := allowRules(t, New("", DontAsk), dontAskEnv(t, true))
	quoted := slices.Index(allow, `Bash("$PLOEG_VERIFY_SCRIPT")`)
	bare := slices.Index(allow, "Bash($PLOEG_VERIFY_SCRIPT)")
	if quoted < 0 || bare < 0 || quoted > bare {
		t.Errorf("Claude Code refuses \"$PLOEG_VERIFY_SCRIPT\" when the bare rule comes first; allow = %q", allow)
	}
}
