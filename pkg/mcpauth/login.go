package mcpauth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ErrLoginRefused means the identity provider answered but redeemed no code,
// or the ID token it returned does not verify.
var ErrLoginRefused = errors.New("the identity provider's sign-in did not verify")

// LoginConfig pins one Login: an OpenID Connect confidential client that signs
// a person in with the authorization code flow and PKCE.
type LoginConfig struct {
	// Issuer is matched byte for byte against the discovery document and the
	// ID token's iss.
	Issuer string
	// ClientID and ClientSecret are this service's own client credentials.
	ClientID     string
	ClientSecret string
	// RedirectURL is the callback registered at the identity provider.
	RedirectURL string
	// Algorithms are the accepted signing algorithms; empty means RS256 and
	// ES256.
	Algorithms []string
	// Leeway is the clock skew tolerated, at most MaxLeeway.
	Leeway time.Duration
	// HTTPClient nil means a client with a five second timeout that follows
	// no redirects.
	HTTPClient *http.Client
	// Now replaces the clock in tests.
	Now func() time.Time
}

// LoginRequest is what one authorization request asks the identity provider
// for. Every login it starts is a fresh one: prompt=login and max_age=0.
type LoginRequest struct {
	State     string
	Nonce     string
	Challenge string
	ACRValues []string
}

// IDToken is the verified content of an OpenID Connect ID token.
type IDToken struct {
	Subject  string
	Username string
	AuthTime time.Time
	ACR      string
	AMR      []string
}

// Login signs a person in at one issuer and verifies the ID token it returns.
type Login struct {
	cfg  LoginConfig
	keys *keySet

	mu         sync.Mutex
	endpoints  loginEndpoints
	discovered time.Time
}

type loginEndpoints struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
}

// NewLogin validates cfg. It fetches nothing until the first login.
func NewLogin(cfg LoginConfig) (*Login, error) {
	u, err := url.Parse(cfg.Issuer)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, errors.New("issuer must be an http(s) URL without credentials, query or fragment")
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" || strings.ContainsAny(cfg.ClientID, ":\r\n") {
		return nil, errors.New("sign-in needs this service's client id (without a colon) and client secret")
	}
	r, err := url.Parse(cfg.RedirectURL)
	if err != nil || (r.Scheme != "https" && r.Scheme != "http") || r.Host == "" || r.User != nil || r.Fragment != "" {
		return nil, errors.New("the redirect URL must be an http(s) URL without credentials or fragment")
	}
	if len(cfg.Algorithms) == 0 {
		cfg.Algorithms = []string{"RS256", "ES256"}
	}
	if cfg.Leeway < 0 || cfg.Leeway > MaxLeeway {
		return nil, fmt.Errorf("leeway must be between 0 and %s", MaxLeeway)
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Login{cfg: cfg, keys: newKeySet(cfg.Issuer, "", cfg.HTTPClient, cfg.Now)}, nil
}

func (l *Login) discover(ctx context.Context) (loginEndpoints, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.endpoints.TokenEndpoint != "" && l.cfg.Now().Sub(l.discovered) < keySetMaxAge {
		return l.endpoints, nil
	}
	var e loginEndpoints
	if err := l.keys.getJSON(ctx, strings.TrimRight(l.cfg.Issuer, "/")+"/.well-known/openid-configuration", &e); err != nil {
		return e, fmt.Errorf("%w: %v", ErrKeysUnavailable, err)
	}
	if e.Issuer != l.cfg.Issuer {
		return e, fmt.Errorf("discovery names issuer %q, not the configured %q", e.Issuer, l.cfg.Issuer)
	}
	for _, endpoint := range []string{e.AuthorizationEndpoint, e.TokenEndpoint} {
		if u, err := url.Parse(endpoint); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Fragment != "" {
			return e, errors.New("discovery has no http(s) authorization_endpoint or token_endpoint")
		}
	}
	l.endpoints, l.discovered = e, l.cfg.Now()
	return e, nil
}

