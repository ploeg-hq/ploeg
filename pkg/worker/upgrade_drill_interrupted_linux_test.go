//go:build linux

package worker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const migrationLockTimeout = 5 * time.Second

type interruptionTarget struct {
	index        int
	name         string
	table        string
	midMigration bool
}

var sqlLineComment = regexp.MustCompile(`(?m)--.*$`)

func chooseInterruption(t *testing.T, root string, oldTables []string, pending []string) interruptionTarget {
	t.Helper()
	statements := map[string]string{}
	for _, name := range pending {
		sql, err := os.ReadFile(filepath.Join(root, migrationsDirectory, name))
		if err != nil {
			t.Fatal(err)
		}
		statements[name] = sqlLineComment.ReplaceAllString(string(sql), "")
	}
	firstMention := func(name, table string) int {
		at := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(table) + `\b`).FindStringIndex(statements[name])
		if at == nil {
			return -1
		}
		return at[0]
	}
	sort.Strings(oldTables)
	var order []int
	for k := len(pending) / 2; k < len(pending); k++ {
		order = append(order, k)
	}
	for k := len(pending)/2 - 1; k >= 1; k-- {
		order = append(order, k)
	}
	for _, needsEarlierStatement := range []bool{true, false} {
		for _, k := range order {
			for _, table := range oldTables {
				at := firstMention(pending[k], table)
				if at < 0 {
					continue
				}
				if end := strings.Index(statements[pending[k]], ";"); needsEarlierStatement && (end < 0 || end > at) {
					continue
				}
				untouched := true
				for _, earlier := range pending[:k] {
					if firstMention(earlier, table) >= 0 {
						untouched = false
						break
					}
				}
				if untouched {
					return interruptionTarget{index: k, name: pending[k], table: table, midMigration: needsEarlierStatement}
				}
			}
		}
	}
	t.Fatalf("no pending migration after the first touches a table the ones before it leave alone: %v", pending)
	return interruptionTarget{}
}

type interruptedUpgrade struct {
	t          *testing.T
	root       string
	binaries   string
	backup     string
	release    *olderRelease
	lineage    releaseLineage
	target     interruptionTarget
	oldColumns schemaColumns
	atBackup   tableRows
	reference  tableRows
	surviving  schemaColumns
	schema     string
}

func (u *interruptedUpgrade) restore(name string) *drillPostgres {
	u.t.Helper()
	data := filepath.Join(u.root, name+"-data")
	if err := restoreBaseBackup(u.backup, data); err != nil {
		u.t.Fatalf("restore %s: %v", name, err)
	}
	db := startDrillPostgres(u.t, u.binaries, filepath.Join(u.root, name+"-runtime"), data)
	db.awaitPromotion(u.t)
	poolOn(u.t, db)
	return db
}

