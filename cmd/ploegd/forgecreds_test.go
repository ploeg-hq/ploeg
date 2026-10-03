package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/forgebroker"
)

func TestForgeCredentials_BotPasswordEnablesPerRunMinting(t *testing.T) {
	creds, sweeper := forgeCredentialsFrom(slog.New(slog.DiscardHandler), forgeCredentialEnv{
		url: "https://forge.example/", sharedToken: "shared", bot: "robot", botPassword: "pw",
	})
	fb, ok := creds.(*forgebroker.Forgejo)
	if !ok || sweeper == nil {
		t.Fatalf("broker = %T, sweeper = %v; want the minting Forgejo broker and its sweeper", creds, sweeper)
	}
	if fb.BaseURL != "https://forge.example" || fb.Bot != "robot" || fb.Password != "pw" {
		t.Errorf("broker = %+v", *fb)
	}
}

func TestForgeCredentials_AnAdminTokenAloneKeepsTheSharedTokenAndSaysWhy(t *testing.T) {
	var logs bytes.Buffer
	creds, sweeper := forgeCredentialsFrom(slog.New(slog.NewTextHandler(&logs, nil)), forgeCredentialEnv{
		sharedToken: "shared", adminToken: "admin",
	})
	if s, ok := creds.(forgebroker.Static); !ok || s.Token != "shared" || sweeper != nil {
		t.Fatalf("broker = %#v, sweeper = %v; want the shared static token and no sweeper", creds, sweeper)
	}
	if !strings.Contains(logs.String(), "PLOEG_FORGEJO_BOT_PASSWORD") || !strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("no warning naming PLOEG_FORGEJO_BOT_PASSWORD:\n%s", logs.String())
	}
}
