package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"log/slog"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/githubapp"
)

func forgeAppKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

func TestGitHubForgeIsOffWithoutAURL(t *testing.T) {
	p, app, err := githubForge(slog.New(slog.DiscardHandler), githubForgeEnv{token: "ghp_x", appID: "1"})
	if p != nil || app != nil || err != nil {
		t.Fatalf("provider %v app %v err %v without PLOEG_GITHUB_URL", p, app, err)
	}
}

func TestGitHubForgeRefusesHalfAnApp(t *testing.T) {
	for _, env := range []githubForgeEnv{
		{url: "https://github.com", appID: "123"},
		{url: "https://github.com", appKey: forgeAppKeyPEM(t)},
		{url: "https://github.com", appID: "123", appKey: "not a key"},
	} {
		if _, _, err := githubForge(slog.New(slog.DiscardHandler), env); err == nil {
			t.Errorf("%+v was accepted", env)
		}
	}
}

func TestGitHubForgeUsesTheSharedAppOverAStaticToken(t *testing.T) {
	p, app, err := githubForge(slog.New(slog.DiscardHandler), githubForgeEnv{url: "https://github.com/", appID: "123", appKey: forgeAppKeyPEM(t),
		token: "ghp_fallback", secret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if app == nil || app.APIURL != "https://api.github.com" || p.BaseURL != "https://github.com" || len(app.Permissions) != 0 {
		t.Fatalf("app %+v provider %+v", app, p)
	}
	tokens, ok := p.Tokens.(*githubapp.Minter)
	if !ok || tokens.Permissions["contents"] != "read" || tokens.Permissions["pull_requests"] != "write" {
		t.Fatalf("tokens = %#v, want App tokens with the forge permissions", p.Tokens)
	}
	p, app, _ = githubForge(slog.New(slog.DiscardHandler), githubForgeEnv{url: "https://ghe.example.com", token: "github_pat_x"})
	if app != nil || p.APIURL != "https://ghe.example.com/api/v3" {
		t.Fatalf("app %v api %q", app, p.APIURL)
	}
	if tok, _ := p.Tokens.Token(context.Background(), "a", "b"); tok != "github_pat_x" {
		t.Fatalf("static token = %q", tok)
	}
}
