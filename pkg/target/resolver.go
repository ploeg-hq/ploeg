// Package target resolves where a Work Item's changes land. The mapping is
// core policy, not provider knowledge: a TrackerProvider reports the opaque
// scope its vendor natively owns (Vikunja's project id) and the item's label
// titles, and this package decides the repository. Keeping the decision here
// is what R7 requires — a provider that resolved Ploeg Targets would make
// every future provider carry the same mapping semantics forever.
//
// A board routes either to one repository of its own (a Rule with a Target)
// or through the registry of named targets (ADR-0038): a `repo/<key>` label
// selects one of the targets the board allows, and the board's default
// applies when the item carries no such label.
package target

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// HintPrefix marks a tracker label as a routing hint (ADR-0038). A label
// titled HintPrefix+key selects the registered target named key.
const HintPrefix = "repo/"

// Resolver maps a tracker item's scope, team and labels to the repository the
// work lands in.
type Resolver interface {
	// Route returns ok=false with a nil error when no rule covers the item's
	// scope: the caller leaves the Target unresolved. A non-nil error is a
	// *Refusal: the item must not be queued anywhere.
	Route(req Request) (r Route, ok bool, err error)
}

// Request is what routing reads from one tracker item.
type Request struct {
	Scope string
	Team  string
	// Labels are the item's label titles, exactly as the tracker returned
	// them. Only whole-title equality with HintPrefix+key selects a target.
	Labels []string
	// LabelsRead is false when the labels came from nowhere authoritative,
	// such as a webhook snapshot after the tracker read failed. A board that
	// can select by label then refuses rather than guess.
	LabelsRead bool
}

// Route is a routing decision, pinned on the Work Item at ingest.
type Route struct {
	Target work.Target
	// Rule is the id of the matched board rule, "<scope>[/<team>]".
	Rule string
	// Hint is the label title that selected the target; empty when the
	// board's default decided.
	Hint string
	// Key is the registry key of the target; empty when the board routes to
	// a repository of its own.
	Key string
}

// Refusal codes name why routing refused an item. They are stable: the
// operator API returns them and front ends phrase each one.
const (
	// RefusalLabelsUnread: the tracker labels could not be read and the board routes by label.
	RefusalLabelsUnread = "labels_unread"
	// RefusalMultipleLabels: the item carries more than one distinct repo/ label.
	RefusalMultipleLabels = "multiple_labels"
	// RefusalLabelMissing: the board requires a repo/ label and the item has none.
	RefusalLabelMissing = "label_missing"
	// RefusalLabelUnregistered: the repo/ label names no registered target.
	RefusalLabelUnregistered = "label_unregistered"
	// RefusalNoBoardRule: the repo/ label names a target, but no board rule covers the item.
	RefusalNoBoardRule = "no_board_rule"
	// RefusalLabelNotAllowed: the repo/ label names a target the board does not allow.
	RefusalLabelNotAllowed = "label_not_allowed"
	// RefusalTargetNotReady: the selected target failed its readiness check at ingest.
	RefusalTargetNotReady = "target_not_ready"
)

// Refusal is a routing failure that must not fall back to any repository.
type Refusal struct {
	// Code is one of the Refusal* constants.
	Code   string
	Reason string
	// Allowed are the repo/ labels the matched board rule allows, sorted; empty
	// and non-nil when no rule matched or the caller cannot know them.
	Allowed []string
}

func (r *Refusal) Error() string { return r.Reason }

// Rule routes one tracker container, optionally for one team. Target is the
// board's own repository and Default a registry key; a rule sets at most one
// of them. A rule with neither routes only by label, so it needs Allow.
type Rule struct {
	Scope   string
	Team    string
	Target  work.Target
	Default string
	Allow   []string
}

// ID is the rule's identity, echoed into work_items.route_rule for audit.
func (r Rule) ID() string {
	if r.Team == "" {
		return r.Scope
	}
	return r.Scope + "/" + r.Team
}

// Table is a whole routing configuration: the board rules and the registry
// of named targets they may select from.
type Table struct {
	Rules   []Rule
	Targets map[string]work.Target
}

// MapResolver resolves from a static Table. Rules are exact-match and ordered
// by specificity: "<scope>/<team>" before "<scope>". Label titles are compared
// with registry keys for equality and never parsed beyond recognising
// HintPrefix.
type MapResolver struct {
	rules   []Rule
	targets map[string]work.Target
	hints   map[string]string
	// DefaultForge fills an entry's empty forge id, so the common
	// single-forge deployment writes owner/repo and nothing else.
	DefaultForge string
}

