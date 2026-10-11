package worker

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const forgeCredentialRefreshMargin = 15 * time.Minute

type forgeTokenSource interface {
	current(ctx context.Context) string
}

type staticForgeToken string

func (s staticForgeToken) current(context.Context) string { return string(s) }

type forgeCredential struct {
	mu        sync.Mutex
	token     string
	expiresAt time.Time
	refresh   func(context.Context) (string, time.Time, error)
	guard     func(string)
	log       *slog.Logger
	now       func() time.Time
}

func (c *forgeCredential) current(ctx context.Context) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	if c.refresh == nil || c.expiresAt.IsZero() || now().Add(forgeCredentialRefreshMargin).Before(c.expiresAt) {
		return c.token
	}
	token, expiresAt, err := c.refresh(ctx)
	if err != nil || token == "" {
		if c.log != nil {
			c.log.Warn("could not refresh the Run's expiring forge credential; keeping the current one",
				"expires_at", c.expiresAt, "err", err)
		}
		return c.token
	}
	if c.guard != nil {
		c.guard(token)
	}
	c.token, c.expiresAt = token, expiresAt
	if c.log != nil {
		c.log.Info("refreshed the Run's expiring forge credential", "expires_at", expiresAt)
	}
	return token
}

func gitBasicUser(token string) string {
	for _, prefix := range []string{"ghs_", "ghp_", "gho_", "ghu_", "github_pat_"} {
		if strings.HasPrefix(token, prefix) {
			return "x-access-token"
		}
	}
	return forgeGitUser
}
