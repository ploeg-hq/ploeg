package claudeplugins

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Limits on one installed plugin's copy.
const (
	MaxPluginFiles = 2000
	MaxPluginBytes = 20 << 20
)

// GuardSubcommand is the argument of the worker binary that runs a hook
// under the guard.
const GuardSubcommand = "hook-guard"

// Result is what happened to one declared plugin in one Run.
type Result struct {
	Name    string
	Version string
	// Dir is the installed copy, passed to Claude Code; "" when refused.
	Dir string
	// Hooks counts the command hooks installed under the guard.
	Hooks int
	// Dropped names the parts of the plugin that were left out.
	Dropped []string
	// Refused says why the plugin was left out of the Run; "" when installed.
	Refused string
	// RunIdentityFile is where the worker keeps the plugin's current Run
	// identity for the guard to read; "" when the plugin did not opt in.
	RunIdentityFile string
	// Audience is the plugin's Run identity audience.
	Audience string
}

// RunIdentityFileName is the file next to a plugin's guard specifications
// that holds its current Run identity.
const RunIdentityFileName = "run-identity.jwt"

// Installer installs declared plugins for one Run.
type Installer struct {
	// Fetch checks a marketplace out; nil is GitFetch.
	Fetch Fetcher
	// GuardBin is the absolute path of the program that runs
	// GuardSubcommand, the worker binary itself.
	GuardBin string
}

// Install fetches and installs every plugin under root. A plugin that cannot
// be fetched or whose content Ploeg refuses is left out with the reason; the
// others are installed and the Run continues either way.
func (in Installer) Install(ctx context.Context, plugins []Plugin, root string) []Result {
	fetch := in.Fetch
	if fetch == nil {
		fetch = GitFetch
	}
	fetched := map[string]error{}
	var results []Result
	for _, p := range plugins {
		r := Result{Name: p.Name}
		if err := in.checkGuard(); err != nil {
			r.Refused = err.Error()
			results = append(results, r)
			continue
		}
		mdir := filepath.Join(root, ".marketplaces", marketplaceKey(p.Marketplace))
		ferr, done := fetched[mdir]
		if !done {
			ferr = fetch(ctx, p.Marketplace, mdir)
			fetched[mdir] = ferr
		}
		if ferr != nil {
			r.Refused = "marketplace not fetched: " + ferr.Error()
			results = append(results, r)
			continue
		}
		if err := in.installOne(p, mdir, root, &r); err != nil {
			_ = os.RemoveAll(filepath.Join(root, p.Name))
			_ = os.RemoveAll(filepath.Join(root, ".guard", p.Name))
			r.Dir, r.Hooks, r.Refused, r.RunIdentityFile, r.Audience = "", 0, err.Error(), "", ""
		}
		results = append(results, r)
	}
	return results
}

// Dirs returns the installed plugin directories of results.
func Dirs(results []Result) []string {
	var dirs []string
	for _, r := range results {
		if r.Dir != "" {
			dirs = append(dirs, r.Dir)
		}
	}
	return dirs
}

func (in Installer) checkGuard() error {
	if !filepath.IsAbs(in.GuardBin) || strings.ContainsAny(in.GuardBin, "'\n") {
		return fmt.Errorf("the hook guard %q is not an absolute path Ploeg can quote", in.GuardBin)
	}
	if _, err := os.Stat(in.GuardBin); err != nil {
		return fmt.Errorf("the hook guard is missing: %w", err)
	}
	return nil
}

func marketplaceKey(m Marketplace) string {
	sum := sha256.Sum256([]byte(m.URL + "\x00" + m.Commit))
	return hex.EncodeToString(sum[:8])
}

type marketplaceFile struct {
	Metadata struct {
		PluginRoot string `json:"pluginRoot"`
	} `json:"metadata"`
	Plugins []map[string]json.RawMessage `json:"plugins"`
}

var marketplaceEntryKeys = map[string]bool{
	"name": true, "source": true, "version": true, "description": true, "author": true,
	"homepage": true, "repository": true, "license": true, "keywords": true, "category": true, "tags": true,
}

