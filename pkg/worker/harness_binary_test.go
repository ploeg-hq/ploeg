package worker

import (
	"errors"
	"strings"
	"testing"
)

func TestCheckHarnessBinaryFindsTheProgramBeforeAClaim(t *testing.T) {
	missing := func(string) (string, error) { return "", errors.New("executable file not found in $PATH") }
	present := func(bin string) (string, error) { return "/usr/bin/" + bin, nil }
	for _, tc := range []struct {
		name string
		hc   HarnessConfig
		bin  string
	}{
		{"openhands default", HarnessConfig{}, "docker-entrypoint.sh"},
		{"openhands override", HarnessConfig{Name: "openhands", Entrypoint: "openhands-runner"}, "openhands-runner"},
		{"claude-code default", HarnessConfig{Name: "claude-code"}, "claude"},
		{"exec", HarnessConfig{Name: "exec", Args: []string{"agent", "{taskfile}"}}, "agent"},
		{"acp opencode", HarnessConfig{Name: "acp"}, "opencode"},
		{"acp entrypoint", HarnessConfig{Name: "acp", Entrypoint: "/opt/opencode"}, "/opt/opencode"},
		{"acp qwen-code", HarnessConfig{Name: "acp", ACP: ACPConfig{Profile: "qwen-code"}}, "qwen"},
		{"acp goose", HarnessConfig{Name: "acp", ACP: ACPConfig{Profile: "goose"}}, "goose"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if bin, err := HarnessBinary(tc.hc); err != nil || bin != tc.bin {
				t.Fatalf("binary=%q err=%v, want %q", bin, err, tc.bin)
			}
			err := CheckHarnessBinary(tc.hc, missing)
			if err == nil || !strings.Contains(err.Error(), tc.bin) {
				t.Fatalf("missing %q not reported: %v", tc.bin, err)
			}
			if err := CheckHarnessBinary(tc.hc, present); err != nil {
				t.Fatalf("present binary rejected: %v", err)
			}
		})
	}
	for _, bin := range []string{"./scripts/agent.sh", "{taskfile}"} {
		if err := CheckHarnessBinary(HarnessConfig{Name: "exec", Args: []string{bin}}, missing); err != nil {
			t.Fatalf("clone-relative program %q checked before the clone exists: %v", bin, err)
		}
	}
}
