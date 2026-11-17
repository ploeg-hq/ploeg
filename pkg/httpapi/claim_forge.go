package httpapi

import (
	"context"
	"fmt"
	"maps"
	"slices"

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
	// PloegdURL is ploegd's own URL for the forge (PLOEG_<DIALECT>_URL): the
	// one a runner outside the cluster pushes to, since URL may be an
	// in-cluster address only workers reach.
	PloegdURL string
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

// servesOneForge reports whether every id in the registry names the same
// forge: a single-forge release, whose workers ignored a Work Target's forge
// id before ADR-0192.
func (d *ForgeDirectory) servesOneForge() bool {
	dialects := map[string]bool{}
	for _, f := range d.Forges {
		dialects[f.Dialect] = true
	}
	return len(dialects) == 1
}

// resolveTargetForge resolves a Work Target's forge id. In a single-forge
// release an id the registry does not know resolves to the only forge, under
// its default id, as it did before ADR-0192; fallbackID is then that id and
// empty otherwise.
func (d *ForgeDirectory) resolveTargetForge(id string) (f ClaimForge, fallbackID string, ok bool) {
	if f, ok := d.Resolve(id); ok {
		return f, "", true
	}
	if !d.servesOneForge() {
		return ClaimForge{}, "", false
	}
	if f, ok := d.Forges[d.Default]; ok {
		return f, d.Default, true
	}
	ids := slices.Sorted(maps.Keys(d.Forges))
	return d.Forges[ids[0]], ids[0], true
}

// claimForge resolves the forge of a claimed Run. A forge id the registry
// does not know ends the Run stuck with a reason naming it, and the claim
// answers 204: nothing was minted and no worker learns a URL to guess from.
// In a single-forge release such an id is rewritten on the claimed item to
// the only forge's, so its credential is minted for the forge it acts on.
func (s *Server) claimForge(ctx context.Context, team, runToken string, item *work.WorkItem) (ClaimForge, bool) {
	if s.ClaimForges == nil {
		return ClaimForge{}, true
	}
	id := ""
	if item.Target != nil {
		id = item.Target.Forge
	}
	if f, fallbackID, ok := s.ClaimForges.resolveTargetForge(id); ok {
		if fallbackID != "" {
			s.Log.Warn("the work target names a forge this release does not register; a single-forge release resolves it to its only forge",
				"team", team, "work_item", item.ID, "forge", id, "resolved_forge", fallbackID, "resolved_dialect", f.Dialect)
			if item.Target != nil {
				item.Target.Forge = fallbackID
			}
		}
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

// sessionForge is the forge a Claude session outside the cluster pushes a
// Work Target to: the forge its claim resolves, at ploegd's own URL for it,
// and the forge id to mint its credential for. Without a forge registry it is
// the Forgejo the sessions were configured with. reason names why there is
// none.
func (s *Server) sessionForge(forgeID string) (forge ClaimForge, mintForge, reason string) {
	if s.ClaimForges == nil {
		if s.ClaudeSessions.ForgeURL == "" {
			return ClaimForge{}, "", "no forge URL is configured for Claude sessions (PLOEG_FORGEJO_URL)"
		}
		return ClaimForge{Dialect: "forgejo", PloegdURL: s.ClaudeSessions.ForgeURL}, forgeID, ""
	}
	shown := forgeID
	if shown == "" {
		shown = s.ClaimForges.Default
	}
	f, fallbackID, ok := s.ClaimForges.resolveTargetForge(forgeID)
	if !ok {
		return ClaimForge{}, "", fmt.Sprintf("forge %q is not configured in this release (ADR-0192)", shown)
	}
	if f.PloegdURL == "" {
		return ClaimForge{}, "", fmt.Sprintf("forge %q has no URL a Claude session can push to", shown)
	}
	if fallbackID != "" {
		return f, fallbackID, ""
	}
	return f, forgeID, ""
}

// mintForgeID is the forge id a credential for a Work Target on forgeID is
// minted for: forgeID itself, or the only forge's id when a single-forge
// release resolves an id it does not register to its only forge.
func (s *Server) mintForgeID(forgeID string) string {
	if s.ClaimForges == nil {
		return forgeID
	}
	if _, fallbackID, ok := s.ClaimForges.resolveTargetForge(forgeID); ok && fallbackID != "" {
		return fallbackID
	}
	return forgeID
}