// New builds a resolver from a Table. A rule that names an unregistered key,
// or that has no way to route unlabelled work and no label to route by, is an
// error: routing that cannot be satisfied must never boot.
func New(table Table, defaultForge string) (*MapResolver, error) {
	m := &MapResolver{DefaultForge: defaultForge, targets: map[string]work.Target{}, hints: map[string]string{}}
	for key, t := range table.Targets {
		if key == "" || strings.TrimSpace(key) != key {
			return nil, fmt.Errorf("target key %q: must be non-empty with no surrounding space", key)
		}
		if !t.Resolved() {
			return nil, fmt.Errorf("target %q: want an owner and a repo", key)
		}
		m.targets[key] = m.withForge(t)
		m.hints[HintPrefix+key] = key
	}
	for _, r := range table.Rules {
		if r.Scope == "" {
			return nil, fmt.Errorf("rule %q: empty scope", r.ID())
		}
		if err := m.checkRule(r); err != nil {
			return nil, fmt.Errorf("rule %q: %w", r.ID(), err)
		}
		if r.Target.Resolved() {
			r.Target = m.withForge(r.Target)
		}
		m.rules = append(m.rules, r)
	}
	sort.SliceStable(m.rules, func(i, j int) bool {
		return m.rules[i].Team != "" && m.rules[j].Team == ""
	})
	return m, nil
}

func (m *MapResolver) checkRule(r Rule) error {
	if r.Target.Resolved() && r.Default != "" {
		return fmt.Errorf("names both a repository and a default target")
	}
	if !r.Target.Resolved() && r.Default == "" && len(r.Allow) == 0 {
		return fmt.Errorf("names no repository, no default target and no allowed targets")
	}
	for _, key := range append([]string{r.Default}, r.Allow...) {
		if key == "" {
			continue
		}
		if _, ok := m.targets[key]; !ok {
			return fmt.Errorf("target %q is not registered", key)
		}
	}
	return nil
}

func (m *MapResolver) withForge(t work.Target) work.Target {
	if t.Forge == "" {
		t.Forge = m.DefaultForge
	}
	return t
}

// ParseRules reads the legacy PLOEG_TARGET_MAP wire format:
//
//	<scope>[/<team>]=<owner>/<repo>[@<baseBranch>][;forge=<id>], comma separated
//
// e.g. "11/bronze=webgrip/erfbeeld@main,11/silver=webgrip/ploeg@development,14=webgrip/homelab-cluster@main"
//
// A malformed entry is an error rather than a silent drop: a typo that routes
// work to the wrong repository must never boot.
func ParseRules(spec string) ([]Rule, error) {
	var rules []Rule
	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		key, val, ok := strings.Cut(entry, "=")
		if !ok {
			return nil, fmt.Errorf("target map entry %q: want <scope>[/<team>]=<owner>/<repo>[@<branch>]", entry)
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)

		var r Rule
		r.Scope, r.Team, _ = strings.Cut(key, "/")
		if r.Scope == "" {
			return nil, fmt.Errorf("target map entry %q: empty scope", entry)
		}
		if rest, forge, found := strings.Cut(val, ";forge="); found {
			r.Target.Forge = strings.TrimSpace(forge)
			val = strings.TrimSpace(rest)
		}
		if rest, branch, found := strings.Cut(val, "@"); found {
			r.Target.BaseBranch = strings.TrimSpace(branch)
			val = strings.TrimSpace(rest)
		}
		owner, repo, found := strings.Cut(val, "/")
		if !found || owner == "" || repo == "" {
			return nil, fmt.Errorf("target map entry %q: want <owner>/<repo> target, got %q", entry, val)
		}
		r.Target.Owner, r.Target.Repo = owner, repo
		rules = append(rules, r)
	}
	return rules, nil
}

// NewMapResolver parses the legacy wire format (see ParseRules) into a
// resolver with no target registry, so labels never affect its routing.
func NewMapResolver(spec, defaultForge string) (*MapResolver, error) {
	rules, err := ParseRules(spec)
	if err != nil {
		return nil, err
	}
	return New(Table{Rules: rules}, defaultForge)
}

