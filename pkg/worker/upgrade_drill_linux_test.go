//go:build linux

package worker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const settledAfterTheUpgrade = 0.5

type upgradeClock struct {
	Base                                                 string
	BackupStarted, BackupPoint, LastLostWrite, Destroyed time.Time
	Files, Recovery, Migrate, Boot, Rollback             time.Duration
	Applied, RowsAtBackup                                int
	BackupBytes                                          int64
	RollbackPromised                                     bool
}

func timed(elapsed *time.Duration, step func()) {
	start := time.Now()
	step()
	*elapsed = time.Since(start)
}

func (c upgradeClock) rto() time.Duration { return c.Files + c.Recovery + c.Migrate + c.Boot }

func (c upgradeClock) report() string {
	line := fmt.Sprintf("upgrade drill from %s: RTO %v (restore files %v, postgres start and recovery %v, %d migrations to head %v, ploegd boot and first sweep %v); "+
		"RPO %v (backup point to destruction; last lost write %v after the backup point); base backup of %d MB took %v; %d rows at the backup point",
		c.Base, c.rto().Round(time.Millisecond), c.Files.Round(time.Millisecond), c.Recovery.Round(time.Millisecond), c.Applied,
		c.Migrate.Round(time.Millisecond), c.Boot.Round(time.Millisecond), c.Destroyed.Sub(c.BackupPoint).Round(time.Millisecond),
		c.LastLostWrite.Sub(c.BackupPoint).Round(time.Millisecond), c.BackupBytes>>20, c.BackupPoint.Sub(c.BackupStarted).Round(time.Millisecond), c.RowsAtBackup)
	if c.RollbackPromised {
		line += fmt.Sprintf("; rollback to %s on the upgraded schema served again after %v", c.Base, c.Rollback.Round(time.Millisecond))
	}
	return line
}

func directoryBytes(t *testing.T, dir string) int64 {
	t.Helper()
	var total int64
	if err := filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		info, err := entry.Info()
		if err == nil {
			total += info.Size()
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return total
}

func rowTotal(t *testing.T, rows tableRows) int {
	t.Helper()
	total := 0
	for _, table := range rows {
		total += len(projectRows(t, table, nil))
	}
	return total
}

func retireLeased(state string) string {
	if state == "leased" {
		return string(work.StateActive)
	}
	return state
}

func poolOn(t *testing.T, db *drillPostgres) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), db.dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.pool = pool
	return pool
}

func storeOn(t *testing.T, db *drillPostgres) *store.Store {
	t.Helper()
	st, err := store.New(context.Background(), db.dsn, store.PoolConfig{})
	if err != nil {
		t.Fatal(err)
	}
	db.store = st
	return st
}

func TestUpgradeDrillRestoresAnOlderReleasesBackupMigratesItToHeadAndFencesItsWorkers(t *testing.T) {
	lineage := upgradeDrillLineage(t)
	for _, release := range lineage.releases {
		t.Run(release.tag, func(t *testing.T) { upgradeFrom(t, lineage, release) })
	}
}

