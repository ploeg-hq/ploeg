package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseToolchainsAcceptsTheChartShape(t *testing.T) {
	tcs, err := ParseToolchains(`[{"name":"go","path":["/opt/ploeg/toolchains/go/usr/local/go/bin"],"env":{"GOTOOLCHAIN":"local"}}]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(tcs) != 1 || tcs[0].Name != "go" || tcs[0].Env["GOTOOLCHAIN"] != "local" {
		t.Fatalf("toolchains = %+v", tcs)
	}
	if tcs, err := ParseToolchains(""); err != nil || tcs != nil {
		t.Fatalf("empty input = %v, %v; want no toolchains", tcs, err)
	}
}

func TestParseToolchainsRejectsWhatCouldMisleadOrLeak(t *testing.T) {
	for label, raw := range map[string]string{
		"not json":          `go`,
		"no name":           `[{"path":["/a"]}]`,
		"duplicate":         `[{"name":"go","path":["/a"]},{"name":"go","path":["/b"]}]`,
		"no path":           `[{"name":"go"}]`,
		"relative path":     `[{"name":"go","path":["bin"]}]`,
		"two dirs in one":   `[{"name":"go","path":["/a:/b"]}]`,
		"overrides PATH":    `[{"name":"go","path":["/a"],"env":{"PATH":"/evil"}}]`,
		"sets a credential": `[{"name":"go","path":["/a"],"env":{"AGENT_BUILDER_TOKEN":"x"}}]`,
		"malformed key":     `[{"name":"go","path":["/a"],"env":{"A=B":"x"}}]`,
	} {
		if _, err := ParseToolchains(raw); err == nil {
			t.Errorf("%s: accepted %s", label, raw)
		}
	}
}

func TestCheckToolchainsRefusesAMissingMount(t *testing.T) {
	dir := t.TempDir()
	if err := CheckToolchains([]Toolchain{{Name: "go", Path: []string{dir}}}); err != nil {
		t.Fatalf("mounted toolchain refused: %v", err)
	}
	err := CheckToolchains([]Toolchain{{Name: "go", Path: []string{filepath.Join(dir, "absent")}}})
	if err == nil || !strings.Contains(err.Error(), `"go"`) {
		t.Fatalf("missing toolchain: err = %v, want one naming it", err)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckToolchains([]Toolchain{{Name: "go", Path: []string{file}}}); err == nil {
		t.Fatal("a file was accepted as a toolchain directory")
	}
}

func TestWithToolchainsPrependsPathInOrderAndKeepsTheFirstValue(t *testing.T) {
	env := []string{"PATH=/usr/bin", "HOME=/h"}
	got := withToolchains(env, []Toolchain{
		{Name: "go", Path: []string{"/tc/go/bin"}, Env: map[string]string{"GOTOOLCHAIN": "local", "SHARED": "first"}},
		{Name: "node", Path: []string{"/tc/node/bin", "/tc/node/lib/bin"}, Env: map[string]string{"SHARED": "second"}},
	})
	if path, _ := lookupEnv(got, "PATH"); path != "/tc/go/bin:/tc/node/bin:/tc/node/lib/bin:/usr/bin" {
		t.Errorf("PATH = %q", path)
	}
	if v, _ := lookupEnv(got, "GOTOOLCHAIN"); v != "local" {
		t.Errorf("GOTOOLCHAIN = %q", v)
	}
	if v, _ := lookupEnv(got, "SHARED"); v != "first" {
		t.Errorf("SHARED = %q, want the first toolchain's value", v)
	}
	if strings.Count(strings.Join(got, "\n"), "PATH=") != 1 {
		t.Errorf("PATH set more than once: %v", got)
	}
	if env[0] != "PATH=/usr/bin" {
		t.Error("withToolchains modified its input")
	}
	if same := withToolchains(env, nil); len(same) != len(env) {
		t.Errorf("no toolchains changed the environment: %v", same)
	}
}
