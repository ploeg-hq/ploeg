package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/team"
	"github.com/ploeg-hq/ploeg/pkg/teamruntime"
	"github.com/ploeg-hq/ploeg/pkg/trackerbinding"
)

type teamStore interface {
	ReconcileStaticTeams(ctx context.Context, teams []store.StaticTeam, newID func() string) (store.TeamReconciliation, error)
	LiveTeams(ctx context.Context) ([]store.TeamRecord, error)
	BackfillTeamIDs(ctx context.Context, batch int) (int64, error)
}

const teamBackfillBatch = 10000

// teamDirectory gives every static Team its id (ADR-0198 section 6) and
// returns the directory ploegd resolves Teams through. A failure leaves
// the directory empty, so every read falls back to the team text, and is
// never a reason not to boot.
func teamDirectory(ctx context.Context, log *slog.Logger, cfg *config.File, st teamStore) *team.Directory {
	static := cfg.StaticTeams()
	in := make([]store.StaticTeam, 0, len(static))
	for _, t := range static {
		in = append(in, store.StaticTeam{Key: t.Key, Aliases: t.Aliases, Name: t.Name, Digest: t.Digest})
	}
	changed, err := st.ReconcileStaticTeams(ctx, in, team.MustNewID)
	if err != nil {
		log.Error("static Teams not reconciled; Team ids are unavailable until the next boot", "err", err)
		return team.NewDirectory(nil)
	}
	records, err := st.LiveTeams(ctx)
	if err != nil {
		log.Error("Teams not loaded; Team ids are unavailable until the next boot", "err", err)
		return team.NewDirectory(nil)
	}
	log.Info("Teams reconciled", "live", len(records), "created", changed.Created, "renamed", changed.Renamed, "removed", changed.Removed)
	for _, r := range changed.SuspectedRenames {
		log.Warn(fmt.Sprintf("static Team %q left the file as %q appeared with the same configuration, so %q is a new Team with a new id and %q's id %s is retired; if this was a rename, set teams.%s.aliases: [%s] so the old key resolves to it, and list the old key under aliases in the same change next time to keep the id",
			r.From, r.To, r.To, r.From, r.FromID, r.To, r.From),
			"from", r.From, "from_team_id", r.FromID, "to", r.To, "to_team_id", r.ToID)
	}
	return team.NewDirectory(store.Directory(records))
}

// backfillTeamIDs sets team_id on every row written before its Team had
// one, in batches, without holding up boot.
func backfillTeamIDs(ctx context.Context, log *slog.Logger, st teamStore) {
	started := time.Now()
	set, err := st.BackfillTeamIDs(ctx, teamBackfillBatch)
	if errors.Is(err, store.ErrTeamBackfillElsewhere) {
		log.Info("Team id backfill left to the ploegd that holds it")
		return
	}
	if err != nil {
		log.Error("Team id backfill stopped; reads fall back to the team text and the next boot resumes it", "err", err, "rows", set)
		return
	}
	log.Info("Team id backfill finished", "rows", set, "took", time.Since(started).Round(time.Millisecond))
}

// runtimeTeamService builds the runtime Team service of ADR-0198 on the
// shared Team directory, decides every runtime Team's state once, and lets
// bindings follow their Team. Without teamCreation it still serves reads
// and refuses every change with creation_not_accepted, so runtime Teams a
// disabled installation holds turn invalid with creation_disabled.
func runtimeTeamService(ctx context.Context, log *slog.Logger, cfg *config.File, st *store.Store, directory *team.Directory,
	targetExists func(string) bool, bindings *trackerbinding.Registry) (*teamruntime.Service, error) {
	pools, err := team.ParsePools(os.Getenv("PLOEG_RUNTIME_POOLS"))
	if err != nil {
		return nil, err
	}
	refinement := map[string]string{}
	for key, t := range cfg.Teams {
		if t.CreatedWork != nil && t.CreatedWork.RefinementTeam != "" {
			refinement[key] = t.CreatedWork.RefinementTeam
		}
	}
	svc := &teamruntime.Service{Store: st, Config: cfg.TeamCreation, Pools: pools, Directory: directory,
		Refinement: refinement, TargetExists: targetExists, Log: log}
	if bindings != nil {
		bindings.Teams = func(id string) (team.Team, bool) {
			t, how, ok := directory.Resolve(id)
			return t, ok && how == team.ByID
		}
		svc.OnChange = func(ctx context.Context) {
			if err := bindings.Rebuild(ctx); err != nil {
				log.Warn("bindings not rebuilt after a Team change", "err", err)
			}
		}
	}
	if err := svc.Reconcile(ctx); err != nil {
		log.Error("runtime Teams not reconciled; their states stay as stored until the next sweep", "err", err)
	}
	log.Info("runtime Teams configured", "accepting", cfg.TeamCreation.Accepting(), "pools", len(pools))
	return svc, nil
}

// runtimeTeamLoop finishes drains and re-decides runtime Team states every
// interval.
func runtimeTeamLoop(ctx context.Context, log *slog.Logger, svc *teamruntime.Service, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := svc.Reconcile(ctx); err != nil {
				log.Warn("runtime Team sweep failed", "err", err)
			}
		}
	}
}

// runtimeLLMPolicy gives a runtime Team's Role the managed inference
// policy its manifest names, with the key lifetime of the static policies.
func runtimeLLMPolicy(svc *teamruntime.Service, static []httpapi.LLMPolicy) func(team, role string) (httpapi.LLMPolicy, bool) {
	ttl := "1h"
	if len(static) > 0 && static[0].TTL != "" {
		ttl = static[0].TTL
	}
	return func(teamKey, role string) (httpapi.LLMPolicy, bool) {
		models, budget, ok := svc.LLMPolicy(teamKey, role)
		if !ok {
			return httpapi.LLMPolicy{}, false
		}
		return httpapi.LLMPolicy{Team: teamKey, Role: role, Models: models, DefaultModel: models[0], BudgetUSD: budget, TTL: ttl}, true
	}
}
