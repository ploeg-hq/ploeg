// Package runidentity issues and verifies the Run identity (ADR-0149): a
// short-lived JWT, signed ES256 by ploegd, that names one Run and its place
// in its Work Item for a declared observer. It authorizes nothing in Ploeg;
// a consumer verifies it offline against the key set ploegd publishes and,
// if it wants more, reads the Run through the operator API with its own
// credential.
package runidentity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TokenType is the JOSE "typ" header of every Run identity (RFC 8725 §3.11),
// so a verifier never mistakes another JWT of the same key for one.
const TokenType = "run-identity+jwt"

// Algorithm is the only signing algorithm a Run identity uses.
const Algorithm = "ES256"

// MaxLifetime bounds a Run identity however long the Run's lease is.
const MaxLifetime = 15 * time.Minute

// MinLifetime is the shortest lifetime ploegd issues; a Run whose lease ends
// sooner gets none.
const MinLifetime = 5 * time.Second

// MaxAudienceLength bounds one declared audience.
const MaxAudienceLength = 256

// SubjectPrefix starts the "sub" claim, followed by the Run's id.
const SubjectPrefix = "run:"

// Run is what a Run identity says about its Run. Ids are decimal strings, as
// the operator API writes them.
type Run struct {
	// ID is the Run's id, the {id} of GET /api/v1/operator/runs/{id}.
	ID         string `json:"id"`
	WorkItemID string `json:"workItemId"`
	// ShiftID is empty for a Run no Shift opened.
	ShiftID string `json:"shiftId,omitempty"`
	Round   int    `json:"round"`
	Role    string `json:"role,omitempty"`
	Team    string `json:"team"`
	Harness string `json:"harness,omitempty"`
	// Branch is the Shift's branch; empty when ploegd derived none.
	Branch string `json:"branch,omitempty"`
	// Trace is the value of the Agent-Trace-Id trailer the Run writes on
	// its commits and the alias of its model key.
	Trace string `json:"trace"`
}

// Claims are a Run identity's JWT claims.
type Claims struct {
	jwt.RegisteredClaims
	Run Run `json:"run"`
}

// ValidAudience reports why aud cannot be a Run identity's audience, or "".
// An audience is an https URL without credentials, query or fragment, or a
// URN, naming the one service that accepts the token.
func ValidAudience(aud string) string {
	if aud == "" || len(aud) > MaxAudienceLength {
		return fmt.Sprintf("must be 1 to %d characters", MaxAudienceLength)
	}
	if strings.ContainsAny(aud, " \t\r\n") {
		return "must not contain whitespace"
	}
	if strings.HasPrefix(aud, "urn:") {
		if len(strings.SplitN(aud, ":", 3)) < 3 {
			return "a URN needs a namespace and a name"
		}
		return ""
	}
	u, err := url.Parse(aud)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "must be an https URL or a urn:"
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "must not carry credentials, a query or a fragment"
	}
	return ""
}

// Signer issues Run identities with ploegd's current key and publishes it,
// with the keys it retired, as a JWK Set.
type Signer struct {
	issuer  string
	key     *ecdsa.PrivateKey
	kid     string
	retired []*ecdsa.PublicKey
	now     func() time.Time
}

// NewSigner reads a P-256 private key (PEM, PKCS#8 or SEC 1) and any retired
// P-256 public keys (concatenated PEM "PUBLIC KEY" blocks, or empty). The
// issuer is the https URL consumers know ploegd by; it is the "iss" claim.
func NewSigner(issuer, privatePEM, retiredPEM string) (*Signer, error) {
	u, err := url.Parse(issuer)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("run identity issuer %q must be the http(s) URL consumers reach ploegd at", issuer)
	}
	key, err := parsePrivateKey(privatePEM)
	if err != nil {
		return nil, err
	}
	s := &Signer{issuer: issuer, key: key, kid: Thumbprint(&key.PublicKey), now: time.Now}
	rest := []byte(retiredPEM)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "PUBLIC KEY" {
			return nil, fmt.Errorf("a retired run identity key must be a PEM PUBLIC KEY, not %s", block.Type)
		}
		pub, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("retired run identity key: %w", err)
		}
		ec, ok := pub.(*ecdsa.PublicKey)
		if !ok || ec.Curve != elliptic.P256() {
			return nil, errors.New("a retired run identity key must be a P-256 key")
		}
		s.retired = append(s.retired, ec)
	}
	if strings.TrimSpace(string(rest)) != "" {
		return nil, errors.New("the retired run identity keys hold text that is not PEM")
	}
	return s, nil
}

func parsePrivateKey(privatePEM string) (*ecdsa.PrivateKey, error) {
	block, rest := pem.Decode([]byte(privatePEM))
	if block == nil || strings.TrimSpace(string(rest)) != "" {
		return nil, errors.New("the run identity signing key must be exactly one PEM block")
	}
	var key *ecdsa.PrivateKey
	switch block.Type {
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("run identity signing key: %w", err)
		}
		ec, ok := k.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errors.New("the run identity signing key must be an ECDSA P-256 key")
		}
		key = ec
	case "EC PRIVATE KEY":
		k, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("run identity signing key: %w", err)
		}
		key = k
	default:
		return nil, fmt.Errorf("the run identity signing key must be a PEM PRIVATE KEY or EC PRIVATE KEY, not %s", block.Type)
	}
	if key.Curve != elliptic.P256() {
		return nil, errors.New("the run identity signing key must be a P-256 key")
	}
	return key, nil
}

// Issuer is the "iss" of every token s signs.
func (s *Signer) Issuer() string { return s.issuer }

// KeyID is the RFC 7638 thumbprint of the current key, the "kid" header.
func (s *Signer) KeyID() string { return s.kid }

