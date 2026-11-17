package httpapi

import (
	"context"
	"fmt"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// ClaimForge is the forge a claimed Run acts on: its dialect and the URL
// workers clone from, push to and call its API at (ADR-0192). An empty URL
// leaves the worker on its own FORGE_URL, which is what a single-forge
// release without minting keeps doing.
type ClaimForge struct {
	Dialect string
	URL     string
}

// ForgeDirectory resolves a Work Target's forge id through the forge
// registry (ADR-0016); an empty id is Default.
type ForgeDirectory struct {
	Default string
	Forges  map[string]ClaimForge
}

// Resolve returns the forge registered under id, or under Default for an
// empty id.
func (d *ForgeDirectory) Resolve(id string) (ClaimForge, bool) {
	if id == "" {
		id = d.Default
	}
	f, ok := d.Forges[id]
	return f, ok
}

// claimForge resolves the forge of a claimed Run. A forge id the registry
// does not know ends the Run stuck with a reason naming it, and the claim
// answers 204: nothing was minted and no worker learns a URL to guess from.
func (s *Server) claimForge(ctx context.Context, team, runToken string, item work.WorkItem) (ClaimForge, bool) {
	if s.ClaimForges == nil {
		return ClaimForge{}, true
	}
	id := ""
	if item.Target != nil {
		id = item.Target.Forge
	}
	if f, ok := s.ClaimForges.Resolve(id); ok {
		return f, true
	}
	shown := id
	if shown == "" {
		shown = s.ClaimForges.Default
	}
	reason := fmt.Sprintf("forge %q is not configured in this release: its Work Target names a forge ploegd has no provider for (ADR-0192)", shown)
	s.Log.Warn("claim refused: the work target names an unknown forge; no run started",
		"team", team, "work_item", item.ID, "forge", shown)
	res, err := s.Store.ReportOutcome(ctx, runToken, store.Report(work.OutcomeStuck, "unknown forge", reason, nil, nil, nil))
	if err != nil {
		s.Log.Error("releasing the run failed", "work_item", item.ID, "err", err)
		return ClaimForge{}, false
	}
	if s.Engine != nil && res.ShiftID != nil {
		if err := s.Engine.EvaluateItem(ctx, res.WorkItemID); err != nil {
			s.Log.Error("shift evaluate failed; sweeper will repair", "shift", *res.ShiftID, "err", err)
		}
	}
	return ClaimForge{}, false
}
