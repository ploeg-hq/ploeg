package main

import (
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

var poolEnv = []string{
	"PLOEG_DB_MAX_CONNS", "PLOEG_DB_MIN_IDLE_CONNS", "PLOEG_DB_MAX_CONN_LIFETIME",
	"PLOEG_DB_MAX_CONN_LIFETIME_JITTER", "PLOEG_DB_MAX_CONN_IDLE_TIME", "PLOEG_DB_HEALTH_CHECK_PERIOD",
	"PLOEG_DB_PING_TIMEOUT", "PLOEG_DB_STATEMENT_TIMEOUT", "PLOEG_DB_IDLE_IN_TRANSACTION_TIMEOUT",
	"PLOEG_DB_APPLICATION_NAME",
}

func clearPoolEnv(t *testing.T) {
	t.Helper()
	for _, key := range poolEnv {
		t.Setenv(key, "")
	}
}

func TestPoolConfigDefaultsSizeThePoolForOneSmallDaemon(t *testing.T) {
	clearPoolEnv(t)
	got, err := poolConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	want := store.PoolConfig{
		MaxConns:                        8,
		MinIdleConns:                    2,
		MaxConnLifetime:                 30 * time.Minute,
		MaxConnLifetimeJitter:           5 * time.Minute,
		MaxConnIdleTime:                 5 * time.Minute,
		HealthCheckPeriod:               30 * time.Second,
		PingTimeout:                     5 * time.Second,
		ApplicationName:                 "ploegd",
		StatementTimeout:                30 * time.Second,
		IdleInTransactionSessionTimeout: 60 * time.Second,
	}
	if got != want {
		t.Errorf("defaults = %+v\nwant %+v", got, want)
	}
}

func TestPoolConfigReadsEveryVariable(t *testing.T) {
	clearPoolEnv(t)
	for key, value := range map[string]string{
		"PLOEG_DB_MAX_CONNS":                   "3",
		"PLOEG_DB_MIN_IDLE_CONNS":              "0",
		"PLOEG_DB_MAX_CONN_LIFETIME":           "1h",
		"PLOEG_DB_MAX_CONN_LIFETIME_JITTER":    "0s",
		"PLOEG_DB_MAX_CONN_IDLE_TIME":          "1m",
		"PLOEG_DB_HEALTH_CHECK_PERIOD":         "10s",
		"PLOEG_DB_PING_TIMEOUT":                "2s",
		"PLOEG_DB_STATEMENT_TIMEOUT":           "0",
		"PLOEG_DB_IDLE_IN_TRANSACTION_TIMEOUT": "2m",
		"PLOEG_DB_APPLICATION_NAME":            "ploegd-canary",
	} {
		t.Setenv(key, value)
	}
	got, err := poolConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	want := store.PoolConfig{
		MaxConns:                        3,
		MaxConnLifetime:                 time.Hour,
		MaxConnIdleTime:                 time.Minute,
		HealthCheckPeriod:               10 * time.Second,
		PingTimeout:                     2 * time.Second,
		ApplicationName:                 "ploegd-canary",
		IdleInTransactionSessionTimeout: 2 * time.Minute,
	}
	if got != want {
		t.Errorf("config = %+v\nwant %+v", got, want)
	}
}

func TestPoolConfigRefusesMalformedValues(t *testing.T) {
	for key, bad := range map[string]string{
		"PLOEG_DB_MAX_CONNS":         "-1",
		"PLOEG_DB_MIN_IDLE_CONNS":    "two",
		"PLOEG_DB_STATEMENT_TIMEOUT": "30",
		"PLOEG_DB_PING_TIMEOUT":      "-5s",
	} {
		clearPoolEnv(t)
		t.Setenv(key, bad)
		_, err := poolConfigFromEnv()
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s=%q: err = %v; want a refusal naming the variable", key, bad, err)
		}
	}
}
