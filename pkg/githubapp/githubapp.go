// Package githubapp mints repository-scoped GitHub App installation tokens.
// It is shared by every Ploeg component that talks to GitHub as the App: one
// App identity, one private key, and a token per repository that carries only
// the permissions its caller asks for.
package githubapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/httpclient"
)

// DefaultAPIURL is GitHub.com's REST API root.
const DefaultAPIURL = "https://api.github.com"

const (
	jwtLifetime   = 9 * time.Minute
	jwtBackdate   = time.Minute
	refreshMargin = 5 * time.Minute
)

// Permissions names the GitHub App permissions an installation token is
// minted with, such as {"issues": "write", "metadata": "read"}.
type Permissions map[string]string

// Minter mints installation tokens for one GitHub App. A token is scoped to
// the single repository it is asked for and to Permissions, and is reused
// until five minutes before GitHub expires it.
type Minter struct {
	// AppID is the App's numeric id or client id, used as the JWT issuer.
	AppID string
	// Key is the App's private key.
	Key *rsa.PrivateKey
	// APIURL is the REST API root; empty uses DefaultAPIURL. GitHub
	// Enterprise Server uses https://<host>/api/v3.
	APIURL string
	// Permissions are requested on every token. Empty requests the App's
	// full installation permissions, which a caller should avoid.
	Permissions Permissions
	// HC is optional; nil uses a 30-second client.
	HC *http.Client
	// Now is optional; nil uses time.Now.
	Now func() time.Time

	mu            sync.Mutex
	installations map[string]int64
	tokens        map[string]cachedToken
}

type cachedToken struct {
	token   string
	expires time.Time
}

// ParsePrivateKey reads a PEM-encoded RSA key in PKCS #1 form, as GitHub
// issues it, or PKCS #8.
func ParsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("githubapp: private key is not PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("githubapp: parse private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("githubapp: private key is not RSA")
	}
	return key, nil
}

// Token returns an installation token for owner/repo, minting one when the
// cached token is missing or near expiry.
func (m *Minter) Token(ctx context.Context, owner, repo string) (string, error) {
	if m.AppID == "" || m.Key == nil {
		return "", errors.New("githubapp: App id and private key are required")
	}
	key := strings.ToLower(owner + "/" + repo)
	m.mu.Lock()
	cached, ok := m.tokens[key]
	m.mu.Unlock()
	if ok && m.now().Add(refreshMargin).Before(cached.expires) {
		return cached.token, nil
	}
	installation, err := m.installation(ctx, owner, repo)
	if err != nil {
		return "", err
	}
	body := map[string]any{"repositories": []string{repo}}
	if len(m.Permissions) > 0 {
		body["permissions"] = m.Permissions
	}
	var minted struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	path := fmt.Sprintf("/app/installations/%d/access_tokens", installation)
	if err := m.appCall(ctx, http.MethodPost, path, body, &minted); err != nil {
		if errors.Is(err, errNotFound) {
			m.forgetInstallation(key)
		}
		return "", err
	}
	if minted.Token == "" {
		return "", errors.New("githubapp: GitHub returned no installation token")
	}
	m.mu.Lock()
	if m.tokens == nil {
		m.tokens = map[string]cachedToken{}
	}
	m.tokens[key] = cachedToken{token: minted.Token, expires: minted.ExpiresAt}
	m.mu.Unlock()
	return minted.Token, nil
}

// Forget drops the cached token for owner/repo, so the next Token call mints
// a fresh one. Call it after GitHub answers 401 to a cached token.
func (m *Minter) Forget(owner, repo string) {
	m.mu.Lock()
	delete(m.tokens, strings.ToLower(owner+"/"+repo))
	m.mu.Unlock()
}

func (m *Minter) forgetInstallation(key string) {
	m.mu.Lock()
	delete(m.installations, key)
	m.mu.Unlock()
}

func (m *Minter) installation(ctx context.Context, owner, repo string) (int64, error) {
	key := strings.ToLower(owner + "/" + repo)
	m.mu.Lock()
	id, ok := m.installations[key]
	m.mu.Unlock()
	if ok {
		return id, nil
	}
	var found struct {
		ID int64 `json:"id"`
	}
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/installation"
	if err := m.appCall(ctx, http.MethodGet, path, nil, &found); err != nil {
		if errors.Is(err, errNotFound) {
			return 0, fmt.Errorf("githubapp: the App is not installed on %s/%s", owner, repo)
		}
		return 0, err
	}
	if found.ID == 0 {
		return 0, fmt.Errorf("githubapp: no installation id for %s/%s", owner, repo)
	}
	m.mu.Lock()
	if m.installations == nil {
		m.installations = map[string]int64{}
	}
	m.installations[key] = found.ID
	m.mu.Unlock()
	return found.ID, nil
}

var errNotFound = errors.New("githubapp: not found")

func (m *Minter) appCall(ctx context.Context, method, path string, body, out any) error {
	jwt, err := m.jwt()
	if err != nil {
		return err
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, m.apiURL()+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := m.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("githubapp: %s %s: HTTP %d: %s", method, path, resp.StatusCode, bytes.TrimSpace(snippet))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

func (m *Minter) jwt() (string, error) {
	now := m.now()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"iat": now.Add(-jwtBackdate).Unix(),
		"exp": now.Add(jwtLifetime).Unix(),
		"iss": m.AppID,
	})
	if err != nil {
		return "", err
	}
	signingInput := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, m.Key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("githubapp: sign JWT: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func (m *Minter) apiURL() string {
	if m.APIURL != "" {
		return strings.TrimRight(m.APIURL, "/")
	}
	return DefaultAPIURL
}

func (m *Minter) client() *http.Client {
	if m.HC != nil {
		return m.HC
	}
	return httpclient.New(30 * time.Second)
}

func (m *Minter) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}
