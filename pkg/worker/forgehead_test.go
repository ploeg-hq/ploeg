package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type capturedRequests struct {
	mu    sync.Mutex
	lines []string
}

func (c *capturedRequests) record(r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, r.Method+" "+r.RequestURI+" Authorization: "+r.Header.Get("Authorization"))
}

func (c *capturedRequests) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.lines...)
}

type cloneConfigVector struct {
	name     string
	settings func(evilURL, forgeURL, reporter string) [][2]string
}

func cloneConfigVectors() []cloneConfigVector {
	return []cloneConfigVector{
		{"http.proxy", func(evilURL, _, _ string) [][2]string {
			return [][2]string{{"http.proxy", evilURL}}
		}},
		{"url.insteadOf with a credential helper", func(evilURL, forgeURL, reporter string) [][2]string {
			return [][2]string{{"url." + evilURL + "/.insteadOf", forgeURL + "/"}, {"credential.helper", reporter}}
		}},
	}
}

func allCloneConfigVectors() cloneConfigVector {
	return cloneConfigVector{"all at once", func(evilURL, forgeURL, reporter string) [][2]string {
		var all [][2]string
		for _, vector := range cloneConfigVectors() {
			all = append(all, vector.settings(evilURL, forgeURL, reporter)...)
		}
		return all
	}}
}

type plantedClone struct {
	dir      string
	cloneURL string
	evil     *capturedRequests
	ran      string
}

func newPlantedClone(t *testing.T, forge *writerForge, token string, vector cloneConfigVector) *plantedClone {
	t.Helper()
	ctx := context.Background()
	cloneURL, err := plainURL(forge.url, "webgrip", "example")
	if err != nil {
		t.Fatal(err)
	}
	c := &plantedClone{dir: filepath.Join(t.TempDir(), "vik-7"), cloneURL: cloneURL, evil: &capturedRequests{}, ran: filepath.Join(t.TempDir(), "ran")}
	if out, err := runGit(ctx, "", cloneURL, token, cloneArgs("development", cloneURL, c.dir)...); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.evil.record(r)
		w.Header().Set("WWW-Authenticate", `Basic realm="evil"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(evil.Close)
	reporter := filepath.Join(t.TempDir(), "report.sh")
	if err := os.WriteFile(reporter, []byte("#!/bin/sh\necho \"$0 $*\" >> "+c.ran+"\ncat >/dev/null\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, setting := range vector.settings(evil.URL, forge.url, reporter) {
		if out, err := runGit(ctx, c.dir, "", "", "config", setting[0], setting[1]); err != nil {
			t.Fatalf("git config %s: %v\n%s", setting[0], err, out)
		}
	}
	return c
}

func (c *plantedClone) assertIgnored(t *testing.T, token string) {
	t.Helper()
	for _, line := range c.evil.all() {
		if strings.Contains(line, forgeAuthorizationHeader(token)) {
			t.Errorf("the real forge token reached the server the clone's .git/config names: %s", line)
			continue
		}
		t.Errorf("the worker's git followed the clone's .git/config to the harness's server: %s", line)
	}
	if ran, err := os.ReadFile(c.ran); err == nil {
		t.Errorf("the worker's git executed the credential helper the clone's .git/config names:\n%s", ran)
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func eachCloneConfigVector(t *testing.T, run func(t *testing.T, vector cloneConfigVector)) {
	for _, vector := range append(cloneConfigVectors(), allCloneConfigVectors()) {
		t.Run(vector.name, func(t *testing.T) { run(t, vector) })
	}
}

func TestRemoteForgeHeadIgnoresGitConfigurationTheHarnessWrote(t *testing.T) {
	const token = "real-forge-token"
	forge := newWriterForge(t, true, pullsNone)
	eachCloneConfigVector(t, func(t *testing.T, vector cloneConfigVector) {
		c := newPlantedClone(t, forge, token, vector)
		read := remoteForgeHead(c.dir, c.cloneURL, func(context.Context) string { return token }, writerBranch, "", true)
		head, byOthers := read(context.Background())
		if head != forge.branchHead() || !byOthers {
			t.Errorf("remoteForgeHead = (%q, %v), want the forge's head %s and that someone else moved the branch", head, byOthers, forge.branchHead())
		}
		c.assertIgnored(t, token)
	})
}

func TestBranchHeadReadOutsideAnyRepositoryIgnoresTheWorkingDirectorysClone(t *testing.T) {
	const token = "real-forge-token"
	forge := newWriterForge(t, true, pullsNone)
	c := newPlantedClone(t, forge, token, allCloneConfigVectors())
	t.Chdir(c.dir)
	head := readBranchHeadOutsideAnyRepository(context.Background(), c.cloneURL, token, writerBranch)
	if head.err != nil || head.commit != forge.branchHead() {
		t.Errorf("readBranchHeadOutsideAnyRepository = %+v, want the forge's head %s", head, forge.branchHead())
	}
	c.assertIgnored(t, token)
}
