package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/ploeg-hq/ploeg/pkg/clauderoutine"
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

// claudeRoutines enables starting claude-environment Runs through the
// person's own routine (ADR-0197 section 7). A routine Run executes in the
// claude-runner, which may hold no credential, so ploegd refuses to start
// with routines on until the credential gateway is configured, and refuses a
// Role with carrier routine while routines are off.
func claudeRoutines(log *slog.Logger, harnesses httpapi.TeamHarnesses) (*httpapi.ClaudeRoutines, error) {
	on := os.Getenv("PLOEG_CLAUDE_ROUTINES") == "true"
	if !on {
		for team, roles := range harnesses {
			for role, h := range roles {
				if h.RoutineCarried() {
					return nil, fmt.Errorf("PLOEG_TEAM_HARNESSES: team %q Role %q has carrier routine, but PLOEG_CLAUDE_ROUTINES is not true", team, role)
				}
			}
		}
		return nil, nil
	}
	if os.Getenv("PLOEG_CREDENTIAL_GATEWAYS") == "" {
		return nil, errors.New("PLOEG_CLAUDE_ROUTINES needs the credential gateway (PLOEG_CREDENTIAL_GATEWAYS, ADR-0197): a routine Run's session runs in the claude-runner, which may hold no forge credential or environment secret")
	}
	log.Info("claude-environment runs start through the person's own routine, behind the credential gateway")
	return &httpapi.ClaudeRoutines{Client: &clauderoutine.Client{BaseURL: trimSlash(os.Getenv("PLOEG_CLAUDE_ROUTINES_URL"))}, GatewayMode: true}, nil
}
