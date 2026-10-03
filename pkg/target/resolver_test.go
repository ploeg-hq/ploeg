package target

import "testing"

func mustResolver(t *testing.T, spec, defaultForge string) *MapResolver {
	t.Helper()
	r, err := NewMapResolver(spec, defaultForge)
	if err != nil {
		t.Fatalf("NewMapResolver(%q): %v", spec, err)
	}
	return r
}

// The live topology this must reproduce: the factory's webhook and every team
// share are wired on ONE Vikunja project (11), so scope alone cannot decide the
// repo — the composite <scope>/<team> key is what keeps bronze on erfbeeld and
// silver on ploeg (docs/ops/board.md, "Dispatch topology — the trap").
const liveSpec = "11/bronze=webgrip/erfbeeld@main,11/silver=webgrip/ploeg@development,11/copper=webgrip/erfbeeld@main,14=webgrip/homelab-cluster@main"

func TestResolve_CompositeKeyReproducesLiveRouting(t *testing.T) {
	r := mustResolver(t, liveSpec, "webgrip")
	cases := []struct {
		scope, team, wantOwner, wantRepo, wantBase string
	}{
		{"11", "bronze", "webgrip", "erfbeeld", "main"},
		{"11", "silver", "webgrip", "ploeg", "development"},
		{"11", "copper", "webgrip", "erfbeeld", "main"},
	}
	for _, c := range cases {
		got, rule, ok := r.Resolve(c.scope, c.team)
		if !ok {
			t.Fatalf("scope %s team %s: no match", c.scope, c.team)
		}
		if got.Owner != c.wantOwner || got.Repo != c.wantRepo || got.BaseBranch != c.wantBase {
			t.Errorf("scope %s team %s = %+v, want %s/%s@%s", c.scope, c.team, got, c.wantOwner, c.wantRepo, c.wantBase)
		}
		if rule != c.scope+"/"+c.team {
			t.Errorf("rule id = %q, want %q", rule, c.scope+"/"+c.team)
		}
		if got.Forge != "webgrip" {
			t.Errorf("forge = %q, want the default", got.Forge)
		}
	}
}

func TestResolve_ScopeOnlyMatchesAnyTeam(t *testing.T) {
	r := mustResolver(t, liveSpec, "")
	for _, team := range []string{"bronze", "silver", "anything"} {
		got, rule, ok := r.Resolve("14", team)
		if !ok || got.Repo != "homelab-cluster" || rule != "14" {
			t.Errorf("scope 14 team %s = %+v rule %q ok=%v", team, got, rule, ok)
		}
	}
}

func TestResolve_CompositeBeatsScopeOnlyRegardlessOfOrder(t *testing.T) {
	// Scope-only listed FIRST: specificity must still win, otherwise a
	// catch-all entry silently captures a team that has its own repo.
	r := mustResolver(t, "11=webgrip/fallback,11/silver=webgrip/ploeg", "")
	got, rule, ok := r.Resolve("11", "silver")
	if !ok || got.Repo != "ploeg" || rule != "11/silver" {
		t.Errorf("got %+v rule %q ok=%v — composite key must outrank scope-only", got, rule, ok)
	}
	got, rule, _ = r.Resolve("11", "bronze")
	if got.Repo != "fallback" || rule != "11" {
		t.Errorf("unmatched team should fall to the scope-only rule, got %+v rule %q", got, rule)
	}
}

func TestResolve_UnmappedAndEmpty(t *testing.T) {
	r := mustResolver(t, liveSpec, "")
	if _, _, ok := r.Resolve("999", "bronze"); ok {
		t.Error("unmapped scope must not resolve")
	}
	if _, _, ok := r.Resolve("", "bronze"); ok {
		t.Error("empty scope must not resolve")
	}
	var nilR *MapResolver
	if _, _, ok := nilR.Resolve("11", "bronze"); ok {
		t.Error("nil resolver must not resolve")
	}
	if nilR.Len() != 0 {
		t.Error("nil resolver Len must be 0")
	}
}

func TestNewMapResolver_EmptySpecIsValid(t *testing.T) {
	r := mustResolver(t, "", "")
	if r.Len() != 0 {
		t.Errorf("empty spec should yield no rules, got %d", r.Len())
	}
	if _, _, ok := r.Resolve("11", "bronze"); ok {
		t.Error("empty map must resolve nothing — the pre-decoupling default")
	}
}

func TestNewMapResolver_OptionalFields(t *testing.T) {
	r := mustResolver(t, "11=webgrip/erfbeeld, 12=acme/thing@trunk;forge=github ", "webgrip")
	got, _, _ := r.Resolve("11", "")
	if got.BaseBranch != "" || got.Forge != "webgrip" {
		t.Errorf("no @branch should mean repo default, got %+v", got)
	}
	got, _, _ = r.Resolve("12", "")
	if got.Forge != "github" || got.Owner != "acme" || got.Repo != "thing" || got.BaseBranch != "trunk" {
		t.Errorf("forge/branch suffixes mis-parsed: %+v", got)
	}
}

// A typo that would route work to the wrong repository must fail at boot, not
// silently drop the entry the way PLOEG_TEAM_MAP does.
func TestNewMapResolver_MalformedIsAnError(t *testing.T) {
	for _, spec := range []string{
		"11",                  // no '='
		"=webgrip/erfbeeld",   // no scope
		"11=erfbeeld",         // no owner/repo split
		"11=webgrip/",         // empty repo
		"11=/erfbeeld",        // empty owner
		"11=webgrip/x,12=bad", // second entry malformed
	} {
		if _, err := NewMapResolver(spec, ""); err == nil {
			t.Errorf("NewMapResolver(%q) should have failed", spec)
		}
	}
}
