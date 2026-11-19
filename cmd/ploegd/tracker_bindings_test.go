package main

import (
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/clickup"
	"github.com/ploeg-hq/ploeg/pkg/secretstore/secretstoretest"
)

func TestStaticClickUpConnectionRegistersListWebhooksWhenASecretStoreExists(t *testing.T) {
	t.Setenv("PLOEG_CLICKUP_WEBHOOK_URL", "")
	t.Setenv("PLOEG_PUBLIC_WEBHOOK_BASE", "https://ploeg.example")
	trackers := map[string]provider.TrackerProvider{"clickup": &clickup.Provider{}}
	cfg := &config.File{Trackers: config.Trackers{Clickup: config.TrackerConfig{Agents: []string{"agent"}}}}

	without := staticTrackerConnections(cfg, trackers, nil)["clickup"]
	if without.Secrets != nil || without.RegisterWebhooks {
		t.Fatalf("without a store: %+v", without)
	}
	with := staticTrackerConnections(cfg, trackers, secretstoretest.NewFake())["clickup"]
	if with.Secrets == nil || !with.RegisterWebhooks || with.WebhookEndpoint != "https://ploeg.example"+clickup.WebhookPath {
		t.Fatalf("with a store: %+v", with)
	}
}
