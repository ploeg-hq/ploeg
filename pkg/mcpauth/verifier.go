// Package mcpauth makes an MCP server, or any Ploeg API, an OAuth 2.1
// resource server for an OpenID Connect issuer such as Authentik. It verifies
// signed JWT access tokens against the issuer's published keys, answers
// RFC 6750 challenges that point at RFC 9728 protected resource metadata,
// and exchanges a verified token for another audience with RFC 8693.
package mcpauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
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
	// Issuer is matched byte for byte against the token's iss. It must be an
	// https URL unless AllowLoopbackHTTP is set and its host is loopback.
	Issuer string
	// Audiences are what aud must name one of. Without AuthorizedParties
	// they are the pre-registered client ids and azp must be one of them too.
	// With Resource set, aud must name the resource instead and Audiences are
	// only the client ids azp must be one of.
	Audiences []string
	// Resource, when set, is this server's resource identifier: the RFC 9728
	// resource its protected resource metadata names and the RFC 8707
	// resource indicator a client requests a token for. A token is accepted
	// only when its aud names it exactly (RFC 9068 section 4), so a token
	// issued to an accepted client for another resource is refused. It must
	// be an absolute URL without credentials or fragment, and cannot be
	// combined with AuthorizedParties or SelfIssuedAudiences.
	Resource string
	// AllowLoopbackHTTP accepts an http Issuer, JWKS URL and jwks_uri whose
	// host is localhost or a loopback address. It is for local development
	// and tests only and is outside deployment profile P1.
	AllowLoopbackHTTP bool
	// AuthorizedParties, when set, are the client ids azp (or client_id when
	// the token has no azp) must be one of, independently of Audiences: a
	// token exchanged for one audience on behalf of a known client.
	AuthorizedParties []string
	// SelfIssuedAudiences are audiences a token may also have been issued to:
	// azp (or client_id) equal to one of them is accepted when aud names that
	// same value, the shape an identity provider gives a token it issues on
	// the exchange target's own client. Such a token must still carry a scope
	// claim, possibly empty, which an access token has and an ID token lacks.
	// Each must also be one of Audiences.
	SelfIssuedAudiences []string
	// ScopeOptional accepts a token without a scope claim. Set it only with
	// AuthorizedParties that never equal an audience, or with
	// SelfIssuedAudiences, so an ID token issued to the audience's own client
	// is still refused.
	ScopeOptional bool
	// JWKSURL is where the signing keys live. Empty means the jwks_uri of the
	// issuer's OpenID discovery document. Either must be https under the same
	// rule as Issuer.
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
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, errors.New("issuer must be an https URL without credentials, query or fragment")
	}
	if !transportAllowed(u, cfg.AllowLoopbackHTTP) {
		return nil, errors.New(httpRefused("issuer"))
	}
	if cfg.JWKSURL != "" {
		j, err := url.Parse(cfg.JWKSURL)
		if err != nil || j.Host == "" || j.User != nil {
			return nil, errors.New("JWKS URL must be an https URL without credentials")
		}
		if !transportAllowed(j, cfg.AllowLoopbackHTTP) {
			return nil, errors.New(httpRefused("JWKS URL"))
		}
	}
	if len(cfg.Audiences) == 0 || slices.Contains(cfg.Audiences, "") {
		return nil, errors.New("at least one non-empty client id is required as audience")
	}
	if cfg.Resource != "" {
		r, err := url.Parse(cfg.Resource)
		if err != nil || !r.IsAbs() || r.Host == "" || r.Fragment != "" || r.User != nil {
			return nil, errors.New("resource must be an absolute URL without credentials or fragment")
		}
		if len(cfg.AuthorizedParties) > 0 || len(cfg.SelfIssuedAudiences) > 0 {
			return nil, errors.New("a resource cannot be combined with authorized parties or self-issued audiences")
		}
	}
	if slices.Contains(cfg.AuthorizedParties, "") {
		return nil, errors.New("authorized parties must be non-empty client ids")
	}
	for _, aud := range cfg.SelfIssuedAudiences {
		if !slices.Contains(cfg.Audiences, aud) {
			return nil, fmt.Errorf("self-issued audience %q is not one of the audiences", aud)
		}
	}
	if cfg.ScopeOptional {
		if len(cfg.AuthorizedParties) == 0 && len(cfg.SelfIssuedAudiences) == 0 {
			return nil, errors.New("an optional scope needs authorized parties or self-issued audiences")
		}
		for _, party := range cfg.AuthorizedParties {
			if slices.Contains(cfg.Audiences, party) {
				return nil, fmt.Errorf("authorized party %q is also an audience, so an ID token could pass as an access token", party)
			}
		}
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
	return &Verifier{cfg: cfg, keys: newKeySet(cfg.Issuer, cfg.JWKSURL, cfg.HTTPClient, cfg.Now, cfg.AllowLoopbackHTTP)}, nil
}

