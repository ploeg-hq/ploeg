package store

import (
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolConfig sizes the connection pool and sets the server-side limits of
// every connection it opens. A zero field leaves the pgx default. A setting
// the connection string names itself (pool_max_conns, application_name,
// statement_timeout, ...) wins over the field, so a hand-written DSN keeps
// working unchanged.
type PoolConfig struct {
	MaxConns                        int32
	MinIdleConns                    int32
	MaxConnLifetime                 time.Duration
	MaxConnLifetimeJitter           time.Duration
	MaxConnIdleTime                 time.Duration
	HealthCheckPeriod               time.Duration
	PingTimeout                     time.Duration
	ApplicationName                 string
	StatementTimeout                time.Duration
	IdleInTransactionSessionTimeout time.Duration
}

func poolConfig(databaseURL string, want PoolConfig) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	named, err := connectionStringSettings(databaseURL)
	if err != nil {
		return nil, err
	}
	unnamed := func(key string) bool { _, ok := named[key]; return !ok }

	if want.MaxConns > 0 && unnamed("pool_max_conns") {
		cfg.MaxConns = want.MaxConns
	}
	if want.MinIdleConns > 0 && unnamed("pool_min_idle_conns") {
		cfg.MinIdleConns = want.MinIdleConns
	}
	cfg.MinIdleConns = min(cfg.MinIdleConns, cfg.MaxConns)
	setDuration(&cfg.MaxConnLifetime, want.MaxConnLifetime, unnamed("pool_max_conn_lifetime"))
	setDuration(&cfg.MaxConnLifetimeJitter, want.MaxConnLifetimeJitter, unnamed("pool_max_conn_lifetime_jitter"))
	setDuration(&cfg.MaxConnIdleTime, want.MaxConnIdleTime, unnamed("pool_max_conn_idle_time"))
	setDuration(&cfg.HealthCheckPeriod, want.HealthCheckPeriod, unnamed("pool_health_check_period"))
	setDuration(&cfg.PingTimeout, want.PingTimeout, unnamed("pool_ping_timeout"))

	params := cfg.ConnConfig.RuntimeParams
	if want.ApplicationName != "" && unnamed("application_name") {
		params["application_name"] = want.ApplicationName
	}
	if want.StatementTimeout > 0 && unnamed("statement_timeout") {
		params["statement_timeout"] = milliseconds(want.StatementTimeout)
	}
	if want.IdleInTransactionSessionTimeout > 0 && unnamed("idle_in_transaction_session_timeout") {
		params["idle_in_transaction_session_timeout"] = milliseconds(want.IdleInTransactionSessionTimeout)
	}
	return cfg, nil
}

func connectionStringSettings(databaseURL string) (map[string]string, error) {
	conn, err := pgconn.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	return conn.RuntimeParams, nil
}

func setDuration(field *time.Duration, want time.Duration, unnamed bool) {
	if want > 0 && unnamed {
		*field = want
	}
}

func milliseconds(d time.Duration) string {
	return strconv.FormatInt(d.Milliseconds(), 10)
}

// PoolStat is a snapshot of the connection pool's counters, read without I/O.
func (s *Store) PoolStat() *pgxpool.Stat { return s.pool.Stat() }