func (in Installer) installOne(p Plugin, mdir, root string, r *Result) error {
	body, err := os.ReadFile(filepath.Join(mdir, ".claude-plugin", "marketplace.json"))
	if err != nil {
		return fmt.Errorf("the marketplace has no .claude-plugin/marketplace.json at %s", p.Marketplace.Commit)
	}
	var mf marketplaceFile
	if err := json.Unmarshal(body, &mf); err != nil {
		return fmt.Errorf("marketplace.json: %w", err)
	}
	var entry map[string]json.RawMessage
	for _, e := range mf.Plugins {
		var name string
		_ = json.Unmarshal(e["name"], &name)
		if name == p.Name {
			entry = e
			break
		}
	}
	if entry == nil {
		return fmt.Errorf("the marketplace lists no plugin %q at %s", p.Name, p.Marketplace.Commit)
	}
	for k := range entry {
		if !marketplaceEntryKeys[k] {
			return fmt.Errorf("the marketplace entry sets %q; Ploeg installs only plugins that declare their parts in their own directory", k)
		}
	}
	var source string
	if err := json.Unmarshal(entry["source"], &source); err != nil || source == "" {
		return fmt.Errorf("the plugin's source is not a path inside the marketplace; pin the plugin's own repository as a marketplace instead")
	}
	_ = json.Unmarshal(entry["version"], &r.Version)
	rel := source
	if mf.Metadata.PluginRoot != "" && !strings.HasPrefix(source, "./") {
		rel = filepath.Join(mf.Metadata.PluginRoot, source)
	}
	src, err := inside(mdir, rel)
	if err != nil {
		return fmt.Errorf("plugin source %q: %w", source, err)
	}
	dst := filepath.Join(root, p.Name)
	if err := copyTree(src, dst); err != nil {
		return err
	}
	return in.shape(p, dst, r)
}

func inside(base, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", errors.New("must be relative")
	}
	clean := filepath.Clean(filepath.Join(base, rel))
	if clean != base && !strings.HasPrefix(clean, base+string(filepath.Separator)) {
		return "", errors.New("leaves the marketplace")
	}
	info, err := os.Lstat(clean)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("is not a directory")
	}
	return clean, nil
}

