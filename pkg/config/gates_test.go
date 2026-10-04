package config

import (
	"context"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/gate"
)

func TestGateBoards_ResolvesNamedAndPinnedProjects(t *testing.T) {
	f, err := Load(write(t, `
trackers:
  vikunja:
    projects:
      - name: "App"
        repo: webgrip/app
        gates:
          development: ["Doing"]
          test: ["In test"]
          acceptance: ["Acceptance", "UAT"]
          done: ["Done"]
      - name: "Ploeg"
        id: "11"
        repo: webgrip/ploeg
  clickup:
    projects:
      - id: "901"
        repo: webgrip/site
        gates:
          test: ["qa"]
          done: ["complete"]
`))
	if err != nil {
		t.Fatal(err)
	}
	boards, err := f.GateBoards(context.Background(), fakeResolver{projects: map[string]string{"App": "10", "Ploeg": "11"}}, discard())
	if err != nil {
		t.Fatal(err)
	}
	app, ok := boards.Lookup("vikunja", "10")
	if !ok {
		t.Fatalf("boards = %+v; the named project must resolve to its id", boards)
	}
	if g, _, ok := app.Resolve([]string{"uat"}); !ok || g != gate.Acceptance {
		t.Errorf("UAT resolves to %q, %v", g, ok)
	}
	if _, ok := boards.Lookup("vikunja", "11"); ok {
		t.Error("a project without gates has a gate map")
	}
	site, ok := boards.Lookup("clickup", "901")
	if g, _, found := site.Resolve([]string{"complete"}); !ok || !found || g != gate.Done {
		t.Errorf("clickup complete resolves to %q", g)
	}
	if boards.Has("forgejo") || !boards.Has("clickup") {
		t.Errorf("Has: %+v", boards)
	}
}

func TestGateBoards_UnknownNameFailsAndPinnedIDsNeedNoResolver(t *testing.T) {
	f, err := Load(write(t, `
trackers:
  vikunja:
    projects:
      - name: "App"
        repo: webgrip/app
        gates:
          test: ["In test"]
`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.GateBoards(context.Background(), fakeResolver{projects: map[string]string{"Other": "3"}}, discard()); err == nil || !strings.Contains(err.Error(), `no tracker project named "App"`) {
		t.Fatalf("err = %v", err)
	}
	if _, err := f.GateBoards(context.Background(), nil, discard()); err == nil {
		t.Fatal("a named project resolved without a tracker client")
	}
	pinned, err := Load(write(t, `
trackers:
  vikunja:
    projects:
      - id: "10"
        repo: webgrip/app
        gates:
          test: ["In test"]
`))
	if err != nil {
		t.Fatal(err)
	}
	boards, err := pinned.GateBoards(context.Background(), nil, discard())
	if _, ok := boards.Lookup("vikunja", "10"); err != nil || !ok {
		t.Fatalf("pinned board: %v %+v", err, boards)
	}
}

func TestGates_InvalidConfigurationFailsAtLoad(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"unknown gate": {`
trackers:
  vikunja:
    projects:
      - id: "10"
        repo: webgrip/app
        gates:
          review: ["In review"]
`, "field review not found"},
		"status in two gates": {`
trackers:
  vikunja:
    projects:
      - id: "10"
        repo: webgrip/app
        gates:
          test: ["Review"]
          acceptance: ["review"]
`, "already mapped to test"},
		"empty gates": {`
trackers:
  vikunja:
    projects:
      - id: "10"
        repo: webgrip/app
        gates: {}
`, "maps no status"},
		"padded status": {`
trackers:
  vikunja:
    projects:
      - id: "10"
        repo: webgrip/app
        gates:
          done: ["Done "]
`, "surrounding space"},
		"one board mapped twice differently": {`
teams:
  silver: {}
  gold: {}
trackers:
  vikunja:
    projects:
      - id: "10"
        repo: webgrip/app
        team: silver
        gates:
          test: ["In test"]
      - id: "10"
        repo: webgrip/app
        team: gold
        gates:
          test: ["QA"]
`, "maps its statuses differently"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(write(t, tc.body)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v; want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestGates_TheSameMappingOnBothTeamsOfABoardIsAllowed(t *testing.T) {
	if _, err := Load(write(t, `
teams:
  silver: {}
  gold: {}
trackers:
  vikunja:
    projects:
      - id: "10"
        repo: webgrip/app
        team: silver
        gates:
          test: ["In test"]
      - id: "10"
        repo: webgrip/app
        team: gold
        gates:
          test: ["In test"]
`)); err != nil {
		t.Fatal(err)
	}
}
