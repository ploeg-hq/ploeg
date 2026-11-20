package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/connection"
	"github.com/ploeg-hq/ploeg/pkg/secretstore/secretstoretest"
	"github.com/ploeg-hq/ploeg/pkg/trackerbinding"
)

func TestEveryClickUpConnectionsOwnAccountHandsWorkOver(t *testing.T) {
	ctx := context.Background()
	for _, table := range []string{"connections", "connection_flows"} {
		if _, err := testPool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v2/user", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"id": 42, "username": "Ploeg Bot"}})
	})
	mux.HandleFunc("GET /api/v2/team", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"teams": []any{}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := &connection.Config{Enabled: true,
		Allow: []connection.Allow{{Port: "tracker", Kind: "clickup", Instance: "clickup-cloud", Methods: []string{"token"}}},
		Instances: map[string]connection.Instance{
			"clickup-cloud": {Kind: "clickup", URL: srv.URL, APIURL: srv.URL + "/api", AllowInsecureHTTP: true},
		}}
	instances, err := cfg.Resolve(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	svc := &connection.Service{Store: testStore, Secrets: secretstoretest.NewFake(), Config: cfg, Instances: instances, Connectors: connectors()}
	caller := connection.Caller{Consumer: "workbench", Person: "person-1", Via: "test"}
	started, e := svc.Start(ctx, caller, connection.StartRequest{Port: "tracker", Kind: "clickup", Instance: "clickup-cloud", Method: "token", Label: "Dispatch member"})
	if e != nil {
		t.Fatalf("start: %+v", e)
	}
	done, e := svc.Complete(ctx, caller, connection.CompleteRequest{State: started.Flow.State, Token: "pk_dispatch"})
	if e != nil || done.Connection == nil {
		t.Fatalf("complete: %+v %+v", done, e)
	}

	conns := clickupConnections{runtime: runtimeTrackerConnections{svc: svc, st: testStore}, reg: &trackerbinding.Registry{}}
	agents := conns.HandOverAgents(ctx, "42")
	if len(agents) != 1 || agents[0].ID != done.Connection.ID || agents[0].Agent != "42" {
		t.Fatalf("hand-over agents for the connection's account = %+v; want the connection, with no option set", agents)
	}
	if others := conns.HandOverAgents(ctx, "7"); len(others) != 0 {
		t.Fatalf("hand-over agents for another member = %+v; want none", others)
	}
	bound, ok := runtimeTrackerConnections{svc: svc, st: testStore}.TrackerConnection(ctx, done.Connection.ID)
	if !ok || !bound.HandOverAgent || len(bound.Agents) != 1 || bound.Agents[0] != "Ploeg Bot" {
		t.Fatalf("binding view of the connection = %+v, %v; want its own account as the hand-over agent", bound, ok)
	}
}
