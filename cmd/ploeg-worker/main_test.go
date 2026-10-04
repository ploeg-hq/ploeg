package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/worker"
)

func TestHarnessBoundsDefaultDisableAndRejectTypos(t *testing.T) {
	if d, err := boundEnv("PLOEG_HARNESS_TIMEOUT", defaultHarnessTimeout); err != nil || d != defaultHarnessTimeout {
		t.Fatalf("unset bound=%s err=%v", d, err)
	}
	t.Setenv("PLOEG_HARNESS_TIMEOUT", "0")
	if d, err := boundEnv("PLOEG_HARNESS_TIMEOUT", defaultHarnessTimeout); err != nil || d != 0 {
		t.Fatalf("explicit zero bound=%s err=%v", d, err)
	}
	for _, bad := range []string{"90", "-5m"} {
		t.Setenv("PLOEG_HARNESS_TIMEOUT", bad)
		if _, err := boundEnv("PLOEG_HARNESS_TIMEOUT", defaultHarnessTimeout); err == nil {
			t.Fatalf("bound %q accepted", bad)
		}
	}
}

func TestAdministrativeWorkerEnvironmentFailsClosedWithoutSecretDisclosure(t *testing.T) {
	t.Setenv("LITELLM_MASTER_KEY", "canary-management")
	err := rejectAdministrativeEnvironment()
	if err == nil || strings.Contains(err.Error(), "canary-management") {
		t.Fatal("worker must reject management authority without disclosing it")
	}
}

func TestForgeTokenAccessRejectsTypos(t *testing.T) {
	for _, ok := range []string{"", "read-only", "read-write"} {
		t.Setenv("PLOEG_FORGE_TOKEN_ACCESS", ok)
		if got, err := forgeTokenAccess(); err != nil || got != ok {
			t.Fatalf("%q: got %q err=%v", ok, got, err)
		}
	}
	t.Setenv("PLOEG_FORGE_TOKEN_ACCESS", "readonly")
	if _, err := forgeTokenAccess(); err == nil {
		t.Fatal("a misspelt access level must fail the boot, not read as read-write")
	}
}

func TestTheForgeBotPasswordNeverEntersAWorker(t *testing.T) {
	t.Setenv("PLOEG_FORGEJO_BOT_PASSWORD", "canary-bot-password")
	err := rejectAdministrativeEnvironment()
	if err == nil || strings.Contains(err.Error(), "canary-bot-password") {
		t.Fatalf("worker accepted the minting password or disclosed it: %v", err)
	}
}

func TestCredentialIsolationOffLogsOneWarningNamingHarnessAndReason(t *testing.T) {
	cases := []struct {
		name, reason, llm, forge string
		hc                       worker.HarnessConfig
		wantWarn                 bool
		wantHarness, wantReason  string
	}{
		{name: "both proxies on", llm: "proxy", forge: "proxy", hc: worker.HarnessConfig{Name: "openhands"}},
		{name: "unqualified harness", reason: "unqualified", hc: worker.HarnessConfig{Name: "claude-code"}, wantWarn: true, wantHarness: "claude-code", wantReason: "unqualified"},
		{name: "dind", reason: "dind", hc: worker.HarnessConfig{Name: "openhands"}, wantWarn: true, wantHarness: "openhands", wantReason: "dind"},
		{name: "acp profile named", reason: "unqualified", hc: worker.HarnessConfig{Name: "acp"}, wantWarn: true, wantHarness: "acp/opencode", wantReason: "unqualified"},
		{name: "one proxy off without a chart reason", llm: "proxy", hc: worker.HarnessConfig{Name: "acp", ACP: worker.ACPConfig{Profile: "openhands"}}, wantWarn: true, wantHarness: "acp/openhands", wantReason: "disabled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PLOEG_CREDENTIAL_ISOLATION_OFF_REASON", tc.reason)
			var out bytes.Buffer
			log := slog.New(slog.NewTextHandler(&out, nil))
			warnWhenCredentialIsolationOff(log, worker.Config{LLMKeyIsolation: tc.llm, ForgeTokenIsolation: tc.forge}, tc.hc)
			got := out.String()
			if !tc.wantWarn {
				if got != "" {
					t.Fatalf("logged with both proxies on: %s", got)
				}
				return
			}
			if strings.Count(got, "level=WARN") != 1 || strings.Count(got, "\n") != 1 {
				t.Fatalf("want exactly one WARN line, got %q", got)
			}
			for _, want := range []string{"harness=" + tc.wantHarness, "reason=" + tc.wantReason} {
				if !strings.Contains(got, want) {
					t.Fatalf("%q missing from %q", want, got)
				}
			}
		})
	}
}
