package config

import (
	"strings"
	"testing"
)

func TestReleaseEnvironments_TargetsAndInlineRepos(t *testing.T) {
	f, err := Load(write(t, `
targets:
  app:
    repo: webgrip/App
    release:
      environment: live
  homelab:
    repo: webgrip/homelab-cluster
trackers:
  vikunja:
    projects:
      - name: "Ploeg"
        id: "11"
        repo: webgrip/ploeg
        release: {environment: acceptance}
      - name: "App"
        id: "10"
        default: app
        allow: [homelab]
      - name: "App again"
        id: "12"
        repo: webgrip/app
        release: {environment: live}
`))
	if err != nil {
		t.Fatal(err)
	}
	environments, err := f.ReleaseEnvironments()
	if err != nil {
		t.Fatal(err)
	}
	if environments["webgrip/app"] != "live" || environments["webgrip/ploeg"] != "acceptance" || len(environments) != 2 {
		t.Errorf("environments = %+v; a target without release is not listed", environments)
	}
}

func TestReleaseEnvironments_InvalidConfigurationFailsAtLoad(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"uppercase": {`
targets:
  app:
    repo: webgrip/app
    release: {environment: Production}
`, "targets.app.release: environment"},
		"empty": {`
targets:
  app:
    repo: webgrip/app
    release: {}
`, "targets.app.release: environment"},
		"unknown key": {`
targets:
  app:
    repo: webgrip/app
    release: {env: production}
`, "env"},
		"release without repo": {`
targets:
  app:
    repo: webgrip/app
trackers:
  vikunja:
    projects:
      - name: "App"
        default: app
        release: {environment: production}
`, "release requires repo"},
		"conflicting environments": {`
targets:
  app:
    repo: webgrip/app
    release: {environment: production}
trackers:
  vikunja:
    projects:
      - name: "App"
        repo: WebGrip/App
        release: {environment: live}
`, "differs"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(write(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v; want it to mention %q", err, tc.want)
			}
		})
	}
}