func upgradeFrom(t *testing.T, lineage releaseLineage, release *olderRelease) {
	ctx := context.Background()
	root := t.TempDir()
	binaries := filepath.Join(root, "postgres-binaries")
	clock := upgradeClock{Base: release.tag, RollbackPromised: release.rollbackPromised}

	primary := startDrillPostgres(t, binaries, filepath.Join(root, "primary-runtime"), filepath.Join(root, "primary-data"))
	older := release.serve(t, primary.dsn)
	front := newServiceFront(t)
	front.route(older.proxy())
	primaryPool := poolOn(t, primary)
	oldWorld := olderWorld{t: t, release: release, dsn: primary.dsn, api: front.url}

	if got := appliedMigrations(t, primaryPool); !sameNames(got, release.migrations) {
		t.Fatalf("%s migrated the primary to %v, want its own %v", release.tag, got, release.migrations)
	}
	data := oldWorld.populate()
	leasedID := oldWorld.ingest(leaseTeam, "7", true)
	holder := release.hold(t, front.url, primary.dsn)
	inFlight := holder.await("claimed")
	if inFlight.ExternalID != "7" {
		t.Fatalf("the older worker claimed %q, want 7", inFlight.ExternalID)
	}
	holder.await("renewed")

	front.holdRenewals(t)
	oldColumns := publicColumns(t, primaryPool)
	atBackup := snapshotRows(t, primaryPool, oldColumns, nil)
	clock.RowsAtBackup = rowTotal(t, atBackup)
	spendAtBackup := (&controlPlane{t: t, pool: primaryPool}).spend()
	var auditAtBackup int64
	var leaseDeadline time.Time
	var settledState string
	if err := primaryPool.QueryRow(ctx, `SELECT (SELECT max(id) FROM audit_log), (SELECT expires_at FROM leases WHERE run_token = $1),
		(SELECT state FROM work_items WHERE id = $2)`, inFlight.Token, data.settledID).Scan(&auditAtBackup, &leaseDeadline, &settledState); err != nil {
		t.Fatal(err)
	}
	if settledState == string(work.StateActive) || settledState == "leased" || settledState == string(work.StateQueued) {
		t.Fatalf("the settled item is %q before the backup, want its Shift closed", settledState)
	}

	backup := filepath.Join(root, "base-backup")
	clock.BackupStarted = time.Now()
	if err := hotBaseBackup(ctx, primary.dsn, primary.data, backup); err != nil {
		t.Fatalf("base backup: %v", err)
	}
	clock.BackupPoint = time.Now()
	front.releaseRenewals()
	clock.BackupBytes = directoryBytes(t, backup)
	if !leaseDeadline.After(clock.BackupPoint) {
		t.Fatalf("the in-flight lease ended at %v, before the backup point %v", leaseDeadline, clock.BackupPoint)
	}

	holder.drain()
	holder.await("renewed")
	gapFinished := oldWorld.run(shiftlessTeam, "", "-account", "ploeg-drill-gap-finished", "-outcome", string(work.OutcomeNoChangeNeeded))
	oldWorld.settle(gapFinished.Token, settledInTheGap)
	gapHeld := oldWorld.run(shiftlessTeam, "", "-account", "ploeg-drill-gap-held", "-branch", "agent/vik-{externalId}")
	oldWorld.ingest(shiftlessTeam, "99", false)
	clock.LastLostWrite = time.Now()
	if lost := (&controlPlane{t: t, pool: primaryPool}).spend(); lost.Settled-spendAtBackup.Settled < settledInTheGap-1e-9 {
		t.Fatalf("the gap settled %v, want %v more than the backup's %v", lost.Settled, settledInTheGap, spendAtBackup.Settled)
	}

	front.outage()
	older.kill()
	primary.destroy()
	clock.Destroyed = time.Now()
	if _, err := os.Stat(primary.data); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the primary's data directory survived its destruction: %v", err)
	}

	restoredData := filepath.Join(root, "restored-data")
	if err := restoreBaseBackup(backup, restoredData); err != nil {
		t.Fatalf("restore: %v", err)
	}
	clock.Files = time.Since(clock.Destroyed)
	var restored *drillPostgres
	timed(&clock.Recovery, func() {
		restored = startDrillPostgres(t, binaries, filepath.Join(root, "restored-runtime"), restoredData)
		restored.awaitPromotion(t)
	})
	restoredPool := poolOn(t, restored)
	if !equalProjection(t, "the restored older-release database against the backup point", atBackup, snapshotRows(t, restoredPool, oldColumns, nil), oldColumns) {
		t.Fatal("the restored database does not match the backup point")
	}
	expectedAfterUpgrade := snapshotRows(t, restoredPool, oldColumns, rewrittenByMigration)
	before := len(appliedMigrations(t, restoredPool))
	var migrateErr error
	timed(&clock.Migrate, func() { migrateErr = storeOn(t, restored).Migrate(ctx) })
	if migrateErr != nil {
		t.Fatalf("migrate %s's database to head: %v", release.tag, migrateErr)
	}
	applied := appliedMigrations(t, restoredPool)
	clock.Applied = len(applied) - before
	if !sameNames(applied, lineage.head) {
		t.Fatalf("after the upgrade schema_migrations holds %v, want head's %v", applied, lineage.head)
	}
	surviving := survivingColumns(t, oldColumns, publicColumns(t, restoredPool))
	if !equalProjection(t, "the upgraded database against the backup point", expectedAfterUpgrade, snapshotRows(t, restoredPool, surviving, nil), surviving) {
		t.Fatal("the migrations to head changed rows the older release wrote")
	}

	cp := unbootedControlPlane(t, restored.store, restoredPool, upgradeDrillLeaseTTL)
	cp.version = lineage.headVersion
	timed(&clock.Boot, func() {
		cp.serve()
		cp.sweep()
	})
	api := cp.client()
	if got := cp.run(inFlight.Token); got.State != "running" || !leaseDeadline.After(time.Now()) {
		t.Fatalf("the in-flight lease ended before ploegd served (%+v, deadline %v); the drill would test only the sweeper, so raise upgradeDrillLeaseTTL", got, leaseDeadline)
	}

	if got := cp.spend(); got != spendAtBackup {
		t.Fatalf("settled spend after the upgrade %+v, want the backup point's %+v", got, spendAtBackup)
	}
	if spendAtBackup.Settled < settledBeforeBackup-1e-9 || spendAtBackup.ShiftSpent < settledBeforeBackup-1e-9 {
		t.Fatalf("the backup point holds %+v, want the %v settled before it", spendAtBackup, settledBeforeBackup)
	}

	if wait := time.Until(leaseDeadline); wait > 0 {
		time.Sleep(wait + 100*time.Millisecond)
	}
	headColumns := publicColumns(t, restoredPool)
	beforeRefusals := snapshotRows(t, restoredPool, headColumns, nil)
	cp.refusesEverything(api, gapFinished.Token, gapFinished.ExternalID)
	cp.refusesEverything(api, gapHeld.Token, gapHeld.ExternalID)
	if late := api.Outcome(data.settled.Token, harness.OutcomeReport{Outcome: work.OutcomeFailed, Summary: "a replayed report that disagrees"}); late == nil {
		t.Error("a Run the older release settled accepted a second, different outcome after the upgrade")
	}
	held := cp.fencedState(leasedID, inFlight.Token, inFlight.Token)
	front.route(cp.through())
	gone := holder.await("gone")
	if gone.Token != inFlight.Token || gone.Failures == 0 {
		t.Fatalf("the older worker's reconnection = %+v, want its pre-upgrade token gone after failed renewals", gone)
	}
	for _, refused := range []string{"checkpoint_after_gone", "outcome_after_gone"} {
		if e := holder.await(refused); !strings.Contains(e.Err, "HTTP 404") {
			t.Errorf("the older worker's %s = %q, want HTTP 404", refused, e.Err)
		}
	}
	if after := cp.fencedState(leasedID, inFlight.Token, inFlight.Token); after != held {
		t.Fatalf("the older worker changed authority after the upgrade:\nbefore %+v\nafter  %+v", held, after)
	}
	cp.refusesEverything(api, inFlight.Token, inFlight.ExternalID)
	if !equalProjection(t, "tokens from before the upgrade", beforeRefusals, snapshotRows(t, restoredPool, headColumns, nil), headColumns) {
		t.Fatal("a token from before the upgrade changed authoritative state")
	}

	cp.sweep()
	for _, id := range append([]int64{leasedID}, data.pendingIDs...) {
		cp.skipInfraBackoff(id)
	}
	if n := cp.count(`SELECT count(*) FROM agent_runs WHERE state = 'running' AND expires_at < now()`); n != 0 {
		t.Errorf("%d overdue Runs after the sweep, want 0", n)
	}
	if n := cp.count(`SELECT count(*) FROM leases WHERE expires_at < now()`); n != 0 {
		t.Errorf("%d overdue leases after the sweep, want 0", n)
	}
	if got := cp.run(inFlight.Token); got != (runRow{State: "finished", Outcome: string(work.OutcomeFailed), FailureReason: string(work.FailureLeaseLost)}) {
		t.Errorf("the in-flight Run of %s = %+v, want the sweeper's lease-lost verdict", release.tag, got)
	}
	for id, want := range map[int64]string{data.settledID: retireLeased(settledState), data.parkedID: string(work.StateNeedsHuman),
		data.pendingIDs[0]: string(work.StateQueued), data.pendingIDs[1]: string(work.StateQueued)} {
		if got := cp.itemState(id); got != want {
			t.Errorf("item %d is %q after the upgrade, want %q", id, got, want)
		}
	}
	if n := cp.count(`SELECT count(*) FROM work_items w WHERE w.state = 'active'
		AND NOT EXISTS (SELECT 1 FROM agent_runs r WHERE r.work_item_id = w.id AND r.state IN ('pending', 'running'))`); n != 0 {
		t.Errorf("%d items active with no live or pending Run after the upgrade", n)
	}
	if n := cp.count(`SELECT count(*) FROM work_items WHERE external_id = '99'`); n != 0 {
		t.Error("the item assigned in the gap survived the restore")
	}
	auditColumns := schemaColumns{"audit_log": surviving["audit_log"]}
	auditNow := tableRows{"audit_log": snapshotTable(t, restoredPool, "audit_log", surviving["audit_log"], nil, fmt.Sprintf("WHERE id <= %d", auditAtBackup))}
	if !equalProjection(t, "the audit trail up to the backup point", expectedAfterUpgrade, auditNow, auditColumns) {
		t.Error("the audit trail up to the backup point changed during the upgrade and reconciliation")
	}

	holder.proceed()
	reclaimed := holder.await("claimed")
	if reclaimed.ExternalID != "7" || reclaimed.Token == inFlight.Token {
		t.Fatalf("the older worker's claim after the upgrade = %+v, want item 7 under a new token", reclaimed)
	}
	if e := holder.await("reclaimed_reported"); e.Err != "" {
		t.Fatalf("the older worker's new attempt was refused: %s", e.Err)
	}
	if got := cp.run(reclaimed.Token); got.State != "finished" || got.Outcome != string(work.OutcomeNoChangeNeeded) {
		t.Fatalf("the older worker's new attempt = %+v", got)
	}

	redispatched := map[string]bool{}
	var accounted string
	for range data.pendingIDs {
		claimed := cp.mustClaim(api, shiftlessTeam, "")
		if claimed.RunToken == gapFinished.Token || claimed.RunToken == gapHeld.Token {
			t.Fatalf("a re-dispatch reused a token minted in the gap: %s", claimed.RunToken)
		}
		redispatched[claimed.WorkItem.ExternalID] = true
		if accounted == "" {
			accounted = claimed.RunToken
			cp.issueLLMAccount(accounted, "ploeg-drill-after-upgrade")
		}
		cp.mustReport(api, claimed.RunToken, harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "re-dispatched after the upgrade"})
	}
	if !redispatched["21"] || !redispatched["22"] {
		t.Fatalf("re-dispatched %v, want both items the gap had claimed", redispatched)
	}
	if again, err := api.Claim(shiftlessTeam, ""); err != nil || again != nil {
		t.Fatalf("a claim found %+v %v; the item parked for a person must stay parked", again, err)
	}
	cp.settleLLMAccount(accounted, settledAfterTheUpgrade)
	if got, want := cp.spend(), (spendLedger{Settled: spendAtBackup.Settled + settledAfterTheUpgrade, ShiftSpent: spendAtBackup.ShiftSpent}); got.ShiftSpent != want.ShiftSpent || got.Settled-want.Settled > 1e-9 || want.Settled-got.Settled > 1e-9 {
		t.Fatalf("spend after settling a Run head dispatched = %+v, want %+v", got, want)
	}

	handshakeWithAFutureControlPlane(t, cp, release)
	if release.rollbackPromised {
		rollBackTo(t, &clock, cp, front, release, restored, lineage)
	}
	publishDrillSummary(t, clock.report())
}

