package worker

import (
	"errors"
	"log/slog"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// TargetSourceEnv pins a worker to its env-configured repo regardless of what
// the claim supplies — the per-team incident lever. Flipping one team back
// needs no ploegd rollback and no change to the target map.
const TargetSourceEnv = "env"

// errNoTarget means the run has no repository to work on. It is a
// configuration failure, reported as a stuck outcome AFTER claiming rather
// than an exit: exiting silently strands the lease until the sweeper reaps it.
var errNoTarget = errors.New("no repository configured: the work item resolved no target and the worker has no fallback repo")

// resolveTarget decides which repository this run acts on.
//
//  1. PLOEG_TARGET_SOURCE=env → the env target, unconditionally.
//  2. a resolved claim target → owner+repo from the claim. Forge URL and base
//     branch fall back field-by-field, because the forge is still global and a
//     map entry legitimately omits the base branch.
//  3. otherwise → the env target (exactly the pre-decoupling behavior).
//
// Owner and repo are ATOMIC: a half-resolved target is never blended with env.
// Blending is how you clone one repo and push to another.
func resolveTarget(cfg Config, item work.WorkItem, log *slog.Logger) (harness.RepoRef, error) {
	env := harness.RepoRef{
		Forge:      cfg.DefaultForge,
		ForgeURL:   cfg.ForgeURL,
		Owner:      cfg.RepoOwner,
		Name:       cfg.RepoName,
		BaseBranch: cfg.BaseBranch,
	}
	envOK := env.Owner != "" && env.Name != ""

	if cfg.TargetSource == TargetSourceEnv {
		if !envOK {
			return harness.RepoRef{}, errNoTarget
		}
		return env, nil
	}

	if item.Target != nil && item.Target.Resolved() {
		ref := harness.RepoRef{
			Forge:      item.Target.Forge,
			ForgeURL:   cfg.ForgeURL, // forge is global today; Target carries an id, not a URL
			Owner:      item.Target.Owner,
			Name:       item.Target.Repo,
			BaseBranch: item.Target.BaseBranch,
		}
		if ref.Forge == "" {
			ref.Forge = cfg.DefaultForge
		}
		if ref.BaseBranch == "" {
			ref.BaseBranch = env.BaseBranch
		}
		// The migration telemetry: during the shadow window you are looking for
		// the ABSENCE of this line. Its presence means the map and the team's
		// pinned repo disagree, and the map is winning.
		if envOK && (env.Owner != ref.Owner || env.Name != ref.Name) {
			log.Warn("claim target differs from the env repo; using the claim target",
				"claim", ref.Owner+"/"+ref.Name, "env", env.Owner+"/"+env.Name, "route_rule", item.RouteRule)
		}
		return ref, nil
	}

	if !envOK {
		return harness.RepoRef{}, errNoTarget
	}
	return env, nil
}
