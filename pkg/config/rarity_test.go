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
  glide:
    repo: webgrip/Glide
    rarity:
      attentionPaths: ["apps/ploeg/pkg/store/**", "**/budget*.go"]
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
      - name: "Glide"
        id: "10"
        default: glide
        allow: [homelab]
`))
	if err != nil {
		t.Fatal(err)
	}
	rules, err := f.RarityRules()
	if err != nil {
		t.Fatal(err)
	}
	glide := rules["webgrip/glide"]
	if glide.SensitivePaths != nil || glide.SizeExclude != nil || !reflect.DeepEqual(glide.AttentionPaths, []string{"apps/ploeg/pkg/store/**", "**/budget*.go"}) {
		t.Errorf("glide = %#v; unset lists keep the defaults", glide)
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
  glide:
    repo: webgrip/glide
    rarity: {attentionPaths: ["src/[x"]}
`, "targets.glide.rarity.attentionPaths"},
		"duplicate pattern": {`
targets:
  glide:
    repo: webgrip/glide
    rarity: {sizeExclude: ["a", "a"]}
`, "listed twice"},
		"unknown key": {`
targets:
  glide:
    repo: webgrip/glide
    rarity: {sensitive: ["a"]}
`, "sensitive"},
		"rarity without repo": {`
targets:
  glide:
    repo: webgrip/glide
trackers:
  vikunja:
    projects:
      - name: "Glide"
        id: "10"
        default: glide
        rarity: {attentionPaths: ["a"]}
`, "rarity requires repo"},
		"two rule sets for one repository": {`
targets:
  glide:
    repo: webgrip/glide
    rarity: {attentionPaths: ["a"]}
trackers:
  vikunja:
    projects:
      - name: "Glide"
        id: "10"
        repo: webgrip/glide
        rarity: {attentionPaths: ["b"]}
`, "rarity for webgrip/glide differs"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(write(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v; want it to mention %q", err, tc.want)
			}
		})
	}
}
