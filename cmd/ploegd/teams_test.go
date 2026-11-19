package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

type fakeTeamStore struct {
	got       []store.StaticTeam
	reconcile error
	records   []store.TeamRecord
}

func (f *fakeTeamStore) ReconcileStaticTeams(_ context.Context, teams []store.StaticTeam, newID func() string) (store.TeamReconciliation, error) {
	f.got = teams
	if f.reconcile != nil {
		return store.TeamReconciliation{}, f.reconcile
	}
	for _, t := range teams {
		f.records = append(f.records, store.TeamRecord{ID: newID(), Key: t.Key, Aliases: t.Aliases, Source: "static", State: "active"})
	}
	return store.TeamReconciliation{}, nil
}

func (f *fakeTeamStore) LiveTeams(context.Context) ([]store.TeamRecord, error) { return f.records, nil }

func (f *fakeTeamStore) BackfillTeamIDs(context.Context, int) (int64, error) { return 0, nil }

func TestBootGivesEveryStaticTeamOfTheFileAnIdAndAKeyThatResolves(t *testing.T) {
	cfg := &config.File{Teams: map[string]config.Team{"docs-review": {Aliases: []string{"bronze"}}, "silver": {}}}
	st := &fakeTeamStore{}
	dir := teamDirectory(context.Background(), slog.New(slog.DiscardHandler), cfg, st)
	if len(st.got) != 2 || st.got[0].Key != "docs-review" || st.got[0].Aliases[0] != "bronze" {
		t.Fatalf("reconciled %+v", st.got)
	}
	if id := dir.ID("bronze"); id == "" || id != dir.ID("docs-review") {
		t.Fatalf("the alias resolves to %q", id)
	}
}

func TestATeamReconciliationFailureLeavesAnEmptyDirectoryAndBoots(t *testing.T) {
	cfg := &config.File{Teams: map[string]config.Team{"silver": {}}}
	dir := teamDirectory(context.Background(), slog.New(slog.DiscardHandler), cfg, &fakeTeamStore{reconcile: errors.New("down")})
	if dir == nil || len(dir.All()) != 0 {
		t.Fatalf("directory after a failure: %+v", dir)
	}
}

type renamingTeamStore struct{ fakeTeamStore }

func (r *renamingTeamStore) ReconcileStaticTeams(ctx context.Context, teams []store.StaticTeam, newID func() string) (store.TeamReconciliation, error) {
	out, err := r.fakeTeamStore.ReconcileStaticTeams(ctx, teams, newID)
	out.SuspectedRenames = []store.SuspectedRename{{From: "bronze", FromID: "team_old", To: "copper", ToID: "team_new"}}
	return out, err
}

func TestBootWarnsWhenAKeyLeftAsAnotherWithTheSameConfigurationAppeared(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	teamDirectory(context.Background(), log, &config.File{Teams: map[string]config.Team{"copper": {}}}, &renamingTeamStore{})
	out := buf.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "teams.copper.aliases: [bronze]") {
		t.Fatalf("no warning suggesting the alias:\n%s", out)
	}
}

type busyBackfillStore struct{ fakeTeamStore }

func (busyBackfillStore) BackfillTeamIDs(context.Context, int) (int64, error) {
	return 0, store.ErrTeamBackfillElsewhere
}

func TestABackfillHeldByAnotherPloegdIsNotAnError(t *testing.T) {
	var buf bytes.Buffer
	backfillTeamIDs(context.Background(), slog.New(slog.NewTextHandler(&buf, nil)), &busyBackfillStore{})
	if strings.Contains(buf.String(), "level=ERROR") {
		t.Fatalf("logged an error:\n%s", buf.String())
	}
}