func handshakeWithAFutureControlPlane(t *testing.T, cp *controlPlane, release *olderRelease) {
	t.Helper()
	future := twoMinorsAhead(cp.version)
	ahead := httptest.NewServer(cp.apiServer(future).Handler())
	t.Cleanup(ahead.Close)
	id := cp.shiftlessItem("41")
	runsBefore := cp.count(`SELECT count(*) FROM agent_runs WHERE work_item_id = $1`, id)
	events := release.exec(t, "run", "-api", ahead.URL, "-team", shiftlessTeam, "-worker-version", release.version,
		"-outcome", string(work.OutcomeNoChangeNeeded))
	if len(events) == 0 {
		t.Fatalf("the %s worker reported nothing against ploegd %s", release.tag, future)
	}
	if !release.sendsVersion {
		if events[0].Event != "claimed" {
			t.Fatalf("a %s worker, which predates the version handshake, got %+v from ploegd %s; the policy does not judge it", release.tag, events[0], future)
		}
		t.Logf("a %s worker predates the version handshake: ploegd %s served it, and only lease fencing guards it", release.tag, future)
		return
	}
	if events[0].Event != "claim_refused" || !strings.Contains(events[0].Err, "426") && !strings.Contains(events[0].Err, release.version) {
		t.Fatalf("the %s worker's claim against ploegd %s = %+v, want 426 naming its version", release.tag, future, events[0])
	}
	if got := cp.count(`SELECT count(*) FROM agent_runs WHERE work_item_id = $1`, id); got != runsBefore {
		t.Fatalf("the refused %s worker started %d Runs", release.tag, got-runsBefore)
	}
	if got := cp.itemState(id); got != string(work.StateQueued) {
		t.Fatalf("the item a refused worker asked for is %q, want queued", got)
	}
}

