//go:build linux

package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/shiftengine"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const (
	leaseTestTTL  = 90 * time.Second
	leaseTestWait = 20 * time.Second
	leaseTeam     = "bronze"
	shiftlessTeam = "silver"
	leaseRole     = "builder"
)

type leaseDatabase struct {
	store *store.Store
	pool  *pgxpool.Pool
	stop  func()
	err   error
}

var (
	sharedLeaseDatabase     leaseDatabase
	sharedLeaseDatabaseOnce sync.Once
)

func stopLeaseDatabase() {
	if sharedLeaseDatabase.stop != nil {
		sharedLeaseDatabase.stop()
	}
}

func startLeaseDatabase() leaseDatabase {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return leaseDatabase{err: err}
	}
	port := uint32(listener.Addr().(*net.TCPAddr).Port)
	_ = listener.Close()
	dir, err := os.MkdirTemp("", "ploeg-worker-lease-epg-*")
	if err != nil {
		return leaseDatabase{err: err}
	}
	epg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().Encoding("UTF8").Port(port).RuntimePath(dir).Logger(io.Discard))
	if err := epg.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return leaseDatabase{err: fmt.Errorf("embedded postgres failed to start: %w", err)}
	}
	db := leaseDatabase{}
	db.stop = func() {
		if db.store != nil {
			db.store.Close()
		}
		if db.pool != nil {
			db.pool.Close()
		}
		_ = epg.Stop()
		_ = os.RemoveAll(dir)
	}
	ctx := context.Background()
	dsn := fmt.Sprintf("postgresql://postgres:postgres@localhost:%d/postgres?sslmode=disable", port)
	if db.store, db.err = store.New(ctx, dsn, store.PoolConfig{}); db.err != nil {
		return db
	}
	if db.err = db.store.Migrate(ctx); db.err != nil {
		return db
	}
	db.pool, db.err = pgxpool.New(ctx, dsn)
	return db
}

type controlPlane struct {
	t       *testing.T
	store   *store.Store
	pool    *pgxpool.Pool
	plans   plan.Plans
	engine  *shiftengine.Engine
	handler atomic.Pointer[http.Handler]
	url     string
	boots   atomic.Int32
	ttl     time.Duration
	version string
}

func newControlPlane(t *testing.T) *controlPlane {
	t.Helper()
	sharedLeaseDatabaseOnce.Do(func() { sharedLeaseDatabase = startLeaseDatabase() })
	if sharedLeaseDatabase.err != nil {
		t.Fatal(sharedLeaseDatabase.err)
	}
	cp := unbootedControlPlane(t, sharedLeaseDatabase.store, sharedLeaseDatabase.pool, leaseTestTTL)
	cp.exec(`TRUNCATE work_items, audit_log, inference_allowances, deployments CASCADE`)
	cp.serve()
	return cp
}

