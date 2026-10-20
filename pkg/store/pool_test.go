package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var ploegdPool = PoolConfig{
	MaxConns:                        8,
	MinIdleConns:                    2,
	MaxConnLifetime:                 30 * time.Minute,
	MaxConnLifetimeJitter:           5 * time.Minute,
	MaxConnIdleTime:                 5 * time.Minute,
	HealthCheckPeriod:               30 * time.Second,
	PingTimeout:                     5 * time.Second,
	ApplicationName:                 "ploegd",
	StatementTimeout:                30 * time.Second,
	IdleInTransactionSessionTimeout: time.Minute,
}

func TestPoolConfigAppliesWhatTheURLLeavesUnset(t *testing.T) {
	cfg, err := poolConfig("postgres://u:p@db.example:5432/ploeg?sslmode=disable", ploegdPool)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConns != 8 || cfg.MinIdleConns != 2 || cfg.MaxConnLifetime != 30*time.Minute ||
		cfg.MaxConnLifetimeJitter != 5*time.Minute || cfg.MaxConnIdleTime != 5*time.Minute ||
		cfg.HealthCheckPeriod != 30*time.Second || cfg.PingTimeout != 5*time.Second {
		t.Errorf("pool settings not applied: %+v", cfg)
	}
	params := cfg.ConnConfig.RuntimeParams
	for key, want := range map[string]string{
		"application_name":                    "ploegd",
		"statement_timeout":                   "30000",
		"idle_in_transaction_session_timeout": "60000",
	} {
		if params[key] != want {
			t.Errorf("runtime param %s = %q, want %q", key, params[key], want)
		}
	}
}

func TestPoolConfigLetsTheURLWin(t *testing.T) {
	url := "postgres://u:p@db.example:5432/ploeg?sslmode=disable" +
		"&pool_max_conns=3&pool_min_idle_conns=1&pool_max_conn_lifetime=2h&pool_max_conn_lifetime_jitter=1m" +
		"&pool_max_conn_idle_time=10m&pool_health_check_period=2m&pool_ping_timeout=1s" +
		"&application_name=hand-written&statement_timeout=5s&idle_in_transaction_session_timeout=7s"
	cfg, err := poolConfig(url, ploegdPool)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConns != 3 || cfg.MinIdleConns != 1 || cfg.MaxConnLifetime != 2*time.Hour ||
		cfg.MaxConnLifetimeJitter != time.Minute || cfg.MaxConnIdleTime != 10*time.Minute ||
		cfg.HealthCheckPeriod != 2*time.Minute || cfg.PingTimeout != time.Second {
		t.Errorf("a URL pool setting was overridden: %+v", cfg)
	}
	params := cfg.ConnConfig.RuntimeParams
	for key, want := range map[string]string{
		"application_name":                    "hand-written",
		"statement_timeout":                   "5s",
		"idle_in_transaction_session_timeout": "7s",
	} {
		if params[key] != want {
			t.Errorf("runtime param %s = %q, want the URL's %q", key, params[key], want)
		}
	}
}

func TestPoolConfigZeroKeepsPgxDefaults(t *testing.T) {
	url := "postgres://u:p@db.example:5432/ploeg?sslmode=disable"
	cfg, err := poolConfig(url, PoolConfig{})
	if err != nil {
		t.Fatal(err)
	}
	def, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConns != def.MaxConns || cfg.MaxConnLifetime != def.MaxConnLifetime || cfg.MinIdleConns != 0 {
		t.Errorf("zero PoolConfig changed the pool: %+v", cfg)
	}
	for _, key := range []string{"application_name", "statement_timeout", "idle_in_transaction_session_timeout"} {
		if _, ok := cfg.ConnConfig.RuntimeParams[key]; ok {
			t.Errorf("zero PoolConfig set runtime param %s", key)
		}
	}
}

func TestPoolConfigNeverKeepsMoreIdleConnectionsThanItMayOpen(t *testing.T) {
	cfg, err := poolConfig("postgres://u:p@db.example:5432/ploeg?pool_max_conns=1", ploegdPool)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MinIdleConns != 1 {
		t.Errorf("MinIdleConns = %d with MaxConns 1", cfg.MinIdleConns)
	}
}

func TestPoolHonoursMaxConnsAndSessionSettings(t *testing.T) {
	ctx := context.Background()
	dsn := fmt.Sprintf("postgresql://postgres:postgres@localhost:%d/postgres?sslmode=disable", testPort)
	s, err := New(ctx, dsn, PoolConfig{MaxConns: 2, ApplicationName: "ploegd-pool-test", StatementTimeout: 30 * time.Second, IdleInTransactionSessionTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	held := make([]*pgxpool.Conn, 0, 2)
	t.Cleanup(func() {
		for _, c := range held {
			c.Release()
		}
	})
	for range 2 {
		c, err := s.pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if c, err := s.pool.Acquire(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		if c != nil {
			c.Release()
		}
		t.Fatalf("third acquire on a pool of 2: err = %v, want it to wait until its context ends", err)
	}
	if st := s.PoolStat(); st.MaxConns() != 2 || st.TotalConns() != 2 || st.CanceledAcquireCount() != 1 {
		t.Errorf("stat: max %d total %d canceled %d", st.MaxConns(), st.TotalConns(), st.CanceledAcquireCount())
	}

	var app, statement, idleInTx string
	if err := held[0].QueryRow(ctx,
		`SELECT current_setting('application_name'), current_setting('statement_timeout'), current_setting('idle_in_transaction_session_timeout')`).
		Scan(&app, &statement, &idleInTx); err != nil {
		t.Fatal(err)
	}
	if app != "ploegd-pool-test" || statement != "30s" || idleInTx != "1min" {
		t.Errorf("session settings: application_name %q statement_timeout %q idle_in_transaction_session_timeout %q", app, statement, idleInTx)
	}
}

func TestMigrateIsNotCutOffByTheStatementTimeout(t *testing.T) {
	ctx := context.Background()
	const database = "ploeg_migrate_timeout"
	if _, err := testStore.pool.Exec(ctx, "CREATE DATABASE "+database); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testStore.pool.Exec(context.Background(), "DROP DATABASE IF EXISTS "+database+" WITH (FORCE)")
	})
	dsn := fmt.Sprintf("postgresql://postgres:postgres@localhost:%d/%s?sslmode=disable", testPort, database)
	s, err := New(ctx, dsn, PoolConfig{MaxConns: 1, StatementTimeout: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if _, err := s.pool.Exec(ctx, `SELECT pg_sleep(0.05)`); err == nil {
		t.Fatal("the pool's 1ms statement timeout did not apply")
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate under a 1ms statement timeout: %v", err)
	}
	var timeout string
	if err := s.pool.QueryRow(ctx, `SELECT current_setting('statement_timeout')`).Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if timeout != "1ms" {
		t.Errorf("statement_timeout after Migrate = %q; the migration connection went back to the pool without its timeout", timeout)
	}
}