// AuthorizationURL is where the person's browser goes to sign in afresh.
func (l *Login) AuthorizationURL(ctx context.Context, r LoginRequest) (string, error) {
	e, err := l.discover(ctx)
	if err != nil {
		return "", err
	}
	u, _ := url.Parse(e.AuthorizationEndpoint)
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", l.cfg.ClientID)
	q.Set("redirect_uri", l.cfg.RedirectURL)
	q.Set("scope", "openid profile")
	q.Set("state", r.State)
	q.Set("nonce", r.Nonce)
	q.Set("code_challenge", r.Challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("prompt", "login")
	q.Set("max_age", "0")
	if len(r.ACRValues) > 0 {
		q.Set("acr_values", strings.Join(r.ACRValues, " "))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Redeem trades an authorization code and its PKCE verifier for the person's
// ID token and verifies it: signature, iss, aud, azp, exp, iat, sub, nonce
// and the presence of auth_time. Freshness and the authentication method are
// the caller's to judge.
func (l *Login) Redeem(ctx context.Context, code, verifier, nonce string) (IDToken, error) {
	e, err := l.discover(ctx)
	if err != nil {
		return IDToken{}, err
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {l.cfg.RedirectURL},
		"code_verifier": {verifier},
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return IDToken{}, errors.New("could not build the token request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(url.QueryEscape(l.cfg.ClientID), url.QueryEscape(l.cfg.ClientSecret))
	resp, err := l.cfg.HTTPClient.Do(req)
	if err != nil {
		return IDToken{}, fmt.Errorf("the identity provider's token endpoint is unreachable: %w", ctx.Err())
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDocumentBytes+1))
	if err != nil || len(body) > maxDocumentBytes {
		return IDToken{}, errors.New("the token endpoint's answer could not be read")
	}
	var answer struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	_ = json.Unmarshal(body, &answer)
	if resp.StatusCode != http.StatusOK || answer.IDToken == "" {
		code := answer.Error
		if code == "" || len(code) > 64 || strings.ContainsFunc(code, func(r rune) bool { return r < 0x21 || r > 0x7e }) {
			code = "no id_token"
		}
		return IDToken{}, fmt.Errorf("%w (%d %s)", ErrLoginRefused, resp.StatusCode, code)
	}
	return l.verify(ctx, answer.IDToken, nonce)
}

type idClaims struct {
	jwt.RegisteredClaims
	AuthorizedParty string           `json:"azp"`
	Nonce           string           `json:"nonce"`
	AuthTime        *jwt.NumericDate `json:"auth_time"`
	ACR             string           `json:"acr"`
	AMR             []string         `json:"amr"`
	Username        string           `json:"preferred_username"`
}

func (l *Login) verify(ctx context.Context, token, nonce string) (IDToken, error) {
	var c idClaims
	parser := jwt.NewParser(
		jwt.WithValidMethods(l.cfg.Algorithms),
		jwt.WithIssuer(l.cfg.Issuer),
		jwt.WithAudience(l.cfg.ClientID),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(l.cfg.Leeway),
		jwt.WithTimeFunc(l.cfg.Now),
	)
	var keyErr error
	_, err := parser.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		key, err := l.keys.key(ctx, kid)
		keyErr = err
		return key, err
	})
	if keyErr != nil && errors.Is(keyErr, ErrKeysUnavailable) {
		return IDToken{}, keyErr
	}
	if err != nil {
		return IDToken{}, fmt.Errorf("%w: %s", ErrLoginRefused, describe(err))
	}
	switch {
	case c.IssuedAt == nil:
		return IDToken{}, fmt.Errorf("%w: the ID token has no iat", ErrLoginRefused)
	case c.Subject == "":
		return IDToken{}, fmt.Errorf("%w: the ID token has no sub", ErrLoginRefused)
	case len(c.Audience) > 1 && c.AuthorizedParty == "", c.AuthorizedParty != "" && c.AuthorizedParty != l.cfg.ClientID:
		return IDToken{}, fmt.Errorf("%w: the ID token was issued to another client", ErrLoginRefused)
	case nonce == "" || subtle.ConstantTimeCompare([]byte(c.Nonce), []byte(nonce)) != 1:
		return IDToken{}, fmt.Errorf("%w: the ID token answers another sign-in (nonce)", ErrLoginRefused)
	case c.AuthTime == nil:
		return IDToken{}, fmt.Errorf("%w: the ID token has no auth_time", ErrLoginRefused)
	}
	amr := slices.DeleteFunc(slices.Clone(c.AMR), func(s string) bool { return s == "" })
	return IDToken{Subject: c.Subject, Username: c.Username, AuthTime: c.AuthTime.Time, ACR: c.ACR, AMR: amr}, nil
}
