//go:build linux

package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const (
	restoreDrillLeaseTTL = 5 * time.Second
	restoreDrillLabel    = "ploeg-restore-drill"
	settledBeforeBackup  = 1.2345
	settledInTheGap      = 0.75
)

type drillPostgres struct {
	epg   *embeddedpostgres.EmbeddedPostgres
	data  string
	dsn   string
	store *store.Store
	pool  *pgxpool.Pool
}

func startDrillPostgres(t *testing.T, binaries, runtime, data string) *drillPostgres {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint32(listener.Addr().(*net.TCPAddr).Port)
	_ = listener.Close()
	epg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().Encoding("UTF8").Port(port).
		BinariesPath(binaries).RuntimePath(runtime).DataPath(data).Logger(io.Discard))
	if err := epg.Start(); err != nil {
		t.Fatalf("embedded postgres on %s failed to start: %v", data, err)
	}
	db := &drillPostgres{epg: epg, data: data, dsn: fmt.Sprintf("postgresql://postgres:postgres@localhost:%d/postgres?sslmode=disable", port)}
	t.Cleanup(db.destroy)
	return db
}

func (db *drillPostgres) open(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	var err error
	if db.store, err = store.New(ctx, db.dsn, store.PoolConfig{}); err != nil {
		t.Fatal(err)
	}
	if err = db.store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if db.pool, err = pgxpool.New(ctx, db.dsn); err != nil {
		t.Fatal(err)
	}
}

func (db *drillPostgres) awaitPromotion(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, db.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(10 * time.Millisecond) {
		var recovering bool
		if err := conn.QueryRow(ctx, `SELECT pg_is_in_recovery()`).Scan(&recovering); err != nil {
			t.Fatal(err)
		}
		if !recovering {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the restored database never left recovery")
		}
	}
}

func (db *drillPostgres) destroy() {
	if db.epg == nil {
		return
	}
	if db.store != nil {
		db.store.Close()
	}
	if db.pool != nil {
		db.pool.Close()
	}
	_ = db.epg.Stop()
	_ = os.RemoveAll(db.data)
	db.epg = nil
}

func hotBaseBackup(ctx context.Context, dsn, data, backup string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `SELECT pg_backup_start($1, true)`, restoreDrillLabel); err != nil {
		return err
	}
	if err := copyDataDirectory(data, backup); err != nil {
		return err
	}
	var label, tablespaceMap string
	if err := conn.QueryRow(ctx, `SELECT labelfile, spcmapfile FROM pg_backup_stop(false)`).Scan(&label, &tablespaceMap); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(backup, "backup_label"), []byte(label), 0o600); err != nil {
		return err
	}
	if tablespaceMap != "" {
		if err := os.WriteFile(filepath.Join(backup, "tablespace_map"), []byte(tablespaceMap), 0o600); err != nil {
			return err
		}
	}
	return copyDataDirectory(filepath.Join(data, "pg_wal"), filepath.Join(backup, "pg_wal"))
}

var notInABaseBackup = map[string]bool{"postmaster.pid": true, "postmaster.opts": true}

func copyDataDirectory(from, to string) error {
	return filepath.WalkDir(from, func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		if notInABaseBackup[rel] {
			return nil
		}
		target := filepath.Join(to, rel)
		info, err := entry.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		return copyDataFile(path, target, info.Mode().Perm())
	})
}

