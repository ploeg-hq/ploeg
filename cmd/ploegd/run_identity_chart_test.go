package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

func TestChartRefusesRunIdentityPluginsUntilClaudeCodeRunsWithIsolatedCredentials(t *testing.T) {
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
	if err == nil || !strings.Contains(string(output), "credential_isolation_required") || !strings.Contains(string(output), "harness claude-code is not qualified") {
		t.Fatalf("render = %v, want a refusal naming the unqualified claude-code harness the plugins need (ADR-0034, amended 2026-11-18):\n%s", err, output)
	}
}
