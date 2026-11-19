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
	// Reasons are the structured reasons a target that the forge described
	// is not ready, in the order ReasonArchived, ReasonMirror,
	// ReasonInstructionFileMissing (ADR-0194). Empty when Ready or Unknown,
	// and when the target is not registered or its forge cannot describe it.
	Reasons []string
	// Reason is the sentence Ploeg comments and answers with.
	Reason string
}

// The structured readiness reasons (ADR-0194).
const (
	ReasonArchived               = "archived"
	ReasonMirror                 = "mirror"
	ReasonInstructionFileMissing = "instruction_file_missing"
)

// PrepareAvailable reports whether the only thing keeping the target from
// ready is its missing instruction file, so a prepare_repository Work Item
// may make it ready (ADR-0194).
func (r Readiness) PrepareAvailable() bool {
	return !r.Ready && !r.Unknown && len(r.Reasons) == 1 && r.Reasons[0] == ReasonInstructionFileMissing
}

// Gate holds the readiness of every registered target. It is checked when the
// configuration loads, when an item resolves to a target that was not ready,
// and again when a Run claims the item.
type Gate struct {
	targets    map[string]work.Target
	inspectors map[string]provider.RepositoryInspector

	mu       sync.Mutex
	verdicts map[string]Readiness
	runtime  map[string]RuntimeEntry
}

// RuntimeEntry is a Work Target registered at runtime (ADR-0193) and the
// inspector that reads it through its own forge connection.
type RuntimeEntry struct {
	Target    work.Target
	Inspector provider.RepositoryInspector
}

// SetRuntime replaces the runtime targets the gate holds beside the static
// ones. A key the configuration registers keeps its static target.
func (g *Gate) SetRuntime(entries map[string]RuntimeEntry) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for key := range g.runtime {
		if _, ok := entries[key]; !ok {
			delete(g.verdicts, key)
		}
	}
	g.runtime = entries
}

func (g *Gate) lookup(key string) (work.Target, provider.RepositoryInspector, bool) {
	if t, ok := g.targets[key]; ok {
		return t, g.inspectors[t.Forge], true
	}
	g.mu.Lock()
	e, ok := g.runtime[key]
	g.mu.Unlock()
	return e.Target, e.Inspector, ok
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
	t, inspector, ok := g.lookup(key)
	if !ok {
		return Readiness{Reason: fmt.Sprintf("target %q is not registered", key)}
	}
	v := inspectWith(ctx, inspector, t)
	g.mu.Lock()
	g.verdicts[key] = v
	g.mu.Unlock()
	return v
}

// Target returns the registered target named key. A nil Gate registers none.
func (g *Gate) Target(key string) (work.Target, bool) {
	if g == nil {
		return work.Target{}, false
	}
	t, _, ok := g.lookup(key)
	return t, ok
}

// KeyOf finds a registered key whose target is t. Keys that share a target
// share its verdict, so any of them will do.
func (g *Gate) KeyOf(t work.Target) (string, bool) {
	for _, key := range g.keys() {
		if candidate, _, ok := g.lookup(key); ok && candidate == t {
			return key, true
		}
	}
	return "", false
}

// Recheck inspects the registered target t now (ADR-0194), as after a merged
// prepare pull request, and returns the verdict last seen before it and the
// new one. registered is false when no key names t.
func (g *Gate) Recheck(ctx context.Context, t work.Target) (before, after Readiness, seen, registered bool) {
	if g == nil {
		return Readiness{}, Readiness{}, false, false
	}
	key, ok := g.KeyOf(t)
	if !ok {
		return Readiness{}, Readiness{}, false, false
	}
	g.mu.Lock()
	before, seen = g.verdicts[key]
	g.mu.Unlock()
	return before, g.Check(ctx, key), seen, true
}

// Inspect reads t's readiness through inspector without recording it, as a
// probe before a target is registered does.
func Inspect(ctx context.Context, inspector provider.RepositoryInspector, t work.Target) Readiness {
	return inspectWith(ctx, inspector, t)
}

func inspectWith(ctx context.Context, inspector provider.RepositoryInspector, t work.Target) Readiness {
	if inspector == nil {
		return Readiness{Reason: fmt.Sprintf("forge %q cannot report whether %s/%s is ready", t.Forge, t.Owner, t.Repo)}
	}
	state, err := inspector.InspectRepository(ctx, t.Owner, t.Repo, t.BaseBranch)
	if err != nil {
		return Readiness{Unknown: true, Reason: fmt.Sprintf("could not read %s/%s from forge %q: %v", t.Owner, t.Repo, t.Forge, err)}
	}
	var reasons, sentences []string
	if state.Archived {
		reasons, sentences = append(reasons, ReasonArchived), append(sentences, "is archived")
	}
	if state.Mirror {
		reasons, sentences = append(reasons, ReasonMirror), append(sentences, "is a mirror")
	}
	if !state.AgentsFile {
		reasons = append(reasons, ReasonInstructionFileMissing)
		sentences = append(sentences, fmt.Sprintf("has no AGENTS.md on branch %q", state.Branch))
	}
	if len(reasons) > 0 {
		return Readiness{Reasons: reasons, Reason: fmt.Sprintf("%s/%s %s", t.Owner, t.Repo, strings.Join(sentences, ", "))}
	}
	return Readiness{Ready: true}
}

func (g *Gate) keys() []string {
	keys := make([]string, 0, len(g.targets))
	for key := range g.targets {
		keys = append(keys, key)
	}
	g.mu.Lock()
	for key := range g.runtime {
		if _, static := g.targets[key]; !static {
			keys = append(keys, key)
		}
	}
	g.mu.Unlock()
	sort.Strings(keys)
	return keys
}

// Verdict returns the verdict last recorded for key without asking the
// forge; seen is false when none was recorded yet.
func (g *Gate) Verdict(key string) (Readiness, bool) {
	if g == nil {
		return Readiness{}, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	v, seen := g.verdicts[key]
	return v, seen
}
