package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/ploeg-hq/ploeg/pkg/runidentity"
)

const perRolePluginValues = `
config:
  teams:
    silver:
      sponsor: oidc-0123456789abcdef0123456789abcdef
    bronze:
      sponsor: oidc-fedcba9876543210fedcba9876543210
executor:
  teams:
    - name: silver
      model: litellm_proxy/claude-sonnet-5
      budget: "6"
      maxReplicaCount: 1
      harness:
        name: claude-code
        dind: false
        claudeCode:
          plugins:
            - name: session-link
              marketplace:
                url: https://github.com/example/claude-plugins.git
                commit: 0123456789abcdef0123456789abcdef01234567
              runIdentity:
                audience: https://link.example.com
      plan:
        - roles:
            - {name: builder, writes: true, cap: "3.00"}
        - roles:
            - name: reviewer
              writes: false
              cap: "0.75"
              harness:
                dind: false
                claudeCode:
                  plugins:
                    - name: review-link
                      marketplace:
                        url: https://github.com/example/claude-plugins.git
                        commit: 0123456789abcdef0123456789abcdef01234567
                      runIdentity:
                        audience: urn:example:review
                    - name: quiet
                      marketplace:
                        url: https://github.com/example/claude-plugins.git
                        commit: 0123456789abcdef0123456789abcdef01234567
    - name: bronze
      model: litellm_proxy/claude-sonnet-5
      budget: "2"
      maxReplicaCount: 1
      harness:
        name: claude-code
        dind: false
        claudeCode:
          plugins:
            - name: bronze-link
              marketplace:
                url: https://github.com/example/claude-plugins.git
                commit: 0123456789abcdef0123456789abcdef01234567
              runIdentity:
                audience: urn:example:bronze
      plan:
        - roles:
            - {name: builder, writes: true, cap: "1.00"}
        - roles:
            - {name: tests, writes: false, cap: "0.50", harness: {name: openhands, dind: false}}
`

type renderedWorker struct {
	team, role string
	audiences  []string
}

func renderRunIdentityChart(t *testing.T) (string, []renderedWorker) {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("Helm is required for chart render qualification")
	}
	chart := filepath.Join("..", "..", "ops", "helm", "ploeg")
	extra := filepath.Join(t.TempDir(), "per-role-plugins.yaml")
	if err := os.WriteFile(extra, []byte(perRolePluginValues), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(helm, "template", "ploeg", chart,
		"-f", filepath.Join(chart, "ci", "required-values.yaml"),
		"-f", filepath.Join(chart, "ci", "executor-github-values.yaml"),
		"-f", extra).CombinedOutput()
	if err != nil {
		t.Fatalf("render: %v\n%s", err, output)
	}
	var allowlist string
	var workers []renderedWorker
	decoder := yaml.NewDecoder(bytes.NewReader(output))
	for {
		var doc map[string]any
		err := decoder.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch doc["kind"] {
		case "Deployment":
			if env := containerEnv(t, dig(doc, "spec", "template", "spec"))["PLOEG_RUN_IDENTITY_AUDIENCES"]; env != "" {
				allowlist = env
			}
		case "ScaledJob":
			env := containerEnv(t, dig(doc, "spec", "jobTargetRef", "template", "spec"))
			var plugins []struct {
				RunIdentity *struct {
					Audience string `json:"audience"`
				} `json:"runIdentity"`
			}
			if raw := env["PLOEG_CLAUDE_PLUGINS"]; raw != "" {
				if err := json.Unmarshal([]byte(raw), &plugins); err != nil {
					t.Fatal(err)
				}
			}
			w := renderedWorker{team: env["PLOEG_TEAM"], role: env["PLOEG_ROLE"]}
			for _, p := range plugins {
				if p.RunIdentity != nil {
					w.audiences = append(w.audiences, p.RunIdentity.Audience)
				}
			}
			workers = append(workers, w)
		}
	}
	return allowlist, workers
}

func dig(doc map[string]any, path ...string) map[string]any {
	for _, key := range path {
		next, _ := doc[key].(map[string]any)
		doc = next
	}
	return doc
}

func containerEnv(t *testing.T, podSpec map[string]any) map[string]string {
	t.Helper()
	env := map[string]string{}
	containers, _ := podSpec["containers"].([]any)
	if len(containers) == 0 {
		t.Fatal("pod spec without containers")
	}
	for _, entry := range containers[0].(map[string]any)["env"].([]any) {
		item := entry.(map[string]any)
		if value, ok := item["value"].(string); ok {
			env[item["name"].(string)] = value
		}
	}
	return env
}

func TestChartAllowsRunIdentitiesOnlyForTheAudiencesEachRolesPluginsName(t *testing.T) {
	raw, workers := renderRunIdentityChart(t)
	if raw == "" {
		t.Fatal("ploegd renders no PLOEG_RUN_IDENTITY_AUDIENCES with runIdentity.signingKeySecret set")
	}
	allowlist, err := runidentity.ParseAudiences(raw)
	if err != nil {
		t.Fatalf("ploegd would refuse to start on the rendered allowlist %s: %v", raw, err)
	}
	want := runidentity.Audiences{
		"silver": {"builder": {"https://link.example.com"}, "reviewer": {"urn:example:review"}},
		"bronze": {"builder": {"urn:example:bronze"}},
	}
	if !reflect.DeepEqual(allowlist, want) {
		t.Errorf("allowlist = %v, want %v", allowlist, want)
	}
	if len(workers) != 4 {
		t.Fatalf("rendered %d worker ScaledJobs, want 4: %+v", len(workers), workers)
	}
	for _, w := range workers {
		listed := append([]string(nil), allowlist[w.team][w.role]...)
		declared := append([]string(nil), w.audiences...)
		sort.Strings(listed)
		sort.Strings(declared)
		if len(listed) == 0 && len(declared) == 0 {
			continue
		}
		if !reflect.DeepEqual(listed, declared) {
			t.Errorf("team %s role %s: ploegd allows %v, its plugins ask for %v", w.team, w.role, listed, declared)
		}
	}
}
