package target

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type fakeInspector struct {
	states map[string]provider.RepositoryState
	err    error
	calls  int
}

func (f *fakeInspector) InspectRepository(_ context.Context, owner, repository, branch string) (provider.RepositoryState, error) {
	f.calls++
	if f.err != nil {
		return provider.RepositoryState{}, f.err
	}
	state, ok := f.states[owner+"/"+repository]
	if !ok {
		return provider.RepositoryState{}, errors.New("HTTP 404")
	}
	state.Branch = branch
	return state, nil
}

func gateFor(inspector *fakeInspector, targets map[string]work.Target) *Gate {
	return NewGate(targets, map[string]provider.RepositoryInspector{"forgejo": inspector})
}

func TestGateLoadsEachUnreadyReasonAndAReadyTarget(t *testing.T) {
	inspector := &fakeInspector{states: map[string]provider.RepositoryState{
		"webgrip/archived": {Archived: true, AgentsFile: true},
		"webgrip/mirror":   {Mirror: true, AgentsFile: true},
		"webgrip/bare":     {},
		"webgrip/app":      {AgentsFile: true},
	}}
	target := func(repo string) work.Target {
		return work.Target{Forge: "forgejo", Owner: "webgrip", Repo: repo, BaseBranch: "main"}
	}
	g := gateFor(inspector, map[string]work.Target{
		"archived": target("archived"),
		"mirror":   target("mirror"),
		"bare":     target("bare"),
		"app":      target("app"),
		"missing":  target("missing"),
		"gitlab":   {Forge: "gitlab", Owner: "acme", Repo: "app"},
	})
	verdicts := g.Load(context.Background())
	for key, reason := range map[string]string{
		"archived": "is archived",
		"mirror":   "is a mirror",
		"bare":     `has no AGENTS.md on branch "main"`,
		"gitlab":   `forge "gitlab" cannot report`,
	} {
		v := verdicts[key]
		if v.Ready || v.Unknown || !strings.Contains(v.Reason, reason) {
			t.Errorf("%s = %+v, want not ready because it %s", key, v, reason)
		}
	}
	if v := verdicts["missing"]; v.Ready || !v.Unknown || v.Reason == "" {
		t.Errorf("missing = %+v, want unknown with a reason", v)
	}
	if v := verdicts["app"]; !v.Ready || v.Reason != "" {
		t.Errorf("app = %+v, want ready", v)
	}
}

func TestGateAdmitTrustsReadyAndRechecksTheRest(t *testing.T) {
	inspector := &fakeInspector{states: map[string]provider.RepositoryState{"webgrip/app": {}}}
	g := gateFor(inspector, map[string]work.Target{"app": app})
	ctx := context.Background()
	if v := g.Load(ctx)["app"]; v.Ready {
		t.Fatalf("loaded %+v, want not ready", v)
	}
	inspector.states["webgrip/app"] = provider.RepositoryState{AgentsFile: true}
	if v := g.Admit(ctx, "app"); !v.Ready {
		t.Fatalf("admit after the fix = %+v, want ready without a restart", v)
	}
	calls := inspector.calls
	if v := g.Admit(ctx, "app"); !v.Ready || inspector.calls != calls {
		t.Errorf("admit of a ready target asked the forge again (%d calls, was %d)", inspector.calls, calls)
	}
}

func TestGateCheckSeesATargetThatBecameUnready(t *testing.T) {
	inspector := &fakeInspector{states: map[string]provider.RepositoryState{"webgrip/app": {AgentsFile: true}}}
	g := gateFor(inspector, map[string]work.Target{"app": app})
	ctx := context.Background()
	if v := g.Admit(ctx, "app"); !v.Ready {
		t.Fatalf("admit = %+v", v)
	}
	inspector.states["webgrip/app"] = provider.RepositoryState{Archived: true, AgentsFile: true}
	key, ok := g.KeyOf(app)
	if !ok || key != "app" {
		t.Fatalf("KeyOf = %q %v", key, ok)
	}
	if v := g.Check(ctx, key); v.Ready || !strings.Contains(v.Reason, "archived") {
		t.Errorf("check = %+v, want the archive seen at claim", v)
	}
	if _, ok := g.KeyOf(homelab); ok {
		t.Error("an unregistered target must have no key")
	}
}
