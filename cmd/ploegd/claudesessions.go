package main

import (
	"log/slog"
	"os"

	"github.com/ploeg-hq/ploeg/pkg/claudesession"
	"github.com/ploeg-hq/ploeg/pkg/httpapi"
)

func claudeSessions(log *slog.Logger) *httpapi.ClaudeSessions {
	if os.Getenv("PLOEG_CLAUDE_ENVIRONMENTS") != "true" {
		return nil
	}
	bot := os.Getenv("PLOEG_FORGEJO_BOT")
	if bot == "" {
		bot = "agent-builder"
	}
	verifier := &claudesession.Verifier{JWKSURL: os.Getenv("PLOEG_CLAUDE_SESSION_JWKS_URL")}
	log.Info("runs in self-hosted claude environments enabled", "git_user", bot)
	return &httpapi.ClaudeSessions{Verifier: verifier, GitUser: bot, ForgeURL: trimSlash(os.Getenv("PLOEG_FORGEJO_URL")),
		SessionTimeout: durationOr("PLOEG_CLAUDE_SESSION_TIMEOUT", httpapi.DefaultClaudeSessionTimeout)}
}
