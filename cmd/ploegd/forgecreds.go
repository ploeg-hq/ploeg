package main

import (
	"log/slog"
	"os"

	"github.com/ploeg-hq/ploeg/pkg/forgebroker"
)

type forgeCredentialEnv struct {
	url, sharedToken, bot, botPassword, adminToken string
}

func forgeCredentials(log *slog.Logger) (forgebroker.Broker, forgebroker.Sweeper) {
	return forgeCredentialsFrom(log, forgeCredentialEnv{
		url:         os.Getenv("PLOEG_FORGEJO_URL"),
		sharedToken: os.Getenv("PLOEG_FORGEJO_TOKEN"),
		bot:         os.Getenv("PLOEG_FORGEJO_BOT"),
		botPassword: os.Getenv("PLOEG_FORGEJO_BOT_PASSWORD"),
		adminToken:  os.Getenv("PLOEG_FORGEJO_ADMIN_TOKEN"),
	})
}

func forgeCredentialsFrom(log *slog.Logger, env forgeCredentialEnv) (forgebroker.Broker, forgebroker.Sweeper) {
	if env.botPassword == "" {
		if env.adminToken != "" {
			log.Warn("PLOEG_FORGEJO_ADMIN_TOKEN is ignored: Forgejo creates access tokens only for a user signed in with a password; " +
				"set PLOEG_FORGEJO_BOT_PASSWORD (chart executor.forgejo.botPasswordSecret) to mint per-run forge credentials")
		}
		log.Info("per-run forge credentials disabled (PLOEG_FORGEJO_BOT_PASSWORD unset); workers use the shared token")
		return forgebroker.Static{Token: env.sharedToken}, nil
	}
	bot := env.bot
	if bot == "" {
		bot = "agent-builder"
	}
	fb := &forgebroker.Forgejo{BaseURL: trimSlash(env.url), Bot: bot, Password: env.botPassword}
	log.Info("per-run forge credentials enabled", "bot", fb.Bot)
	return fb, fb
}
