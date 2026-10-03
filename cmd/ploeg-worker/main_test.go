package main

import (
	"strings"
	"testing"
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
