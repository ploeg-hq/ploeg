package worker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Toolchain is a language toolchain the pod mounted for its Runs
// (PLOEG_TOOLCHAINS). The kubelet pulls the image that holds it, so the Run's
// sandbox never needs registry egress.
type Toolchain struct {
	Name string `json:"name"`
	// Path lists absolute directories to put on the harness's PATH, in order.
	Path []string `json:"path"`
	// Env is set in the harness environment, for example GOTOOLCHAIN=local.
	Env map[string]string `json:"env,omitempty"`
}

var reservedToolchainEnv = map[string]bool{
	"PATH": true, "HOME": true, "TMPDIR": true,
	"XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true, "XDG_CACHE_HOME": true,
	"AGENT_BUILDER_TOKEN": true, "LLM_API_KEY": true, "LLM_BASE_URL": true, "LLM_MODEL": true, "LLM_TRACE_ID": true,
	"PLOEG_OUTCOME_FILE": true, "PLOEG_SKILLS_DIR": true, "PLOEG_VERIFY_SCRIPT": true,
	canaryEnv: true,
}

// ParseToolchains decodes PLOEG_TOOLCHAINS. Empty input means no toolchains.
func ParseToolchains(raw string) ([]Toolchain, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var tcs []Toolchain
	if err := json.Unmarshal([]byte(raw), &tcs); err != nil {
		return nil, fmt.Errorf("PLOEG_TOOLCHAINS: %w", err)
	}
	seen := map[string]bool{}
	for i, tc := range tcs {
		if tc.Name == "" {
			return nil, fmt.Errorf("PLOEG_TOOLCHAINS[%d]: name is required", i)
		}
		if seen[tc.Name] {
			return nil, fmt.Errorf("PLOEG_TOOLCHAINS: toolchain %q is listed twice", tc.Name)
		}
		seen[tc.Name] = true
		if len(tc.Path) == 0 {
			return nil, fmt.Errorf("PLOEG_TOOLCHAINS[%s]: path must name at least one directory", tc.Name)
		}
		for _, p := range tc.Path {
			if !filepath.IsAbs(p) || strings.Contains(p, string(os.PathListSeparator)) {
				return nil, fmt.Errorf("PLOEG_TOOLCHAINS[%s]: path %q must be absolute and hold one directory", tc.Name, p)
			}
		}
		for k := range tc.Env {
			if reservedToolchainEnv[k] || strings.ContainsAny(k, "= ") || k == "" {
				return nil, fmt.Errorf("PLOEG_TOOLCHAINS[%s]: env %q is reserved or malformed", tc.Name, k)
			}
		}
	}
	return tcs, nil
}

// CheckToolchains fails when a toolchain's directory is missing, so a pod
// whose image volume did not mount refuses to claim instead of running an
// agent that cannot run its checks.
func CheckToolchains(tcs []Toolchain) error {
	for _, tc := range tcs {
		for _, p := range tc.Path {
			info, err := os.Stat(p)
			if err != nil {
				return fmt.Errorf("toolchain %q: %w", tc.Name, err)
			}
			if !info.IsDir() {
				return fmt.Errorf("toolchain %q: %s is not a directory", tc.Name, p)
			}
		}
	}
	return nil
}

// withToolchains puts every toolchain's directories in front of PATH and adds
// its environment. A later toolchain never overrides an earlier one's value.
func withToolchains(env []string, tcs []Toolchain) []string {
	if len(tcs) == 0 {
		return env
	}
	var dirs []string
	extra := map[string]string{}
	for _, tc := range tcs {
		dirs = append(dirs, tc.Path...)
		for k, v := range tc.Env {
			if _, set := extra[k]; !set {
				extra[k] = v
			}
		}
	}
	path := strings.Join(dirs, string(os.PathListSeparator))
	if current, ok := lookupEnv(env, "PATH"); ok && current != "" {
		path += string(os.PathListSeparator) + current
	}
	out := withEnv(append([]string{}, env...), "PATH", path)
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = withEnv(out, k, extra[k])
	}
	return out
}

func lookupEnv(env []string, key string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}
