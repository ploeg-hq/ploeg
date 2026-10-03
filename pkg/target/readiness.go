package target

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// Readiness is the readiness gate's verdict on one registered target
// (ADR-0038): archived repositories, mirrors and repositories without an
// AGENTS.md on their base branch are not ready.
type Readiness struct {
	Ready bool
	// Unknown means the forge could not be asked. It is not a verdict on the
	// repository, so it is retried rather than trusted.
	Unknown bool
	Reason  string
}

// Gate holds the readiness of every registered target. It is checked when the
// configuration loads, when an item resolves to a target that was not ready,
// and again when a Run claims the item.
type Gate struct {
	targets    map[string]work.Target
	inspectors map[string]provider.RepositoryInspector

	mu       sync.Mutex
	verdicts map[string]Readiness
}

// NewGate gates targets by asking the inspector registered under each
// target's forge id. A target whose forge has no inspector is never ready.
func NewGate(targets map[string]work.Target, inspectors map[string]provider.RepositoryInspector) *Gate {
	return &Gate{targets: targets, inspectors: inspectors, verdicts: map[string]Readiness{}}
}

// Load inspects every registered target and returns the verdicts by key.
func (g *Gate) Load(ctx context.Context) map[string]Readiness {
	out := map[string]Readiness{}
	for _, key := range g.keys() {
		out[key] = g.Check(ctx, key)
	}
	return out
}

// Admit is the ingest-time check. A target last seen ready is trusted,
// because the claim checks it again before any Run starts; any other verdict
// is inspected afresh, so a repository fixed after boot is admitted without a
// restart.
func (g *Gate) Admit(ctx context.Context, key string) Readiness {
	g.mu.Lock()
	v, seen := g.verdicts[key]
	g.mu.Unlock()
	if seen && v.Ready {
		return v
	}
	return g.Check(ctx, key)
}

// Check inspects the target now and records the verdict.
func (g *Gate) Check(ctx context.Context, key string) Readiness {
	t, ok := g.targets[key]
	if !ok {
		return Readiness{Reason: fmt.Sprintf("target %q is not registered", key)}
	}
	v := g.inspect(ctx, t)
	g.mu.Lock()
	g.verdicts[key] = v
	g.mu.Unlock()
	return v
}

// KeyOf finds a registered key whose target is t. Keys that share a target
// share its verdict, so any of them will do.
func (g *Gate) KeyOf(t work.Target) (string, bool) {
	for _, key := range g.keys() {
		if g.targets[key] == t {
			return key, true
		}
	}
	return "", false
}

func (g *Gate) inspect(ctx context.Context, t work.Target) Readiness {
	inspector, ok := g.inspectors[t.Forge]
	if !ok {
		return Readiness{Reason: fmt.Sprintf("forge %q cannot report whether %s/%s is ready", t.Forge, t.Owner, t.Repo)}
	}
	state, err := inspector.InspectRepository(ctx, t.Owner, t.Repo, t.BaseBranch)
	if err != nil {
		return Readiness{Unknown: true, Reason: fmt.Sprintf("could not read %s/%s from forge %q: %v", t.Owner, t.Repo, t.Forge, err)}
	}
	var reasons []string
	if state.Archived {
		reasons = append(reasons, "is archived")
	}
	if state.Mirror {
		reasons = append(reasons, "is a mirror")
	}
	if !state.AgentsFile {
		reasons = append(reasons, fmt.Sprintf("has no AGENTS.md on branch %q", state.Branch))
	}
	if len(reasons) > 0 {
		return Readiness{Reason: fmt.Sprintf("%s/%s %s", t.Owner, t.Repo, strings.Join(reasons, ", "))}
	}
	return Readiness{Ready: true}
}

func (g *Gate) keys() []string {
	keys := make([]string, 0, len(g.targets))
	for key := range g.targets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