func transportAllowed(u *url.URL, allowLoopbackHTTP bool) bool {
	switch u.Scheme {
	case "https":
		return true
	case "http":
		return allowLoopbackHTTP && loopbackHost(u.Hostname())
	}
	return false
}

func loopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

func httpRefused(what string) string {
	return what + " must be https; http is accepted only on a loopback host with the local-development setting, which is outside profile P1"
}

// Ready reports whether the issuer's keys have been fetched at least once,
// fetching them if not.
func (v *Verifier) Ready(ctx context.Context) error {
	return v.keys.ready(ctx)
}

type claims struct {
	jwt.RegisteredClaims
	AuthorizedParty string    `json:"azp"`
	ClientID        string    `json:"client_id"`
	Scope           *string   `json:"scope"`
	Groups          []string  `json:"groups"`
	Username        string    `json:"preferred_username"`
	Act             *actClaim `json:"act"`
}

type actClaim struct {
	Subject string `json:"sub"`
}

// Verify is an auth.TokenVerifier. Every rejection wraps auth.ErrInvalidToken
// except an unreachable key set, which wraps ErrKeysUnavailable. TokenInfo
// carries the subject as UserID and, in Extra, "groups", "username", "client"
// (azp, or client_id without azp), "act" (the RFC 8693 act claim's sub, or
// empty when the token has no act), "audiences" (the token's aud) and
// "claims" (every claim of the verified payload, as JSON decodes it).
func (v *Verifier) Verify(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	var c claims
	parser := jwt.NewParser(
		jwt.WithValidMethods(v.cfg.Algorithms),
		jwt.WithIssuer(v.cfg.Issuer),
		jwt.WithAudience(v.tokenAudiences()...),
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
	if err != nil && v.cfg.Resource != "" && errors.Is(err, jwt.ErrTokenInvalidAudience) {
		return nil, invalid(fmt.Sprintf("the token's aud does not name this resource %q; request the token with the RFC 8707 resource parameter", v.cfg.Resource))
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
	scope := ""
	if c.Scope != nil {
		scope = *c.Scope
	}
	exchanging := len(v.cfg.AuthorizedParties) > 0 || len(v.cfg.SelfIssuedAudiences) > 0
	client := c.AuthorizedParty
	if client == "" && exchanging {
		client = c.ClientID
	}
	if client == "" || (scope == "" && !v.cfg.ScopeOptional) {
		return nil, invalid("the token has no azp or scope; an ID token is not an access token")
	}
	parties := v.cfg.Audiences
	if exchanging {
		parties = v.cfg.AuthorizedParties
	}
	selfIssued := slices.Contains(v.cfg.SelfIssuedAudiences, client) && slices.Contains(c.Audience, client)
	if selfIssued && c.Scope == nil {
		return nil, invalid("the token was issued to its own audience and has no scope claim; an ID token is not an access token")
	}
	if !selfIssued && !slices.Contains(parties, client) {
		return nil, invalid("the token was issued to a client this server does not accept")
	}
	act := ""
	if c.Act != nil {
		if c.Act.Subject == "" {
			return nil, invalid("the token's act claim names no sub")
		}
		act = c.Act.Subject
	}
	groups := make([]string, 0, len(c.Groups))
	for _, g := range c.Groups {
		if g != "" {
			groups = append(groups, g)
		}
	}
	return &auth.TokenInfo{
		Scopes:     strings.Fields(scope),
		Expiration: c.ExpiresAt.Time,
		UserID:     c.Subject,
		Extra:      map[string]any{"groups": groups, "username": c.Username, "client": client, "act": act, "audiences": []string(c.Audience), "claims": payloadClaims(parser, token)},
	}, nil
}

func (v *Verifier) tokenAudiences() []string {
	if v.cfg.Resource != "" {
		return []string{v.cfg.Resource}
	}
	return v.cfg.Audiences
}

func payloadClaims(parser *jwt.Parser, token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return map[string]any{}
	}
	payload, err := parser.DecodeSegment(parts[1])
	if err != nil {
		return map[string]any{}
	}
	claims := map[string]any{}
	if json.Unmarshal(payload, &claims) != nil {
		return map[string]any{}
	}
	return claims
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
