package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

var (
	testStore *store.Store
	testPool  *pgxpool.Pool
)

func TestMain(m *testing.M) {
	os.Exit(withTestDatabase(m))
}

func withTestDatabase(m *testing.M) int {
	dir, err := os.MkdirTemp("", "ploeg-ploegd-epg-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	port := freeTestPort()
	epg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().Encoding("UTF8").Port(port).RuntimePath(dir))
	if err := epg.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "embedded postgres failed to start: %v\n", err)
		return 1
	}
	defer func() { _ = epg.Stop(); _ = os.RemoveAll(dir) }()
	ctx := context.Background()
	dsn := fmt.Sprintf("postgresql://postgres:postgres@localhost:%d/postgres?sslmode=disable", port)
	s, err := store.New(ctx, dsn, store.PoolConfig{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer pool.Close()
	testStore, testPool = s, pool
	return m.Run()
}

func freeTestPort() uint32 {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	defer l.Close()
	return uint32(l.Addr().(*net.TCPAddr).Port)
}

type hangingGateway struct {
	*httptest.Server
	entered chan struct{}
}

func newHangingGateway(t *testing.T) *hangingGateway {
	g := &hangingGateway{entered: make(chan struct{}, 1)}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case g.entered <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	t.Cleanup(g.Close)
	return g
}

type spendLogBroker struct{ gateway string }

func (b spendLogBroker) Mint(_ context.Context, r llmbroker.MintRequest) (llmbroker.Credential, error) {
	return llmbroker.Credential{APIKey: "fixture-inference-" + r.RunToken[:12], Alias: "ploeg-" + r.RunToken[:12]}, nil
}
func (spendLogBroker) Revoke(context.Context, llmbroker.Credential) error   { return nil }
func (spendLogBroker) RevokeForRun(context.Context, string) error           { return nil }
func (spendLogBroker) SpendForRun(context.Context, string) (float64, error) { return 0, nil }
func (b spendLogBroker) SettledSpendForRun(ctx context.Context, _ string, _ []string) (llmbroker.SettledSpend, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.gateway+"/spend/logs", nil)
	if err != nil {
		return llmbroker.SettledSpend{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return llmbroker.SettledSpend{}, err
	}
	resp.Body.Close()
	return llmbroker.SettledSpend{}, nil
}

func openShift(t *testing.T, externalID string, role store.Role) {
	t.Helper()
	ctx := context.Background()
	id, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: externalID, Team: "bronze", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	shiftID, err := testStore.OpenShift(ctx, id, "bronze", "agent/vik-"+externalID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.OpenRound(ctx, shiftID, 0, []store.Role{role}); err != nil {
		t.Fatal(err)
	}
}

func blockedAccountAwaitingSettlement(t *testing.T, control *httpapi.LLMControl) {
	t.Helper()
	ctx := context.Background()
	openShift(t, "settles", store.Role{Name: "reviewer", Cap: 1})
	run, err := testStore.ClaimRole(ctx, "bronze", "reviewer", time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.Reserve(ctx, run.RunToken); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Issue(ctx, run.RunToken); err != nil {
		t.Fatal(err)
	}
	if _, err := testStore.ReportOutcome(ctx, run.RunToken, store.Report(work.OutcomeNoChangeNeeded, "done", "", nil, []byte(`{"costUsd":0}`), nil)); err != nil {
		t.Fatal(err)
	}
	if err := control.Block(ctx, run.RunToken); err != nil {
		t.Fatal(err)
	}
}

func runState(t *testing.T, token string) string {
	t.Helper()
	var state string
	if err := testPool.QueryRow(context.Background(), `SELECT state FROM agent_runs WHERE run_token=$1`, token).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestSweepExpiresAWriterWhileSettlementWaitsOnTheGateway(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gateway := newHangingGateway(t)
	control, err := httpapi.NewLLMControl(testStore, spendLogBroker{gateway: gateway.URL},
		`[{"team":"bronze","role":"reviewer","budgetUsd":1,"models":["trusted-model"],"ttl":"1h"}]`)
	if err != nil {
		t.Fatal(err)
	}
	blockedAccountAwaitingSettlement(t, control)
	openShift(t, "expires", store.Role{Name: "builder", Writes: true, Cap: 1})
	writer, err := testStore.ClaimRole(ctx, "bronze", "builder", 2*time.Second, 1)
	if err != nil {
		t.Fatal(err)
	}

	server := &httpapi.Server{Store: testStore, LLMControl: control, Log: slog.New(slog.DiscardHandler)}
	var loops backgroundLoops
	startSweepLoops(ctx, &loops, slog.New(slog.DiscardHandler), testStore, nil, nil, nil, server, 100*time.Millisecond, 0)
	stopped := make(chan struct{})
	go func() {
		loops.Wait()
		close(stopped)
	}()

	select {
	case <-gateway.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("settlement never reached the gateway")
	}
	deadline := time.Now().Add(6 * time.Second)
	for runState(t, writer.RunToken) == "running" {
		if time.Now().After(deadline) {
			t.Fatal("the writer's Run outlived its deadline while settlement waited on the gateway")
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the sweep loops did not stop after their context ended")
	}
}
