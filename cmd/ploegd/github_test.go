package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"log/slog"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/config"
)

func githubConfig() *config.File {
	return &config.File{
		Trackers: config.Trackers{Github: config.TrackerConfig{Projects: []config.Project{{Name: "WebGrip/Ploeg", Repo: "webgrip/ploeg"}}}},
		Teams:    map[string]config.Team{"bronze": {}},
	}
}

func TestGitHubTrackerIsOffUntilConfigured(t *testing.T) {
	p, err := githubTracker(&config.File{}, githubTrackerEnv{}, slog.New(slog.DiscardHandler))
	if err != nil || p != nil {
		t.Fatalf("provider %v err %v; want no GitHub tracker without a secret, App or repository", p, err)
	}
}

func TestGitHubTrackerWiresRepositoriesTeamsAndTheApp(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	p, err := githubTracker(githubConfig(), githubTrackerEnv{secret: "s", appID: "12", privateKey: pemKey, teamMap: "Ploeg-Bot=bronze"},
		slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "github" || !p.Repositories["webgrip:ploeg"] || !p.Teams["bronze"] || p.TeamMap["ploeg-bot"] != "bronze" || p.Tokens == nil {
		t.Fatalf("provider %+v", p)
	}
}

func TestGitHubTrackerRefusesHalfAnApp(t *testing.T) {
	for name, env := range map[string]githubTrackerEnv{
		"id without key": {appID: "12"},
		"key without id": {privateKey: "-----BEGIN RSA PRIVATE KEY-----"},
		"unreadable key": {appID: "12", privateKey: "not pem"},
	} {
		if _, err := githubTracker(githubConfig(), env, slog.New(slog.DiscardHandler)); err == nil || !strings.Contains(err.Error(), "PLOEG_GITHUB_APP") {
			t.Errorf("%s: err %v; want a boot failure naming the App setting", name, err)
		}
	}
}
