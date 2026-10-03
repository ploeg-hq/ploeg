package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestCardShapeRules_TargetsAndInlineRepos(t *testing.T) {
	f, err := Load(write(t, `
targets:
  glide:
    repo: webgrip/Glide
    cardShape:
      testPaths: ["e2e/**", "**/*_test.go"]
  homelab:
    repo: webgrip/homelab-cluster
trackers:
  vikunja:
    projects:
      - name: "Ploeg"
        id: "11"
        repo: webgrip/ploeg
        cardShape:
          docPaths: []
      - name: "Glide"
        id: "10"
        default: glide
        allow: [homelab]
`))
	if err != nil {
		t.Fatal(err)
	}
	rules, err := f.CardShapeRules()
	if err != nil {
		t.Fatal(err)
	}
	glide := rules["webgrip/glide"]
	if glide.DocPaths != nil || !reflect.DeepEqual(glide.TestPaths, []string{"e2e/**", "**/*_test.go"}) {
		t.Errorf("glide = %#v; an unset list keeps the defaults", glide)
	}
	ploeg := rules["webgrip/ploeg"]
	if ploeg.TestPaths != nil || ploeg.DocPaths == nil || len(ploeg.DocPaths) != 0 {
		t.Errorf("ploeg = %#v; an empty list is kept as none", ploeg)
	}
	if _, ok := rules["webgrip/homelab-cluster"]; ok || len(rules) != 2 {
		t.Errorf("rules = %+v; a target without cardShape is not listed", rules)
	}
	m, err := glide.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if !m.Test("e2e/login.spec.js") || m.Test("src/a.test.ts") || !m.Doc("docs/a.md") {
		t.Error("glide's test paths replace the defaults and its doc paths stay the defaults")
	}
}

func TestCardShapeRules_InvalidConfigurationFailsAtLoad(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"bad pattern": {`
targets:
  glide:
    repo: webgrip/glide
    cardShape: {testPaths: ["src/[x"]}
`, "targets.glide.cardShape.testPaths"},
		"duplicate pattern": {`
targets:
  glide:
    repo: webgrip/glide
    cardShape: {docPaths: ["a", "a"]}
`, "listed twice"},
		"unknown key": {`
targets:
  glide:
    repo: webgrip/glide
    cardShape: {tests: ["a"]}
`, "tests"},
		"cardShape without repo": {`
targets:
  glide:
    repo: webgrip/glide
trackers:
  vikunja:
    projects:
      - name: "Glide"
        id: "10"
        default: glide
        cardShape: {testPaths: ["a"]}
`, "cardShape requires repo"},
		"two rule sets for one repository": {`
targets:
  glide:
    repo: webgrip/glide
    cardShape: {testPaths: ["a"]}
trackers:
  vikunja:
    projects:
      - name: "Glide"
        id: "10"
        repo: webgrip/glide
        cardShape: {testPaths: ["b"]}
`, "cardShape for webgrip/glide differs"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(write(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v; want it to mention %q", err, tc.want)
			}
		})
	}
}
