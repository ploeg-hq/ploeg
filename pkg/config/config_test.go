package config

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeResolver struct {
	projects map[string]string
	err      error
}

func (f fakeResolver) ProjectsByName(context.Context) (map[string]string, error) {
	return f.projects, f.err
}

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ploeg.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// The whole point: an operator names the board, and the number never appears
// in configuration.
func TestRoutingTable_ResolvesProjectNamesToIDs(t *testing.T) {
	f, err := Load(write(t, `
trackers:
  vikunja:
    projects:
      - name: "Ploeg Test"
        repo: webgrip/ploeg
        branch: development
      - name: "Erfbeeld"
        repo: webgrip/erfbeeld
        branch: main
        forge: webgrip
teams:
  bronze:
    assignees: [jake]
`))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := targetSpec(f, fakeResolver{projects: map[string]string{
		"Ploeg Test": "11", "Erfbeeld": "14", "Something Else": "9",
	}}, discard())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(spec, "11=webgrip/ploeg@development") {
		t.Errorf("spec = %q, want the resolved id for Ploeg Test", spec)
	}
	if !strings.Contains(spec, "14=webgrip/erfbeeld@main;forge=webgrip") {
		t.Errorf("spec = %q, want the resolved id and forge for Erfbeeld", spec)
	}
}

// One tracker project serving several repositories, disambiguated by team, is
// the transitional routing pkg/target exists to express. Validation used to
// reject it as a duplicate — the config was unbootable and the deployment
// crash-looped — so this pins that the whole path works end to end.
func TestRoutingTable_OneProjectRoutesPerTeam(t *testing.T) {
	f, err := Load(write(t, `
trackers:
  vikunja:
    projects:
      - name: "Ploeg Test"
        repo: webgrip/ploeg
        branch: development
        team: bronze
      - name: "Ploeg Test"
        repo: webgrip/erfbeeld
        branch: main
        team: copper
teams:
  bronze:
    assignees: [jake]
  copper:
    assignees: [copper]
`))
	if err != nil {
		t.Fatalf("per-team routing on one project was rejected: %v", err)
	}
	spec, err := targetSpec(f, fakeResolver{projects: map[string]string{
		"Ploeg Test": "11",
	}}, discard())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"11/bronze=webgrip/ploeg@development", "11/copper=webgrip/erfbeeld@main"} {
		if !strings.Contains(spec, want) {
			t.Errorf("spec = %q, want %q", spec, want)
		}
	}
}

// A typo must not route work somewhere plausible — it must refuse to boot,
// and say what the names actually are.
func TestRoutingTable_UnknownProjectNameFailsLoudly(t *testing.T) {
	f, _ := Load(write(t, `
trackers:
  vikunja:
    projects:
      - name: "Ploeg Tset"
        repo: webgrip/ploeg
`))
	_, err := targetSpec(f, fakeResolver{projects: map[string]string{
		"Ploeg Test": "11", "Erfbeeld": "14",
	}}, discard())
	if err == nil {
		t.Fatal("a misspelled project name was accepted")
	}
	for _, want := range []string{"Ploeg Tset", "Ploeg Test", "Erfbeeld"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q, so the operator cannot fix it: %v", want, err)
		}
	}
}

// A pinned id skips resolution — the escape hatch, and it must not require a
// tracker client at all.
func TestRoutingTable_PinnedIDNeedsNoResolver(t *testing.T) {
	f, _ := Load(write(t, `
trackers:
  vikunja:
    projects:
      - id: "11"
        repo: webgrip/ploeg
        branch: development
`))
	spec, err := targetSpec(f, nil, discard())
	if err != nil {
		t.Fatalf("a pinned id required a resolver: %v", err)
	}
	if spec != "11=webgrip/ploeg@development" {
		t.Errorf("spec = %q", spec)
	}
}

// Naming projects without a tracker client is a configuration error the
// operator can act on, not a nil-pointer panic at boot.
func TestRoutingTable_NamesWithoutAResolverIsAClearError(t *testing.T) {
	f, _ := Load(write(t, `
trackers:
  vikunja:
    projects:
      - name: "Ploeg Test"
        repo: webgrip/ploeg
`))
	_, err := targetSpec(f, nil, discard())
	if err == nil || !strings.Contains(err.Error(), "no tracker client") {
		t.Errorf("error = %v, want an explanation of what to configure", err)
	}
}

