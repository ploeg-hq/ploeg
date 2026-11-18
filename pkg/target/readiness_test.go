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

func TestGateReturnsStructuredReasonsAndOffersThePrepareOnlyForTheMissingFileAlone(t *testing.T) {
	inspector := &fakeInspector{states: map[string]provider.RepositoryState{
		"webgrip/bare":            {},
		"webgrip/archived":        {Archived: true},
		"webgrip/archived-with":   {Archived: true, AgentsFile: true},
		"webgrip/mirror":          {Mirror: true},
		"webgrip/archived-mirror": {Archived: true, Mirror: true},
		"webgrip/app":             {AgentsFile: true},
	}}
	targets := map[string]work.Target{}
	for repo := range inspector.states {
		targets[strings.TrimPrefix(repo, "webgrip/")] = work.Target{Forge: "forgejo", Owner: "webgrip", Repo: strings.TrimPrefix(repo, "webgrip/")}
	}
	targets["gitlab"] = work.Target{Forge: "gitlab", Owner: "acme", Repo: "app"}
	targets["missing"] = work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "missing"}
	verdicts := gateFor(inspector, targets).Load(context.Background())

	for key, want := range map[string]struct {
		reasons []string
		prepare bool
	}{
		"bare":            {reasons: []string{ReasonInstructionFileMissing}, prepare: true},
		"archived":        {reasons: []string{ReasonArchived, ReasonInstructionFileMissing}},
		"archived-with":   {reasons: []string{ReasonArchived}},
		"mirror":          {reasons: []string{ReasonMirror, ReasonInstructionFileMissing}},
		"archived-mirror": {reasons: []string{ReasonArchived, ReasonMirror, ReasonInstructionFileMissing}},
		"app":             {},
		"gitlab":          {},
		"missing":         {},
	} {
		v := verdicts[key]
		if strings.Join(v.Reasons, ",") != strings.Join(want.reasons, ",") || v.PrepareAvailable() != want.prepare {
			t.Errorf("%s = %+v (prepare %v), want reasons %v and prepare %v", key, v, v.PrepareAvailable(), want.reasons, want.prepare)
		}
	}
	if !strings.Contains(verdicts["archived"].Reason, `is archived, has no AGENTS.md on branch ""`) {
		t.Errorf("the sentence changed: %q", verdicts["archived"].Reason)
	}
}

func TestGateRecheckReportsTheVerdictBeforeAndAfter(t *testing.T) {
	inspector := &fakeInspector{states: map[string]provider.RepositoryState{"webgrip/bare": {}}}
	bare := work.Target{Forge: "forgejo", Owner: "webgrip", Repo: "bare"}
	g := gateFor(inspector, map[string]work.Target{"bare": bare})
	g.Load(context.Background())
	inspector.states["webgrip/bare"] = provider.RepositoryState{AgentsFile: true}

	before, after, seen, registered := g.Recheck(context.Background(), bare)

	if !registered || !seen || before.Ready || !after.Ready {
		t.Errorf("recheck = %+v -> %+v (seen %v, registered %v), want not ready then ready", before, after, seen, registered)
	}
	if _, _, _, registered := g.Recheck(context.Background(), work.Target{Owner: "o", Repo: "r"}); registered {
		t.Error("an unregistered target was rechecked")
	}
}
