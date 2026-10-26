//go:build linux

package worker

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	upgradeDrillBasesVariable = "PLOEG_UPGRADE_DRILL_BASES"
	previousReleaseBase       = "previous"
	upgradeDrillLeaseTTL      = 20 * time.Second
	upgradeDrillRenewEvery    = time.Second
	upgradeDrillWait          = 2 * time.Minute
	upgradeDrillBulkItems     = 1000
	upgradeDrillBulkSpend     = 0.01
	bulkTeam                  = "gold"
	seedSource                = "testdata/upgrade-drill-seed/main.go"
	seedPackage               = "cmd/ploeg-upgrade-drill-seed"
	migrationsDirectory       = "pkg/store/migrations"
)

var upgradeDrillPlans = `{"` + leaseTeam + `": {"pool": 10, "rounds": [{"roles": [{"name": "` + leaseRole + `", "writes": true, "cap": 3}]}]}}`

type olderRelease struct {
	tag              string
	version          string
	seed             string
	sendsVersion     bool
	migrations       []string
	rollbackPromised bool
}

type releaseLineage struct {
	root        string
	newest      string
	headVersion string
	head        []string
	releases    []*olderRelease
}

func upgradeDrillLineage(t *testing.T) releaseLineage {
	t.Helper()
	requested := strings.Fields(os.Getenv(upgradeDrillBasesVariable))
	if len(requested) == 0 {
		t.Skipf("set %s to the releases to upgrade from, such as %q (mise run upgrade-drill)", upgradeDrillBasesVariable, previousReleaseBase+" v0.2.0-rc.1")
	}
	root := moduleRoot(t)
	lineage := releaseLineage{root: root, newest: strings.TrimSpace(releaseGit(t, root, "describe", "--tags", "--match", "v*", "--abbrev=0", "HEAD")),
		head: migrationFiles(t, filepath.Join(root, migrationsDirectory))}
	lineage.headVersion = releaseCore(lineage.newest)
	built := map[string]bool{}
	for _, name := range requested {
		tag := name
		if name == previousReleaseBase {
			tag = lineage.newest
		}
		if built[tag] {
			continue
		}
		built[tag] = true
		release := buildOlderRelease(t, root, tag)
		release.rollbackPromised = tag == lineage.newest
		lineage.releases = append(lineage.releases, release)
	}
	return lineage
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test")
		}
		dir = parent
	}
}

func releaseGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v (fetch the release tags: actions/checkout fetch-depth: 0)", strings.Join(args, " "), commandFailure(err))
	}
	return string(out)
}

func commandFailure(err error) string {
	if exit, ok := err.(*exec.ExitError); ok {
		return fmt.Sprintf("%v: %s", err, strings.TrimSpace(string(exit.Stderr)))
	}
	return err.Error()
}

func releaseCore(tag string) string {
	core, _, _ := strings.Cut(strings.TrimPrefix(tag, "v"), "-")
	return core
}

func twoMinorsAhead(version string) string {
	var major, minor, patch int
	if _, err := fmt.Sscanf(version, "%d.%d.%d", &major, &minor, &patch); err != nil {
		return "0.0.0"
	}
	return fmt.Sprintf("%d.%d.0", major, minor+2)
}

func migrationFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

func pendingMigrations(applied, head []string) []string {
	have := map[string]bool{}
	for _, name := range applied {
		have[name] = true
	}
	var pending []string
	for _, name := range head {
		if !have[name] {
			pending = append(pending, name)
		}
	}
	return pending
}

func buildOlderRelease(t *testing.T, root, tag string) *olderRelease {
	t.Helper()
	work := t.TempDir()
	tree := filepath.Join(work, "tree")
	archive := filepath.Join(work, "release.tar")
	releaseGit(t, root, "archive", "--format=tar", "-o", archive, tag)
	if err := os.MkdirAll(filepath.Join(tree, seedPackage), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("tar", "-xf", archive, "-C", tree).CombinedOutput(); err != nil {
		t.Fatalf("unpack %s: %v: %s", tag, err, out)
	}
	source, err := os.ReadFile(seedSource)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, seedPackage, "main.go"), source, 0o600); err != nil {
		t.Fatal(err)
	}
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("the upgrade drill builds %s with go: %v", tag, err)
	}
	release := &olderRelease{tag: tag, version: strings.TrimPrefix(tag, "v"), seed: filepath.Join(work, "seed"),
		migrations: migrationFiles(t, filepath.Join(tree, migrationsDirectory))}
	build := exec.Command(goBinary, "build", "-o", release.seed, "./"+seedPackage)
	build.Dir = tree
	build.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local", "CGO_ENABLED=0", "GOFLAGS=-mod=readonly")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the %s drill seed against %s's own packages: %v\n%s", tag, tag, err, out)
	}
	var capabilities struct {
		WorkerVersion bool `json:"workerVersion"`
	}
	out, err := exec.Command(release.seed, "capabilities").Output()
	if err != nil || json.Unmarshal(out, &capabilities) != nil {
		t.Fatalf("%s seed capabilities: %s %v", tag, out, err)
	}
	release.sendsVersion = capabilities.WorkerVersion
	return release
}

