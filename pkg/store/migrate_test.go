package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

func TestConcurrentMigratorsApplyEachMigrationOnce(t *testing.T) {
	ctx := context.Background()
	const database = "ploeg_migrate_race"
	if _, err := testStore.pool.Exec(ctx, "CREATE DATABASE "+database); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testStore.pool.Exec(context.Background(), "DROP DATABASE IF EXISTS "+database+" WITH (FORCE)")
	})
	dsn := fmt.Sprintf("postgresql://postgres:postgres@localhost:%d/%s?sslmode=disable", testPort, database)

	const migrators = 2
	stores := make([]*Store, migrators)
	for i := range stores {
		s, err := New(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Ping(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Close)
		stores[i] = s
	}

	start := make(chan struct{})
	errs := make([]error, migrators)
	var wg sync.WaitGroup
	for i, s := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = s.Migrate(ctx)
		}()
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("migrator %d: %v", i, err)
		}
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	var recorded, distinct int
	if err := stores[0].pool.QueryRow(ctx,
		`SELECT count(*), count(DISTINCT name) FROM schema_migrations`).Scan(&recorded, &distinct); err != nil {
		t.Fatal(err)
	}
	if recorded != len(entries) || distinct != len(entries) {
		t.Errorf("schema_migrations holds %d rows for %d names; want each of the %d migrations once", recorded, distinct, len(entries))
	}

	var held int
	if err := stores[0].pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_locks l JOIN pg_database d ON d.oid = l.database
		 WHERE l.locktype = 'advisory' AND d.datname = $1`, database).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if held != 0 {
		t.Errorf("%d advisory locks still held after both migrators returned", held)
	}
}
