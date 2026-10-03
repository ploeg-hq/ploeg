package worker

import (
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func envCfg() Config {
	return Config{
		ForgeURL:   "http://forge",
		RepoOwner:  "webgrip",
		RepoName:   "erfbeeld",
		BaseBranch: "main",
	}
}

func itemWith(t *work.Target) work.WorkItem { return work.WorkItem{ExternalID: "1", Target: t} }

func TestResolveTarget_Precedence(t *testing.T) {
	claim := &work.Target{Owner: "webgrip", Repo: "ploeg", BaseBranch: "development"}

	cases := []struct {
		name              string
		cfg               Config
		item              work.WorkItem
		wantOwner, wantNm string
		wantBase          string
		wantErr           bool
	}{
		{
			name: "no claim target falls back to env (pre-decoupling behavior)",
			cfg:  envCfg(), item: itemWith(nil),
			wantOwner: "webgrip", wantNm: "erfbeeld", wantBase: "main",
		},
		{
			name: "resolved claim target wins",
			cfg:  envCfg(), item: itemWith(claim),
			wantOwner: "webgrip", wantNm: "ploeg", wantBase: "development",
		},
		{
			name:      "targetSource=env ignores the claim target entirely",
			cfg:       func() Config { c := envCfg(); c.TargetSource = TargetSourceEnv; return c }(),
			item:      itemWith(claim),
			wantOwner: "webgrip", wantNm: "erfbeeld", wantBase: "main",
		},
		{
			name: "claim target without a base branch inherits the env base",
			cfg:  envCfg(), item: itemWith(&work.Target{Owner: "acme", Repo: "thing"}),
			wantOwner: "acme", wantNm: "thing", wantBase: "main",
		},
		{
			name: "no claim target and no env target is a configuration failure",
			cfg:  Config{ForgeURL: "http://forge"}, item: itemWith(nil),
			wantErr: true,
		},
		{
			name: "targetSource=env with no env target is a configuration failure",
			cfg:  Config{ForgeURL: "http://forge", TargetSource: TargetSourceEnv},
			item: itemWith(claim), wantErr: true,
		},
		{
			name: "claim target rescues a worker with no env repo at all",
			cfg:  Config{ForgeURL: "http://forge"}, item: itemWith(claim),
			wantOwner: "webgrip", wantNm: "ploeg", wantBase: "development",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveTarget(c.cfg, c.item, discardLog())
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveTarget: %v", err)
			}
			if got.Owner != c.wantOwner || got.Name != c.wantNm || got.BaseBranch != c.wantBase {
				t.Errorf("got %s/%s@%s, want %s/%s@%s",
					got.Owner, got.Name, got.BaseBranch, c.wantOwner, c.wantNm, c.wantBase)
			}
			if got.ForgeURL != c.cfg.ForgeURL {
				t.Errorf("forge URL = %q, want %q (forge is global today)", got.ForgeURL, c.cfg.ForgeURL)
			}
		})
	}
}

// Owner and repo are atomic: a target naming only one of them is not resolved,
// so it must never be blended with the env fallback — cloning one repo and
// pushing to another is the worst failure this seam can produce.
func TestResolveTarget_HalfResolvedNeverBlends(t *testing.T) {
	for _, partial := range []*work.Target{
		{Owner: "webgrip"},
		{Repo: "ploeg"},
		{},
	} {
		got, err := resolveTarget(envCfg(), itemWith(partial), discardLog())
		if err != nil {
			t.Fatalf("resolveTarget: %v", err)
		}
		if got.Owner != "webgrip" || got.Name != "erfbeeld" {
			t.Errorf("partial target %+v produced %s/%s — must fall back to env wholesale",
				partial, got.Owner, got.Name)
		}
	}
}