func copyTree(src, dst string) error {
	files, total := 0, int64(0)
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			return fmt.Errorf("the plugin carries a symbolic link (%s); Ploeg installs only regular files", rel)
		case d.IsDir():
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return os.MkdirAll(target, 0o755)
		case !d.Type().IsRegular():
			return fmt.Errorf("the plugin carries %s, which is not a regular file", rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files++
		total += info.Size()
		if files > MaxPluginFiles || total > MaxPluginBytes {
			return fmt.Errorf("the plugin is larger than %d files or %d MiB", MaxPluginFiles, MaxPluginBytes>>20)
		}
		mode := os.FileMode(0o644)
		if info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

var manifestKeptKeys = []string{"name", "version", "description", "author", "homepage", "repository", "license", "keywords"}

var componentPathKeys = []string{"commands", "agents", "skills"}

var droppedManifestKeys = map[string]string{
	"mcpServers": "MCP servers", "lspServers": "LSP servers", "monitors": "monitors",
	"outputStyles": "output styles", "userConfig": "user configuration", "channels": "channels",
	"settings": "settings", "dependencies": "dependencies",
}

var droppedDefaultPaths = map[string]string{
	".mcp.json": "MCP servers", ".lsp.json": "LSP servers", "monitors": "monitors",
	"bin": "executables added to PATH", "settings.json": "settings",
}

func (in Installer) shape(p Plugin, dir string, r *Result) error {
	manifestPath := filepath.Join(dir, ".claude-plugin", "plugin.json")
	manifest := map[string]json.RawMessage{}
	if body, err := os.ReadFile(manifestPath); err == nil {
		if err := json.Unmarshal(body, &manifest); err != nil {
			return fmt.Errorf("plugin.json: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var name string
	_ = json.Unmarshal(manifest["name"], &name)
	if name != "" && name != p.Name {
		return fmt.Errorf("plugin.json names the plugin %q, not %q", name, p.Name)
	}
	var version string
	if json.Unmarshal(manifest["version"], &version) == nil && version != "" {
		r.Version = version
	}
	dropped := map[string]bool{}
	for k, what := range droppedManifestKeys {
		if _, ok := manifest[k]; ok {
			dropped[what] = true
		}
	}
	for path, what := range droppedDefaultPaths {
		full := filepath.Join(dir, path)
		if _, err := os.Lstat(full); err == nil {
			dropped[what] = true
			if err := os.RemoveAll(full); err != nil {
				return err
			}
		}
	}

	out := map[string]any{"name": p.Name}
	for _, k := range manifestKeptKeys[1:] {
		if v, ok := manifest[k]; ok {
			out[k] = v
		}
	}
	textDirs := []string{"commands", "agents", "skills"}
	for _, k := range componentPathKeys {
		raw, ok := manifest[k]
		if !ok {
			continue
		}
		paths, err := relativePaths(dir, raw)
		if err != nil {
			return fmt.Errorf("plugin.json %s: %w", k, err)
		}
		out[k] = paths
		textDirs = append(textDirs, paths...)
	}
	if file, err := hookInFrontMatter(dir, textDirs); err != nil {
		return err
	} else if file != "" {
		return fmt.Errorf("%s declares hooks in its front matter, which Ploeg cannot guard", file)
	}

	hooks, droppedHooks, err := collectHooks(dir, manifest["hooks"])
	if err != nil {
		return err
	}
	for _, d := range droppedHooks {
		dropped[d] = true
	}
	guarded, n, err := in.guardHooks(p, dir, hooks)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "hooks"), 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "hooks", "hooks.json"), map[string]any{"hooks": guarded}, 0o644); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		return err
	}
	if err := writeJSON(manifestPath, out, 0o644); err != nil {
		return err
	}
	r.Dir, r.Hooks = dir, n
	if p.RunIdentity != nil && n > 0 {
		r.RunIdentityFile = filepath.Join(filepath.Dir(dir), ".guard", p.Name, RunIdentityFileName)
		r.Audience = p.RunIdentity.Audience
	}
	for d := range dropped {
		r.Dropped = append(r.Dropped, d)
	}
	sort.Strings(r.Dropped)
	return nil
}

func relativePaths(dir string, raw json.RawMessage) ([]string, error) {
	var list []string
	var one string
	if json.Unmarshal(raw, &one) == nil {
		list = []string{one}
	} else if err := json.Unmarshal(raw, &list); err != nil {
		return nil, errors.New("want a path or a list of paths")
	}
	for _, p := range list {
		if !strings.HasPrefix(p, "./") {
			return nil, fmt.Errorf("path %q must start with ./", p)
		}
		clean := filepath.Clean(filepath.Join(dir, p))
		if clean != dir && !strings.HasPrefix(clean, dir+string(filepath.Separator)) {
			return nil, fmt.Errorf("path %q leaves the plugin", p)
		}
	}
	return list, nil
}

func hookInFrontMatter(dir string, roots []string) (string, error) {
	var found string
	for _, root := range roots {
		base := filepath.Join(dir, root)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") || found != "" {
				return err
			}
			if declaresHooks(path) {
				found, _ = filepath.Rel(dir, path)
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	return found, nil
}

func declaresHooks(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "---" {
		return false
	}
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "---" {
			return false
		}
		if strings.HasPrefix(line, "hooks:") {
			return true
		}
	}
	return false
}

type hookGroup struct {
	Matcher json.RawMessage   `json:"matcher,omitempty"`
	Hooks   []json.RawMessage `json:"hooks"`
}

type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Async   bool   `json:"async,omitempty"`
}

type hooksFile struct {
	Hooks map[string][]hookGroup `json:"hooks"`
}

