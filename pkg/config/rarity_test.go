package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/rarity"
)

func TestRarityRules_TargetsAndInlineRepos(t *testing.T) {
	f, err := Load(write(t, `
targets:
  app:
    repo: webgrip/App
    rarity:
      attentionPaths: ["services/engine/pkg/store/**", "**/budget*.go"]
  homelab:
    repo: webgrip/homelab-cluster
trackers:
  vikunja:
    projects:
      - name: "Ploeg"
        id: "11"
        repo: webgrip/ploeg
        rarity:
          sensitivePaths: []
          sizeExclude: ["docs/**"]
      - name: "App"
        id: "10"
        default: app
        allow: [homelab]
`))
	if err != nil {
		t.Fatal(err)
	}
	rules, err := f.RarityRules()
	if err != nil {
		t.Fatal(err)
	}
	app := rules["webgrip/app"]
	if app.SensitivePaths != nil || app.SizeExclude != nil || !reflect.DeepEqual(app.AttentionPaths, []string{"services/engine/pkg/store/**", "**/budget*.go"}) {
		t.Errorf("app = %#v; unset lists keep the defaults", app)
	}
	ploeg := rules["webgrip/ploeg"]
	if ploeg.SensitivePaths == nil || len(ploeg.SensitivePaths) != 0 || !reflect.DeepEqual(ploeg.SizeExclude, []string{"docs/**"}) {
		t.Errorf("ploeg = %#v; an empty list is kept as none, not as the defaults", ploeg)
	}
	if _, ok := rules["webgrip/homelab-cluster"]; ok || len(rules) != 2 {
		t.Errorf("rules = %+v; a target without rarity is not listed", rules)
	}
	m, err := ploeg.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if m.Sensitive("migrations/0001.sql") || !m.Excluded("docs/a.md") || m.Excluded("go.sum") {
		t.Error("ploeg's lists replace the defaults")
	}
	if _, err := (rarity.Rules{}).Compile(); err != nil {
		t.Fatal(err)
	}
}

func TestRarityRules_InvalidConfigurationFailsAtLoad(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"bad pattern": {`
targets:
  app:
    repo: webgrip/app
    rarity: {attentionPaths: ["src/[x"]}
`, "targets.app.rarity.attentionPaths"},
		"duplicate pattern": {`
targets:
  app:
    repo: webgrip/app
    rarity: {sizeExclude: ["a", "a"]}
`, "listed twice"},
		"unknown key": {`
targets:
  app:
    repo: webgrip/app
    rarity: {sensitive: ["a"]}
`, "sensitive"},
		"rarity without repo": {`
targets:
  app:
    repo: webgrip/app
trackers:
  vikunja:
    projects:
      - name: "App"
        id: "10"
        default: app
        rarity: {attentionPaths: ["a"]}
`, "rarity requires repo"},
		"two rule sets for one repository": {`
targets:
  app:
    repo: webgrip/app
    rarity: {attentionPaths: ["a"]}
trackers:
  vikunja:
    projects:
      - name: "App"
        id: "10"
        repo: webgrip/app
        rarity: {attentionPaths: ["b"]}
`, "rarity for webgrip/app differs"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(write(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v; want it to mention %q", err, tc.want)
			}
		})
	}
}
