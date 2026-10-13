// Package mcpauth makes an MCP server an OAuth 2.1 resource server for an
// OpenID Connect issuer such as Authentik. It verifies signed JWT access
// tokens against the issuer's published keys, answers RFC 6750 challenges
// that point at RFC 9728 protected resource metadata, and never forwards the
// caller's token anywhere.
package mcpauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/auth"
)

// MaxLeeway is the most clock skew a Verifier tolerates on exp, nbf and iat.
const MaxLeeway = time.Minute

// DefaultMaxLifetime is how long an access token may live, from iat to exp,
// unless the Config says otherwise. The issuer publishes only its current
// key, so a short lifetime is what bounds a token that outlives a rotation.
const DefaultMaxLifetime = 10 * time.Minute

// Config pins what a Verifier accepts.
type Config struct {
	// Issuer is matched byte for byte against the token's iss.
	Issuer string
	// Audiences are the pre-registered client ids; aud must name one and azp
	// must be one.
	Audiences []string
	// JWKSURL is where the signing keys live. Empty means the jwks_uri of the
	// issuer's OpenID discovery document.
	JWKSURL string
	// Algorithms are the accepted signing algorithms; empty means RS256 and
	// ES256.
	Algorithms []string
	// Leeway is the clock skew tolerated, at most MaxLeeway.
	Leeway time.Duration
	// MaxLifetime bounds exp minus iat; zero means DefaultMaxLifetime.
	MaxLifetime time.Duration
	// HTTPClient fetches discovery and keys; nil means a client with a five
	// second timeout that follows no redirects.
	HTTPClient *http.Client
	// Now replaces the clock in tests.
	Now func() time.Time
}

// Verifier checks access tokens for one issuer.
type Verifier struct {
	cfg  Config
	keys *keySet
}

// NewVerifier validates cfg and returns a Verifier. It fetches nothing until
// the first token or Ready call.
func NewVerifier(cfg Config) (*Verifier, error) {
	u, err := url.Parse(cfg.Issuer)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, errors.New("issuer must be an http(s) URL without credentials, query or fragment")
	}
	if cfg.JWKSURL != "" {
		j, err := url.Parse(cfg.JWKSURL)
		if err != nil || (j.Scheme != "https" && j.Scheme != "http") || j.Host == "" || j.User != nil {
			return nil, errors.New("JWKS URL must be an http(s) URL without credentials")
		}
	}
	if len(cfg.Audiences) == 0 || slices.Contains(cfg.Audiences, "") {
		return nil, errors.New("at least one non-empty client id is required as audience")
	}
	if len(cfg.Algorithms) == 0 {
		cfg.Algorithms = []string{"RS256", "ES256"}
	}
	for _, alg := range cfg.Algorithms {
		if !slices.Contains([]string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512"}, alg) {
			return nil, fmt.Errorf("signing algorithm %q is not an asymmetric JWS algorithm", alg)
		}
	}
	if cfg.Leeway < 0 || cfg.Leeway > MaxLeeway {
		return nil, fmt.Errorf("leeway must be between 0 and %s", MaxLeeway)
	}
	if cfg.MaxLifetime == 0 {
		cfg.MaxLifetime = DefaultMaxLifetime
	}
	if cfg.MaxLifetime < 0 {
		return nil, errors.New("maximum token lifetime must be positive")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Verifier{cfg: cfg, keys: newKeySet(cfg.Issuer, cfg.JWKSURL, cfg.HTTPClient, cfg.Now)}, nil
}

// Ready reports whether the issuer's keys have been fetched at least once,
// fetching them if not.
func (v *Verifier) Ready(ctx context.Context) error {
	return v.keys.ready(ctx)
}

type claims struct {
	jwt.RegisteredClaims
	AuthorizedParty string   `json:"azp"`
	Scope           string   `json:"scope"`
	Groups          []string `json:"groups"`
	Username        string   `json:"preferred_username"`
}

// Verify is an auth.TokenVerifier. Every rejection wraps auth.ErrInvalidToken
// except an unreachable key set, which wraps ErrKeysUnavailable. TokenInfo
// carries the subject as UserID and the groups and username in Extra.
func (v *Verifier) Verify(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	var c claims
	parser := jwt.NewParser(
		jwt.WithValidMethods(v.cfg.Algorithms),
		jwt.WithIssuer(v.cfg.Issuer),
		jwt.WithAudience(v.cfg.Audiences...),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(v.cfg.Leeway),
		jwt.WithTimeFunc(v.cfg.Now),
		jwt.WithStrictDecoding(),
	)
	var keyErr error
	_, err := parser.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		key, err := v.keys.key(ctx, kid)
		keyErr = err
		return key, err
	})
	if keyErr != nil && errors.Is(keyErr, ErrKeysUnavailable) {
		return nil, keyErr
	}
	if err != nil {
		return nil, invalid(describe(err))
	}
	if c.IssuedAt == nil {
		return nil, invalid("the token has no iat")
	}
	if c.ExpiresAt.Sub(c.IssuedAt.Time) > v.cfg.MaxLifetime {
		return nil, invalid(fmt.Sprintf("the token lives longer than %s", v.cfg.MaxLifetime))
	}
	if c.Subject == "" {
		return nil, invalid("the token has no sub")
	}
	if c.AuthorizedParty == "" || c.Scope == "" {
		return nil, invalid("the token has no azp or scope; an ID token is not an access token")
	}
	if !slices.Contains(v.cfg.Audiences, c.AuthorizedParty) {
		return nil, invalid("the token was issued to a client this server does not accept")
	}
	groups := make([]string, 0, len(c.Groups))
	for _, g := range c.Groups {
		if g != "" {
			groups = append(groups, g)
		}
	}
	return &auth.TokenInfo{
		Scopes:     strings.Fields(c.Scope),
		Expiration: c.ExpiresAt.Time,
		UserID:     c.Subject,
		Extra:      map[string]any{"groups": groups, "username": c.Username, "client": c.AuthorizedParty},
	}, nil
}

func invalid(reason string) error {
	return fmt.Errorf("%w: %s", auth.ErrInvalidToken, reason)
}

func describe(err error) string {
	switch {
	case errors.Is(err, jwt.ErrTokenExpired):
		return "the token has expired"
	case errors.Is(err, jwt.ErrTokenRequiredClaimMissing):
		return "the token lacks a required claim"
	case errors.Is(err, jwt.ErrTokenInvalidIssuer):
		return "the token was issued by another issuer"
	case errors.Is(err, jwt.ErrTokenInvalidAudience):
		return "the token is meant for another audience"
	case errors.Is(err, jwt.ErrTokenNotValidYet), errors.Is(err, jwt.ErrTokenUsedBeforeIssued):
		return "the token is not valid yet"
	case errors.Is(err, jwt.ErrTokenSignatureInvalid), errors.Is(err, jwt.ErrTokenUnverifiable):
		return "the token's signature does not verify"
	case errors.Is(err, jwt.ErrTokenMalformed):
		return "the token is not a JWT"
	}
	return "the token is not acceptable"
}
