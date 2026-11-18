package main

import (
	"log/slog"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/connection"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/github"
)

func TestRuntimeAppWebhooksAreWiredWithoutAStaticGitHubForge(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	svc := &connection.Service{}
	if hooks := appWebhooks(log, svc, map[string]provider.ForgeProvider{}); hooks["github"] == nil {
		t.Fatal("no receiver for runtime GitHub Apps without a static GitHub forge")
	}
	static := &github.Provider{Secret: "static"}
	other := &github.Provider{Secret: "static"}
	hooks := appWebhooks(log, svc, map[string]provider.ForgeProvider{"github": static, "ghe": other})
	if hooks["github"] == nil || static.AppSecret == nil || other.AppSecret == nil {
		t.Fatal("a static GitHub forge did not get the runtime Apps' secrets")
	}
	if appWebhooks(log, nil, nil) != nil {
		t.Fatal("receivers wired while runtime connections are off")
	}
}
