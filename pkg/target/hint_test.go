package target

import (
	"errors"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

var (
	app      = work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "app", BaseBranch: "development"}
	homelab  = work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "homelab-cluster", BaseBranch: "main"}
	erfbeeld = work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "erfbeeld", BaseBranch: "main"}
)

func registryResolver(t *testing.T) *MapResolver {
	t.Helper()
	r, err := New(Table{
		Targets: map[string]work.Target{
			"app":                app,
			"homelab-cluster":    homelab,
			"erfbeeld":           erfbeeld,
			"nuala-nalatenschap": erfbeeld,
		},
		Rules: []Rule{
			{Scope: "10", Default: "app", Allow: []string{"homelab-cluster"}},
			{Scope: "7", Allow: []string{"homelab-cluster", "app"}},
			{Scope: "4", Target: erfbeeld},
			{Scope: "3", Default: "homelab-cluster"},
			{Scope: "11", Team: "console", Default: "app"},
			{Scope: "11", Default: "erfbeeld"},
		},
	}, "forgejo")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func TestRouteSelectsARegisteredTargetByLabel(t *testing.T) {
	r := registryResolver(t)
	cases := []struct {
		name string
		req  Request
		want work.Target
		rule string
		hint string
		key  string
	}{
		{"no repo label uses the board default", Request{Scope: "10", LabelsRead: true}, app, "10", "", "app"},
		{"labels that are not hints are ignored", Request{Scope: "10", Labels: []string{"do-next", "theme/routing"}, LabelsRead: true}, app, "10", "", "app"},
		{"one allowed label wins over the default", Request{Scope: "10", Labels: []string{"do-next", "repo/homelab-cluster"}, LabelsRead: true}, homelab, "10", "repo/homelab-cluster", "homelab-cluster"},
		{"a label naming the board default selects it", Request{Scope: "10", Labels: []string{"repo/app"}, LabelsRead: true}, app, "10", "repo/app", "app"},
		{"two labels with one title are one hint", Request{Scope: "10", Labels: []string{"repo/homelab-cluster", "repo/homelab-cluster"}, LabelsRead: true}, homelab, "10", "repo/homelab-cluster", "homelab-cluster"},
		{"a hint-required board routes an allowed label", Request{Scope: "7", Labels: []string{"repo/app"}, LabelsRead: true}, app, "7", "repo/app", "app"},
		{"a board without allow may select its own repository", Request{Scope: "4", Labels: []string{"repo/erfbeeld"}, LabelsRead: true}, erfbeeld, "4", "repo/erfbeeld", "erfbeeld"},
		{"two keys for one repository both select it", Request{Scope: "4", Labels: []string{"repo/nuala-nalatenschap"}, LabelsRead: true}, erfbeeld, "4", "repo/nuala-nalatenschap", "nuala-nalatenschap"},
		{"a board repository without a label is unchanged", Request{Scope: "4", LabelsRead: true}, erfbeeld, "4", "", ""},
		{"a default-only board may select its default", Request{Scope: "3", Labels: []string{"repo/homelab-cluster"}, LabelsRead: true}, homelab, "3", "repo/homelab-cluster", "homelab-cluster"},
		{"the team-qualified rule decides first", Request{Scope: "11", Team: "console", LabelsRead: true}, app, "11/console", "", "app"},
		{"other teams take the bare rule", Request{Scope: "11", Team: "bronze", LabelsRead: true}, erfbeeld, "11", "", "erfbeeld"},
		{"unreadable labels do not matter to a board without label routing", Request{Scope: "4"}, erfbeeld, "4", "", ""},
		{"unreadable labels do not matter to a default-only board", Request{Scope: "3"}, homelab, "3", "", "homelab-cluster"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok, err := r.Route(c.req)
			if err != nil || !ok {
				t.Fatalf("Route(%+v) = ok %v, err %v; want a route", c.req, ok, err)
			}
			if got.Target != c.want || got.Rule != c.rule || got.Hint != c.hint || got.Key != c.key {
				t.Errorf("Route(%+v) = %+v, want target %+v rule %q hint %q key %q", c.req, got, c.want, c.rule, c.hint, c.key)
			}
		})
	}
}