// Route applies ADR-0038. Without a registry, labels are ignored and the
// matched rule's own repository decides, exactly as before the registry
// existed. With one, at most one distinct `repo/*` label may be present; it
// must name a registered target that the board allows, and without it the
// board's default decides. None of the refusals falls back.
func (m *MapResolver) Route(req Request) (Route, bool, error) {
	if m == nil {
		return Route{}, false, nil
	}
	rule, matched := m.match(req.Scope, req.Team)
	if len(m.targets) == 0 {
		if !matched {
			return Route{}, false, nil
		}
		return Route{Target: rule.Target, Rule: rule.ID()}, true, nil
	}
	if matched && rule.selectsByLabel() && !req.LabelsRead {
		return Route{}, false, refuse(RefusalLabelsUnread, m.allowed(rule, matched), "its labels could not be read from the tracker, and board rule %q routes by label", rule.ID())
	}
	hints := hintLabels(req.Labels)
	if len(hints) > 1 {
		return Route{}, false, refuse(RefusalMultipleLabels, m.allowed(rule, matched), "it carries more than one repository label (%s); keep exactly one", strings.Join(hints, ", "))
	}
	if len(hints) == 1 {
		return m.routeByHint(rule, matched, hints[0])
	}
	if !matched {
		return Route{}, false, nil
	}
	switch {
	case rule.Default != "":
		return Route{Target: m.targets[rule.Default], Rule: rule.ID(), Key: rule.Default}, true, nil
	case rule.Target.Resolved():
		return Route{Target: rule.Target, Rule: rule.ID()}, true, nil
	default:
		allowed := m.allowed(rule, matched)
		return Route{}, false, refuse(RefusalLabelMissing, allowed, "board rule %q requires a repository label and the item has none; add one of: %s", rule.ID(), labelList(allowed))
	}
}

func (m *MapResolver) routeByHint(rule Rule, matched bool, hint string) (Route, bool, error) {
	key, registered := m.hints[hint]
	if !registered {
		return Route{}, false, refuse(RefusalLabelUnregistered, m.allowed(rule, matched), "label %q names no registered target", hint)
	}
	if !matched {
		return Route{}, false, refuse(RefusalNoBoardRule, []string{}, "label %q selects target %q, but no board rule covers this item, so it may select nothing", hint, key)
	}
	if !m.allows(rule, key) {
		allowed := m.allowed(rule, matched)
		return Route{}, false, refuse(RefusalLabelNotAllowed, allowed, "label %q selects target %q, which board rule %q does not allow; allowed: %s", hint, key, rule.ID(), labelList(allowed))
	}
	return Route{Target: m.targets[key], Rule: rule.ID(), Hint: hint, Key: key}, true, nil
}

func (m *MapResolver) allows(rule Rule, key string) bool {
	if key == rule.Default {
		return true
	}
	for _, allowed := range rule.Allow {
		if allowed == key {
			return true
		}
	}
	return rule.Target.Resolved() && m.targets[key] == rule.Target
}

func (m *MapResolver) allowed(rule Rule, matched bool) []string {
	labels := []string{}
	if !matched {
		return labels
	}
	for _, key := range m.sortedKeys() {
		if m.allows(rule, key) {
			labels = append(labels, HintPrefix+key)
		}
	}
	return labels
}

func labelList(labels []string) string {
	if len(labels) == 0 {
		return "none"
	}
	return strings.Join(labels, ", ")
}

func (m *MapResolver) sortedKeys() []string {
	keys := make([]string, 0, len(m.targets))
	for key := range m.targets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (r Rule) selectsByLabel() bool {
	return len(r.Allow) > 0 || (r.Default == "" && !r.Target.Resolved())
}

func hintLabels(labels []string) []string {
	seen := map[string]bool{}
	var hints []string
	for _, label := range labels {
		if !strings.HasPrefix(label, HintPrefix) || seen[label] {
			continue
		}
		seen[label] = true
		hints = append(hints, label)
	}
	sort.Strings(hints)
	return hints
}

func refuse(code string, allowed []string, format string, args ...any) *Refusal {
	return &Refusal{Code: code, Reason: fmt.Sprintf(format, args...), Allowed: allowed}
}

func (m *MapResolver) match(scope, team string) (Rule, bool) {
	if scope == "" {
		return Rule{}, false
	}
	for _, r := range m.rules {
		if r.Scope != scope {
			continue
		}
		if r.Team != "" && r.Team != team {
			continue
		}
		return r, true
	}
	return Rule{}, false
}

// Resolve routes an item that carries no labels. It is Route for callers that
// only know a scope and a team, and reports ok=false for a refusal as well as
// for an unmatched scope.
func (m *MapResolver) Resolve(scope, team string) (work.Target, string, bool) {
	r, ok, err := m.Route(Request{Scope: scope, Team: team, LabelsRead: true})
	if err != nil || !ok {
		return work.Target{}, "", false
	}
	return r.Target, r.Rule, true
}

// Len reports how many rules were loaded, for the boot log.
func (m *MapResolver) Len() int {
	if m == nil {
		return 0
	}
	return len(m.rules)
}

// Registered returns a copy of the target registry with forge ids filled in.
func (m *MapResolver) Registered() map[string]work.Target {
	out := map[string]work.Target{}
	if m == nil {
		return out
	}
	for key, t := range m.targets {
		out[key] = t
	}
	return out
}
