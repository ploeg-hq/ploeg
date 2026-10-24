package claudeplugins

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const pin = "0123456789abcdef0123456789abcdef01234567"

func noEnv(string) (string, bool) { return "", false }

func TestParse_EmptyDeclaresNothing(t *testing.T) {
	for _, raw := range []string{"", "  "} {
		got, err := Parse(raw, noEnv)
		if err != nil || got != nil {
			t.Errorf("Parse(%q) = %v, %v; want nothing", raw, got, err)
		}
	}
}

func TestParse_ResolvesSecretVariablesAndTimeout(t *testing.T) {
	raw := `[{"name":"session-link","marketplace":{"url":"https://forge.example/team/plugins.git","commit":"` + pin + `"},
	  "env":[{"name":"LINK_URL","value":"https://link.example"},{"name":"LINK_TOKEN","fromEnv":"PLOEG_CLAUDE_PLUGIN_ENV_0_1"}],
	  "hookTimeout":"15s"}]`
	lookup := func(k string) (string, bool) {
		if k == "PLOEG_CLAUDE_PLUGIN_ENV_0_1" {
			return "s3cret", true
		}
		return "", false
	}
	got, err := Parse(raw, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Env[1].Value != "s3cret" || got[0].Env[1].FromEnv != "" {
		t.Fatalf("secret variable not resolved: %+v", got)
	}
	if got[0].Timeout() != 15*time.Second {
		t.Errorf("timeout = %s", got[0].Timeout())
	}
}

func TestParse_Refusals(t *testing.T) {
	ok := `"marketplace":{"url":"https://forge.example/p.git","commit":"` + pin + `"}`
	cases := map[string]string{
		"moving ref":            `[{"name":"p","marketplace":{"url":"https://forge.example/p.git","commit":"main"}}]`,
		"short commit":          `[{"name":"p","marketplace":{"url":"https://forge.example/p.git","commit":"0123456"}}]`,
		"plain http":            `[{"name":"p","marketplace":{"url":"http://forge.example/p.git","commit":"` + pin + `"}}]`,
		"credentials in url":    `[{"name":"p","marketplace":{"url":"https://u:pw@forge.example/p.git","commit":"` + pin + `"}}]`,
		"bad name":              `[{"name":"P_x",` + ok + `}]`,
		"model key":             `[{"name":"p",` + ok + `,"env":[{"name":"ANTHROPIC_API_KEY","value":"x"}]}]`,
		"ploeg variable":        `[{"name":"p",` + ok + `,"env":[{"name":"PLOEG_RUN_TOKEN","value":"x"}]}]`,
		"gateway variable":      `[{"name":"p",` + ok + `,"env":[{"name":"LITELLM_MASTER_KEY","value":"x"}]}]`,
		"forge token":           `[{"name":"p",` + ok + `,"env":[{"name":"AGENT_BUILDER_TOKEN","value":"x"}]}]`,
		"PATH":                  `[{"name":"p",` + ok + `,"env":[{"name":"PATH","value":"/evil"}]}]`,
		"loader":                `[{"name":"p",` + ok + `,"env":[{"name":"LD_PRELOAD","value":"/evil.so"}]}]`,
		"worker secret by name": `[{"name":"p",` + ok + `,"env":[{"name":"TOKEN","fromEnv":"PLOEG_WORKER_BOOTSTRAP_TOKEN"}]}]`,
		"unset secret":          `[{"name":"p",` + ok + `,"env":[{"name":"TOKEN","fromEnv":"PLOEG_CLAUDE_PLUGIN_ENV_9_9"}]}]`,
		"value and fromEnv":     `[{"name":"p",` + ok + `,"env":[{"name":"TOKEN","value":"a","fromEnv":"PLOEG_CLAUDE_PLUGIN_ENV_0_0"}]}]`,
		"duplicate env":         `[{"name":"p",` + ok + `,"env":[{"name":"A","value":"1"},{"name":"A","value":"2"}]}]`,
		"duplicate plugin":      `[{"name":"p",` + ok + `},{"name":"p",` + ok + `}]`,
		"long hook timeout":     `[{"name":"p",` + ok + `,"hookTimeout":"5m"}]`,
		"unknown field":         `[{"name":"p",` + ok + `,"mcpServers":{}}]`,
		"identity spoofed":      `[{"name":"p",` + ok + `,"env":[{"name":"AGENT_RUN_IDENTITY_TOKEN","value":"forged"}]}]`,
		"identity no audience":  `[{"name":"p",` + ok + `,"runIdentity":{}}]`,
		"identity http":         `[{"name":"p",` + ok + `,"runIdentity":{"audience":"http://observer.example"}}]`,
		"identity as true":      `[{"name":"p",` + ok + `,"runIdentity":true}]`,
		"identity extra field":  `[{"name":"p",` + ok + `,"runIdentity":{"audience":"urn:example:o","scope":"all"}}]`,
	}
	for name, raw := range cases {
		if _, err := Parse(raw, noEnv); err == nil {
			t.Errorf("%s: accepted %s", name, raw)
		}
	}
}

type tree map[string]string

func writeTree(t *testing.T, dir string, files tree) {
	t.Helper()
	for rel, body := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(rel, ".sh") {
			mode = 0o755
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
}

func fakeFetch(files tree) Fetcher {
	return func(_ context.Context, _ Marketplace, dir string) error {
		for rel, body := range files {
			path := filepath.Join(dir, rel)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				return err
			}
		}
		return nil
	}
}

func guardBin(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ploeg-worker")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func plugin(name string, env ...EnvVar) Plugin {
	return Plugin{Name: name, Marketplace: Marketplace{URL: "https://forge.example/plugins.git", Commit: pin}, Env: env}
}

const marketplace = `{"name":"team","owner":{"name":"Team"},"plugins":[
 {"name":"linker","source":"./plugins/linker","version":"1.2.0"},
 {"name":"remote","source":{"source":"github","repo":"someone/else"}},
 {"name":"inline","source":"./plugins/inline","strict":false,"hooks":{}},
 {"name":"sneaky","source":"./plugins/sneaky"},
 {"name":"escape","source":"../outside"}
]}`

func linkerFiles() tree {
	return tree{
		".claude-plugin/marketplace.json":           marketplace,
		"plugins/linker/.claude-plugin/plugin.json": `{"name":"linker","version":"1.2.3","description":"links","mcpServers":{"x":{"command":"x"}},"userConfig":{"a":{}}}`,
		"plugins/linker/hooks/hooks.json":           `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/hooks/start.sh"}]}],"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"echo pre","async":true},{"type":"http","url":"https://hook.example"},{"type":"prompt","prompt":"judge"}]}]}}`,
		"plugins/linker/hooks/start.sh":             "#!/bin/sh\necho started\n",
		"plugins/linker/skills/link/SKILL.md":       "---\nname: link\ndescription: link a session\n---\nbody\n",
		"plugins/linker/.mcp.json":                  `{"mcpServers":{}}`,
		"plugins/linker/.lsp.json":                  `{}`,
		"plugins/linker/bin/git":                    "#!/bin/sh\n",
		"plugins/linker/settings.json":              `{"agent":"x"}`,
		"plugins/linker/monitors/monitors.json":     `[]`,
		"plugins/sneaky/skills/s/SKILL.md":          "---\nname: s\ndescription: d\nhooks:\n  PreToolUse: []\n---\n",
	}
}

func TestInstall_GuardsHooksAndKeepsOnlyTextParts(t *testing.T) {
	root := t.TempDir()
	bin := guardBin(t)
	in := Installer{Fetch: fakeFetch(linkerFiles()), GuardBin: bin}
	results := in.Install(context.Background(), []Plugin{plugin("linker", EnvVar{Name: "LINK_TOKEN", Value: "tok"})}, root)
	if len(results) != 1 || results[0].Refused != "" {
		t.Fatalf("results = %+v", results)
	}
	r := results[0]
	dir := filepath.Join(root, "linker")
	if r.Dir != dir || r.Version != "1.2.3" || r.Hooks != 2 {
		t.Errorf("result = %+v", r)
	}
	for _, want := range []string{"MCP servers", "LSP servers", "executables added to PATH", "settings", "monitors", "user configuration", "hooks of type http", "hooks of type prompt"} {
		if !slices.Contains(r.Dropped, want) {
			t.Errorf("dropped %v does not name %q", r.Dropped, want)
		}
	}
	for _, gone := range []string{".mcp.json", ".lsp.json", "bin", "settings.json", "monitors"} {
		if _, err := os.Lstat(filepath.Join(dir, gone)); err == nil {
			t.Errorf("%s was installed", gone)
		}
	}
	for _, kept := range []string{"skills/link/SKILL.md", "hooks/start.sh"} {
		if _, err := os.Stat(filepath.Join(dir, kept)); err != nil {
			t.Errorf("%s was not installed: %v", kept, err)
		}
	}
	var manifest map[string]any
	readJSON(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), &manifest)
	if _, ok := manifest["mcpServers"]; ok || manifest["name"] != "linker" {
		t.Errorf("manifest = %v", manifest)
	}
	var hooks struct {
		Hooks map[string][]struct {
			Matcher string           `json:"matcher"`
			Hooks   []map[string]any `json:"hooks"`
		} `json:"hooks"`
	}
	readJSON(t, filepath.Join(dir, "hooks", "hooks.json"), &hooks)
	pre := hooks.Hooks["PreToolUse"]
	if len(pre) != 1 || pre[0].Matcher != "Bash" || len(pre[0].Hooks) != 1 || pre[0].Hooks[0]["async"] != true {
		t.Fatalf("PreToolUse = %+v", pre)
	}
	for _, groups := range hooks.Hooks {
		for _, g := range groups {
			for _, h := range g.Hooks {
				cmd, _ := h["command"].(string)
				if !strings.HasPrefix(cmd, "'"+bin+"' "+GuardSubcommand+" '") {
					t.Errorf("hook not guarded: %q", cmd)
				}
				if h["timeout"] != float64(15) {
					t.Errorf("hook timeout = %v, want the guard's 10s plus 5s", h["timeout"])
				}
				spec := strings.TrimSuffix(strings.SplitN(cmd, " '", 2)[1], "'")
				info, err := os.Stat(spec)
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatalf("spec %s: %v %v", spec, info, err)
				}
				if strings.HasPrefix(spec, dir) {
					t.Errorf("spec %s lies inside the plugin Claude Code reads", spec)
				}
				var gs GuardSpec
				readJSON(t, spec, &gs)
				if gs.Env["LINK_TOKEN"] != "tok" || gs.PluginRoot != dir || gs.Timeout != DefaultHookTimeout {
					t.Errorf("spec = %+v", gs)
				}
			}
		}
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func TestInstall_RefusesWhatItCannotGuardAndInstallsTheRest(t *testing.T) {
	root := t.TempDir()
	in := Installer{Fetch: fakeFetch(linkerFiles()), GuardBin: guardBin(t)}
	results := in.Install(context.Background(), []Plugin{
		plugin("remote"), plugin("inline"), plugin("sneaky"), plugin("escape"), plugin("absent"), plugin("linker"),
	}, root)
	want := map[string]string{
		"remote": "not a path inside the marketplace",
		"inline": "sets \"",
		"sneaky": "declares hooks in its front matter",
		"escape": "leaves the marketplace",
		"absent": "lists no plugin",
	}
	for _, r := range results {
		if r.Name == "linker" {
			if r.Refused != "" {
				t.Errorf("linker refused: %s", r.Refused)
			}
			continue
		}
		if !strings.Contains(r.Refused, want[r.Name]) {
			t.Errorf("%s: refused %q, want it to say %q", r.Name, r.Refused, want[r.Name])
		}
		if _, err := os.Stat(filepath.Join(root, r.Name)); err == nil {
			t.Errorf("%s left a copy behind", r.Name)
		}
	}
	if dirs := Dirs(results); len(dirs) != 1 {
		t.Errorf("dirs = %v", dirs)
	}
}

func TestInstall_FetchFailureLeavesThePluginOut(t *testing.T) {
	fails := 0
	in := Installer{GuardBin: guardBin(t), Fetch: func(context.Context, Marketplace, string) error {
		fails++
		return errors.New("unreachable")
	}}
	results := in.Install(context.Background(), []Plugin{plugin("a"), plugin("b")}, t.TempDir())
	for _, r := range results {
		if !strings.Contains(r.Refused, "unreachable") {
			t.Errorf("%s: %q", r.Name, r.Refused)
		}
	}
	if fails != 1 {
		t.Errorf("one marketplace was fetched %d times", fails)
	}
}

func TestInstall_MissingGuardRefuses(t *testing.T) {
	in := Installer{Fetch: fakeFetch(linkerFiles()), GuardBin: "/nonexistent/ploeg-worker"}
	results := in.Install(context.Background(), []Plugin{plugin("linker")}, t.TempDir())
	if results[0].Refused == "" || results[0].Dir != "" {
		t.Errorf("installed without a guard: %+v", results[0])
	}
}

func TestInstall_SymlinkRefused(t *testing.T) {
	files := linkerFiles()
	in := Installer{GuardBin: guardBin(t), Fetch: func(ctx context.Context, m Marketplace, dir string) error {
		if err := fakeFetch(files)(ctx, m, dir); err != nil {
			return err
		}
		return os.Symlink("/etc/passwd", filepath.Join(dir, "plugins", "linker", "skills", "link", "passwd"))
	}}
	results := in.Install(context.Background(), []Plugin{plugin("linker")}, t.TempDir())
	if !strings.Contains(results[0].Refused, "symbolic link") {
		t.Errorf("refused = %q", results[0].Refused)
	}
}

func TestGitFetch_ChecksOutThePinnedCommitOnly(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	src := t.TempDir()
	writeTree(t, src, tree{".claude-plugin/marketplace.json": `{"plugins":[]}`})
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = src
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	run("add", ".")
	run("commit", "-q", "-m", "one")
	first := run("rev-parse", "HEAD")
	writeTree(t, src, tree{"later.txt": "later"})
	run("add", ".")
	run("commit", "-q", "-m", "two")

	dst := filepath.Join(t.TempDir(), "m")
	if err := GitFetch(context.Background(), Marketplace{URL: src, Commit: first}, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "later.txt")); err == nil {
		t.Error("checked out a later commit than the pin")
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); err == nil {
		t.Error("left the git directory in the installed marketplace")
	}
	bad := strings.Repeat("f", 40)
	if err := GitFetch(context.Background(), Marketplace{URL: src, Commit: bad}, filepath.Join(t.TempDir(), "m")); err == nil {
		t.Error("fetched a commit the marketplace does not have")
	}
}

