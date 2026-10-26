package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func bootUntilReady(t *testing.T, dbURL string) {
	t.Helper()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"keys":[],"total_count":0,"current_page":1,"total_pages":1}`)
	}))
	defer gateway.Close()

	addr := freeAddr(t)
	t.Setenv("PLOEG_LISTEN", addr)
	t.Setenv("PLOEG_DATABASE_URL", dbURL)
	t.Setenv("LITELLM_ADMIN_URL", gateway.URL)
	t.Setenv("LITELLM_MASTER_KEY", "sk-test-master")
	t.Setenv("PLOEG_WORKER_AUTH_MODE", "legacy")
	for _, unset := range []string{"PLOEG_WORKER_SIGNING_KEY", "PLOEG_WORKER_BOOTSTRAPS", "PLOEG_CONFIG", "PLOEG_FORGEJO_URL", "PLOEG_GITLAB_URL", "PLOEG_GITHUB_URL", "PLOEG_VIKUNJA_URL", "PLOEG_CLICKUP_SECRET", "PLOEG_CLICKUP_TOKEN"} {
		t.Setenv(unset, "")
		_ = os.Unsetenv(unset)
	}

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- run(ctx, slog.New(slog.DiscardHandler)) }()
	defer func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(15 * time.Second):
			t.Fatal("run did not stop after its context was cancelled")
		}
	}()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-stopped:
			t.Fatalf("run stopped before it was ready: %v", err)
		default:
		}
		if code, _, _, err := get(addr, "/readyz"); err == nil && code == http.StatusOK {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("run was not ready within 60s")
}

func execSQL(t *testing.T, dbURL, sql string) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(context.Background(), sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func TestBootGrantsObserverToARoleCreatedAfterTheMigrations(t *testing.T) {
	dbURL := startEmbeddedPostgres(t)
	bootUntilReady(t, dbURL)

	execSQL(t, dbURL, `CREATE ROLE ploeg_observer NOLOGIN`)
	bootUntilReady(t, dbURL)

	conn, err := pgx.Connect(context.Background(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	var readable bool
	if err := conn.QueryRow(context.Background(),
		`SELECT has_table_privilege('ploeg_observer', 'shifts', 'SELECT') AND has_column_privilege('ploeg_observer', 'work_items', 'state', 'SELECT')`).Scan(&readable); err != nil {
		t.Fatal(err)
	}
	if !readable {
		t.Fatal("a restart left ploeg_observer without read grants on shifts and work_items.state")
	}
}

func TestBootCompletesWhenTheObserverGrantFails(t *testing.T) {
	dbURL := startEmbeddedPostgres(t)
	bootUntilReady(t, dbURL)

	execSQL(t, dbURL, `CREATE OR REPLACE FUNCTION ploeg_grant_observer() RETURNS boolean LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'grant refused'; END $$`)
	bootUntilReady(t, dbURL)
}

type observerGranterFunc func(context.Context) (bool, error)

func (f observerGranterFunc) GrantObserver(ctx context.Context) (bool, error) { return f(ctx) }

func TestGrantObserverLogsWhatItDid(t *testing.T) {
	for _, tc := range []struct {
		name    string
		granted bool
		err     error
		want    string
	}{
		{"granted", true, nil, "ploeg_observer grants applied"},
		{"role absent", false, nil, "ploeg_observer role absent"},
		{"failed", false, errors.New("permission denied"), "ploeg_observer grants not applied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			grantObserver(context.Background(), observerGranterFunc(func(context.Context) (bool, error) {
				return tc.granted, tc.err
			}), slog.New(slog.NewTextHandler(&out, nil)))
			if !strings.Contains(out.String(), tc.want) {
				t.Errorf("log = %q, want it to contain %q", out.String(), tc.want)
			}
		})
	}
}
