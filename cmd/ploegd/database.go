package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

func poolConfigFromEnv() (store.PoolConfig, error) {
	var cfg store.PoolConfig
	errs := make([]error, 9)
	cfg.MaxConns, errs[0] = connectionCount("PLOEG_DB_MAX_CONNS", 8)
	cfg.MinIdleConns, errs[1] = connectionCount("PLOEG_DB_MIN_IDLE_CONNS", 2)
	cfg.MaxConnLifetime, errs[2] = poolDuration("PLOEG_DB_MAX_CONN_LIFETIME", 30*time.Minute)
	cfg.MaxConnLifetimeJitter, errs[3] = poolDuration("PLOEG_DB_MAX_CONN_LIFETIME_JITTER", 5*time.Minute)
	cfg.MaxConnIdleTime, errs[4] = poolDuration("PLOEG_DB_MAX_CONN_IDLE_TIME", 5*time.Minute)
	cfg.HealthCheckPeriod, errs[5] = poolDuration("PLOEG_DB_HEALTH_CHECK_PERIOD", 30*time.Second)
	cfg.PingTimeout, errs[6] = poolDuration("PLOEG_DB_PING_TIMEOUT", 5*time.Second)
	cfg.StatementTimeout, errs[7] = poolDuration("PLOEG_DB_STATEMENT_TIMEOUT", 30*time.Second)
	cfg.IdleInTransactionSessionTimeout, errs[8] = poolDuration("PLOEG_DB_IDLE_IN_TRANSACTION_TIMEOUT", 60*time.Second)
	cfg.ApplicationName = envOr("PLOEG_DB_APPLICATION_NAME", "ploegd")
	if err := errors.Join(errs...); err != nil {
		return store.PoolConfig{}, err
	}
	return cfg, nil
}

func connectionCount(key string, def int32) (int32, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s: want a connection count of 0 or more, got %q", key, v)
	}
	return int32(n), nil
}

func poolDuration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s: want a Go duration of 0 or more, got %q", key, v)
	}
	return d, nil
}
