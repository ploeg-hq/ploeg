package config

import (
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func targetSpec(f *File, r ScopeResolver, log *slog.Logger) (string, error) {
	table, err := f.RoutingTable(context.Background(), r, log)
	if err != nil {
		return "", err
	}
	var entries []string
	for _, rule := range table.Rules {
		entry := rule.ID() + "=" + rule.Target.Owner + "/" + rule.Target.Repo
		if rule.Target.BaseBranch != "" {
			entry += "@" + rule.Target.BaseBranch
		}
		if rule.Target.Forge != "" {
			entry += ";forge=" + rule.Target.Forge
		}
		entries = append(entries, entry)
	}
	return strings.Join(entries, ","), nil
}

const multiRepoBoard = `
targets:
  glide:
    repo: webgrip/glide
    branch: development
    forge: forgejo
  homelab-cluster:
    repo: webgrip/homelab-cluster
    branch: main
    forge: forgejo
trackers:
  vikunja:
    projects:
      - name: "Glide"
        id: "10"
        default: glide
        allow: [homelab-cluster]
      - name: "Forgejo Migration"
        id: "7"
        allow: [homelab-cluster, glide]
      - name: "Erfbeeld"
        id: "4"
        repo: webgrip/erfbeeld
        branch: main
`

func TestRoutingTable_CarriesTheRegistryAndBoardPolicies(t *testing.T) {
	f, err := Load(write(t, multiRepoBoard))
	if err != nil {
		t.Fatal(err)
	}
	table, err := f.RoutingTable(context.Background(), nil, discard())
	if err != nil {
		t.Fatal(err)
	}
	wantTargets := map[string]work.Target{
		"glide":           {Forge: "forgejo", Owner: "webgrip", Repo: "glide", BaseBranch: "development"},
		"homelab-cluster": {Forge: "forgejo", Owner: "webgrip", Repo: "homelab-cluster", BaseBranch: "main"},
	}
	if !reflect.DeepEqual(table.Targets, wantTargets) {
		t.Errorf("targets = %+v", table.Targets)
	}
	wantRules := []target.Rule{
		{Scope: "10", Default: "glide", Allow: []string{"homelab-cluster"}},
		{Scope: "7", Allow: []string{"homelab-cluster", "glide"}},
		{Scope: "4", Target: work.Target{Owner: "webgrip", Repo: "erfbeeld", BaseBranch: "main"}},
	}
	if !reflect.DeepEqual(table.Rules, wantRules) {
		t.Errorf("rules = %+v", table.Rules)
	}
	if _, err := target.New(table, "forgejo"); err != nil {
		t.Errorf("the rendered table does not build a resolver: %v", err)
	}
}

func TestRoutingTable_WithoutTargetsHasNoRegistry(t *testing.T) {
	f, err := Load(write(t, "trackers:\n  vikunja:\n    projects:\n      - {id: \"10\", repo: webgrip/glide}\n"))
	if err != nil {
		t.Fatal(err)
	}
	table, err := f.RoutingTable(context.Background(), nil, discard())
	if err != nil {
		t.Fatal(err)
	}
	if table.Targets != nil {
		t.Errorf("targets = %+v, want none so labels never route", table.Targets)
	}
}

func TestLoad_RejectsBadTargetRouting(t *testing.T) {
	registry := "targets:\n  glide: {repo: webgrip/glide}\n"
	for name, body := range map[string]string{
		"target repo without owner": "targets:\n  glide: {repo: glide}\n",
		"unregistered default":      registry + "trackers:\n  vikunja:\n    projects:\n      - {id: \"10\", default: ploeg}\n",
		"unregistered allow":        registry + "trackers:\n  vikunja:\n    projects:\n      - {id: \"10\", default: glide, allow: [ploeg]}\n",
		"default and repo":          registry + "trackers:\n  vikunja:\n    projects:\n      - {id: \"10\", default: glide, repo: webgrip/glide}\n",
		"default and branch":        registry + "trackers:\n  vikunja:\n    projects:\n      - {id: \"10\", default: glide, branch: main}\n",
		"no route at all":           registry + "trackers:\n  vikunja:\n    projects:\n      - {id: \"10\"}\n",
		"allow without registry":    "trackers:\n  vikunja:\n    projects:\n      - {id: \"10\", repo: webgrip/glide, allow: [glide]}\n",
		"misspelt key":              registry + "trackers:\n  vikunja:\n    projects:\n      - {id: \"10\", defualt: glide}\n",
	} {
		if _, err := Load(write(t, body)); err == nil {
			t.Errorf("%s: Load accepted it", name)
		}
	}
}
