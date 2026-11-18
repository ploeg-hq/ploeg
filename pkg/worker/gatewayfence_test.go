package worker

import (
	"context"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

type gatewayPrepareRig struct {
	bare, checkout, remote string
}

func newGatewayPrepareRig(t *testing.T, base map[string]string, shape ...func(*FencedForgeRun)) *gatewayPrepareRig {
	t.Helper()
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skip("git is not available")
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend is not available")
	}
	projects := t.TempDir()
	bare := filepath.Join(projects, "webgrip", "example.git")
	gitIn(t, "", "init", "--bare", bare)
	gitIn(t, bare, "config", "http.receivepack", "true")
	seed := t.TempDir()
	writeTree(t, seed, base)
	gitIn(t, seed, "init")
	gitIn(t, seed, "add", ".")
	gitIn(t, seed, "commit", "-m", "base")
	gitIn(t, seed, "push", bare, "HEAD:refs/heads/development")
	forge := httptest.NewServer(&cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + projects, "GIT_HTTP_EXPORT_ALL=1"}})
	t.Cleanup(forge.Close)
	run := FencedForgeRun{
		Repo:   harness.RepoRef{ForgeURL: forge.URL, Owner: "webgrip", Name: "example", BaseBranch: "development"},
		Writes: true, Branch: writerBranch, Prepares: true, ScratchDir: t.TempDir(),
		Token: func(context.Context) string { return "real-forge-token" },
	}
	for _, f := range shape {
		f(&run)
	}
	fenced, err := NewFencedForge(run)
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(fenced)
	t.Cleanup(gateway.Close)
	remote, err := plainURL(gateway.URL, "webgrip", "example")
	if err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(t.TempDir(), "clone")
	gitIn(t, "", "clone", "--branch", "development", bare, checkout)
	gitIn(t, checkout, "checkout", "-b", writerBranch)
	return &gatewayPrepareRig{bare: bare, checkout: checkout, remote: remote}
}

func (rig *gatewayPrepareRig) commitAndPush(t *testing.T, files map[string]string) error {
	t.Helper()
	writeTree(t, rig.checkout, files)
	gitIn(t, rig.checkout, "add", "-A")
	gitIn(t, rig.checkout, "commit", "-m", "prepare")
	cmd := exec.Command("git", "push", rig.remote, "HEAD:refs/heads/"+writerBranch)
	cmd.Dir = rig.checkout
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("push: %s", out)
	}
	return err
}

func (rig *gatewayPrepareRig) forgeHasBranch(t *testing.T) bool {
	out, _ := exec.Command("git", "-C", rig.bare, "show-ref").CombinedOutput()
	return strings.Contains(string(out), "refs/heads/"+writerBranch)
}

func TestTheGatewayFencesAPrepareRunsPushFromThePushedPack(t *testing.T) {
	rig := newGatewayPrepareRig(t, map[string]string{"main.go": "package main\n"})
	if err := rig.commitAndPush(t, map[string]string{"AGENTS.md": "# Example\n"}); err != nil {
		t.Fatalf("pushing only AGENTS.md through the gateway was refused: %v", err)
	}
	if !rig.forgeHasBranch(t) {
		t.Fatal("the run branch did not reach the forge")
	}
}

func TestTheGatewayRefusesAPrepareRunsPushOutsideItsDeliverable(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"another path as well": {"AGENTS.md": "# x\n", "main.go": "package main\n\nfunc main() {}\n"},
		"a nested AGENTS.md":   {"docs/AGENTS.md": "# x\n"},
	} {
		t.Run(name, func(t *testing.T) {
			rig := newGatewayPrepareRig(t, map[string]string{"main.go": "package main\n"})
			if err := rig.commitAndPush(t, files); err == nil {
				t.Fatal("a push outside the deliverable passed the gateway")
			}
			if rig.forgeHasBranch(t) {
				t.Fatal("the forge has the run branch after a refused push")
			}
		})
	}
}

func TestTheGatewayFencesAnOpenHandsRunOffTheRepositoryHooksFile(t *testing.T) {
	openHands := func(r *FencedForgeRun) { r.Prepares, r.LoadsRepositoryHooks = false, true }
	rig := newGatewayPrepareRig(t, map[string]string{"main.go": "package main\n"}, openHands)
	if err := rig.commitAndPush(t, map[string]string{"main.go": "package main\n\nfunc main() {}\n"}); err != nil {
		t.Fatalf("an OpenHands push that leaves the hooks file alone was refused: %v", err)
	}
	rig = newGatewayPrepareRig(t, map[string]string{"main.go": "package main\n"}, openHands)
	if err := rig.commitAndPush(t, map[string]string{".openhands/hooks.json": "{}\n"}); err == nil {
		t.Fatal("an OpenHands push that adds the hooks file passed the gateway")
	}
	if rig.forgeHasBranch(t) {
		t.Fatal("the forge has the run branch after a refused push")
	}
}