func TestLoad_RejectsBadConfig(t *testing.T) {
	for name, body := range map[string]string{
		"no repo":            "trackers:\n  vikunja:\n    projects:\n      - name: X\n",
		"repo without owner": "trackers:\n  vikunja:\n    projects:\n      - {name: X, repo: ploeg}\n",
		"no name or id":      "trackers:\n  vikunja:\n    projects:\n      - {repo: webgrip/ploeg}\n",
		"unknown team":       "trackers:\n  vikunja:\n    projects:\n      - {name: X, repo: webgrip/ploeg, team: ghost}\n",
		"duplicate project":  "trackers:\n  vikunja:\n    projects:\n      - {name: X, repo: webgrip/a}\n      - {name: X, repo: webgrip/b}\n",
		// Per-team routing is allowed; the SAME team twice on one project is
		// still ambiguous — pkg/target would silently keep whichever rule
		// sorted first.
		"duplicate project and team": "teams:\n  bronze: {}\ntrackers:\n  vikunja:\n    projects:\n      - {name: X, repo: webgrip/a, team: bronze}\n      - {name: X, repo: webgrip/b, team: bronze}\n",
		// A typo'd key must fail rather than silently defaulting — the whole
		// reason for moving off an env-var DSL.
		"typo'd key": "trackers:\n  vikunja:\n    projects:\n      - {name: X, repo: webgrip/ploeg, brnach: main}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(write(t, body)); err == nil {
				t.Errorf("accepted bad config: %s", body)
			}
		})
	}
}

// A missing file means "no file-backed config", not a crash: the env-var path
// stays usable through the migration.
func TestLoad_MissingFileIsEmptyNotAnError(t *testing.T) {
	f, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil || f == nil {
		t.Fatalf("Load(missing) = %v, %v", f, err)
	}
	if len(f.Teams) != 0 {
		t.Error("missing file produced teams")
	}
	if _, err := Load(""); err != nil {
		t.Errorf("Load(\"\") = %v", err)
	}
}

// The roster replaces PLOEG_TEAM_MAP, lowercased the same way.
func TestAssigneeTeamsAndPlans(t *testing.T) {
	f, err := Load(write(t, `
teams:
  bronze:
    assignees: [Jake, bronze]
    plan:
      pool: "6"
      maxFixRounds: 2
      rounds:
        - roles: [{name: builder, writes: true, cap: "3"}]
        - roles: [{name: reviewer, cap: "1"}]
  copper:
    assignees: [copper]
`))
	if err != nil {
		t.Fatal(err)
	}
	at := f.AssigneeTeams()
	if at["jake"] != "bronze" || at["copper"] != "copper" {
		t.Errorf("assignee teams = %v", at)
	}
	plans := f.Plans()
	if _, ok := plans["copper"]; ok {
		t.Error("a team with no plan got one")
	}
	if p, ok := plans["bronze"]; !ok || p.MaxFixRounds != 2 || len(p.Rounds) != 2 {
		t.Errorf("bronze plan = %+v", p)
	}
}

// A plan that cannot fix anything is refused here too — one validator, not
// two that drift.
func TestLoad_PlanValidationApplies(t *testing.T) {
	_, err := Load(write(t, `
teams:
  bronze:
    plan:
      pool: "6"
      maxFixRounds: 2
      rounds:
        - roles: [{name: reviewer, cap: "1"}]
`))
	if err == nil || !strings.Contains(err.Error(), "no writing round") {
		t.Errorf("error = %v, want the plan validator's message", err)
	}
}

// An assignee on two teams used to load fine and then route by coin flip:
// AssigneeTeams() ranged a Go map, so the same file dispatched the same
// person's tickets to a different team on different boots.
func TestLoad_RejectsAnAssigneeSharedByTwoTeams(t *testing.T) {
	_, err := Load(write(t, `
teams:
  bronze:
    assignees: [bronze, builder]
  silver:
    assignees: [silver, builder]
`))
	if err == nil {
		t.Fatal("an assignee on two teams loaded; routing would be nondeterministic")
	}
	for _, want := range []string{"builder", "bronze", "silver"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %q, so the operator cannot find it: %v", want, err)
		}
	}
}

// Case-folding is what the check compares on, because AssigneeTeams()
// lowercases: "Builder" and "builder" are the same person to the router.
func TestLoad_RejectsASharedAssigneeRegardlessOfCase(t *testing.T) {
	if _, err := Load(write(t, `
teams:
  bronze:
    assignees: [Builder]
  silver:
    assignees: [builder]
`)); err == nil {
		t.Fatal("case-differing duplicate loaded; the router would still collide")
	}
}

// The same name twice inside ONE team is a harmless typo — it resolves to that
// team either way. Rejecting it would be a gratuitous boot failure.
func TestLoad_AllowsARepeatedAssigneeWithinOneTeam(t *testing.T) {
	f, err := Load(write(t, `
teams:
  bronze:
    assignees: [builder, builder]
`))
	if err != nil {
		t.Fatalf("a repeat within one team should load: %v", err)
	}
	if got := f.AssigneeTeams()["builder"]; got != "bronze" {
		t.Errorf("builder routed to %q, want bronze", got)
	}
}

// The executable form of the evidence: 200 projections, one answer.
func TestAssigneeTeams_IsDeterministic(t *testing.T) {
	f := &File{Teams: map[string]Team{
		"bronze": {Assignees: []string{"bronze", "builder"}},
		"silver": {Assignees: []string{"silver", "reviewer"}},
		"copper": {Assignees: []string{"copper"}},
	}}
	first := f.AssigneeTeams()
	for i := 0; i < 200; i++ {
		got := f.AssigneeTeams()
		for k, v := range first {
			if got[k] != v {
				t.Fatalf("iteration %d routed %q to %q, first pass said %q", i, k, got[k], v)
			}
		}
	}
}
