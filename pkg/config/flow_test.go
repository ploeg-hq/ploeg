package config

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/flow"
	"github.com/ploeg-hq/ploeg/pkg/gate"
)

func TestFlowBoards_RecordEveryBoardWithGatesOrStatusKinds(t *testing.T) {
	f, err := Load(write(t, `
trackers:
  vikunja:
    projects:
      - name: "Unfold"
        repo: webgrip/glide
        gates:
          development: ["Doing"]
          acceptance: ["UAT"]
        statusKinds:
          active: ["UAT", "Refinement"]
          blocked: ["Parked"]
      - name: "Ploeg"
        id: "11"
        repo: webgrip/ploeg
        gates:
          test: ["In test"]
      - id: "12"
        repo: webgrip/site
        statusKinds: {}
      - id: "13"
        repo: webgrip/docs
  clickup:
    projects:
      - id: "901"
        repo: webgrip/site
        statusKinds:
          waiting: ["in test"]
`))
	if err != nil {
		t.Fatal(err)
	}
	boards, err := f.FlowBoards(context.Background(), fakeResolver{projects: map[string]string{"Unfold": "10"}}, discard())
	if err != nil {
		t.Fatal(err)
	}
	unfold, ok := boards.Lookup("vikunja", "10")
	if !ok || unfold.Kind("uat", gate.Acceptance) != flow.Active || unfold.Kind("Parked", "") != flow.Blocked ||
		unfold.Kind("Doing", gate.Development) != flow.Active {
		t.Fatalf("Unfold's kinds: %v %+v", ok, unfold)
	}
	if ploeg, ok := boards.Lookup("vikunja", "11"); !ok || ploeg.Configured("In test") || ploeg.Kind("In test", gate.Test) != flow.Active {
		t.Fatalf("a gated board without statusKinds records statuses with the defaults: %v", ok)
	}
	if _, ok := boards.Lookup("vikunja", "12"); !ok {
		t.Fatal("statusKinds: {} records the board's statuses with the defaults")
	}
	if _, ok := boards.Lookup("vikunja", "13"); ok {
		t.Fatal("a board with neither gates nor statusKinds records no statuses")
	}
	if site, ok := boards.Lookup("clickup", "901"); !ok || site.Kind("in test", gate.Test) != flow.Waiting {
		t.Fatalf("clickup kinds: %v", ok)
	}
	if _, err := f.FlowBoards(context.Background(), nil, discard()); err == nil {
		t.Fatal("a named project resolved without a tracker client")
	}
}

func TestStatusKindsAndWorkingHours_InvalidConfigurationFailsAtLoad(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"unknown kind": {`
trackers:
  vikunja:
    projects:
      - id: "10"
        repo: webgrip/glide
        statusKinds:
          paused: ["Parked"]
`, "field paused not found"},
		"status under two kinds": {`
trackers:
  vikunja:
    projects:
      - id: "10"
        repo: webgrip/glide
        statusKinds:
          active: ["UAT"]
          waiting: ["uat"]
`, "already active"},
		"padded status": {`
trackers:
  vikunja:
    projects:
      - id: "10"
        repo: webgrip/glide
        statusKinds:
          blocked: [" Parked"]
`, "surrounding space"},
		"one board, two kind lists": {`
trackers:
  vikunja:
    projects:
      - id: "10"
        repo: webgrip/glide
        team: a
        statusKinds:
          active: ["UAT"]
      - id: "10"
        repo: webgrip/glide
        team: b
        statusKinds:
          waiting: ["UAT"]
teams:
  a: {}
  b: {}
`, "sets its status kinds differently"},
		"unknown timezone": {`
teams:
  silver:
    workingHours:
      timezone: Europe/Atlantis
`, "teams.silver.workingHours: timezone"},
		"unknown day": {`
teams:
  silver:
    workingHours:
      days: [monday]
`, "teams.silver.workingHours: days"},
		"hours backwards": {`
teams:
  silver:
    workingHours:
      start: "17:00"
      end: "09:00"
`, "must come before end"},
		"bad holiday": {`
teams:
  silver:
    workingHours:
      holidays: ["25 December"]
`, "holidays"},
		"unknown field": {`
teams:
  silver:
    workingHours:
      lunch: "12:00"
`, "field lunch not found"},
	} {
		if _, err := Load(write(t, tc.body)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

func TestWorkingCalendars_FillTheDefaultsPerTeam(t *testing.T) {
	f, err := Load(write(t, `
teams:
  silver:
    workingHours:
      timezone: Europe/London
      days: [mon, tue, wed, thu]
      holidays: ["2026-12-25"]
  gold:
    workingHours:
      start: "08:30"
      end: "16:30"
  bronze: {}
`))
	if err != nil {
		t.Fatal(err)
	}
	calendars, err := f.WorkingCalendars()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := calendars["bronze"]; ok || len(calendars) != 2 {
		t.Fatalf("calendars = %v; a team without workingHours uses the default", calendars)
	}
	silver := calendars["silver"].Info()
	if silver.Timezone != "Europe/London" || strings.Join(silver.Days, ",") != "mon,tue,wed,thu" || silver.Start != "09:00" ||
		silver.End != "17:00" || silver.Holidays != 1 {
		t.Fatalf("silver = %+v", silver)
	}
	gold := calendars["gold"]
	monday := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	if info := gold.Info(); info.Timezone != "Europe/Amsterdam" || info.Start != "08:30" ||
		gold.WorkingSeconds(monday, monday.Add(24*time.Hour)) != 8*3600 {
		t.Fatalf("gold = %+v", info)
	}
}