func unbootedControlPlane(t *testing.T, st *store.Store, pool *pgxpool.Pool, ttl time.Duration) *controlPlane {
	t.Helper()
	plans, err := plan.Parse(`{"` + leaseTeam + `": {"pool": 10, "rounds": [{"roles": [{"name": "` + leaseRole + `", "writes": true, "cap": 3}]}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	return &controlPlane{t: t, store: st, pool: pool, plans: plans, ttl: ttl}
}

func (cp *controlPlane) serve() {
	cp.boot()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		(*cp.handler.Load()).ServeHTTP(w, r)
	}))
	cp.t.Cleanup(front.Close)
	cp.url = front.URL
}

func (cp *controlPlane) boot() {
	cp.engine = &shiftengine.Engine{Store: cp.store, Plans: cp.plans, Log: slog.New(slog.DiscardHandler)}
	handler := cp.apiServer(cp.version).Handler()
	cp.handler.Store(&handler)
	cp.boots.Add(1)
}

func (cp *controlPlane) apiServer(version string) *httpapi.Server {
	return &httpapi.Server{
		Store:          cp.store,
		LeaseTTL:       cp.ttl,
		Log:            slog.New(slog.DiscardHandler),
		RoleCaps:       cp.plans,
		Engine:         cp.engine,
		WorkerSecurity: &httpapi.WorkerSecurity{AllowLegacy: true},
		Version:        version,
	}
}

func (cp *controlPlane) restart() {
	cp.boot()
	cp.sweep()
}

func (cp *controlPlane) sweep() {
	cp.t.Helper()
	ctx := context.Background()
	if _, err := cp.store.ExpireLeases(ctx); err != nil {
		cp.t.Fatal(err)
	}
	if _, err := cp.store.ExpireRuns(ctx); err != nil {
		cp.t.Fatal(err)
	}
	cp.engine.EvaluateAll(ctx)
}

func (cp *controlPlane) exec(sql string, args ...any) {
	cp.t.Helper()
	if _, err := cp.pool.Exec(context.Background(), sql, args...); err != nil {
		cp.t.Fatalf("%s: %v", sql, err)
	}
}

func (cp *controlPlane) count(sql string, args ...any) int {
	cp.t.Helper()
	var n int
	if err := cp.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		cp.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (cp *controlPlane) plannedItem(externalID string) int64 {
	cp.t.Helper()
	item := work.WorkItem{Provider: "vikunja", ExternalID: externalID, Team: leaseTeam, Title: "t"}
	id, _, err := cp.store.IngestAssigned(context.Background(), item)
	if err != nil {
		cp.t.Fatal(err)
	}
	if err := cp.engine.EnsureShift(context.Background(), id, item); err != nil {
		cp.t.Fatal(err)
	}
	return id
}

func (cp *controlPlane) shiftlessItem(externalID string) int64 {
	cp.t.Helper()
	id, _, err := cp.store.IngestAssigned(context.Background(), work.WorkItem{Provider: "vikunja", ExternalID: externalID, Team: shiftlessTeam, Title: "t"})
	if err != nil {
		cp.t.Fatal(err)
	}
	return id
}

func (cp *controlPlane) expireRun(runToken string) {
	cp.exec(`UPDATE agent_runs SET expires_at = now() - interval '1 second' WHERE run_token = $1 AND expires_at IS NOT NULL`, runToken)
	cp.exec(`UPDATE leases SET expires_at = now() - interval '1 second' WHERE run_token = $1`, runToken)
}

func (cp *controlPlane) skipInfraBackoff(workItemID int64) {
	cp.exec(`UPDATE work_items SET next_eligible_at = NULL WHERE id = $1`, workItemID)
}

type runRow struct {
	State, Outcome, FailureReason string
}

func (cp *controlPlane) run(runToken string) runRow {
	cp.t.Helper()
	var r runRow
	if err := cp.pool.QueryRow(context.Background(),
		`SELECT state, COALESCE(outcome, ''), COALESCE(failure_reason, '') FROM agent_runs WHERE run_token = $1`, runToken).
		Scan(&r.State, &r.Outcome, &r.FailureReason); err != nil {
		cp.t.Fatalf("run %s: %v", runToken, err)
	}
	return r
}

func (cp *controlPlane) leaseHolder(workItemID int64) string {
	cp.t.Helper()
	var token string
	err := cp.pool.QueryRow(context.Background(), `SELECT run_token FROM leases WHERE work_item_id = $1`, workItemID).Scan(&token)
	if err != nil && !strings.Contains(err.Error(), "no rows") {
		cp.t.Fatal(err)
	}
	return token
}

func (cp *controlPlane) itemState(workItemID int64) string {
	cp.t.Helper()
	var state string
	if err := cp.pool.QueryRow(context.Background(), `SELECT state FROM work_items WHERE id = $1`, workItemID).Scan(&state); err != nil {
		cp.t.Fatal(err)
	}
	return state
}

func (cp *controlPlane) client() *APIClient {
	return &APIClient{Base: cp.url, HC: &http.Client{Timeout: 10 * time.Second}}
}

type partition struct {
	cut       atomic.Bool
	renewOnly atomic.Bool
	hang      atomic.Bool
	renewals  atomic.Int32
	settled   atomic.Int32
}

var errPartitioned = errors.New("partitioned from ploegd")

func (p *partition) RoundTrip(r *http.Request) (*http.Response, error) {
	renewal := strings.HasSuffix(r.URL.Path, "/renew")
	if renewal {
		p.renewals.Add(1)
	}
	if renewal {
		defer p.settled.Add(1)
	}
	if renewal && p.hang.Load() {
		<-r.Context().Done()
		return nil, r.Context().Err()
	}
	if p.cut.Load() && (renewal || !p.renewOnly.Load()) {
		return nil, errPartitioned
	}
	return http.DefaultTransport.RoundTrip(r)
}

type manualClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*manualTimer
}

type manualTimer struct {
	at            time.Time
	fire          func()
	stopped, done bool
}

func newManualClock() *manualClock {
	return &manualClock{now: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &manualTimer{at: c.now.Add(d), fire: f}
	c.timers = append(c.timers, t)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		pending := !t.stopped && !t.done
		t.stopped = true
		return pending
	}
}

func (c *manualClock) advanceTo(at time.Time) int {
	c.mu.Lock()
	c.now = at
	var due []func()
	for _, t := range c.timers {
		if !t.stopped && !t.done && !t.at.After(at) {
			t.done = true
			due = append(due, t.fire)
		}
	}
	c.mu.Unlock()
	for _, fire := range due {
		fire()
	}
	return len(due)
}

type manualTicks struct {
	ch       chan time.Time
	interval atomic.Int64
}

func (m *manualTicks) start(interval time.Duration) (<-chan time.Time, func()) {
	m.interval.Store(int64(interval))
	return m.ch, func() {}
}

type leaseHoldingAdapter struct {
	started  chan struct{}
	stopped  chan struct{}
	finish   chan harness.OutcomeReport
	cause    error
	renewals int32
	counter  *atomic.Int32
}

func newLeaseHoldingAdapter(counter *atomic.Int32) *leaseHoldingAdapter {
	return &leaseHoldingAdapter{started: make(chan struct{}), stopped: make(chan struct{}), finish: make(chan harness.OutcomeReport), counter: counter}
}

func (a *leaseHoldingAdapter) Name() string     { return "lease-holding" }
func (a *leaseHoldingAdapter) ExpectsLLM() bool { return false }
func (a *leaseHoldingAdapter) Run(ctx context.Context, _ harness.TaskSpec, _ harness.RunEnv) (harness.OutcomeReport, error) {
	close(a.started)
	select {
	case <-ctx.Done():
		a.cause = context.Cause(ctx)
		a.renewals = a.counter.Load()
		close(a.stopped)
		return harness.OutcomeReport{}, ctx.Err()
	case rep := <-a.finish:
		return rep, nil
	}
}

type runningWorker struct {
	t       *testing.T
	worker  *Worker
	adapter *leaseHoldingAdapter
	net     *partition
	ticks   *manualTicks
	clock   *manualClock
	done    chan error
	token   string
	sent    int32
}

func startWorker(t *testing.T, cp *controlPlane, forge *writerForge, team, role string) *runningWorker {
	t.Helper()
	net := &partition{}
	ticks := &manualTicks{ch: make(chan time.Time)}
	adapter := newLeaseHoldingAdapter(&net.renewals)
	w := New(Config{APIURL: cp.url, ForgeURL: forge.url, DefaultForge: harness.ForgeForgejo, BuilderToken: "tok",
		RepoOwner: "webgrip", RepoName: "example", BaseBranch: "development", WorkDir: t.TempDir(), Team: team, Role: role},
		adapter, llmbroker.Static{}, discardLog())
	w.API.HC = &http.Client{Transport: net, Timeout: 10 * time.Second}
	w.RenewTicks = ticks.start
	clock := newManualClock()
	w.clock = clock
	rw := &runningWorker{t: t, worker: w, adapter: adapter, net: net, ticks: ticks, clock: clock, done: make(chan error, 1)}
	go func() { rw.done <- w.RunContext(context.Background()) }()
	select {
	case <-adapter.started:
	case err := <-rw.done:
		t.Fatalf("the worker ended before its harness started: %v", err)
	case <-time.After(leaseTestWait):
		t.Fatal("the harness never started")
	}
	if err := cp.pool.QueryRow(context.Background(),
		`SELECT run_token FROM agent_runs WHERE team = $1 AND state = 'running' ORDER BY id DESC LIMIT 1`, team).Scan(&rw.token); err != nil {
		t.Fatal(err)
	}
	return rw
}

func (rw *runningWorker) tick() bool {
	select {
	case rw.ticks.ch <- time.Now():
		return true
	case <-rw.adapter.stopped:
		return false
	case <-time.After(leaseTestWait):
		rw.t.Fatal("the renewal loop stopped taking ticks while the harness still ran")
		return false
	}
}

func wantHTTPStatus(t *testing.T, what string, err error, status int) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", status)) {
		t.Fatalf("%s: got %v, want HTTP %d", what, err, status)
	}
}

type fencedState struct {
	Old, Current     runRow
	LeaseHolder      string
	ItemState        string
	Checkpoints      int
	OpenShifts       int
	RunsForItem      int
	OutcomeAudits    int
	CheckpointAudits int
}

func (cp *controlPlane) fencedState(workItemID int64, old, current string) fencedState {
	cp.t.Helper()
	return fencedState{
		Old: cp.run(old), Current: cp.run(current),
		LeaseHolder:      cp.leaseHolder(workItemID),
		ItemState:        cp.itemState(workItemID),
		Checkpoints:      cp.count(`SELECT count(*) FROM checkpoints WHERE work_item_id = $1`, workItemID),
		OpenShifts:       cp.count(`SELECT count(*) FROM shifts WHERE work_item_id = $1 AND closed_at IS NULL`, workItemID),
		RunsForItem:      cp.count(`SELECT count(*) FROM agent_runs WHERE work_item_id = $1`, workItemID),
		OutcomeAudits:    cp.count(`SELECT count(*) FROM audit_log WHERE work_item_id = $1 AND action LIKE 'outcome.%'`, workItemID),
		CheckpointAudits: cp.count(`SELECT count(*) FROM audit_log WHERE work_item_id = $1 AND action = 'checkpoint.written'`, workItemID),
	}
}

type dispatchShape struct {
	name, team, role string
	item             func(*controlPlane, string) int64
}

var dispatchShapes = []dispatchShape{
	{name: "planned shift", team: leaseTeam, role: leaseRole, item: (*controlPlane).plannedItem},
	{name: "shift-less claim", team: shiftlessTeam, item: (*controlPlane).shiftlessItem},
}

func TestLateReportsFromASupersededAttemptAreRefusedAndChangeNothing(t *testing.T) {
	for _, shape := range dispatchShapes {
		t.Run(shape.name, func(t *testing.T) {
			cp := newControlPlane(t)
			id := shape.item(cp, "7")
			late, current := cp.client(), cp.client()
			first, err := late.Claim(shape.team, shape.role)
			if err != nil || first == nil {
				t.Fatalf("first attempt claim: %v %v", first, err)
			}
			cp.expireRun(first.RunToken)
			cp.sweep()
			cp.skipInfraBackoff(id)
			second, err := current.Claim(shape.team, shape.role)
			if err != nil || second == nil || second.RunToken == first.RunToken {
				t.Fatalf("second attempt claim: %+v %v", second, err)
			}
			if err := current.Checkpoint(second.RunToken, work.Checkpoint{Phase: "branch_created", Branch: writerBranch}); err != nil {
				t.Fatalf("the current attempt's checkpoint: %v", err)
			}
			before := cp.fencedState(id, first.RunToken, second.RunToken)
			if before.LeaseHolder != second.RunToken || before.Current.State != "running" {
				t.Fatalf("attempt N+1 does not hold the item: %+v", before)
			}

			if renewal, err := late.Renew(context.Background(), first.RunToken); err != nil || !renewal.Gone {
				t.Errorf("late renewal: %+v %v, want gone", renewal, err)
			}
			wantHTTPStatus(t, "late checkpoint", late.Checkpoint(first.RunToken, work.Checkpoint{Phase: "branch_created", Branch: writerBranch}), http.StatusNotFound)
			wantHTTPStatus(t, "late outcome", late.Outcome(first.RunToken, harness.OutcomeReport{
				Outcome: work.OutcomeNoChangeNeeded, Summary: "late completion from attempt N"}), http.StatusNotFound)

			if after := cp.fencedState(id, first.RunToken, second.RunToken); after != before {
				t.Fatalf("a late report from attempt N changed authoritative state:\nbefore %+v\nafter  %+v", before, after)
			}
			if err := current.Outcome(second.RunToken, harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "attempt N+1"}); err != nil {
				t.Fatalf("the current attempt could not report: %v", err)
			}
			if got := cp.run(second.RunToken); got.State != "finished" || got.Outcome != string(work.OutcomeNoChangeNeeded) {
				t.Fatalf("attempt N+1 = %+v", got)
			}
		})
	}
}

func TestControllerRestartMidRunKeepsTheLiveRunAndDispatchesNothingTwice(t *testing.T) {
	for _, shape := range dispatchShapes {
		t.Run(shape.name, func(t *testing.T) { restartMidRun(t, shape) })
	}
}

func restartMidRun(t *testing.T, shape dispatchShape) {
	cp := newControlPlane(t)
	id := shape.item(cp, "7")
	rw := startWorker(t, cp, newWriterForge(t, false, pullsNone), shape.team, shape.role)

	rw.net.cut.Store(true)
	rw.renew()
	cp.restart()
	rw.net.cut.Store(false)
	rw.renew()
	rw.renew()
	if cp.boots.Load() != 2 {
		t.Fatalf("ploegd booted %d times, want 2", cp.boots.Load())
	}
	if got := cp.run(rw.token); got.State != "running" {
		t.Fatalf("the live Run after the restart = %+v", got)
	}
	if again, err := cp.client().Claim(shape.team, shape.role); err != nil || again != nil {
		t.Fatalf("a claim after the restart got %+v %v, want nothing", again, err)
	}
	if holder := cp.leaseHolder(id); holder != rw.token {
		t.Fatalf("lease holder %q, want the live Run", holder)
	}
	if n := cp.count(`SELECT count(*) FROM agent_runs WHERE work_item_id = $1 AND state IN ('pending', 'running')`, id); n != 1 {
		t.Fatalf("%d live or pending Runs after the restart, want 1", n)
	}

	rw.adapter.finish <- harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "finished across a ploegd restart"}
	if err := rw.result(); err != nil {
		t.Fatalf("the worker's report after the restart: %v", err)
	}
	if got := cp.run(rw.token); got.State != "finished" || got.FailureReason == string(work.FailureLeaseLost) {
		t.Fatalf("the Run after its report = %+v", got)
	}
	if n := cp.count(`SELECT count(*) FROM agent_runs WHERE work_item_id = $1`, id); n != 1 {
		t.Fatalf("%d Runs for the item, want 1: the restart dispatched it twice", n)
	}
}

func TestPartitionLongerThanTheLeaseRedispatchesAndFencesTheOldWorker(t *testing.T) {
	for _, shape := range dispatchShapes {
		t.Run(shape.name, func(t *testing.T) { partitionPastTheLease(t, shape) })
	}
}

func partitionPastTheLease(t *testing.T, shape dispatchShape) {
	cp := newControlPlane(t)
	id := shape.item(cp, "7")
	stale := startWorker(t, cp, newWriterForge(t, false, pullsNone), shape.team, shape.role)

	stale.net.cut.Store(true)
	stale.renew()
	cp.expireRun(stale.token)
	cp.sweep()
	cp.skipInfraBackoff(id)
	fresh, err := cp.client().Claim(shape.team, shape.role)
	if err != nil || fresh == nil || fresh.RunToken == stale.token {
		t.Fatalf("re-dispatch after the lease expired: %+v %v", fresh, err)
	}

	stale.net.cut.Store(false)
	stale.tick()
	stale.awaitStop()
	if !errors.Is(stale.adapter.cause, errLeaseLost) {
		t.Fatalf("the old harness stopped for %v, want a lost lease", stale.adapter.cause)
	}
	if err := stale.result(); err == nil || !strings.Contains(err.Error(), "outcome report failed") {
		t.Fatalf("the old worker's report was not refused: %v", err)
	}
	if got := cp.run(stale.token); got != (runRow{State: "finished", Outcome: string(work.OutcomeFailed), FailureReason: string(work.FailureLeaseLost)}) {
		t.Fatalf("the old attempt = %+v, want the sweeper's verdict unchanged", got)
	}
	if got := cp.run(fresh.RunToken); got.State != "running" {
		t.Fatalf("the re-dispatched attempt = %+v", got)
	}
	if holder := cp.leaseHolder(id); holder != fresh.RunToken {
		t.Fatalf("lease holder %q, want the re-dispatched attempt", holder)
	}
	if state := cp.itemState(id); state != string(work.StateActive) {
		t.Fatalf("item state %q, want active under the new attempt", state)
	}
	if renewal, err := cp.client().Renew(context.Background(), fresh.RunToken); err != nil || renewal.Gone {
		t.Fatalf("the re-dispatched attempt cannot renew: %+v %v", renewal, err)
	}
}

func TestRacingClaimsForOneItemDispatchItExactlyOnce(t *testing.T) {
	const racers = 8
	for _, shape := range dispatchShapes {
		t.Run(shape.name, func(t *testing.T) {
			cp := newControlPlane(t)
			id := shape.item(cp, "7")
			start := make(chan struct{})
			claims := make([]*ClaimResponse, racers)
			errs := make([]error, racers)
			var wg sync.WaitGroup
			for i := range racers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					claims[i], errs[i] = cp.client().Claim(shape.team, shape.role)
				}()
			}
			close(start)
			wg.Wait()
			winners := 0
			for i := range racers {
				if errs[i] != nil {
					t.Fatalf("racer %d: %v", i, errs[i])
				}
				if claims[i] != nil {
					winners++
				}
			}
			if winners != 1 {
				t.Fatalf("%d racers won the item, want exactly 1", winners)
			}
			if n := cp.count(`SELECT count(*) FROM agent_runs WHERE work_item_id = $1 AND state = 'running'`, id); n != 1 {
				t.Fatalf("%d running Runs, want 1", n)
			}
			if n := cp.count(`SELECT count(*) FROM leases WHERE work_item_id = $1`, id); n != 1 {
				t.Fatalf("%d leases, want 1", n)
			}
		})
	}
}

func TestThreeConsecutiveFailedRenewalsStopTheHarnessThreeQuartersOfALeaseAfterTheLastSuccess(t *testing.T) {
	cp := newControlPlane(t)
	cp.plannedItem("7")
	rw := startWorker(t, cp, newWriterForge(t, false, pullsNone), leaseTeam, leaseRole)
	if interval := time.Duration(rw.ticks.interval.Load()); interval > leaseTestTTL/4 || interval < leaseTestTTL/4-time.Second {
		t.Fatalf("renewal interval %v, want a quarter of the %v lease", interval, leaseTestTTL)
	}
	rw.net.renewOnly.Store(true)

	rw.net.cut.Store(true)
	rw.renew()
	rw.renew()
	rw.net.cut.Store(false)
	rw.renew()
	rw.net.cut.Store(true)
	rw.renew()
	rw.renew()
	rw.renew()
	rw.awaitStop()

	if rw.adapter.renewals != 6 {
		t.Fatalf("the harness stopped after %d renewals, want 6: two failures, a success, then three failures", rw.adapter.renewals)
	}
	if !errors.Is(rw.adapter.cause, errLeaseLost) {
		t.Fatalf("the harness stopped for %v, want a lost lease", rw.adapter.cause)
	}
	if err := rw.result(); err != nil {
		t.Fatalf("the stopped Run's report: %v", err)
	}
	if got := cp.run(rw.token); got != (runRow{State: "finished", Outcome: string(work.OutcomeFailed), FailureReason: string(work.FailureLeaseLost)}) {
		t.Fatalf("the stopped Run = %+v, want it failed as a lost lease", got)
	}
}

type leaseLossVerdict struct {
	Run            runRow
	ItemState      string
	OpenShifts     int
	PendingRetries int
	Attempts       int
	InfraFailures  int
	BackedOff      bool
}

func (cp *controlPlane) leaseLossVerdict(workItemID int64, runToken string) leaseLossVerdict {
	cp.t.Helper()
	v := leaseLossVerdict{
		Run:            cp.run(runToken),
		ItemState:      cp.itemState(workItemID),
		OpenShifts:     cp.count(`SELECT count(*) FROM shifts WHERE work_item_id = $1 AND closed_at IS NULL`, workItemID),
		PendingRetries: cp.count(`SELECT count(*) FROM agent_runs WHERE work_item_id = $1 AND state = 'pending'`, workItemID),
	}
	if err := cp.pool.QueryRow(context.Background(),
		`SELECT attempts, infra_failures, COALESCE(next_eligible_at > now(), false) FROM work_items WHERE id = $1`, workItemID).
		Scan(&v.Attempts, &v.InfraFailures, &v.BackedOff); err != nil {
		cp.t.Fatal(err)
	}
	return v
}

func TestAWorkerThatLosesItsLeaseReportsTheSweepersVerdict(t *testing.T) {
	for _, shape := range dispatchShapes {
		t.Run(shape.name, func(t *testing.T) {
			swept := newControlPlane(t)
			sweptItem := shape.item(swept, "7")
			claimed, err := swept.client().Claim(shape.team, shape.role)
			if err != nil || claimed == nil {
				t.Fatalf("claim: %+v %v", claimed, err)
			}
			swept.expireRun(claimed.RunToken)
			swept.sweep()
			sweeper := swept.leaseLossVerdict(sweptItem, claimed.RunToken)

			cp := newControlPlane(t)
			id := shape.item(cp, "7")
			rw := startWorker(t, cp, newWriterForge(t, false, pullsNone), shape.team, shape.role)
			rw.net.renewOnly.Store(true)
			rw.net.cut.Store(true)
			rw.renew()
			rw.renew()
			rw.renew()
			rw.awaitStop()
			if err := rw.result(); err != nil {
				t.Fatalf("the worker could not report its lost lease: %v", err)
			}
			cp.sweep()

			if worker := cp.leaseLossVerdict(id, rw.token); worker != sweeper {
				t.Fatalf("the same lost lease ends differently by who noticed it:\nworker  %+v\nsweeper %+v", worker, sweeper)
			}
			if sweeper.Run.Outcome != string(work.OutcomeFailed) || sweeper.ItemState == string(work.StateNeedsHuman) {
				t.Fatalf("the sweeper's verdict %+v parks the item for a person", sweeper)
			}
		})
	}
}

func TestACutOffWorkerStopsBeforeItsLeaseCanBeReclaimed(t *testing.T) {
	for _, shape := range dispatchShapes {
		t.Run(shape.name, func(t *testing.T) { cutOffBeforeTheLeaseEnds(t, shape) })
	}
}

func cutOffBeforeTheLeaseEnds(t *testing.T, shape dispatchShape) {
	cp := newControlPlane(t)
	id := shape.item(cp, "7")
	rw := startWorker(t, cp, newWriterForge(t, false, pullsNone), shape.team, shape.role)

	lastRenewal := rw.clock.Now().Add(renewInterval(leaseTestTTL))
	rw.clock.advanceTo(lastRenewal)
	rw.renew()
	rw.net.hang.Store(true)
	if !rw.tick() {
		t.Fatal("the harness stopped before the partition")
	}
	leaseEnds := lastRenewal.Add(leaseTestTTL)
	stopBy := leaseEnds.Add(-leaseStopMargin(leaseTestTTL))
	if fired := rw.clock.advanceTo(stopBy.Add(-time.Second)); fired != 0 {
		t.Fatalf("the worker gave up its lease %v before the margin", leaseEnds.Sub(stopBy.Add(-time.Second)))
	}
	if fired := rw.clock.advanceTo(stopBy); fired != 1 {
		t.Fatalf("with a renewal hung since %v, nothing stopped the harness %v before the lease ends at %v",
			lastRenewal.Add(renewInterval(leaseTestTTL)), leaseStopMargin(leaseTestTTL), leaseEnds)
	}
	rw.awaitStop()
	if !errors.Is(rw.adapter.cause, errLeaseLost) {
		t.Fatalf("the harness stopped for %v, want a lost lease", rw.adapter.cause)
	}
	if err := rw.result(); err != nil {
		t.Fatalf("the worker stopped before its lease ended, so its report must land: %v", err)
	}
	if got := cp.run(rw.token); got != (runRow{State: "finished", Outcome: string(work.OutcomeFailed), FailureReason: string(work.FailureLeaseLost)}) {
		t.Fatalf("the stopped Run = %+v, want the sweeper's verdict", got)
	}
	if holder := cp.leaseHolder(id); holder != "" {
		t.Fatalf("lease still held by %q after the report", holder)
	}
}

func TestAnExpiredLeaseIsRefusedBeforeTheSweeperRuns(t *testing.T) {
	for _, shape := range dispatchShapes {
		t.Run(shape.name, func(t *testing.T) {
			cp := newControlPlane(t)
			id := shape.item(cp, "7")
			late := cp.client()
			claimed, err := late.Claim(shape.team, shape.role)
			if err != nil || claimed == nil {
				t.Fatalf("claim: %+v %v", claimed, err)
			}
			cp.expireRun(claimed.RunToken)
			before := cp.fencedState(id, claimed.RunToken, claimed.RunToken)

			if renewal, err := late.Renew(context.Background(), claimed.RunToken); err != nil || !renewal.Gone {
				t.Errorf("renewal after the deadline: %+v %v, want gone", renewal, err)
			}
			wantHTTPStatus(t, "checkpoint after the deadline", late.Checkpoint(claimed.RunToken, work.Checkpoint{Phase: "branch_created", Branch: writerBranch}), http.StatusNotFound)
			wantHTTPStatus(t, "outcome after the deadline", late.Outcome(claimed.RunToken, harness.OutcomeReport{
				Outcome: work.OutcomeNoChangeNeeded, Summary: "late completion after the deadline"}), http.StatusNotFound)
			if after := cp.fencedState(id, claimed.RunToken, claimed.RunToken); after != before {
				t.Fatalf("a call after the deadline changed authoritative state:\nbefore %+v\nafter  %+v", before, after)
			}

			cp.sweep()
			if got := cp.run(claimed.RunToken); got != (runRow{State: "finished", Outcome: string(work.OutcomeFailed), FailureReason: string(work.FailureLeaseLost)}) {
				t.Fatalf("the swept Run = %+v, want the sweeper's verdict", got)
			}
		})
	}
}

func TestWithdrawnRunStopsItsHarnessAtTheNextRenewal(t *testing.T) {
	cp := newControlPlane(t)
	id := cp.plannedItem("7")
	rw := startWorker(t, cp, newWriterForge(t, false, pullsNone), leaseTeam, leaseRole)
	withdrawal, err := cp.store.WithdrawWorkItem(context.Background(), id, []string{leaseTeam}, "operator:test", store.CloseReasonWithdrawnByOperator)
	if err != nil || len(withdrawal.StoppedRunTokens) != 1 || withdrawal.StoppedRunTokens[0] != rw.token {
		t.Fatalf("withdrawal %+v %v", withdrawal, err)
	}
	withdrawn := cp.run(rw.token)

	rw.tick()
	rw.awaitStop()
	if rw.adapter.renewals != 1 || !errors.Is(rw.adapter.cause, errLeaseLost) {
		t.Fatalf("the harness stopped after %d renewals for %v, want the first renewal to stop it", rw.adapter.renewals, rw.adapter.cause)
	}
	if err := rw.result(); err == nil {
		t.Fatal("the withdrawn Run's outcome was accepted")
	}
	if got := cp.run(rw.token); got != withdrawn {
		t.Fatalf("the withdrawn Run's late report changed it from %+v to %+v", withdrawn, got)
	}
}

func (rw *runningWorker) renew() {
	rw.t.Helper()
	if !rw.tick() {
		rw.t.Fatalf("the harness stopped after %d renewals, before this one", rw.net.renewals.Load())
	}
	rw.sent++
	want := rw.sent
	deadline := time.Now().Add(leaseTestWait)
	for rw.net.settled.Load() < want {
		if time.Now().After(deadline) {
			rw.t.Fatalf("renewal %d never settled", want)
		}
		time.Sleep(time.Millisecond)
	}
}

func (rw *runningWorker) awaitStop() {
	rw.t.Helper()
	select {
	case <-rw.adapter.stopped:
	case <-time.After(leaseTestWait):
		rw.t.Fatal("the harness was never stopped")
	}
}

func (rw *runningWorker) stillRunning() bool {
	select {
	case <-rw.adapter.stopped:
		return false
	default:
		return true
	}
}

func (rw *runningWorker) result() error {
	rw.t.Helper()
	select {
	case err := <-rw.done:
		return err
	case <-time.After(leaseTestWait):
		rw.t.Fatal("the worker never returned")
		return nil
	}
}

func TestRenewalIntervalIsAQuarterOfTheLeaseAndNeverUnderFiveSeconds(t *testing.T) {
	for ttl, want := range map[time.Duration]time.Duration{
		5 * time.Minute:  75 * time.Second,
		60 * time.Second: 15 * time.Second,
		9 * time.Second:  5 * time.Second,
	} {
		if got := renewInterval(ttl); got != want {
			t.Errorf("renewInterval(%v) = %v, want %v", ttl, got, want)
		}
	}
}

func TestThreeFastStrikesComeBeforeTheStopMarginAndTheMarginBeforeTheLeaseEnds(t *testing.T) {
	for _, ttl := range []time.Duration{5 * time.Minute, 60 * time.Second, leaseTestTTL, 20 * time.Second} {
		stop := ttl - leaseStopMargin(ttl)
		if strikes := renewalStrikes * renewInterval(ttl); strikes >= stop {
			t.Errorf("TTL %v: three strikes land at %v, not before the stop at %v", ttl, strikes, stop)
		}
		if stop >= ttl || leaseStopMargin(ttl) < ttl/6 {
			t.Errorf("TTL %v: the worker stops at %v, want at least a sixth of the lease before it ends", ttl, stop)
		}
	}
}
