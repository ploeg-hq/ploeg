package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/githubapp"
	"github.com/ploeg-hq/ploeg/pkg/provider/githubissues"
)

type githubTrackerEnv struct {
	secret, apiURL, appID, privateKey, labelPrefix, teamMap string
}

func githubTrackerEnvFromOS() githubTrackerEnv {
	return githubTrackerEnv{
		secret:      os.Getenv("PLOEG_GITHUB_TRACKER_SECRET"),
		apiURL:      trimSlash(os.Getenv("PLOEG_GITHUB_API_URL")),
		appID:       strings.TrimSpace(os.Getenv("PLOEG_GITHUB_APP_ID")),
		privateKey:  os.Getenv("PLOEG_GITHUB_APP_PRIVATE_KEY"),
		labelPrefix: os.Getenv("PLOEG_GITHUB_TEAM_LABEL_PREFIX"),
		teamMap:     os.Getenv("PLOEG_GITHUB_TEAM_MAP"),
	}
}

func githubTracker(cfg *config.File, env githubTrackerEnv, log *slog.Logger) (*githubissues.Provider, error) {
	repositories := cfg.GitHubRepositories()
	if env.secret == "" && env.appID == "" && len(repositories) == 0 {
		return nil, nil
	}
	p := &githubissues.Provider{
		Secret:          env.secret,
		APIURL:          env.apiURL,
		Repositories:    repositories,
		TeamLabelPrefix: env.labelPrefix,
		TeamMap:         parseTeamMap(env.teamMap),
		Log:             log,
	}
	if len(cfg.Teams) > 0 {
		p.Teams = map[string]bool{}
		for name := range cfg.Teams {
			p.Teams[name] = true
		}
	}
	if env.appID != "" || env.privateKey != "" {
		if env.appID == "" || env.privateKey == "" {
			return nil, errors.New("PLOEG_GITHUB_APP_ID and PLOEG_GITHUB_APP_PRIVATE_KEY must be set together")
		}
		key, err := githubapp.ParsePrivateKey([]byte(env.privateKey))
		if err != nil {
			return nil, fmt.Errorf("PLOEG_GITHUB_APP_PRIVATE_KEY: %w", err)
		}
		p.Tokens = &githubapp.Minter{AppID: env.appID, Key: key, APIURL: env.apiURL,
			Permissions: githubapp.Permissions{"issues": "write", "metadata": "read"}}
	}
	if p.Secret == "" {
		log.Warn("PLOEG_GITHUB_TRACKER_SECRET is unset; every GitHub Issues webhook is rejected")
	}
	if len(repositories) == 0 {
		log.Warn("no trackers.github.projects configured; every GitHub issue event is ignored")
	}
	log.Info("github issues tracker configured", "repositories", len(repositories), "write_backs", p.Tokens != nil,
		"api", p.TrackerAPIBaseURL())
	return p, nil
}
