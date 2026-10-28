package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/forgebroker"
)

func forgeReportingVersion(t *testing.T, version string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/version" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"version":"` + version + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestForgeCredentials_BotPasswordEnablesPerRunMintingOnForgejo15(t *testing.T) {
	forgeURL := forgeReportingVersion(t, "15.0.10+gitea-1.22.0")
	var logs bytes.Buffer
	creds, sweeper, err := forgeCredentialsFrom(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), forgeCredentialEnv{
		url: forgeURL + "/", sharedToken: "shared", bot: "robot", botPassword: "pw",
	})
	if err != nil {
		t.Fatalf("forgeCredentialsFrom: %v", err)
	}
	fb, ok := creds.(*forgebroker.Forgejo)
	if !ok || sweeper == nil {
		t.Fatalf("broker = %T, sweeper = %v; want the minting Forgejo broker and its sweeper", creds, sweeper)
	}
	if fb.BaseURL != forgeURL || fb.Bot != "robot" || fb.Password != "pw" {
		t.Errorf("broker = %+v", *fb)
	}
	if !strings.Contains(logs.String(), "forgejo_version=15.0.10+gitea-1.22.0") {
		t.Errorf("the start log does not name the forge version:\n%s", logs.String())
	}
}

func TestForgeCredentials_RefusesTheBrokerOnAForgeOlderThan15AndNamesTheVersion(t *testing.T) {
	creds, sweeper, err := forgeCredentialsFrom(context.Background(), slog.New(slog.DiscardHandler), forgeCredentialEnv{
		url: forgeReportingVersion(t, "14.0.2+gitea-1.22.0"), sharedToken: "shared", botPassword: "pw",
	})
	if !errors.Is(err, forgebroker.ErrForgeTooOld) || !strings.Contains(err.Error(), "14.0.2+gitea-1.22.0") {
		t.Fatalf("forgeCredentialsFrom = %v, want ErrForgeTooOld naming 14.0.2+gitea-1.22.0", err)
	}
	if creds != nil || sweeper != nil {
		t.Errorf("broker = %T, sweeper = %v; want neither, not a fallback to the shared token", creds, sweeper)
	}
}

func TestForgeCredentials_AnAdminTokenAloneKeepsTheSharedTokenAndSaysWhy(t *testing.T) {
	var logs bytes.Buffer
	creds, sweeper, err := forgeCredentialsFrom(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), forgeCredentialEnv{
		sharedToken: "shared", adminToken: "admin",
	})
	if s, ok := creds.(forgebroker.Static); err != nil || !ok || s.Token != "shared" || sweeper != nil {
		t.Fatalf("broker = %#v, sweeper = %v; want the shared static token and no sweeper", creds, sweeper)
	}
	if !strings.Contains(logs.String(), "PLOEG_FORGEJO_BOT_PASSWORD") || !strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("no warning naming PLOEG_FORGEJO_BOT_PASSWORD:\n%s", logs.String())
	}
}