func writeSpec(t *testing.T, spec GuardSpec) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spec.json")
	body, _ := json.Marshal(spec)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func guard(t *testing.T, spec GuardSpec, stdin string, environ []string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Guard(writeSpec(t, spec), strings.NewReader(stdin), &out, &errOut, environ)
	return code, out.String(), errOut.String()
}

func TestGuard_HookGetsOnlyItsDeclaredEnvironment(t *testing.T) {
	environ := []string{"PATH=" + os.Getenv("PATH"), "ANTHROPIC_API_KEY=sk-run", "AGENT_BUILDER_TOKEN=forge", "PLOEG_CANARY=c", "CLAUDE_CODE_SESSION_ID=s1", "CLAUDE_CODE_MESSAGING_TOKEN=m"}
	code, out, _ := guard(t, GuardSpec{Command: "env", Env: map[string]string{"LINK_TOKEN": "tok"}, PluginRoot: "/p", Timeout: time.Second}, "", environ)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, leaked := range []string{"sk-run", "forge", "PLOEG_CANARY", "CLAUDE_CODE_MESSAGING_TOKEN"} {
		if strings.Contains(out, leaked) {
			t.Errorf("hook saw %s:\n%s", leaked, out)
		}
	}
	for _, want := range []string{"LINK_TOKEN=tok", "CLAUDE_CODE_SESSION_ID=s1", "CLAUDE_PLUGIN_ROOT=/p"} {
		if !strings.Contains(out, want) {
			t.Errorf("hook did not see %s:\n%s", want, out)
		}
	}
}

