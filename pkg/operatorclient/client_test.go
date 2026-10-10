package operatorclient_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"

	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/operatorclient"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

var testStore *store.Store

func TestMain(m *testing.M) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	port := uint32(l.Addr().(*net.TCPAddr).Port)
	_ = l.Close()
	dir, err := os.MkdirTemp("", "ploeg-operatorclient-epg-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	epg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().Port(port).RuntimePath(dir))
	if err := epg.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "embedded postgres failed to start: %v\n", err)
		os.Exit(1)
	}
	code := func() int {
		defer func() { _ = epg.Stop(); _ = os.RemoveAll(dir) }()
		ctx := context.Background()
		s, err := store.New(ctx, fmt.Sprintf("postgresql://postgres:postgres@localhost:%d/postgres?sslmode=disable", port))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer s.Close()
		if err := s.Migrate(ctx); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		testStore = s
		return m.Run()
	}()
	os.Exit(code)
}

func operatorServer(t *testing.T, teams []string) (*httptest.Server, string) {
	t.Helper()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(secret)
	raw, _ := json.Marshal([]map[string]any{{"name": "reader", "tokenEnv": "READER_TOKEN", "teams": teams, "execute": false}})
	consumers, err := httpapi.ParseOperatorConsumers(string(raw), func(name string) (string, bool) { return token, name == "READER_TOKEN" })
	if err != nil {
		t.Fatal(err)
	}
	s := &httpapi.Server{Store: testStore, OperatorConfig: httpapi.OperatorConfig{Consumers: consumers, Teams: map[string][]string{"silver": {"writer"}, "gold": {"writer"}}}}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, token
}

func TestClientReadsTheRealOperatorHandler(t *testing.T) {
	ctx := context.Background()
	visible, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "client-silver", Team: "silver", Title: "Visible to the client"})
	if err != nil {
		t.Fatal(err)
	}
	hidden, _, err := testStore.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: "client-gold", Team: "gold", Title: "Outside the consumer's teams"})
	if err != nil {
		t.Fatal(err)
	}
	srv, token := operatorServer(t, []string{"silver"})
	c, err := operatorclient.New(srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}

	teams, err := c.Teams(ctx)
	if err != nil || len(teams) != 1 || teams[0].ID != "silver" {
		t.Fatalf("teams = %+v, %v", teams, err)
	}
	summary, err := c.Summary(ctx, "24h")
	if err != nil || summary.Window != "24h" {
		t.Fatalf("summary = %+v, %v", summary, err)
	}
	page, err := c.WorkItems(ctx, operatorclient.ItemFilter{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].Title != "Visible to the client" {
		t.Fatalf("work items = %+v, %v", page, err)
	}
	byRef, err := c.WorkItems(ctx, operatorclient.ItemFilter{Provider: "vikunja", ExternalID: "client-silver"})
	if err != nil || len(byRef.Items) != 1 || byRef.Items[0].ID != fmt.Sprint(visible) {
		t.Fatalf("lookup by tracker reference = %+v, %v", byRef, err)
	}
	detail, err := c.WorkItem(ctx, fmt.Sprint(visible))
	if err != nil || detail.Item.ExternalID != "client-silver" {
		t.Fatalf("detail = %+v, %v", detail, err)
	}
	if _, err := c.WorkItem(ctx, fmt.Sprint(hidden)); !errors.Is(err, operatorclient.ErrNotFound) {
		t.Fatalf("another team's item: %v", err)
	}
	if _, err := c.Runs(ctx, operatorclient.RunFilter{Limit: 5}); err != nil {
		t.Fatal(err)
	}
	events, err := c.Events(ctx, operatorclient.EventFilter{WorkItemID: fmt.Sprint(visible), Desc: true, Limit: 5})
	if err != nil || events.LastCursor == "" {
		t.Fatalf("events = %+v, %v", events, err)
	}
	if _, err := c.WorkItems(ctx, operatorclient.ItemFilter{Team: "gold"}); !errors.Is(err, operatorclient.ErrForbidden) {
		t.Fatalf("other team filter: %v", err)
	}
}

func TestClientNeverDisclosesItsToken(t *testing.T) {
	srv, token := operatorServer(t, nil)
	wrong := strings.Repeat("x", 40)
	c, err := operatorclient.New(srv.URL, wrong)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Teams(context.Background())
	if !errors.Is(err, operatorclient.ErrUnauthorized) || strings.Contains(err.Error(), wrong) || strings.Contains(err.Error(), token) {
		t.Fatalf("unauthorized: %v", err)
	}

	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprintf(w, `{"error":{"code":"conflict","message":"saw %s"}}`, r.Header.Get("Authorization"))
	}))
	defer echo.Close()
	c, _ = operatorclient.New(echo.URL, wrong)
	_, err = c.Teams(context.Background())
	if !errors.Is(err, operatorclient.ErrConflict) || strings.Contains(err.Error(), wrong) {
		t.Fatalf("echoed token leaked: %v", err)
	}
}

func TestClientRefusesRedirectsAndBoundsTime(t *testing.T) {
	token := strings.Repeat("t", 40)
	var followed bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed = true }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	defer redirect.Close()
	c, _ := operatorclient.New(redirect.URL, token)
	var apiErr *operatorclient.Error
	if _, err := c.Teams(context.Background()); !errors.As(err, &apiErr) || apiErr.Status != http.StatusFound || followed {
		t.Fatalf("redirect: %v followed=%v", err, followed)
	}

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer slow.Close()
	c, _ = operatorclient.New(slow.URL, token)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.Teams(ctx); !errors.Is(err, operatorclient.ErrTimeout) {
		t.Fatalf("timeout: %v", err)
	}
}

func TestClientRejectsUnsafeConfiguration(t *testing.T) {
	token := strings.Repeat("t", 40)
	for _, base := range []string{"", "ftp://ploeg", "http://user:pass@ploeg", "http://ploeg?x=1", "ploeg:8080"} {
		if _, err := operatorclient.New(base, token); err == nil {
			t.Fatalf("accepted %q", base)
		}
	}
	for _, bad := range []string{"short", strings.Repeat("t", 40) + " x"} {
		if _, err := operatorclient.New("http://ploeg", bad); err == nil || strings.Contains(err.Error(), bad) {
			t.Fatalf("accepted or echoed token %q: %v", bad, err)
		}
	}
	c, _ := operatorclient.New("http://ploeg", token)
	if _, err := c.WorkItem(context.Background(), "../teams"); err == nil {
		t.Fatal("path injection accepted")
	}
}
