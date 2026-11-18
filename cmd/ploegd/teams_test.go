package main

import (
	"context"
	"errors"
	"log/slog"
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
