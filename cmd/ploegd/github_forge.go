package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/forgebroker"
	"github.com/ploeg-hq/ploeg/pkg/githubapi"
	"github.com/ploeg-hq/ploeg/pkg/githubapp"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/github"
)

type githubForgeEnv struct {
	url, apiURL, secret, token, appID, appKey string
}

func githubForgeEnvFromOS() githubForgeEnv {
	return githubForgeEnv{
		url:    os.Getenv("PLOEG_GITHUB_URL"),
		apiURL: os.Getenv("PLOEG_GITHUB_API_URL"),
		secret: os.Getenv("PLOEG_GITHUB_FORGE_SECRET"),
		token:  os.Getenv("PLOEG_GITHUB_TOKEN"),
		appID:  strings.TrimSpace(os.Getenv("PLOEG_GITHUB_APP_ID")),
		appKey: os.Getenv("PLOEG_GITHUB_APP_PRIVATE_KEY"),
	}
}

func githubForge(log *slog.Logger, env githubForgeEnv) (*github.Provider, *githubapp.Minter, error) {
	webURL := trimSlash(env.url)
	if webURL == "" {
		return nil, nil, nil
	}
	apiURL := trimSlash(env.apiURL)
	if apiURL == "" {
		apiURL = githubapi.APIURL(webURL)
	}
	p := &github.Provider{BaseURL: webURL, APIURL: apiURL, Secret: env.secret, Log: log}
	if env.secret == "" {
		log.Warn("PLOEG_GITHUB_FORGE_SECRET is unset; every GitHub forge webhook is rejected")
	}
	if env.appID != "" || env.appKey != "" {
		if env.appID == "" || env.appKey == "" {
			return nil, nil, errors.New("PLOEG_GITHUB_APP_ID and PLOEG_GITHUB_APP_PRIVATE_KEY must be set together")
		}
		key, err := githubapp.ParsePrivateKey([]byte(env.appKey))
		if err != nil {
			return nil, nil, fmt.Errorf("PLOEG_GITHUB_APP_PRIVATE_KEY: %w", err)
		}
		p.Tokens = &githubapp.Minter{AppID: env.appID, Key: key, APIURL: apiURL, Permissions: githubapp.ForgePermissions()}
		log.Info("github forge authenticates as a GitHub App", "url", webURL, "api", apiURL)
		return p, &githubapp.Minter{AppID: env.appID, Key: key, APIURL: apiURL}, nil
	}
	if env.token != "" {
		p.Tokens = githubapi.StaticToken(env.token)
		log.Warn("github forge uses a static token (PLOEG_GITHUB_TOKEN): no per-Run push credential, and Ploeg cannot revoke it; prefer a GitHub App",
			"url", webURL)
		return p, nil, nil
	}
	log.Warn("github forge has no credential (PLOEG_GITHUB_APP_ID or PLOEG_GITHUB_TOKEN); it reads public repositories only", "url", webURL)
	return p, nil, nil
}

func withGitHubCredentials(log *slog.Logger, base forgebroker.Broker, baseSweeper forgebroker.Sweeper, app *githubapp.Minter,
	githubForges []string, defaultForge string) (forgebroker.Broker, forgebroker.Sweeper) {
	if app == nil {
		return base, baseSweeper
	}
	r := &forgebroker.Router{Default: base, DefaultSweeper: baseSweeper, GitHub: &forgebroker.GitHubApp{App: app},
		GitHubForges: githubForges, DefaultForge: defaultForge}
	log.Info("per-run GitHub App installation tokens enabled", "forges", githubForges)
	return r, r
}

func forgeIDsOf(forges map[string]provider.ForgeProvider, fp provider.ForgeProvider) []string {
	var ids []string
	for id, candidate := range forges {
		if candidate == fp {
			ids = append(ids, id)
		}
	}
	return ids
}