type seedEvent struct {
	Event      string `json:"event"`
	Token      string `json:"token"`
	ExternalID string `json:"externalId"`
	WorkItemID int64  `json:"workItemId"`
	Err        string `json:"err"`
	Failures   int    `json:"failures"`
}

func parseSeedEvents(t *testing.T, out []byte) []seedEvent {
	t.Helper()
	var events []seedEvent
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var e seedEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("seed output %q: %v", line, err)
		}
		events = append(events, e)
	}
	return events
}

func (r *olderRelease) exec(t *testing.T, args ...string) []seedEvent {
	t.Helper()
	out, err := exec.Command(r.seed, args...).Output()
	if err != nil {
		t.Fatalf("%s seed %s: %s", r.tag, strings.Join(args, " "), commandFailure(err))
	}
	return parseSeedEvents(t, out)
}

type olderPloegd struct {
	cmd  *exec.Cmd
	url  string
	once sync.Once
}

func (r *olderRelease) serve(t *testing.T, dsn string) *olderPloegd {
	t.Helper()
	dir := t.TempDir()
	addr := filepath.Join(dir, "addr")
	logFile, err := os.Create(filepath.Join(dir, "ploegd.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logFile.Close() })
	cmd := exec.Command(r.seed, "serve", "-dsn", dsn, "-plans", upgradeDrillPlans, "-ttl", upgradeDrillLeaseTTL.String(), "-addr-file", addr)
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	p := &olderPloegd{cmd: cmd}
	t.Cleanup(p.kill)
	for deadline := time.Now().Add(upgradeDrillWait); ; time.Sleep(10 * time.Millisecond) {
		if b, err := os.ReadFile(addr); err == nil {
			p.url = string(b)
			return p
		}
		select {
		case err := <-exited:
			b, _ := os.ReadFile(logFile.Name())
			t.Fatalf("%s ploegd exited before serving: %v\n%s", r.tag, err, b)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s ploegd never served", r.tag)
		}
	}
}

func (p *olderPloegd) kill() {
	p.once.Do(func() {
		_ = p.cmd.Process.Kill()
	})
}

func (p *olderPloegd) proxy() http.Handler {
	target, err := url.Parse(p.url)
	if err != nil {
		panic(err)
	}
	return httputil.NewSingleHostReverseProxy(target)
}

type olderWorker struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  interface{ Write([]byte) (int, error) }
	events chan seedEvent
	seen   []string
}

func (r *olderRelease) hold(t *testing.T, api, dsn string) *olderWorker {
	t.Helper()
	cmd := exec.Command(r.seed, "hold", "-api", api, "-dsn", dsn, "-team", leaseTeam, "-role", leaseRole,
		"-worker-version", r.version, "-account", "ploeg-drill-in-flight", "-branch", writerBranch, "-renew-every", upgradeDrillRenewEvery.String())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w := &olderWorker{t: t, cmd: cmd, stdin: stdin, events: make(chan seedEvent, 4096)}
	go func() {
		defer close(w.events)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			var e seedEvent
			if json.Unmarshal(scanner.Bytes(), &e) == nil {
				w.events <- e
			}
		}
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return w
}

func (w *olderWorker) await(name string) seedEvent {
	w.t.Helper()
	timeout := time.After(upgradeDrillWait)
	for {
		select {
		case e, open := <-w.events:
			if !open {
				w.t.Fatalf("the older worker exited before %q; it reported %v", name, w.seen)
			}
			w.seen = append(w.seen, e.Event)
			if e.Event == name {
				return e
			}
		case <-timeout:
			w.t.Fatalf("the older worker never reported %q; it reported %v", name, w.seen)
		}
	}
}

func (w *olderWorker) drain() {
	for {
		select {
		case e, open := <-w.events:
			if !open {
				return
			}
			w.seen = append(w.seen, e.Event)
		default:
			return
		}
	}
}

func (w *olderWorker) proceed() {
	w.t.Helper()
	if _, err := w.stdin.Write([]byte("go\n")); err != nil {
		w.t.Fatal(err)
	}
}

type olderWorld struct {
	t       *testing.T
	release *olderRelease
	dsn     string
	api     string
}

func (w olderWorld) ingest(team, external string, planned bool) int64 {
	w.t.Helper()
	args := []string{"ingest", "-dsn", w.dsn, "-plans", upgradeDrillPlans, "-team", team, "-external", external}
	if planned {
		args = append(args, "-planned")
	}
	events := w.release.exec(w.t, args...)
	if len(events) != 1 || events[0].Event != "ingested" {
		w.t.Fatalf("ingest %s: %+v", external, events)
	}
	return events[0].WorkItemID
}

func (w olderWorld) run(team, role string, extra ...string) seedEvent {
	w.t.Helper()
	args := append([]string{"run", "-api", w.api, "-dsn", w.dsn, "-team", team, "-role", role, "-worker-version", w.release.version}, extra...)
	for _, e := range w.release.exec(w.t, args...) {
		if e.Event == "claimed" {
			return e
		}
	}
	w.t.Fatalf("%s claimed nothing for %s/%s", w.release.tag, team, role)
	return seedEvent{}
}

func (w olderWorld) settle(token string, spend float64) {
	w.t.Helper()
	w.release.exec(w.t, "settle", "-dsn", w.dsn, "-token", token, "-spend", fmt.Sprint(spend))
}

type olderDataset struct {
	settledID, parkedID int64
	pendingIDs          []int64
	settled, parked     seedEvent
}

func (w olderWorld) populate() olderDataset {
	w.t.Helper()
	var d olderDataset
	d.settledID = w.ingest(leaseTeam, "3", true)
	d.settled = w.run(leaseTeam, leaseRole, "-account", "ploeg-drill-settled", "-outcome", "no_change_needed")
	w.settle(d.settled.Token, settledBeforeBackup)
	d.parkedID = w.ingest(shiftlessTeam, "5", false)
	d.parked = w.run(shiftlessTeam, "", "-outcome", "stuck", "-stuck-reason", "ambiguous ticket")
	d.pendingIDs = []int64{w.ingest(shiftlessTeam, "21", false), w.ingest(shiftlessTeam, "22", false)}
	w.release.exec(w.t, "bulk", "-dsn", w.dsn, "-plans", upgradeDrillPlans, "-team", bulkTeam,
		"-count", fmt.Sprint(upgradeDrillBulkItems), "-spend", fmt.Sprint(upgradeDrillBulkSpend))
	if d.settled.ExternalID != "3" || d.parked.ExternalID != "5" {
		w.t.Fatalf("the older release dispatched %q and %q, want 3 and 5", d.settled.ExternalID, d.parked.ExternalID)
	}
	return d
}

type serviceFront struct {
	backend          atomic.Pointer[http.Handler]
	renewalsHeld     atomic.Bool
	renewalsInFlight atomic.Int32
	url              string
}

func newServiceFront(t *testing.T) *serviceFront {
	t.Helper()
	f := &serviceFront{}
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	f.url = server.URL
	return f
}

func (f *serviceFront) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/renew") {
		f.renewalsInFlight.Add(1)
		defer f.renewalsInFlight.Add(-1)
		if f.renewalsHeld.Load() {
			http.Error(w, "renewals held while the backup is taken", http.StatusServiceUnavailable)
			return
		}
	}
	backend := f.backend.Load()
	if backend == nil {
		http.Error(w, "no ploegd behind the Service", http.StatusServiceUnavailable)
		return
	}
	(*backend).ServeHTTP(w, r)
}

func (f *serviceFront) route(h http.Handler) { f.backend.Store(&h) }

func (f *serviceFront) outage() { f.backend.Store(nil) }

func (f *serviceFront) holdRenewals(t *testing.T) {
	t.Helper()
	f.renewalsHeld.Store(true)
	for deadline := time.Now().Add(upgradeDrillWait); f.renewalsInFlight.Load() > 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("a renewal never finished")
		}
	}
}

func (f *serviceFront) releaseRenewals() { f.renewalsHeld.Store(false) }

func (cp *controlPlane) through() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { (*cp.handler.Load()).ServeHTTP(w, r) })
}
