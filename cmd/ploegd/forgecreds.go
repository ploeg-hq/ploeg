package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/forgebroker"
)

type forgeCredentialEnv struct {
	url, sharedToken, bot, botPassword, adminToken string
}

const forgeVersionCheckTimeout = 30 * time.Second

func forgeCredentials(ctx context.Context, log *slog.Logger) (forgebroker.Broker, forgebroker.Sweeper, error) {
	return forgeCredentialsFrom(ctx, log, forgeCredentialEnv{
		url:         os.Getenv("PLOEG_FORGEJO_URL"),
		sharedToken: os.Getenv("PLOEG_FORGEJO_TOKEN"),
		bot:         os.Getenv("PLOEG_FORGEJO_BOT"),
		botPassword: os.Getenv("PLOEG_FORGEJO_BOT_PASSWORD"),
		adminToken:  os.Getenv("PLOEG_FORGEJO_ADMIN_TOKEN"),
	})
}

func forgeCredentialsFrom(ctx context.Context, log *slog.Logger, env forgeCredentialEnv) (forgebroker.Broker, forgebroker.Sweeper, error) {
	if env.botPassword == "" {
		if env.adminToken != "" {
			log.Warn("PLOEG_FORGEJO_ADMIN_TOKEN is ignored: Forgejo creates access tokens only for a user signed in with a password; " +
				"set PLOEG_FORGEJO_BOT_PASSWORD (chart executor.forgejo.botPasswordSecret) to mint per-run forge credentials")
		}
		log.Info("per-run forge credentials disabled (PLOEG_FORGEJO_BOT_PASSWORD unset); workers use the shared token")
		return forgebroker.Static{Token: env.sharedToken}, nil, nil
	}
	bot := env.bot
	if bot == "" {
		bot = "agent-builder"
	}
	fb := &forgebroker.Forgejo{BaseURL: trimSlash(env.url), Bot: bot, Password: env.botPassword}
	versionCtx, cancel := context.WithTimeout(ctx, forgeVersionCheckTimeout)
	defer cancel()
	version, err := fb.RequireRepositoryTokens(versionCtx)
	if err != nil {
		return nil, nil, fmt.Errorf("per-run forge credentials (PLOEG_FORGEJO_BOT_PASSWORD is set): %w", err)
	}
	log.Info("per-run forge credentials enabled", "bot", fb.Bot, "forgejo_version", version)
	return fb, fb, nil
}
