package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/team"
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