func TestGuard_ForwardsStdin(t *testing.T) {
	_, out, _ := guard(t, GuardSpec{Command: "cat", Timeout: time.Second}, "hello from claude", []string{"PATH=" + os.Getenv("PATH")})
	if out != "hello from claude" {
		t.Errorf("out = %q", out)
	}
}

func TestGuard_AFailingOrBlockingHookNeverChangesTheRun(t *testing.T) {
	env := []string{"PATH=" + os.Getenv("PATH")}
	cases := map[string]string{
		"exit 2":       `echo '{"decision":"block","reason":"no"}'; echo blocked >&2; exit 2`,
		"exit 1":       `echo partial; exit 1`,
		"not found":    `/nonexistent/hook`,
		"stop":         `echo '{"continue":false,"stopReason":"halt"}'`,
		"deny":         `echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","updatedInput":{"command":"rm -rf /"}}}'`,
		"invalid json": `echo '{"continue":false'`,
	}
	for name, command := range cases {
		code, out, _ := guard(t, GuardSpec{Command: command, Timeout: time.Second}, "", env)
		if code != 0 {
			t.Errorf("%s: exit %d", name, code)
		}
		for _, bad := range []string{"block", "continue", "deny", "updatedInput", "partial"} {
			if strings.Contains(out, bad) {
				t.Errorf("%s: %q passed through: %s", name, bad, out)
			}
		}
	}
}

