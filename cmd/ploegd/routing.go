package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/target"
)

const readinessLoadTimeout = time.Minute

func routing(ctx context.Context, log *slog.Logger, cfg *config.File, projects config.ScopeResolver,
	forges map[string]provider.ForgeProvider, forgeID string) (*target.MapResolver, *target.Gate, error) {
	table, err := cfg.RoutingTable(ctx, projects, log)
	if err != nil {
		return nil, nil, fmt.Errorf("routing config: %w", err)
	}
	if len(table.Rules) == 0 {
		if table.Rules, err = target.ParseRules(os.Getenv("PLOEG_TARGET_MAP")); err != nil {
			return nil, nil, fmt.Errorf("routing rules: %w", err)
		}
	}
	targets, err := target.New(table, forgeID)
	if err != nil {
		return nil, nil, fmt.Errorf("routing rules: %w", err)
	}
	registered := targets.Registered()
	log.Info("target map loaded", "rules", targets.Len(), "registered_targets", len(registered))
	if len(registered) == 0 {
		return targets, nil, nil
	}
	gate := target.NewGate(registered, repositoryInspectors(forges))
	loadCtx, cancel := context.WithTimeout(ctx, readinessLoadTimeout)
	defer cancel()
	for key, v := range gate.Load(loadCtx) {
		t := registered[key]
		if v.Ready {
			log.Info("registered target ready", "target", key, "repo", t.Owner+"/"+t.Repo, "branch", t.BaseBranch, "forge", t.Forge)
			continue
		}
		log.Warn("registered target not ready; items routed to it are refused", "target", key,
			"repo", t.Owner+"/"+t.Repo, "branch", t.BaseBranch, "forge", t.Forge, "unknown", v.Unknown, "reason", v.Reason)
	}
	return targets, gate, nil
}

func repositoryInspectors(forges map[string]provider.ForgeProvider) map[string]provider.RepositoryInspector {
	out := map[string]provider.RepositoryInspector{}
	for id, forge := range forges {
		if inspector, ok := forge.(provider.RepositoryInspector); ok {
			out[id] = inspector
		}
	}
	return out
}
