package githubapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Bot is the GitHub user an App acts as: its login, <slug>[bot], and the
// user id GitHub gives that login.
type Bot struct {
	Login string
	ID    int64
}

// NoReplyEmail is the address GitHub attributes a commit to the bot by.
func (b Bot) NoReplyEmail() string {
	return fmt.Sprintf("%d+%s@users.noreply.github.com", b.ID, b.Login)
}

// Bot reads the App's slug with its JWT and the id of the bot user GitHub
// created for it.
func (m *Minter) Bot(ctx context.Context) (Bot, error) {
	var app struct {
		Slug string `json:"slug"`
	}
	if err := m.appCall(ctx, http.MethodGet, "/app", nil, &app); err != nil {
		return Bot{}, fmt.Errorf("githubapp: read the App: %w", err)
	}
	if app.Slug == "" {
		return Bot{}, fmt.Errorf("githubapp: GitHub returned the App without a slug")
	}
	login := app.Slug + "[bot]"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.apiURL()+"/users/"+url.PathEscape(login), nil)
	if err != nil {
		return Bot{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := m.client().Do(req)
	if err != nil {
		return Bot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Bot{}, fmt.Errorf("githubapp: read the bot user %s: HTTP %d: %s", login, resp.StatusCode, snippet)
	}
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&user); err != nil {
		return Bot{}, err
	}
	if user.ID == 0 {
		return Bot{}, fmt.Errorf("githubapp: GitHub returned the bot user %s without an id", login)
	}
	if user.Login != "" {
		login = user.Login
	}
	return Bot{Login: login, ID: user.ID}, nil
}
