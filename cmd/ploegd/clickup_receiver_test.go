package main

import (
	"log/slog"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/connection"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/clickup"
	"github.com/ploeg-hq/ploeg/pkg/provider/vikunja"
)

func TestAClickUpReceiverExistsOnlyForRuntimeConnectionsWithoutAStaticClickUp(t *testing.T) {
	cfg := &config.File{}
	log := slog.New(slog.DiscardHandler)
	allowing := &connection.Service{Config: &connection.Config{Allow: []connection.Allow{{Port: "tracker", Kind: "clickup", Instance: "clickup", Methods: []string{"oauth"}}}}}
	other := &connection.Service{Config: &connection.Config{Allow: []connection.Allow{{Port: "forge", Kind: "gitlab", Instance: "gitlab", Methods: []string{"oauth"}}}}}
	only := map[string]provider.TrackerProvider{"vikunja": &vikunja.Provider{}}

	receiver, err := runtimeClickUpReceiver(cfg, allowing, only, log)
	if err != nil || receiver == nil || !receiver.ConnectionsOnly || receiver.Secret != "" || receiver.Token != "" {
		t.Fatalf("receiver = %+v, %v; want one without a static credential", receiver, err)
	}
	withStatic := map[string]provider.TrackerProvider{"clickup": &clickup.Provider{Secret: "s"}}
	for name, c := range map[string]struct {
		svc      *connection.Service
		trackers map[string]provider.TrackerProvider
	}{"static ClickUp configured": {allowing, withStatic}, "ClickUp not allowed": {other, only}, "no connections": {nil, only}} {
		if r, err := runtimeClickUpReceiver(cfg, c.svc, c.trackers, log); err != nil || r != nil {
			t.Errorf("%s: receiver = %+v, %v; want none", name, r, err)
		}
	}

	only["clickup"] = receiver
	if _, listed := staticTrackerConnections(cfg, only, nil)["clickup"]; listed {
		t.Fatal("the runtime receiver is listed as a static connection a binding may name")
	}
}