// Sign issues a Run identity for run and audience that expires at expires
// or after MaxLifetime, whichever is first. It refuses an audience
// ValidAudience refuses and a lifetime shorter than MinLifetime.
func (s *Signer) Sign(run Run, audience string, expires time.Time) (string, time.Time, error) {
	if why := ValidAudience(audience); why != "" {
		return "", time.Time{}, fmt.Errorf("audience %q %s", audience, why)
	}
	if run.ID == "" || run.Team == "" || run.Trace == "" {
		return "", time.Time{}, errors.New("a run identity needs the Run's id, Team and trace")
	}
	now := s.now().Truncate(time.Second)
	if limit := now.Add(MaxLifetime); expires.After(limit) {
		expires = limit
	}
	expires = expires.Truncate(time.Second)
	if expires.Sub(now) < MinLifetime {
		return "", time.Time{}, errors.New("the Run's lease ends too soon for a run identity")
	}
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", time.Time{}, err
	}
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.issuer,
			Subject:   SubjectPrefix + run.ID,
			Audience:  jwt.ClaimStrings{audience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expires),
			ID:        hex.EncodeToString(jti),
		},
		Run: run,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["typ"] = TokenType
	token.Header["kid"] = s.kid
	signed, err := token.SignedString(s.key)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, expires, nil
}

// JWK is one public key of a JWK Set (RFC 7517).
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
}

// JWKS is a JWK Set.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// KeySet is the JWK Set ploegd publishes: the current key first, then the
// retired ones, so a token signed just before a rotation still verifies.
func (s *Signer) KeySet() JWKS {
	set := JWKS{Keys: []JWK{jwk(&s.key.PublicKey)}}
	for _, k := range s.retired {
		set.Keys = append(set.Keys, jwk(k))
	}
	return set
}

func jwk(k *ecdsa.PublicKey) JWK {
	x, y := coordinates(k)
	return JWK{Kty: "EC", Crv: "P-256", X: x, Y: y, Kid: Thumbprint(k), Use: "sig", Alg: Algorithm}
}

func coordinates(k *ecdsa.PublicKey) (string, string) {
	raw, _ := k.Bytes()
	return base64.RawURLEncoding.EncodeToString(raw[1:33]), base64.RawURLEncoding.EncodeToString(raw[33:65])
}

// Thumbprint is the RFC 7638 JWK thumbprint of a P-256 key.
func Thumbprint(k *ecdsa.PublicKey) string {
	x, y := coordinates(k)
	sum := sha256.Sum256([]byte(`{"crv":"P-256","kty":"EC","x":"` + x + `","y":"` + y + `"}`))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ParseKeySet reads a JWK Set and keeps its P-256 signing keys by kid.
func ParseKeySet(body []byte) (map[string]*ecdsa.PublicKey, error) {
	var set JWKS
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, fmt.Errorf("key set: %w", err)
	}
	keys := map[string]*ecdsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "EC" || k.Crv != "P-256" || (k.Use != "" && k.Use != "sig") || (k.Alg != "" && k.Alg != Algorithm) || k.Kid == "" {
			continue
		}
		x, errX := base64.RawURLEncoding.DecodeString(k.X)
		y, errY := base64.RawURLEncoding.DecodeString(k.Y)
		if errX != nil || errY != nil || len(x) != 32 || len(y) != 32 {
			continue
		}
		pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, errors.New("the key set holds no P-256 signing key")
	}
	return keys, nil
}

// Expect is what a consumer pins before it trusts a Run identity.
type Expect struct {
	// Issuer is the ploegd URL the consumer is configured with, compared
	// byte for byte with "iss".
	Issuer string
	// Audience is the consumer's own audience, which "aud" must name.
	Audience string
	// Leeway is the clock skew tolerated on exp, nbf and iat; at most a
	// minute.
	Leeway time.Duration
	// Now replaces the clock; nil is time.Now.
	Now func() time.Time
}

// Verify checks a Run identity's type, ES256 signature against keys, issuer,
// audience, expiry and lifetime, and returns its claims.
func Verify(token string, keys map[string]*ecdsa.PublicKey, want Expect) (Claims, error) {
	var claims Claims
	if want.Issuer == "" || want.Audience == "" {
		return claims, errors.New("pin the issuer and your own audience before verifying a run identity")
	}
	leeway := want.Leeway
	if leeway < 0 || leeway > time.Minute {
		leeway = time.Minute
	}
	now := want.Now
	if now == nil {
		now = time.Now
	}
	parsed, err := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		if typ, _ := t.Header["typ"].(string); typ != TokenType {
			return nil, fmt.Errorf("typ %q is not %s", typ, TokenType)
		}
		kid, _ := t.Header["kid"].(string)
		key, ok := keys[kid]
		if !ok {
			return nil, fmt.Errorf("kid %q is not in the key set", kid)
		}
		return key, nil
	},
		jwt.WithValidMethods([]string{Algorithm}),
		jwt.WithIssuer(want.Issuer),
		jwt.WithAudience(want.Audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(leeway),
		jwt.WithTimeFunc(now),
	)
	if err != nil {
		return Claims{}, err
	}
	if !parsed.Valid || claims.IssuedAt == nil || claims.ExpiresAt.Sub(claims.IssuedAt.Time) > MaxLifetime {
		return Claims{}, errors.New("the run identity lives longer than a run identity may")
	}
	if claims.Subject != SubjectPrefix+claims.Run.ID || claims.Run.ID == "" {
		return Claims{}, errors.New("the run identity's subject does not name its Run")
	}
	return claims, nil
}

// UnverifiedExpiry reads a token's "exp" without checking its signature, so
// a hook guard can withhold a token that already expired. Never trust it for
// anything else.
func UnverifiedExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(body, &claims) != nil || claims.Exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0), true
}
