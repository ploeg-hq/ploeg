package httpapi

import (
	"context"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

// PlanHashes is implemented by plan.Plans. A RoleCaps that also implements
// it records each claimed Run's plan hash (ADR-0091).
type PlanHashes interface {
	Hash(team string) string
}

// runHarness is what a worker says about its harness in its claim.
type runHarness struct {
	Name    string
	Profile string
}

// recordRunConfig records what a Run was configured with when ploegd claimed
// or admitted it (ADR-0091). The model comes from the (team, Role) managed
// inference policy, the plan hash from the team's plan, and the harness from
// the worker's claim. A failure leaves the fields unknown; it never stops
// the Run.
func (s *Server) recordRunConfig(ctx context.Context, runToken, team, role string, withPlan bool, h runHarness) {
	c := store.RunConfig{Harness: h.Name, HarnessProfile: h.Profile, PloegdVersion: s.Version}
	if s.LLMControl != nil {
		if p, ok := s.LLMControl.Policy(team, role); ok && len(p.Models) == 1 {
			c.ConfiguredModel = p.Models[0]
		}
	}
	if hashes, ok := s.RoleCaps.(PlanHashes); ok && withPlan {
		c.PlanHash = hashes.Hash(team)
	}
	if err := s.Store.RecordRunConfig(ctx, runToken, c); err != nil && s.Log != nil {
		s.Log.Warn("run configuration not recorded; it stays unknown", "team", team, "role", role, "err", err)
	}
}