func TestRouteRefusesWithoutFallingBack(t *testing.T) {
	r := registryResolver(t)
	cases := []struct {
		name   string
		req    Request
		reason string
	}{
		{"two different repo labels", Request{Scope: "10", Labels: []string{"repo/homelab-cluster", "repo/app"}, LabelsRead: true}, "more than one repository label"},
		{"a registered and an unregistered repo label", Request{Scope: "10", Labels: []string{"repo/homelab-cluster", "repo/ploeg"}, LabelsRead: true}, "more than one repository label"},
		{"an unregistered repo label", Request{Scope: "10", Labels: []string{"repo/ploeg"}, LabelsRead: true}, `label "repo/ploeg" names no registered target`},
		{"a label is compared by whole-title equality", Request{Scope: "10", Labels: []string{"repo/Homelab-Cluster"}, LabelsRead: true}, "names no registered target"},
		{"a registered target the board does not allow", Request{Scope: "10", Labels: []string{"repo/erfbeeld"}, LabelsRead: true}, `which board rule "10" does not allow`},
		{"another repository on a board without allow", Request{Scope: "4", Labels: []string{"repo/homelab-cluster"}, LabelsRead: true}, "does not allow"},
		{"no label on a hint-required board", Request{Scope: "7", Labels: []string{"do-next"}, LabelsRead: true}, "requires a repository label"},
		{"a label on a board no rule covers", Request{Scope: "99", Labels: []string{"repo/app"}, LabelsRead: true}, "no board rule covers this item"},
		{"unreadable labels on a board that routes by label", Request{Scope: "10"}, "labels could not be read"},
		{"unreadable labels on a hint-required board", Request{Scope: "7"}, "labels could not be read"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok, err := r.Route(c.req)
			var refusal *Refusal
			if !errors.As(err, &refusal) {
				t.Fatalf("Route(%+v) err = %v, want a refusal", c.req, err)
			}
			if ok || got != (Route{}) {
				t.Errorf("a refusal must carry no target, got ok %v route %+v", ok, got)
			}
			if !strings.Contains(refusal.Reason, c.reason) {
				t.Errorf("reason = %q, want it to mention %q", refusal.Reason, c.reason)
			}
		})
	}
}

func TestRouteLeavesAnUncoveredUnlabelledItemUnresolved(t *testing.T) {
	r := registryResolver(t)
	got, ok, err := r.Route(Request{Scope: "99", Labels: []string{"do-next"}, LabelsRead: true})
	if err != nil || ok || got != (Route{}) {
		t.Errorf("Route = %+v ok %v err %v; an uncovered scope without a repo label keeps today's unresolved result", got, ok, err)
	}
}

func TestRouteIgnoresLabelsWithoutARegistry(t *testing.T) {
	r := mustResolver(t, "10=webgrip/app@development;forge=forgejo", "forgejo")
	for _, labels := range [][]string{nil, {"repo/homelab-cluster"}, {"repo/a", "repo/b"}} {
		got, ok, err := r.Route(Request{Scope: "10", Labels: labels})
		if err != nil || !ok || got.Target != app || got.Rule != "10" || got.Hint != "" || got.Key != "" {
			t.Errorf("labels %q: Route = %+v ok %v err %v, want the board repository exactly as before", labels, got, ok, err)
		}
	}
}

func TestNewRejectsUnsatisfiableRouting(t *testing.T) {
	targets := map[string]work.Target{"app": app}
	cases := []struct {
		name  string
		table Table
	}{
		{"an unregistered default", Table{Targets: targets, Rules: []Rule{{Scope: "10", Default: "homelab-cluster"}}}},
		{"an unregistered allowed target", Table{Targets: targets, Rules: []Rule{{Scope: "10", Default: "app", Allow: []string{"ploeg"}}}}},
		{"a board with no way to route", Table{Targets: targets, Rules: []Rule{{Scope: "10"}}}},
		{"a repository and a default together", Table{Targets: targets, Rules: []Rule{{Scope: "10", Target: app, Default: "app"}}}},
		{"a key with surrounding space", Table{Targets: map[string]work.Target{"app ": app}}},
		{"an empty key", Table{Targets: map[string]work.Target{"": app}}},
		{"a target without a repository", Table{Targets: map[string]work.Target{"app": {Owner: "webgrip"}}}},
		{"a rule without a scope", Table{Targets: targets, Rules: []Rule{{Default: "app"}}}},
	}
	for _, c := range cases {
		if _, err := New(c.table, "forgejo"); err == nil {
			t.Errorf("%s: New should have failed", c.name)
		}
	}
}

func TestNewFillsTheDefaultForge(t *testing.T) {
	r, err := New(Table{Targets: map[string]work.Target{"app": {Owner: "webgrip", Repo: "app"}}}, "forgejo")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Registered()["app"].Forge; got != "forgejo" {
		t.Errorf("forge = %q, want the default forge", got)
	}
}
