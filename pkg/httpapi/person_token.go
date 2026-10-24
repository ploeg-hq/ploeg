package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/ploeg-hq/ploeg/pkg/mcpauth"
	"github.com/ploeg-hq/ploeg/pkg/operatorclient"
)

// DefaultPersonTokenLifetime bounds exp minus iat of a person token unless
// the deployment configures another bound (ADR-0150).
const DefaultPersonTokenLifetime = 5 * time.Minute

// MaxPersonTokenLifetime is the longest bound a deployment may configure.
const MaxPersonTokenLifetime = time.Hour

// PersonTokenHeader carries a person token next to an Operator Consumer's own
// bearer credential (ADR-0150).
const PersonTokenHeader = "X-Ploeg-Person-Token"

const maxPersonTokenBytes = 8192

// Attribution proof values (ADR-0150): proven when the person comes from a
// verified person token, asserted when only a consumer header names them.
const (
	ProofProven   = "proven"
	ProofAsserted = "asserted"
)

// PersonTokenConfig configures how ploegd verifies person tokens: JWTs an
// identity provider issued for Ploeg's audience on behalf of a person, which
// an Operator Consumer acting for that person obtained by RFC 8693 token
// exchange (ADR-0150).
type PersonTokenConfig struct {
	// Issuer is matched byte for byte against iss.
	Issuer string
	// Audience is Ploeg's own audience, shared by the consumers whose
	// clientIds name the client a person token was issued to. It is
	// required when any consumer names clientIds and optional when every
	// consumer that accepts person tokens names its own audiences.
	Audience string
	// JWKSURL is where the signing keys live; empty means the issuer's
	// OpenID discovery document.
	JWKSURL string
	// MaxLifetime bounds exp minus iat; zero means DefaultPersonTokenLifetime.
	MaxLifetime time.Duration
	// SubjectNamespace replaces the issuer in the person id derivation, so
	// person ids survive a change of issuer; empty means the issuer.
	SubjectNamespace string
	// HTTPClient and Now replace the defaults in tests.
	HTTPClient *http.Client
	Now        func() time.Time
}

// PersonTokens verifies person tokens for the Operator Consumers that name
// client ids or audiences.
type PersonTokens struct {
	verifier  *mcpauth.Verifier
	namespace string
	audience  string
}

// NewPersonTokens validates cfg against the consumers' client ids and
// audiences: an audience belongs to one consumer, equals no client id and is
// not Ploeg's shared audience.
func NewPersonTokens(cfg PersonTokenConfig, consumers []OperatorConsumer) (*PersonTokens, error) {
	if strings.ContainsAny(cfg.Audience, " \t\r\n") {
		return nil, errors.New("Ploeg's person token audience must not contain whitespace")
	}
	if cfg.MaxLifetime == 0 {
		cfg.MaxLifetime = DefaultPersonTokenLifetime
	}
	if cfg.MaxLifetime < 0 || cfg.MaxLifetime > MaxPersonTokenLifetime {
		return nil, fmt.Errorf("person token lifetime must be positive and at most %s", MaxPersonTokenLifetime)
	}
	var parties, owned []string
	audienceOwner := map[string]string{}
	for _, c := range consumers {
		for _, aud := range c.audiences {
			if other, ok := audienceOwner[aud]; ok {
				return nil, fmt.Errorf("operator consumers %s and %s both name audience %q; each audience belongs to one consumer", other, c.Principal.Name, aud)
			}
			if aud == cfg.Audience {
				return nil, fmt.Errorf("operator consumer %s names Ploeg's shared audience in audiences", c.Principal.Name)
			}
			audienceOwner[aud] = c.Principal.Name
			owned = append(owned, aud)
		}
	}
	for _, c := range consumers {
		for _, id := range c.clientIDs {
			if id == cfg.Audience {
				return nil, fmt.Errorf("operator consumer %s names Ploeg's own audience as a client id", c.Principal.Name)
			}
			if other, ok := audienceOwner[id]; ok {
				return nil, fmt.Errorf("operator consumer %s names client id %q, which is operator consumer %s's audience", c.Principal.Name, id, other)
			}
			parties = append(parties, id)
		}
	}
	if len(parties) == 0 && len(owned) == 0 {
		return nil, errors.New("person tokens are configured but no operator consumer names clientIds or audiences")
	}
	if len(parties) > 0 && cfg.Audience == "" {
		return nil, errors.New("operator consumers that name clientIds need Ploeg's audience")
	}
	audiences := owned
	if cfg.Audience != "" {
		audiences = append([]string{cfg.Audience}, owned...)
	}
	v, err := mcpauth.NewVerifier(mcpauth.Config{Issuer: cfg.Issuer, Audiences: audiences, AuthorizedParties: parties, SelfIssuedAudiences: owned, ScopeOptional: true,
		JWKSURL: cfg.JWKSURL, Leeway: 30 * time.Second, MaxLifetime: cfg.MaxLifetime, HTTPClient: cfg.HTTPClient, Now: cfg.Now})
	if err != nil {
		return nil, err
	}
	namespace := cfg.SubjectNamespace
	if namespace == "" {
		namespace = cfg.Issuer
	}
	return &PersonTokens{verifier: v, namespace: namespace, audience: cfg.Audience}, nil
}

// ProvenPerson is the person a verified person token names.
type ProvenPerson struct {
	// ID is Ploeg's person id for the token's sub (operatorclient.PersonID).
	ID string
	// Client is the token's azp, or client_id without azp.
	Client string
	// Act is the sub of the token's RFC 8693 act claim, empty without one.
	Act string

	audiences []string
}

type provenPersonKey struct{}

// ProvenPersonFromContext returns the person a request's verified person
// token named.
func ProvenPersonFromContext(ctx context.Context) (ProvenPerson, bool) {
	p, ok := ctx.Value(provenPersonKey{}).(ProvenPerson)
	return p, ok && p.ID != ""
}

func (p *PersonTokens) verify(ctx context.Context, token string) (ProvenPerson, error) {
	info, err := p.verifier.Verify(ctx, token, nil)
	if err != nil {
		return ProvenPerson{}, err
	}
	return provenPerson(p.namespace, info), nil
}

func provenPerson(namespace string, info *auth.TokenInfo) ProvenPerson {
	client, _ := info.Extra["client"].(string)
	act, _ := info.Extra["act"].(string)
	audiences, _ := info.Extra["audiences"].([]string)
	return ProvenPerson{ID: operatorclient.PersonID(namespace, info.UserID), Client: client, Act: boundedLabel(act), audiences: audiences}
}

func boundedLabel(s string) string {
	runes := []rune(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s))
	if len(runes) > 128 {
		runes = runes[:128]
	}
	return string(runes)
}

func looksLikeJWT(token string) bool {
	return len(token) <= maxPersonTokenBytes && strings.Count(token, ".") == 2 && !strings.ContainsAny(token, " \t\r\n,")
}
