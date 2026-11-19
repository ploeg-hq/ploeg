package main

import (
	"context"
	"log/slog"
	"slices"

	"github.com/ploeg-hq/ploeg/pkg/connection"
	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/gitlab"
	"github.com/ploeg-hq/ploeg/pkg/worktarget"
)

// runtimeForgeDialects are the forge kinds a runtime forge connection may
// be made on (ADR-0195).
var runtimeForgeDialects = []string{"forgejo", "gitlab", "github"}

// runtimeForges serves the forges a release reaches only through runtime
// forge connections: every instance connections.allow names on the forge
// port, of a dialect no static forge of the release serves. Each resolves
// a claim on its instance id to the instance's URL, so a worker without a
// forge of its own learns the Run's forge from the claim. A GitLab instance
// also becomes the forge provider of its instance id, acting on each
// project with the credential of the connection its runtime Work Target
// was granted through: it posts the Run's card, reads the merge request,
// its CI and its notes, and takes the project webhooks Ploeg registered.
// With exactly one such GitLab instance it also serves
// /webhooks/forge/gitlab. A release with a static forge of a dialect keeps
// serving that dialect's runtime targets exactly as before.
func runtimeForges(log *slog.Logger, conns *connection.Service, forges map[string]provider.ForgeProvider, targets *worktarget.Service) map[string]httpapi.ClaimForge {
	if conns == nil || conns.Config == nil {
		return nil
	}
	static := map[string]bool{}
	for _, fp := range forges {
		static[fp.Name()] = true
	}
	out := map[string]httpapi.ClaimForge{}
	var gitlabs []string
	for _, a := range conns.Config.Allow {
		inst, ok := conns.Instances[a.Instance]
		if a.Port != connection.PortForge || !ok || inst.URL == "" || !slices.Contains(runtimeForgeDialects, inst.Kind) || static[inst.Kind] {
			continue
		}
		if _, taken := forges[inst.ID]; taken {
			continue
		}
		if _, seen := out[inst.ID]; seen {
			continue
		}
		url := trimSlash(inst.URL)
		out[inst.ID] = httpapi.ClaimForge{Dialect: inst.Kind, URL: url, PloegdURL: url}
		if inst.Kind == "gitlab" && targets != nil {
			gitlabs = append(gitlabs, inst.ID)
		}
	}
	slices.Sort(gitlabs)
	for _, id := range gitlabs {
		instance := id
		forges[instance] = &gitlab.Provider{BaseURL: out[instance].URL, HookSecret: conns.WebhookSecret, Log: log,
			TokenFor: func(ctx context.Context, project string) (string, error) {
				return targets.ActingToken(ctx, instance, project)
			}}
		log.Info("runtime GitLab forge configured: Ploeg acts on each project through its target's connection", "forge_id", instance, "url", out[instance].URL)
	}
	_, routed := forges["gitlab"]
	switch {
	case len(gitlabs) == 1 && !routed:
		forges["gitlab"] = forges[gitlabs[0]]
	case len(gitlabs) > 1:
		log.Warn("more than one runtime GitLab instance and no static GitLab: /webhooks/forge/gitlab verifies and acknowledges their deliveries without acting on them", "instances", gitlabs)
	}
	return out
}
