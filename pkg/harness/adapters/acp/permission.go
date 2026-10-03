package acp

import (
	"strings"
	"time"
)

// Permission handling for an unattended run.
//
// ACP's session/request_permission exists so an editor can ask a human. There
// is no human in a worker pod, so the policy must answer every request
// immediately and deterministically. It must never block (the handler runs on
// the protocol read loop) and never prompt.
//
// Default is allow-all, matching the claudecode adapter's bypassPermissions and
// for the reason recorded there: the pod is a disposable, credential-scoped
// sandbox whose blast radius is bounded by the per-run LiteLLM key, the
// repo-scoped forge token, and the fact that it is destroyed at exit. The
// permission prompt is not the security boundary; the pod is.
//
// What the policy DOES protect against is a runaway agent: an agent that asks
// the same thing hundreds of times is not progressing, and a storm cap turns
// that from a silent 45-minute burn into an actionable stuck reason.

type PermissionMode string

const (
	// PermissionAllowAll grants everything. The default.
	PermissionAllowAll PermissionMode = "allow_always"
	// PermissionReadOnly grants read-shaped tools and rejects mutations. For a
	// reviewer persona, where the whole point is that it cannot edit.
	PermissionReadOnly PermissionMode = "allow_read_only"
	// PermissionDenyAll rejects everything. Diagnostic use only.
	PermissionDenyAll PermissionMode = "deny_all"
)

func ParsePermissionMode(s string) (PermissionMode, bool) {
	switch m := PermissionMode(normalise(s)); m {
	case PermissionAllowAll, PermissionReadOnly, PermissionDenyAll:
		return m, true
	case "":
		return PermissionAllowAll, true
	default:
		return PermissionAllowAll, false
	}
}

// PermissionOption is one choice the agent offered. Ploeg-owned rather than
// SDK-generated, for the same reason as the wire enums.
type PermissionOption struct {
	ID   string
	Name string
	Kind string // allow_once | allow_always | reject_once | reject_always
}

// PermissionRequest is what the agent asked for.
type PermissionRequest struct {
	ToolKind ToolKind
	Title    string
	Options  []PermissionOption
}

// PermissionPolicy decides, counts, and detects storms.
type PermissionPolicy struct {
	Mode PermissionMode
	// MaxRequests caps the total for one run.
	MaxRequests int
	// MaxPerMinute caps the rate.
	MaxPerMinute int

	// now is injectable so the rate window is testable without sleeping.
	now func() time.Time

	total  int
	stamps []time.Time
	titles map[string]int
}

const (
	defaultMaxPermissionRequests = 200
	defaultMaxPermissionsPerMin  = 60
)

func NewPermissionPolicy(mode PermissionMode, maxRequests, maxPerMinute int) *PermissionPolicy {
	if maxRequests <= 0 {
		maxRequests = defaultMaxPermissionRequests
	}
	if maxPerMinute <= 0 {
		maxPerMinute = defaultMaxPermissionsPerMin
	}
	return &PermissionPolicy{
		Mode: mode, MaxRequests: maxRequests, MaxPerMinute: maxPerMinute,
		now: time.Now, titles: map[string]int{},
	}
}

// Decision is the answer handed back to the agent.
type Decision struct {
	// OptionID names the chosen option. Empty means "no option was
	// acceptable" — the caller answers with ACP's cancelled variant rather
	// than guessing at an allow.
	OptionID string
	// Storm is set when the caps tripped. The caller cancels the session and
	// reports stuck; the run is not making progress.
	Storm bool
}

// Decide answers one request. Pure apart from the clock, so the whole matrix
// is a table test.
func (p *PermissionPolicy) Decide(req PermissionRequest) Decision {
	now := p.now()
	p.total++
	p.stamps = append(p.stamps, now)
	if t := strings.TrimSpace(req.Title); t != "" {
		p.titles[t]++
	}

	// Trim the rate window before measuring it.
	cutoff := now.Add(-time.Minute)
	keep := p.stamps[:0]
	for _, s := range p.stamps {
		if s.After(cutoff) {
			keep = append(keep, s)
		}
	}
	p.stamps = keep

	if p.total > p.MaxRequests || len(p.stamps) > p.MaxPerMinute {
		return Decision{Storm: true}
	}

	allow := p.allows(req.ToolKind)
	if id := pick(req.Options, allow); id != "" {
		return Decision{OptionID: id}
	}
	// The agent offered nothing matching. Never fall back to "the first
	// option" — on a deny decision that could silently grant a mutation.
	return Decision{}
}

func (p *PermissionPolicy) allows(k ToolKind) bool {
	switch p.Mode {
	case PermissionDenyAll:
		return false
	case PermissionReadOnly:
		switch k {
		case ToolRead, ToolSearch, ToolFetch, ToolThink:
			return true
		default:
			return false
		}
	default:
		return true
	}
}

// pick selects an option by kind, then by a name/id heuristic for agents that
// ship non-standard option sets. Order within each tier is deliberate:
// allow_once before allow_always so a grant never widens beyond this call, and
// reject_once before reject_always for the mirror reason.
func pick(opts []PermissionOption, allow bool) string {
	want := []string{"allow_once", "allow_always"}
	pattern := []string{"allow", "yes", "proceed", "approve"}
	if !allow {
		want = []string{"reject_once", "reject_always"}
		pattern = []string{"reject", "deny", "no", "cancel"}
	}
	for _, w := range want {
		for _, o := range opts {
			if normalise(o.Kind) == w {
				return o.ID
			}
		}
	}
	for _, needle := range pattern {
		for _, o := range opts {
			hay := strings.ToLower(o.ID + " " + o.Name)
			if strings.Contains(hay, needle) {
				// Guard the obvious trap: "reject" contains no "allow", but
				// "disallow" contains "allow".
				if allow && strings.Contains(hay, "disallow") {
					continue
				}
				return o.ID
			}
		}
	}
	return ""
}

// Stats summarises the run for the outcome report.
func (p *PermissionPolicy) Stats() (total int, topTitles []string) {
	type kv struct {
		t string
		n int
	}
	var all []kv
	for t, n := range p.titles {
		all = append(all, kv{t, n})
	}
	for i := 1; i < len(all); i++ {
		for j := i; j > 0 && (all[j].n > all[j-1].n || (all[j].n == all[j-1].n && all[j].t < all[j-1].t)); j-- {
			all[j], all[j-1] = all[j-1], all[j]
		}
	}
	for i, e := range all {
		if i == 3 {
			break
		}
		topTitles = append(topTitles, e.t)
	}
	return p.total, topTitles
}
