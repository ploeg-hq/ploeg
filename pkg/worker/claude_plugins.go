package worker

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/harness/claudeplugins"
)

// claudePluginHarness is the only harness that loads Team plugins.
const claudePluginHarness = "claude-code"

// ClaudePluginsWithheldReason says why a harness does not receive the
// declared Claude Code plugins, or "" when it does.
func ClaudePluginsWithheldReason(harnessName string) string {
	if harnessName == claudePluginHarness {
		return ""
	}
	return "only the claude-code harness loads Claude Code plugins"
}

func (w *Worker) installClaudePlugins(ctx context.Context, scratchDir string) []string {
	if len(w.Cfg.ClaudePlugins) == 0 || w.Adapter == nil || w.Adapter.Name() != claudePluginHarness {
		return nil
	}
	in := claudeplugins.Installer{Fetch: w.pluginFetch, GuardBin: w.Cfg.PluginHookGuard}
	results := in.Install(ctx, w.Cfg.ClaudePlugins, filepath.Join(scratchDir, "claude-plugins"))
	for _, r := range results {
		if r.Refused != "" {
			w.Log.Warn("Claude Code plugin left out of the Run; the Run continues without it",
				"plugin", r.Name, "reason", r.Refused)
			continue
		}
		w.Log.Info("Claude Code plugin installed", "plugin", r.Name, "version", r.Version,
			"guarded_hooks", r.Hooks, "dropped", strings.Join(r.Dropped, ", "))
	}
	return claudeplugins.Dirs(results)
}
