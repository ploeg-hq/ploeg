package main

import "testing"

// The forge id feeds the registry key, the resolver's default and the
// Engine's DefaultForge. When those were three reads, one of them defaulted
// to "" and every review ever written was silently discarded — the registry
// was keyed "forgejo", every Work Target carried "", and publishRound looked
// up the empty string as a literal key.
//
// cmd/ had no tests at all, which is why nothing noticed: the value's
// producer and its consumer were never in the same test process.
func TestForgeIDFromEnv_NeverEmpty(t *testing.T) {
	t.Setenv("PLOEG_TARGET_FORGE", "")
	if got := forgeIDFromEnv(); got == "" {
		t.Fatal("forge id defaulted to the empty string; publishRound cannot look that up")
	} else if got != "forgejo" {
		t.Errorf("forge id = %q, want forgejo", got)
	}
}

func TestForgeIDFromEnv_HonoursAnExplicitID(t *testing.T) {
	t.Setenv("PLOEG_TARGET_FORGE", "webgrip-forgejo")
	if got := forgeIDFromEnv(); got != "webgrip-forgejo" {
		t.Errorf("forge id = %q, want webgrip-forgejo", got)
	}
}

// The usage report defaults on when unset; PLOEG_USAGE_REPORT=false is the
// only value that silences it, matching PLOEG_SHIFTS_UNIFORM's convention.
func TestEngineUsageReportFromEnv(t *testing.T) {
	t.Setenv("PLOEG_USAGE_REPORT", "")
	if !usageReportFromEnv() {
		t.Error("the usage report defaulted off; the outcome is every agent PR")
	}
	t.Setenv("PLOEG_USAGE_REPORT", "true")
	if !usageReportFromEnv() {
		t.Error("PLOEG_USAGE_REPORT=true disabled the report")
	}
	t.Setenv("PLOEG_USAGE_REPORT", "false")
	if usageReportFromEnv() {
		t.Error("PLOEG_USAGE_REPORT=false did not disable the report")
	}
}