func rollBackTo(t *testing.T, clock *upgradeClock, cp *controlPlane, front *serviceFront, release *olderRelease, db *drillPostgres, lineage releaseLineage) {
	t.Helper()
	front.outage()
	var previous *olderPloegd
	timed(&clock.Rollback, func() {
		previous = release.serve(t, db.dsn)
		front.route(previous.proxy())
	})
	if got := appliedMigrations(t, db.pool); !sameNames(got, lineage.head) {
		t.Fatalf("the rollback to %s changed schema_migrations to %v", release.tag, got)
	}
	rolledBack := olderWorld{t: t, release: release, dsn: db.dsn, api: front.url}
	rolledBack.ingest(shiftlessTeam, "51", false)
	claimed := rolledBack.run(shiftlessTeam, "", "-outcome", string(work.OutcomeNoChangeNeeded))
	if got := cp.run(claimed.Token); got.State != "finished" || got.Outcome != string(work.OutcomeNoChangeNeeded) {
		t.Fatalf("a Run %s dispatched on the upgraded schema = %+v", release.tag, got)
	}
	previous.kill()
	if err := db.store.Migrate(context.Background()); err != nil {
		t.Fatalf("rolling forward after the rollback: %v", err)
	}
	cp.boot()
	cp.sweep()
	front.route(cp.through())
}
