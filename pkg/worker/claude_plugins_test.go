package worker

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/harness/claudeplugins"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type pluginRecorder struct {
	contextAdapter
	name       string
	pluginDirs []string
}

func (a *pluginRecorder) Name() string { return a.name }
func (a *pluginRecorder) Run(ctx context.Context, spec harness.TaskSpec, env harness.RunEnv) (harness.OutcomeReport, error) {
	a.pluginDirs = env.PluginDirs
	return a.contextAdapter.Run(ctx, spec, env)
}

func pluginMarketplace(_ context.Context, _ claudeplugins.Marketplace, dir string) error {
	files := map[string]string{
		".claude-plugin/marketplace.json": `{"plugins":[{"name":"linker","source":"./linker"}]}`,
		"linker/hooks/hooks.json":         `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"true"}]}]}}`,
		"linker/skills/link/SKILL.md":     "---\nname: link\ndescription: d\n---\n",
	}
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

func runWithPlugins(t *testing.T, harnessName string, plugins []claudeplugins.Plugin) *pluginRecorder {
	t.Helper()
	forgeURL := gitForge(t, map[string]string{"main.go": "package main\n"})
	guard := filepath.Join(t.TempDir(), "ploeg-worker")
	if err := os.WriteFile(guard, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	adapter := &pluginRecorder{name: harnessName}
	w := New(Config{APIURL: contextAPI(t, nil), ForgeURL: forgeURL, DefaultForge: harness.ForgeForgejo, BuilderToken: "tok",
		ForgeTokenAccess: ForgeTokenReadOnly, RepoOwner: "webgrip", RepoName: "example", BaseBranch: "development",
		WorkDir: t.TempDir(), RepoKnowledgeDir: "-", ClaudePlugins: plugins, PluginHookGuard: guard},
		adapter, llmbroker.Static{}, discardLog())
	w.pluginFetch = pluginMarketplace
	claimed := &ClaimResponse{RunToken: "rt", Role: "reviewer", PreAuthor: true, WorkItem: work.WorkItem{ID: "7", Title: "read"}}
	w.execute(context.Background(), claimed, "agent/vik-7", "trace", "", "")
	if !adapter.ran {
		t.Fatal("the harness never ran")
	}
	return adapter
}

var declaredLinker = claudeplugins.Plugin{Name: "linker", Marketplace: claudeplugins.Marketplace{URL: "https://forge.example/p.git", Commit: "0123456789abcdef0123456789abcdef01234567"}}

func TestWorkerInstallsDeclaredPluginsForClaudeCode(t *testing.T) {
	adapter := runWithPlugins(t, "claude-code", []claudeplugins.Plugin{declaredLinker})
	if len(adapter.pluginDirs) != 1 || filepath.Base(adapter.pluginDirs[0]) != "linker" {
		t.Fatalf("plugin dirs = %v", adapter.pluginDirs)
	}
	if _, err := os.Stat(filepath.Join(adapter.pluginDirs[0], "skills", "link", "SKILL.md")); err != nil {
		t.Errorf("the plugin's skill was not installed: %v", err)
	}
}

func TestWorkerRunsWithoutAPluginItCouldNotInstall(t *testing.T) {
	missing := declaredLinker
	missing.Name = "absent"
	adapter := runWithPlugins(t, "claude-code", []claudeplugins.Plugin{missing})
	if len(adapter.pluginDirs) != 0 {
		t.Errorf("plugin dirs = %v", adapter.pluginDirs)
	}
}

func TestWorkerGivesOtherHarnessesNoPlugins(t *testing.T) {
	for _, name := range []string{"openhands", "acp", "exec", "claude-environment"} {
		if dirs := runWithPlugins(t, name, []claudeplugins.Plugin{declaredLinker}).pluginDirs; len(dirs) != 0 {
			t.Errorf("%s received plugins %v", name, dirs)
		}
		if ClaudePluginsWithheldReason(name) == "" {
			t.Errorf("%s withholds plugins without saying why", name)
		}
	}
}

func TestWorkerWithoutPluginsInstallsNone(t *testing.T) {
	if dirs := runWithPlugins(t, "claude-code", nil).pluginDirs; dirs != nil {
		t.Errorf("plugin dirs = %v", dirs)
	}
}
