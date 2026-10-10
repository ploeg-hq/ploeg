package config

import (
	"reflect"
	"testing"
)

func TestRetiredRunCardKeysAreAcceptedAndListed(t *testing.T) {
	f, err := Load(write(t, `
cards:
  enabled: false
targets:
  app:
    repo: webgrip/app
    cardStyle: {skin: default, theme: acme}
    release: {environment: live}
    rarity: {sensitivePaths: ["**/auth/**"]}
    cardShape: {testPaths: ["**/*_test.go"]}
trackers:
  vikunja:
    projects:
      - name: "Ploeg"
        id: "11"
        repo: webgrip/ploeg
        cardStyle: {skin: default}
        release: {environment: acceptance}
        rarity: {sizeExclude: []}
        cardShape: {docPaths: []}
        statusKinds: {blocked: ["Parked"]}
        gates: {development: ["Doing"]}
teams:
  silver:
    cards: {prComment: true, referees: [anna]}
    workingHours: {timezone: UTC}
`))
	if err != nil {
		t.Fatalf("a configuration with retired keys must still boot: %v", err)
	}
	want := []string{
		"cards",
		"targets.app.cardStyle", "targets.app.release", "targets.app.rarity", "targets.app.cardShape",
		"trackers.vikunja.projects[0].cardStyle", "trackers.vikunja.projects[0].release", "trackers.vikunja.projects[0].rarity",
		"trackers.vikunja.projects[0].cardShape", "trackers.vikunja.projects[0].statusKinds",
		"teams.silver.cards", "teams.silver.workingHours",
	}
	if got := f.RetiredKeys(); !reflect.DeepEqual(got, want) {
		t.Fatalf("retired keys =\n%v\nwant\n%v", got, want)
	}
}

func TestRetiredKeysAreNoneForACurrentConfiguration(t *testing.T) {
	f, err := Load(write(t, `
trackers:
  vikunja:
    projects:
      - name: "Ploeg"
        id: "11"
        repo: webgrip/ploeg
        gates: {development: ["Doing"]}
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.RetiredKeys(); len(got) != 0 {
		t.Fatalf("retired keys = %v", got)
	}
	if got := (&File{}).RetiredKeys(); len(got) != 0 {
		t.Fatalf("an empty file lists %v", got)
	}
}

func TestAnUnknownKeyStillFailsAtLoad(t *testing.T) {
	if _, err := Load(write(t, "teams:\n  silver:\n    cardz: {}\n")); err == nil {
		t.Fatal("a typo'd key must still be a boot failure")
	}
}