func copyDataFile(from, to string, mode fs.FileMode) error {
	src, err := os.Open(from)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(to, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	return dst.Close()
}

func restoreBaseBackup(backup, data string) error {
	if err := copyDataDirectory(backup, data); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(data, "recovery.signal"), nil, 0o600); err != nil {
		return err
	}
	auto, err := os.OpenFile(filepath.Join(data, "postgresql.auto.conf"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := auto.WriteString("restore_command = 'false'\nrecovery_target_action = 'promote'\n"); err != nil {
		_ = auto.Close()
		return err
	}
	return auto.Close()
}

var authorityTables = map[string]string{
	"work_items":       `SELECT id, provider, external_id, team, state, attempts, infra_failures FROM work_items ORDER BY id`,
	"shifts":           `SELECT id, work_item_id, spent, closed_at FROM shifts ORDER BY id`,
	"agent_runs":       `SELECT id, run_token, work_item_id, state, outcome, failure_reason, expires_at FROM agent_runs ORDER BY id`,
	"leases":           `SELECT run_token, work_item_id, expires_at FROM leases ORDER BY work_item_id`,
	"run_llm_accounts": `SELECT run_token, alias, state, gateway_key_id, settled_spend, reconciled_spend FROM run_llm_accounts ORDER BY run_token`,
	"checkpoints":      `SELECT id, work_item_id, phase, branch FROM checkpoints ORDER BY id`,
	"audit_log":        `SELECT id, actor, action, work_item_id, detail, at FROM audit_log ORDER BY id`,
}

type authoritySnapshot map[string]string

func (cp *controlPlane) authority() authoritySnapshot {
	cp.t.Helper()
	snapshot := authoritySnapshot{}
	for table, query := range authorityTables {
		var rows string
		if err := cp.pool.QueryRow(context.Background(),
			`SELECT COALESCE(jsonb_agg(to_jsonb(t)), '[]'::jsonb)::text FROM (`+query+`) t`).Scan(&rows); err != nil {
			cp.t.Fatalf("%s: %v", table, err)
		}
		snapshot[table] = rows
	}
	return snapshot
}

func (cp *controlPlane) auditUpTo(id int64) string {
	cp.t.Helper()
	var rows string
	if err := cp.pool.QueryRow(context.Background(), `SELECT COALESCE(jsonb_agg(to_jsonb(t)), '[]'::jsonb)::text FROM (
		SELECT id, actor, action, work_item_id, detail, at FROM audit_log WHERE id <= $1 ORDER BY id) t`, id).Scan(&rows); err != nil {
		cp.t.Fatal(err)
	}
	return rows
}

type spendLedger struct {
	Settled    float64
	ShiftSpent float64
}

func (cp *controlPlane) spend() spendLedger {
	cp.t.Helper()
	var l spendLedger
	if err := cp.pool.QueryRow(context.Background(), `SELECT
		COALESCE((SELECT sum(settled_spend) FROM run_llm_accounts WHERE state = 'reconciled'), 0)::float8,
		COALESCE((SELECT sum(spent) FROM shifts), 0)::float8`).Scan(&l.Settled, &l.ShiftSpent); err != nil {
		cp.t.Fatal(err)
	}
	return l
}

func (cp *controlPlane) issueLLMAccount(runToken, alias string) {
	cp.t.Helper()
	ctx := context.Background()
	if err := cp.store.ReserveLLMAccount(ctx, store.LLMAccount{RunToken: runToken, Alias: alias, Authorized: 2, Models: []string{"drill-model"}, TTLSeconds: 3600}); err != nil {
		cp.t.Fatalf("reserve %s: %v", alias, err)
	}
	if _, err := cp.store.BeginLLMMint(ctx, runToken); err != nil {
		cp.t.Fatalf("mint %s: %v", alias, err)
	}
	if err := cp.store.RecordLLMIssued(ctx, runToken, "key-"+alias); err != nil {
		cp.t.Fatalf("issue %s: %v", alias, err)
	}
}

func (cp *controlPlane) settleLLMAccount(runToken string, spend float64) {
	cp.t.Helper()
	ctx := context.Background()
	if err := cp.store.RecordLLMBlocked(ctx, runToken, &spend); err != nil {
		cp.t.Fatal(err)
	}
	if settled, err := cp.store.SettleLLMAccount(ctx, runToken, store.LLMSettlement{Spend: spend, Evidence: "restore drill spend log", CostKnown: true}); err != nil || !settled {
		cp.t.Fatalf("settle %s: %v %v", runToken, settled, err)
	}
}

func (cp *controlPlane) mustClaim(client *APIClient, team, role string) *ClaimResponse {
	cp.t.Helper()
	claimed, err := client.Claim(team, role)
	if err != nil || claimed == nil {
		cp.t.Fatalf("claim %s/%s: %+v %v", team, role, claimed, err)
	}
	return claimed
}

func (cp *controlPlane) mustReport(client *APIClient, runToken string, rep harness.OutcomeReport) {
	cp.t.Helper()
	if err := client.Outcome(runToken, rep); err != nil {
		cp.t.Fatalf("outcome for %s: %v", runToken, err)
	}
}

func (cp *controlPlane) refusesEverything(client *APIClient, runToken, externalID string) {
	cp.t.Helper()
	if renewal, err := client.Renew(context.Background(), runToken); err != nil || !renewal.Gone {
		cp.t.Errorf("renewal with %s: %+v %v, want gone", runToken, renewal, err)
	}
	wantHTTPStatus(cp.t, "checkpoint with "+runToken, client.Checkpoint(runToken, work.Checkpoint{Phase: "branch_created", Branch: "agent/vik-" + externalID}), http.StatusNotFound)
	wantHTTPStatus(cp.t, "outcome with "+runToken, client.Outcome(runToken, harness.OutcomeReport{
		Outcome: work.OutcomePROpened, Summary: "a report from before the restore", Links: []string{"https://forge.invalid/pr/1"}}), http.StatusNotFound)
}

type restoreClock struct {
	BackupStarted, BackupPoint, LastLostWrite, Destroyed time.Time
	FilesRestored, DatabaseUp, Migrated, Serving         time.Time
}

func (c restoreClock) rpo() time.Duration { return c.Destroyed.Sub(c.BackupPoint) }

func (c restoreClock) rto() time.Duration { return c.Serving.Sub(c.Destroyed) }

func (c restoreClock) report() string {
	return fmt.Sprintf("restore drill: RTO %v (files %v, postgres recovery %v, migrate %v, ploegd boot and first sweep %v); "+
		"RPO %v (backup point to destruction; last lost write %v after the backup point); base backup took %v",
		c.rto().Round(time.Millisecond), c.FilesRestored.Sub(c.Destroyed).Round(time.Millisecond),
		c.DatabaseUp.Sub(c.FilesRestored).Round(time.Millisecond), c.Migrated.Sub(c.DatabaseUp).Round(time.Millisecond),
		c.Serving.Sub(c.Migrated).Round(time.Millisecond), c.rpo().Round(time.Millisecond),
		c.LastLostWrite.Sub(c.BackupPoint).Round(time.Millisecond), c.BackupPoint.Sub(c.BackupStarted).Round(time.Millisecond))
}

func publishDrillSummary(t *testing.T, line string) {
	t.Helper()
	t.Log(line)
	path := os.Getenv("GITHUB_STEP_SUMMARY")
	if path == "" {
		return
	}
	summary, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Errorf("step summary: %v", err)
		return
	}
	defer summary.Close()
	_, _ = fmt.Fprintf(summary, "%s\n", line)
}

func TestRestoreDrillReconcilesToTheBackupPointAndFencesEveryOlderLease(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	binaries := filepath.Join(root, "postgres-binaries")
	primary := startDrillPostgres(t, binaries, filepath.Join(root, "primary-runtime"), filepath.Join(root, "primary-data"))
	primary.open(t)
	cp := unbootedControlPlane(t, primary.store, primary.pool, restoreDrillLeaseTTL)
	cp.serve()
	api := cp.client()

	settledID := cp.plannedItem("3")
	settled := cp.mustClaim(api, leaseTeam, leaseRole)
	cp.issueLLMAccount(settled.RunToken, "ploeg-drill-settled")
	cp.mustReport(api, settled.RunToken, harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "settled before the backup"})
	cp.settleLLMAccount(settled.RunToken, settledBeforeBackup)

	parkedID := cp.shiftlessItem("5")
	parked := cp.mustClaim(api, shiftlessTeam, "")
	cp.mustReport(api, parked.RunToken, harness.OutcomeReport{Outcome: work.OutcomeStuck, Summary: "needs a person", StuckReason: "ambiguous ticket"})

	pendingIDs := []int64{cp.shiftlessItem("21"), cp.shiftlessItem("22")}

	leasedID := cp.plannedItem("7")
	old := startWorker(t, cp, newWriterForge(t, false, pullsNone), leaseTeam, leaseRole)
	cp.issueLLMAccount(old.token, "ploeg-drill-in-flight")
	if err := api.Checkpoint(old.token, work.Checkpoint{Phase: "branch_created", Branch: writerBranch}); err != nil {
		t.Fatalf("the in-flight Run's checkpoint: %v", err)
	}
	old.renew()

	settledState := cp.itemState(settledID)
	if settledState == string(work.StateActive) || settledState == string(work.StateQueued) {
		t.Fatalf("the settled item is %q before the backup, want its Shift closed", settledState)
	}
	atBackup := cp.authority()
	spendAtBackup := cp.spend()
	var auditAtBackup int64
	if err := cp.pool.QueryRow(ctx, `SELECT max(id) FROM audit_log`).Scan(&auditAtBackup); err != nil {
		t.Fatal(err)
	}
	var leaseDeadline time.Time
	if err := cp.pool.QueryRow(ctx, `SELECT expires_at FROM leases WHERE run_token = $1`, old.token).Scan(&leaseDeadline); err != nil {
		t.Fatal(err)
	}

	var clock restoreClock
	backup := filepath.Join(root, "base-backup")
	clock.BackupStarted = time.Now()
	if err := hotBaseBackup(ctx, primary.dsn, primary.data, backup); err != nil {
		t.Fatalf("base backup: %v", err)
	}
	clock.BackupPoint = time.Now()
	if !leaseDeadline.After(clock.BackupPoint) {
		t.Fatalf("the in-flight lease ended at %v, before the backup point %v: the backup holds no live lease to fence", leaseDeadline, clock.BackupPoint)
	}

	old.renew()
	gapFinished := cp.mustClaim(api, shiftlessTeam, "")
	cp.issueLLMAccount(gapFinished.RunToken, "ploeg-drill-gap-finished")
	cp.mustReport(api, gapFinished.RunToken, harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "finished in the gap"})
	cp.settleLLMAccount(gapFinished.RunToken, settledInTheGap)
	gapHeld := cp.mustClaim(api, shiftlessTeam, "")
	cp.issueLLMAccount(gapHeld.RunToken, "ploeg-drill-gap-held")
	if err := api.Checkpoint(gapHeld.RunToken, work.Checkpoint{Phase: "branch_created", Branch: "agent/vik-" + gapHeld.WorkItem.ExternalID}); err != nil {
		t.Fatalf("the gap Run's checkpoint: %v", err)
	}
	if _, _, err := cp.store.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "99", Team: shiftlessTeam, Title: "assigned in the gap"}); err != nil {
		t.Fatal(err)
	}
	clock.LastLostWrite = time.Now()
	if lost := cp.spend(); lost.Settled-spendAtBackup.Settled < settledInTheGap-1e-9 {
		t.Fatalf("the gap settled %v, want %v more than the backup's %v", lost.Settled, settledInTheGap, spendAtBackup.Settled)
	}

	old.net.cut.Store(true)
	primary.destroy()
	clock.Destroyed = time.Now()
	if _, err := os.Stat(primary.data); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the primary's data directory survived its destruction: %v", err)
	}

	restoredData := filepath.Join(root, "restored-data")
	if err := restoreBaseBackup(backup, restoredData); err != nil {
		t.Fatalf("restore: %v", err)
	}
	clock.FilesRestored = time.Now()
	restored := startDrillPostgres(t, binaries, filepath.Join(root, "restored-runtime"), restoredData)
	restored.awaitPromotion(t)
	clock.DatabaseUp = time.Now()
	restored.open(t)
	clock.Migrated = time.Now()
	cp.store, cp.pool = restored.store, restored.pool

	if got := cp.authority(); !equalAuthority(t, atBackup, got) {
		t.Fatal("the restored database does not match the backup point")
	}
	cp.boot()
	cp.sweep()
	clock.Serving = time.Now()

	if got := cp.spend(); got != spendAtBackup {
		t.Fatalf("settled spend after the restore %+v, want the backup point's %+v", got, spendAtBackup)
	}
	if spendAtBackup.Settled < settledBeforeBackup-1e-9 || spendAtBackup.ShiftSpent < settledBeforeBackup-1e-9 {
		t.Fatalf("the backup point holds %+v, want the %v settled before it", spendAtBackup, settledBeforeBackup)
	}

	if wait := time.Until(leaseDeadline); wait > 0 {
		time.Sleep(wait + 100*time.Millisecond)
	}

	before := cp.authority()
	cp.refusesEverything(api, old.token, "7")
	cp.refusesEverything(api, gapFinished.RunToken, gapFinished.WorkItem.ExternalID)
	cp.refusesEverything(api, gapHeld.RunToken, gapHeld.WorkItem.ExternalID)
	if late := api.Outcome(settled.RunToken, harness.OutcomeReport{Outcome: work.OutcomeFailed, Summary: "a replayed report that disagrees"}); late == nil {
		t.Error("a settled pre-backup Run accepted a second, different outcome")
	}
	if after := cp.authority(); !equalAuthority(t, before, after) {
		t.Fatal("a token from before the restore changed authoritative state")
	}

	cp.sweep()
	for _, id := range append([]int64{leasedID}, pendingIDs...) {
		cp.skipInfraBackoff(id)
	}
	if n := cp.count(`SELECT count(*) FROM agent_runs WHERE state = 'running' AND expires_at < now()`); n != 0 {
		t.Errorf("%d overdue Runs after the sweep, want 0", n)
	}
	if n := cp.count(`SELECT count(*) FROM leases WHERE expires_at < now()`); n != 0 {
		t.Errorf("%d overdue leases after the sweep, want 0", n)
	}
	if got := cp.run(old.token); got != (runRow{State: "finished", Outcome: string(work.OutcomeFailed), FailureReason: string(work.FailureLeaseLost)}) {
		t.Errorf("the pre-backup in-flight Run = %+v, want the sweeper's lease-lost verdict", got)
	}
	for id, want := range map[int64]work.State{settledID: work.State(settledState), parkedID: work.StateNeedsHuman, pendingIDs[0]: work.StateQueued, pendingIDs[1]: work.StateQueued} {
		if got := cp.itemState(id); got != string(want) {
			t.Errorf("item %d is %q after the restore, want %q", id, got, want)
		}
	}
	if n := cp.count(`SELECT count(*) FROM work_items w WHERE w.state = 'active'
		AND NOT EXISTS (SELECT 1 FROM agent_runs r WHERE r.work_item_id = w.id AND r.state IN ('pending', 'running'))`); n != 0 {
		t.Errorf("%d items active with no live or pending Run after the restore", n)
	}
	if n := cp.count(`SELECT count(*) FROM work_items WHERE external_id = '99'`); n != 0 {
		t.Errorf("the item assigned in the gap survived the restore")
	}
	if cp.auditUpTo(auditAtBackup) != atBackup["audit_log"] {
		t.Error("the audit trail up to the backup point changed during reconciliation")
	}

	fresh := cp.mustClaim(api, leaseTeam, leaseRole)
	if fresh.WorkItem.ExternalID != "7" || fresh.RunToken == old.token {
		t.Fatalf("re-dispatch after the restore = %+v", fresh)
	}
	redispatched := map[string]bool{}
	for range pendingIDs {
		claimed := cp.mustClaim(api, shiftlessTeam, "")
		if claimed.RunToken == gapFinished.RunToken || claimed.RunToken == gapHeld.RunToken {
			t.Fatalf("a re-dispatch reused a token minted in the gap: %s", claimed.RunToken)
		}
		redispatched[claimed.WorkItem.ExternalID] = true
	}
	if !redispatched["21"] || !redispatched["22"] {
		t.Fatalf("re-dispatched %v, want both items the gap had claimed", redispatched)
	}
	if again, err := api.Claim(shiftlessTeam, ""); err != nil || again != nil {
		t.Fatalf("a claim found %+v %v; the item parked for a person must stay parked", again, err)
	}

	held := cp.fencedState(leasedID, old.token, fresh.RunToken)
	old.net.cut.Store(false)
	old.tick()
	old.awaitStop()
	if !errors.Is(old.adapter.cause, errLeaseLost) {
		t.Fatalf("the pre-backup worker stopped for %v, want a lost lease", old.adapter.cause)
	}
	if err := old.result(); err == nil || !strings.Contains(err.Error(), "outcome report failed") {
		t.Fatalf("the pre-backup worker's report was not refused: %v", err)
	}
	if after := cp.fencedState(leasedID, old.token, fresh.RunToken); after != held {
		t.Fatalf("the pre-backup worker changed authority after the restore:\nbefore %+v\nafter  %+v", held, after)
	}
	cp.mustReport(api, fresh.RunToken, harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "the restored item finished under its new attempt"})
	if got := cp.run(fresh.RunToken); got.State != "finished" || got.Outcome != string(work.OutcomeNoChangeNeeded) {
		t.Fatalf("the re-dispatched attempt = %+v", got)
	}

	publishDrillSummary(t, clock.report())
}

func equalAuthority(t *testing.T, want, got authoritySnapshot) bool {
	t.Helper()
	same := true
	for table, rows := range want {
		if got[table] != rows {
			t.Errorf("%s differs:\nwant %s\ngot  %s", table, rows, got[table])
			same = false
		}
	}
	return same
}
