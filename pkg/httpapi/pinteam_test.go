package httpapi

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestPinTeam_ContainerPinBeatsAssigneeMapping(t *testing.T) {
	s := &Server{
		ScopeTeams: map[string]string{"900000000077": "app"},
		Log:        slog.New(slog.DiscardHandler),
	}
	item := work.WorkItem{Team: "docs", ExternalScope: "900000000077"}
	s.pinTeam(&item)
	if item.Team != "app" {
		t.Errorf("team = %q, want the container's pinned team to win", item.Team)
	}
}

func TestPinTeam_UnpinnedContainerLeavesAssigneeDecision(t *testing.T) {
	s := &Server{
		ScopeTeams: map[string]string{"900000000077": "app"},
		Log:        slog.New(slog.DiscardHandler),
	}
	item := work.WorkItem{Team: "infra", ExternalScope: "somewhere-else"}
	s.pinTeam(&item)
	if item.Team != "infra" {
		t.Errorf("team = %q, want the assignee's team untouched", item.Team)
	}
}

func TestPinTeam_NoScopeNoOpinion(t *testing.T) {
	s := &Server{
		ScopeTeams: map[string]string{"900000000077": "app"},
		Log:        slog.New(slog.DiscardHandler),
	}
	item := work.WorkItem{Team: "docs"}
	s.pinTeam(&item)
	if item.Team != "docs" {
		t.Errorf("team = %q, want unchanged when the scope is unknown", item.Team)
	}
}

func TestPinTeam_RunsBeforeTargetResolution(t *testing.T) {
	r, err := target.NewMapResolver("900000000077/app=acme/internal/widgets@main", "gitlab")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Targets:    r,
		ScopeTeams: map[string]string{"900000000077": "app"},
		Log:        slog.New(slog.DiscardHandler),
	}
	item := work.WorkItem{Team: "docs", ExternalScope: "900000000077"}
	s.pinTeam(&item)
	s.resolveTarget(context.Background(), &item, true, provider.TrackerEvent{ExternalID: "9001"})
	if item.Target == nil || item.Target.Repo != "internal/widgets" {
		t.Fatalf("target = %+v, want the team-qualified rule matched via the pinned team", item.Target)
	}
	if item.RouteRule != "900000000077/app" {
		t.Errorf("rule = %q", item.RouteRule)
	}
}
