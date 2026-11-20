// Package forgeidentity reads the git identity a forge credential commits
// as, so a Run's commits name the account that pushes them (ADR-0206).
package forgeidentity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/githubapi"
	"github.com/ploeg-hq/ploeg/pkg/githubapp"
	"github.com/ploeg-hq/ploeg/pkg/httpclient"
)

// Identity is a git author and committer.
type Identity struct {
	Name  string
	Email string
}

// Source reads the identity the credential for one repository commits as.
type Source func(ctx context.Context, owner, repo string) (Identity, error)

// TokenFor returns the forge credential for one repository.
type TokenFor func(ctx context.Context, owner, repo string) (string, error)

// FromToken reads the account a token belongs to from the forge's own user
// endpoint: Forgejo's and GitLab's GET /user, and GitHub's GET /user for a
// personal or fine-grained token.
func FromToken(dialect, webURL string, token TokenFor, hc *http.Client) Source {
	return func(ctx context.Context, owner, repo string) (Identity, error) {
		t, err := token(ctx, owner, repo)
		if err != nil {
			return Identity{}, err
		}
		if t == "" {
			return Identity{}, errors.New("forgeidentity: no credential to read the account of")
		}
		return readUser(ctx, client(hc), dialect, strings.TrimRight(webURL, "/"), t)
	}
}

// FromGitHubApp is the App's bot user and its noreply address.
func FromGitHubApp(app *githubapp.Minter) Source {
	return func(ctx context.Context, _, _ string) (Identity, error) {
		bot, err := app.Bot(ctx)
		if err != nil {
			return Identity{}, err
		}
		return Identity{Name: bot.Login, Email: bot.NoReplyEmail()}, nil
	}
}

type user struct {
	ID          int64  `json:"id"`
	Login       string `json:"login"`
	Username    string `json:"username"`
	Name        string `json:"name"`
	FullName    string `json:"full_name"`
	Email       string `json:"email"`
	CommitEmail string `json:"commit_email"`
	PublicEmail string `json:"public_email"`
}

func readUser(ctx context.Context, hc *http.Client, dialect, webURL, token string) (Identity, error) {
	req, err := userRequest(ctx, dialect, webURL, token)
	if err != nil {
		return Identity{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Identity{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Identity{}, fmt.Errorf("forgeidentity: %s GET %s: HTTP %d: %s", dialect, req.URL.Path, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	var u user
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&u); err != nil {
		return Identity{}, fmt.Errorf("forgeidentity: %s user: %w", dialect, err)
	}
	return identityOf(dialect, u)
}

func userRequest(ctx context.Context, dialect, webURL, token string) (*http.Request, error) {
	var endpoint string
	header, value := "Authorization", "token "+token
	switch dialect {
	case "forgejo":
		endpoint = webURL + "/api/v1/user"
	case "gitlab":
		endpoint = webURL + "/api/v4/user"
		header, value = "PRIVATE-TOKEN", token
	case "github":
		endpoint = githubapi.APIURL(webURL) + "/user"
		value = "Bearer " + token
	default:
		return nil, fmt.Errorf("forgeidentity: forge dialect %q has no user endpoint", dialect)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(header, value)
	req.Header.Set("Accept", "application/json")
	return req, nil
}

func identityOf(dialect string, u user) (Identity, error) {
	login := firstOf(u.Login, u.Username)
	id := Identity{Name: firstOf(u.FullName, u.Name, login)}
	switch dialect {
	case "github":
		if login != "" && u.ID != 0 {
			id.Email = fmt.Sprintf("%d+%s@users.noreply.github.com", u.ID, login)
		}
	case "gitlab":
		id.Email = firstOf(u.CommitEmail, u.Email, u.PublicEmail)
	default:
		id.Email = u.Email
	}
	if id.Name == "" || id.Email == "" {
		return Identity{}, fmt.Errorf("forgeidentity: the %s account %q has no name or e-mail Ploeg can read", dialect, login)
	}
	return id, nil
}

func firstOf(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func client(hc *http.Client) *http.Client {
	if hc != nil {
		return hc
	}
	return httpclient.New(15 * time.Second)
}

// RetryAfter is how long a Cached source waits before it reads again after a
// failed read.
const RetryAfter = 5 * time.Minute

// Cached reads each key's identity once and keeps it; a failed read is
// retried after RetryAfter. key chooses what shares an identity: a static
// forge's every repository, a runtime forge's each repository.
func Cached(src Source, key func(owner, repo string) string) Source {
	c := &cache{src: src, key: key, entries: map[string]cacheEntry{}, now: time.Now}
	return c.get
}

type cache struct {
	mu      sync.Mutex
	src     Source
	key     func(owner, repo string) string
	entries map[string]cacheEntry
	now     func() time.Time
}

type cacheEntry struct {
	id       Identity
	err      error
	failedAt time.Time
}

func (c *cache) get(ctx context.Context, owner, repo string) (Identity, error) {
	k := c.key(owner, repo)
	c.mu.Lock()
	e, ok := c.entries[k]
	c.mu.Unlock()
	if ok && (e.err == nil || c.now().Sub(e.failedAt) < RetryAfter) {
		return e.id, e.err
	}
	id, err := c.src(ctx, owner, repo)
	e = cacheEntry{id: id, err: err}
	if err != nil {
		e.failedAt = c.now()
	}
	c.mu.Lock()
	c.entries[k] = e
	c.mu.Unlock()
	return id, err
}

// Shared keys every repository to one identity.
func Shared(string, string) string { return "" }

// PerRepository keys each repository to its own identity.
func PerRepository(owner, repo string) string { return strings.ToLower(owner + "/" + repo) }
