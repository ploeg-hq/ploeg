package config

import (
	"strings"
	"testing"
)

func TestReleaseEnvironments_TargetsAndInlineRepos(t *testing.T) {
	f, err := Load(write(t, `
targets:
  glide:
    repo: webgrip/Glide
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
      - name: "Glide"
        id: "10"
        default: glide
        allow: [homelab]
      - name: "Glide again"
        id: "12"
        repo: webgrip/glide
        release: {environment: live}
`))
	if err != nil {
		t.Fatal(err)
	}
	environments, err := f.ReleaseEnvironments()
	if err != nil {
		t.Fatal(err)
	}
	if environments["webgrip/glide"] != "live" || environments["webgrip/ploeg"] != "acceptance" || len(environments) != 2 {
		t.Errorf("environments = %+v; a target without release is not listed", environments)
	}
}

func TestReleaseEnvironments_InvalidConfigurationFailsAtLoad(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"uppercase": {`
targets:
  glide:
    repo: webgrip/glide
    release: {environment: Production}
`, "targets.glide.release: environment"},
		"empty": {`
targets:
  glide:
    repo: webgrip/glide
    release: {}
`, "targets.glide.release: environment"},
		"unknown key": {`
targets:
  glide:
    repo: webgrip/glide
    release: {env: production}
`, "env"},
		"release without repo": {`
targets:
  glide:
    repo: webgrip/glide
trackers:
  vikunja:
    projects:
      - name: "Glide"
        default: glide
        release: {environment: production}
`, "release requires repo"},
		"conflicting environments": {`
targets:
  glide:
    repo: webgrip/glide
    release: {environment: production}
trackers:
  vikunja:
    projects:
      - name: "Glide"
        repo: WebGrip/Glide
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