func (u *interruptedUpgrade) blockVictim(db *drillPostgres) (pgx.Tx, *pgx.Conn) {
	u.t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, db.dsn)
	if err != nil {
		u.t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		u.t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE `+pgx.Identifier{u.target.table}.Sanitize()+` IN ACCESS EXCLUSIVE MODE`); err != nil {
		u.t.Fatal(err)
	}
	return tx, conn
}

func (u *interruptedUpgrade) waitingMigration(db *drillPostgres) int {
	u.t.Helper()
	for deadline := time.Now().Add(migrationLockTimeout); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		var pid int
		err := db.pool.QueryRow(context.Background(), `SELECT pid FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND pid <> pg_backend_pid() LIMIT 1`).Scan(&pid)
		if err == nil {
			return pid
		}
	}
	u.t.Fatalf("no migration waited on %s", u.target.table)
	return 0
}

func (u *interruptedUpgrade) migrateUntilInterrupted(db *drillPostgres, interrupt func(*drillPostgres) *drillPostgres) *drillPostgres {
	u.t.Helper()
	tx, conn := u.blockVictim(db)
	st := storeOn(u.t, db)
	done := make(chan error, 1)
	go func() { done <- st.Migrate(context.Background()) }()
	db = interrupt(db)
	var err error
	select {
	case err = <-done:
	case <-time.After(upgradeDrillWait):
		u.t.Fatal("the interrupted migration never returned")
	}
	if err == nil {
		u.t.Fatalf("the upgrade finished although %s was blocked", u.target.name)
	}
	u.t.Logf("interrupted: %v", err)
	_ = tx.Rollback(context.Background())
	_ = conn.Close(context.Background())
	st.Close()
	want := append(append([]string{}, u.release.migrations...), pendingMigrations(u.release.migrations, u.lineage.head)[:u.target.index]...)
	sort.Strings(want)
	if got := appliedMigrations(u.t, db.pool); !sameNames(got, want) {
		u.t.Fatalf("after the interruption schema_migrations holds %v, want every migration before %s and none after", got, u.target.name)
	}
	return db
}

func (u *interruptedUpgrade) rerunToHead(db *drillPostgres) string {
	u.t.Helper()
	var migrate, boot time.Duration
	var err error
	timed(&migrate, func() { err = storeOn(u.t, db).Migrate(context.Background()) })
	if err != nil {
		u.t.Fatalf("rerunning the upgrade after the interruption: %v", err)
	}
	if got := appliedMigrations(u.t, db.pool); !sameNames(got, u.lineage.head) {
		u.t.Fatalf("after the rerun schema_migrations holds %v, want head's", got)
	}
	if got := schemaFingerprint(u.t, db.pool); got != u.schema {
		u.t.Errorf("the rerun left a schema that differs from an uninterrupted upgrade:\n%s", fingerprintDiff(u.schema, got))
	}
	if !equalProjection(u.t, "the recovered database against an uninterrupted upgrade", u.reference, snapshotRows(u.t, db.pool, u.surviving, nil), u.surviving) {
		u.t.Fatal("the rerun upgrade holds different rows from an uninterrupted one")
	}
	cp := unbootedControlPlane(u.t, db.store, db.pool, upgradeDrillLeaseTTL)
	cp.version = u.lineage.headVersion
	timed(&boot, func() {
		cp.serve()
		cp.sweep()
	})
	api := cp.client()
	claimed := cp.mustClaim(api, shiftlessTeam, "")
	cp.mustReport(api, claimed.RunToken, harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "dispatched after the recovered upgrade"})
	return fmt.Sprintf("rerun recovered in %v (rerun migrate %v, ploegd boot and first sweep %v)",
		(migrate + boot).Round(time.Millisecond), migrate.Round(time.Millisecond), boot.Round(time.Millisecond))
}

func fingerprintDiff(want, got string) string {
	have := map[string]bool{}
	for _, line := range strings.Split(got, "\n") {
		have[line] = true
	}
	expected := map[string]bool{}
	var diff []string
	for _, line := range strings.Split(want, "\n") {
		expected[line] = true
		if !have[line] {
			diff = append(diff, "- "+line)
		}
	}
	for _, line := range strings.Split(got, "\n") {
		if !expected[line] {
			diff = append(diff, "+ "+line)
		}
	}
	return strings.Join(diff, "\n")
}

func TestUpgradeDrillAnInterruptedMigrationRecoversByRerunningOrByRestoringTheOlderRelease(t *testing.T) {
	lineage := upgradeDrillLineage(t)
	for _, release := range lineage.releases {
		t.Run(release.tag, func(t *testing.T) {
			pending := pendingMigrations(release.migrations, lineage.head)
			if len(pending) < 2 {
				t.Skipf("%s lacks %d of head's migrations; an interruption needs one applied and one pending", release.tag, len(pending))
			}
			interruptUpgradeFrom(t, lineage, release, pending)
		})
	}
}

func interruptUpgradeFrom(t *testing.T, lineage releaseLineage, release *olderRelease, pending []string) {
	ctx := context.Background()
	root := t.TempDir()
	u := &interruptedUpgrade{t: t, root: root, binaries: filepath.Join(root, "postgres-binaries"), backup: filepath.Join(root, "base-backup"),
		release: release, lineage: lineage}

	primary := startDrillPostgres(t, u.binaries, filepath.Join(root, "primary-runtime"), filepath.Join(root, "primary-data"))
	older := release.serve(t, primary.dsn)
	primaryPool := poolOn(t, primary)
	world := olderWorld{t: t, release: release, dsn: primary.dsn, api: older.url}
	world.populate()
	world.ingest(shiftlessTeam, "23", false)
	u.oldColumns = publicColumns(t, primaryPool)
	u.atBackup = snapshotRows(t, primaryPool, u.oldColumns, nil)
	if err := hotBaseBackup(ctx, primary.dsn, primary.data, u.backup); err != nil {
		t.Fatalf("base backup: %v", err)
	}
	older.kill()
	primary.destroy()

	tables := make([]string, 0, len(u.oldColumns))
	for table := range u.oldColumns {
		tables = append(tables, table)
	}
	u.target = chooseInterruption(t, lineage.root, tables, pending)
	t.Logf("interrupting %s (%d of %d pending) by locking %s; statements before it in the same migration: %v", u.target.name, u.target.index+1, len(pending), u.target.table, u.target.midMigration)

	reference := u.restore("reference")
	if err := storeOn(t, reference).Migrate(ctx); err != nil {
		t.Fatalf("uninterrupted upgrade: %v", err)
	}
	u.schema = schemaFingerprint(t, reference.pool)
	u.surviving = survivingColumns(t, u.oldColumns, publicColumns(t, reference.pool))
	u.reference = snapshotRows(t, reference.pool, u.surviving, nil)
	reference.destroy()

	var results []string
	t.Run("a lock wait past lock_timeout fails the migration and the next boot finishes it", func(t *testing.T) {
		u.t = t
		db := u.migrateUntilInterrupted(u.restore("lock-timeout"), func(db *drillPostgres) *drillPostgres { return db })
		results = append(results, "lock timeout: "+u.rerunToHead(db))
	})
	t.Run("ploegd killed mid-migration leaves the migration unapplied and the next boot finishes it", func(t *testing.T) {
		u.t = t
		db := u.migrateUntilInterrupted(u.restore("killed"), func(db *drillPostgres) *drillPostgres {
			pid := u.waitingMigration(db)
			if _, err := db.pool.Exec(context.Background(), `SELECT pg_terminate_backend($1)`, pid); err != nil {
				t.Fatal(err)
			}
			return db
		})
		results = append(results, "ploegd killed: "+u.rerunToHead(db))
	})
	t.Run("PostgreSQL crashing mid-migration recovers and the next boot finishes it", func(t *testing.T) {
		u.t = t
		db := u.migrateUntilInterrupted(u.restore("crashed"), func(db *drillPostgres) *drillPostgres {
			u.waitingMigration(db)
			if out, err := exec.Command(filepath.Join(u.binaries, "bin", "pg_ctl"), "stop", "-D", db.data, "-m", "immediate", "-w").CombinedOutput(); err != nil {
				t.Fatalf("crash PostgreSQL: %v: %s", err, out)
			}
			db.pool.Close()
			db.pool = nil
			restarted := startDrillPostgres(t, u.binaries, filepath.Join(u.root, "crashed-runtime-after"), db.data)
			poolOn(t, restarted)
			return restarted
		})
		results = append(results, "PostgreSQL crashed: "+u.rerunToHead(db))
	})
	t.Run("restoring the pre-upgrade backup puts the older release back in service", func(t *testing.T) {
		u.t = t
		u.migrateUntilInterrupted(u.restore("abandoned"), func(db *drillPostgres) *drillPostgres { return db }).destroy()
		var back *drillPostgres
		var previous *olderPloegd
		var files, recovery, start time.Duration
		timed(&files, func() {
			if err := restoreBaseBackup(u.backup, filepath.Join(u.root, "restored-again-data")); err != nil {
				t.Fatal(err)
			}
		})
		timed(&recovery, func() {
			back = startDrillPostgres(t, u.binaries, filepath.Join(u.root, "restored-again-runtime"), filepath.Join(u.root, "restored-again-data"))
			back.awaitPromotion(t)
		})
		timed(&start, func() { previous = release.serve(t, back.dsn) })
		poolOn(t, back)
		if got := appliedMigrations(t, back.pool); !sameNames(got, release.migrations) {
			t.Fatalf("the restored database holds migrations %v, want %s's own", got, release.tag)
		}
		if !equalProjection(t, "the database restored after an abandoned upgrade", u.atBackup, snapshotRows(t, back.pool, u.oldColumns, nil), u.oldColumns) {
			t.Fatal("the restore after an abandoned upgrade does not match the backup")
		}
		resumed := olderWorld{t: t, release: release, dsn: back.dsn, api: previous.url}
		claimed := resumed.run(shiftlessTeam, "", "-outcome", string(work.OutcomeNoChangeNeeded))
		var state string
		if err := back.pool.QueryRow(ctx, `SELECT state FROM agent_runs WHERE run_token = $1`, claimed.Token).Scan(&state); err != nil || state != "finished" {
			t.Fatalf("a Run %s dispatched after the restore is %q %v", release.tag, state, err)
		}
		results = append(results, fmt.Sprintf("restore to %s: served again in %v (restore files %v, postgres start and recovery %v, %s ploegd start %v)",
			release.tag, (files+recovery+start).Round(time.Millisecond), files.Round(time.Millisecond), recovery.Round(time.Millisecond), release.tag, start.Round(time.Millisecond)))
	})
	u.t = t
	publishDrillSummary(t, fmt.Sprintf("interrupted upgrade from %s at %s (%d of %d pending, %s locked): %s",
		release.tag, u.target.name, u.target.index+1, len(pending), u.target.table, strings.Join(results, "; ")))
}
