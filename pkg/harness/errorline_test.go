package harness

import (
	"strings"
	"testing"
)

func TestErrorLinePicksTheHarnessErrorAndDropsTheLabel(t *testing.T) {
	cases := map[string]string{
		"last agent message: Error: 6 validation errors for MCPConfig mcpServers.litellm | {\"code\":-32603}": "Error: 6 validation errors for MCPConfig mcpServers.litellm",
		"starting\nTypeError: observe() got an unexpected keyword argument 'rollout_entrypoint'\nexiting":     "TypeError: observe() got an unexpected keyword argument 'rollout_entrypoint'",
		"stderr: segmentation fault": "segmentation fault",
		"first\nsecond\n":            "second",
		"":                           "",
	}
	for in, want := range cases {
		if got := ErrorLine(in); got != want {
			t.Errorf("ErrorLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestErrorLineIsBounded(t *testing.T) {
	if got := ErrorLine("Error: " + strings.Repeat("x", 2000)); len([]rune(got)) > maxErrorLine+1 {
		t.Errorf("ErrorLine returned %d runes, want at most %d", len([]rune(got)), maxErrorLine+1)
	}
}
