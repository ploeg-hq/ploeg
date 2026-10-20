package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func scratchDatabase(t *testing.T, name string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := testStore.pool.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testStore.pool.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	return fmt.Sprintf("postgresql://postgres:postgres@localhost:%d/%s?sslmode=disable", testPort, name)
}

func TestMigrationGivesUpOnAHeldLockInsteadOfQueueingTraffic(t *testing.T) {
	ctx := context.Background()
	dsn := scratchDatabase(t, "ploeg_migrate_lock_timeout")
	migrator, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrator.Close(context.Background()) })
	reader, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close(context.Background()) })

	first := fstest.MapFS{"0001_table.sql": {Data: []byte(`CREATE TABLE held (id BIGINT)`)}}
	if err := applyMigrations(ctx, migrator, first, time.Second); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	both := fstest.MapFS{
		"0001_table.sql":  first["0001_table.sql"],
		"0002_column.sql": {Data: []byte(`ALTER TABLE held ADD COLUMN note TEXT`)},
	}

	readerTx, err := reader.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readerTx.Exec(ctx, `SELECT count(*) FROM held`); err != nil {
		t.Fatal(err)
	}

	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	started := time.Now()
	err = applyMigrations(bounded, migrator, both, 200*time.Millisecond)
	waited := time.Since(started)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("migration behind a held lock returned %v after %s; want lock_not_available (55P03) after the lock timeout", err, waited)
	}
	if waited > 5*time.Second {
		t.Errorf("migration waited %s for the lock; want about the 200ms lock timeout", waited)
	}
	var recorded bool
	if err := migrator.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = '0002_column.sql')`).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded {
		t.Error("the migration that timed out is recorded as applied")
	}

	if err := readerTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := applyMigrations(ctx, migrator, both, 200*time.Millisecond); err != nil {
		t.Fatalf("retry after the reader finished: %v", err)
	}
	var sessionTimeout string
	if err := migrator.QueryRow(ctx, `SHOW lock_timeout`).Scan(&sessionTimeout); err != nil {
		t.Fatal(err)
	}
	if sessionTimeout != "0" {
		t.Errorf("lock_timeout leaked out of the migration transaction into the session: %q", sessionTimeout)
	}
}