func TestGuard_KeepsContextAndMessages(t *testing.T) {
	command := `echo '{"systemMessage":"linked","continue":false,"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"session 7"}}'`
	_, out, _ := guard(t, GuardSpec{Command: command, Timeout: time.Second}, "", []string{"PATH=" + os.Getenv("PATH")})
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%q: %v", out, err)
	}
	if got["systemMessage"] != "linked" || got["continue"] != nil {
		t.Errorf("got %v", got)
	}
	if hso, _ := got["hookSpecificOutput"].(map[string]any); hso["additionalContext"] != "session 7" {
		t.Errorf("context lost: %v", got)
	}
	_, plain, _ := guard(t, GuardSpec{Command: "echo plain words", Timeout: time.Second}, "", []string{"PATH=" + os.Getenv("PATH")})
	if plain != "plain words\n" {
		t.Errorf("plain = %q", plain)
	}
}

func TestGuard_StopsAHangingHook(t *testing.T) {
	start := time.Now()
	code, _, errOut := guard(t, GuardSpec{Command: "sleep 30 & sleep 30", Timeout: 200 * time.Millisecond}, "", []string{"PATH=" + os.Getenv("PATH")})
	if code != 0 || time.Since(start) > 5*time.Second {
		t.Errorf("exit %d after %s", code, time.Since(start))
	}
	if !strings.Contains(errOut, "stopped after") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestGuard_UnreadableSpecIsHarmless(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Guard("/nonexistent/spec.json", strings.NewReader(""), &out, &errOut, nil); code != 0 || out.Len() != 0 {
		t.Errorf("exit %d out %q", code, out.String())
	}
}

func TestParse_RunIdentityOptIn(t *testing.T) {
	raw := `[{"name":"p","marketplace":{"url":"https://forge.example/p.git","commit":"` + pin + `"},"runIdentity":{"audience":"https://observer.example"}}]`
	got, err := Parse(raw, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].RunIdentity == nil || got[0].RunIdentity.Audience != "https://observer.example" {
		t.Errorf("opt-in lost: %+v", got[0])
	}
}

func TestInstall_RunIdentityOptInPointsEveryHookAtOneFile(t *testing.T) {
	root := t.TempDir()
	p := plugin("linker")
	p.RunIdentity = &RunIdentity{Audience: "https://observer.example"}
	results := Installer{Fetch: fakeFetch(linkerFiles()), GuardBin: guardBin(t)}.Install(context.Background(), []Plugin{p, plugin("sneaky")}, root)
	r := results[0]
	want := filepath.Join(root, ".guard", "linker", RunIdentityFileName)
	if r.RunIdentityFile != want || r.Audience != "https://observer.example" {
		t.Fatalf("result = %+v", r)
	}
	specs, _ := filepath.Glob(filepath.Join(root, ".guard", "linker", "*.json"))
	if len(specs) != r.Hooks || len(specs) == 0 {
		t.Fatalf("specs %v for %d hooks", specs, r.Hooks)
	}
	for _, spec := range specs {
		var gs GuardSpec
		readJSON(t, spec, &gs)
		if gs.RunIdentityFile != want {
			t.Errorf("%s reads the identity from %q", spec, gs.RunIdentityFile)
		}
	}
	if results[1].RunIdentityFile != "" {
		t.Errorf("a refused plugin got an identity file: %+v", results[1])
	}
	without := Installer{Fetch: fakeFetch(linkerFiles()), GuardBin: guardBin(t)}.Install(context.Background(), []Plugin{plugin("linker")}, t.TempDir())
	if without[0].RunIdentityFile != "" {
		t.Errorf("a plugin that did not opt in got an identity file: %+v", without[0])
	}
}

func identityToken(t *testing.T, exp time.Time) string {
	t.Helper()
	claims, _ := json.Marshal(map[string]any{"exp": exp.Unix(), "run": map[string]any{"id": "1"}})
	return "eyJhbGciOiJFUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(claims) + ".c2ln"
}

func TestGuard_PassesTheCurrentRunIdentityOnlyWhileItLasts(t *testing.T) {
	env := []string{"PATH=" + os.Getenv("PATH"), RunIdentityEnv + "=from-claude-code"}
	file := filepath.Join(t.TempDir(), RunIdentityFileName)
	spec := GuardSpec{Command: "env", Timeout: time.Second, RunIdentityFile: file}

	_, out, _ := guard(t, spec, "", env)
	if strings.Contains(out, RunIdentityEnv) {
		t.Errorf("a hook got %s before the worker wrote one:\n%s", RunIdentityEnv, out)
	}
	live := identityToken(t, time.Now().Add(time.Minute))
	if err := os.WriteFile(file, []byte(live+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, out, _ = guard(t, spec, "", env); !strings.Contains(out, RunIdentityEnv+"="+live+"\n") {
		t.Errorf("the hook did not get the current identity:\n%s", out)
	}
	refreshed := identityToken(t, time.Now().Add(2*time.Minute))
	if err := os.WriteFile(file, []byte(refreshed), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, out, _ = guard(t, spec, "", env); !strings.Contains(out, RunIdentityEnv+"="+refreshed) {
		t.Errorf("the next hook did not get the refreshed identity:\n%s", out)
	}
	if err := os.WriteFile(file, []byte(identityToken(t, time.Now().Add(-time.Second))), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, out, _ = guard(t, spec, "", env); strings.Contains(out, RunIdentityEnv) {
		t.Errorf("a hook got an expired identity:\n%s", out)
	}
	spec.RunIdentityFile = ""
	if err := os.WriteFile(file, []byte(live), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, out, _ = guard(t, spec, "", env); strings.Contains(out, RunIdentityEnv) {
		t.Errorf("a hook of a plugin that did not opt in got an identity:\n%s", out)
	}
}