func collectHooks(dir string, manifestHooks json.RawMessage) (map[string][]hookGroup, []string, error) {
	all := map[string][]hookGroup{}
	add := func(h hooksFile) {
		for event, groups := range h.Hooks {
			all[event] = append(all[event], groups...)
		}
	}
	readFile := func(rel string) error {
		body, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			return fmt.Errorf("hooks file %s: %w", rel, err)
		}
		var h hooksFile
		if err := json.Unmarshal(body, &h); err != nil {
			return fmt.Errorf("hooks file %s: %w", rel, err)
		}
		add(h)
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, "hooks", "hooks.json")); err == nil {
		if err := readFile(filepath.Join("hooks", "hooks.json")); err != nil {
			return nil, nil, err
		}
	}
	if len(manifestHooks) > 0 {
		var inline hooksFile
		if bytes.HasPrefix(bytes.TrimSpace(manifestHooks), []byte("{")) {
			if err := json.Unmarshal(manifestHooks, &inline); err != nil {
				return nil, nil, fmt.Errorf("plugin.json hooks: %w", err)
			}
			add(inline)
		} else {
			paths, err := relativePaths(dir, manifestHooks)
			if err != nil {
				return nil, nil, fmt.Errorf("plugin.json hooks: %w", err)
			}
			for _, p := range paths {
				if filepath.Clean(p) == filepath.Join("hooks", "hooks.json") {
					continue
				}
				if err := readFile(p); err != nil {
					return nil, nil, err
				}
			}
		}
	}
	var dropped []string
	for event, groups := range all {
		var kept []hookGroup
		for _, g := range groups {
			var hooks []json.RawMessage
			for _, raw := range g.Hooks {
				var h hookEntry
				if err := json.Unmarshal(raw, &h); err != nil {
					return nil, nil, fmt.Errorf("a %s hook: %w", event, err)
				}
				if h.Type != "command" || strings.TrimSpace(h.Command) == "" {
					dropped = append(dropped, "hooks of type "+cmpOr(h.Type, "(none)"))
					continue
				}
				hooks = append(hooks, raw)
			}
			if len(hooks) > 0 {
				kept = append(kept, hookGroup{Matcher: g.Matcher, Hooks: hooks})
			}
		}
		if len(kept) > 0 {
			all[event] = kept
		} else {
			delete(all, event)
		}
	}
	return all, dropped, nil
}

func cmpOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// GuardSpec is what one guarded hook runs.
type GuardSpec struct {
	Command    string            `json:"command"`
	Timeout    time.Duration     `json:"timeout"`
	Env        map[string]string `json:"env,omitempty"`
	PluginRoot string            `json:"pluginRoot"`
	// RunIdentityFile holds the plugin's current Run identity, which the
	// guard passes as RunIdentityEnv while it has not expired.
	RunIdentityFile string `json:"runIdentityFile,omitempty"`
}

func (in Installer) guardHooks(p Plugin, dir string, hooks map[string][]hookGroup) (map[string][]map[string]any, int, error) {
	specDir := filepath.Join(filepath.Dir(dir), ".guard", p.Name)
	if err := os.MkdirAll(specDir, 0o700); err != nil {
		return nil, 0, err
	}
	env := map[string]string{}
	for _, e := range p.Env {
		env[e.Name] = e.Value
	}
	identityFile := ""
	if p.RunIdentity != nil {
		identityFile = filepath.Join(specDir, RunIdentityFileName)
	}
	events := make([]string, 0, len(hooks))
	for e := range hooks {
		events = append(events, e)
	}
	sort.Strings(events)
	out := map[string][]map[string]any{}
	n := 0
	seconds := int((p.Timeout() + 5*time.Second + time.Second - 1) / time.Second)
	for _, event := range events {
		for _, g := range hooks[event] {
			var rewritten []map[string]any
			for _, raw := range g.Hooks {
				var h hookEntry
				_ = json.Unmarshal(raw, &h)
				n++
				spec := filepath.Join(specDir, fmt.Sprintf("%d.json", n))
				if strings.ContainsAny(spec, "'\n") {
					return nil, 0, fmt.Errorf("the guard spec path %q cannot be quoted", spec)
				}
				if err := writeJSON(spec, GuardSpec{Command: h.Command, Timeout: p.Timeout(), Env: env, PluginRoot: dir, RunIdentityFile: identityFile}, 0o600); err != nil {
					return nil, 0, err
				}
				entry := map[string]any{
					"type":    "command",
					"command": "'" + in.GuardBin + "' " + GuardSubcommand + " '" + spec + "'",
					"timeout": seconds,
				}
				if h.Async {
					entry["async"] = true
				}
				rewritten = append(rewritten, entry)
			}
			group := map[string]any{"hooks": rewritten}
			if len(g.Matcher) > 0 {
				group["matcher"] = g.Matcher
			}
			out[event] = append(out[event], group)
		}
	}
	return out, n, nil
}

func writeJSON(path string, v any, mode os.FileMode) error {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, body, mode)
}
