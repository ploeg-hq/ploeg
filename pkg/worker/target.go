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

// errNoForgeURL means neither the claim nor the worker names a forge URL: a
// multi-forge release whose ploegd resolved no forge for the Run.
var errNoForgeURL = errors.New("no forge URL: the claim carries no forgeUrl and the worker has no FORGE_URL")

// resolveClaimTarget is resolveTarget with the forge the claim carries
// (ADR-0192): ploegd resolved it from the Work Target through the forge
// registry, so it wins over the worker's boot-time PLOEG_TARGET_FORGE and
// FORGE_URL. A Run pinned to its env target keeps the env forge when the
// worker has one.
func resolveClaimTarget(cfg Config, claimed *ClaimResponse, log *slog.Logger) (harness.RepoRef, error) {
	ref, err := resolveTarget(cfg, claimed.WorkItem, log)
	if err != nil {
		return harness.RepoRef{}, err
	}
	pinnedToEnvForge := cfg.TargetSource == TargetSourceEnv && cfg.ForgeURL != ""
	if !pinnedToEnvForge {
		if claimed.ForgeDialect != "" {
			ref.Forge = claimed.ForgeDialect
		}
		if claimed.ForgeURL != "" {
			ref.ForgeURL = claimed.ForgeURL
		}
	}
	if ref.ForgeURL == "" {
		return harness.RepoRef{}, errNoForgeURL
	}
	return ref, nil
}

// resolveTarget decides which repository this run acts on.
//
//  1. PLOEG_TARGET_SOURCE=env → the env target, unconditionally.
//  2. a resolved claim target → owner+repo from the claim. Forge URL and base
//     branch fall back field-by-field to the env, because a map entry
//     legitimately omits the base branch; resolveClaimTarget then lays the
//     claim's forge over it.
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
			ForgeURL:   cfg.ForgeURL,
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
