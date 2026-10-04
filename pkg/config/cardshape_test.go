package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestCardShapeRules_TargetsAndInlineRepos(t *testing.T) {
	f, err := Load(write(t, `
targets:
  app:
    repo: webgrip/App
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
      - name: "App"
        id: "10"
        default: app
        allow: [homelab]
`))
	if err != nil {
		t.Fatal(err)
	}
	rules, err := f.CardShapeRules()
	if err != nil {
		t.Fatal(err)
	}
	app := rules["webgrip/app"]
	if app.DocPaths != nil || !reflect.DeepEqual(app.TestPaths, []string{"e2e/**", "**/*_test.go"}) {
		t.Errorf("app = %#v; an unset list keeps the defaults", app)
	}
	ploeg := rules["webgrip/ploeg"]
	if ploeg.TestPaths != nil || ploeg.DocPaths == nil || len(ploeg.DocPaths) != 0 {
		t.Errorf("ploeg = %#v; an empty list is kept as none", ploeg)
	}
	if _, ok := rules["webgrip/homelab-cluster"]; ok || len(rules) != 2 {
		t.Errorf("rules = %+v; a target without cardShape is not listed", rules)
	}
	m, err := app.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if !m.Test("e2e/login.spec.js") || m.Test("src/a.test.ts") || !m.Doc("docs/a.md") {
		t.Error("app's test paths replace the defaults and its doc paths stay the defaults")
	}
}

func TestCardShapeRules_InvalidConfigurationFailsAtLoad(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"bad pattern": {`
targets:
  app:
    repo: webgrip/app
    cardShape: {testPaths: ["src/[x"]}
`, "targets.app.cardShape.testPaths"},
		"duplicate pattern": {`
targets:
  app:
    repo: webgrip/app
    cardShape: {docPaths: ["a", "a"]}
`, "listed twice"},
		"unknown key": {`
targets:
  app:
    repo: webgrip/app
    cardShape: {tests: ["a"]}
`, "tests"},
		"cardShape without repo": {`
targets:
  app:
    repo: webgrip/app
trackers:
  vikunja:
    projects:
      - name: "App"
        id: "10"
        default: app
        cardShape: {testPaths: ["a"]}
`, "cardShape requires repo"},
		"two rule sets for one repository": {`
targets:
  app:
    repo: webgrip/app
    cardShape: {testPaths: ["a"]}
trackers:
  vikunja:
    projects:
      - name: "App"
        id: "10"
        repo: webgrip/app
        cardShape: {testPaths: ["b"]}
`, "cardShape for webgrip/app differs"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(write(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v; want it to mention %q", err, tc.want)
			}
		})
	}
}
