// Package txntoken issues and verifies the Run's transaction token
// (ADR-0156): a JWT shaped like an OAuth Transaction Token
// (draft-ietf-oauth-transaction-tokens-11) that ploegd signs ES256 when a Run
// is admitted and again at each lease renewal. It names the person the Run
// acts for in "sub", the Team and Role acting for them in "act" with any
// actor the identity provider named nested inside, the Run in "txn", and the
// Run's immutable facts in "tctx". It expires with the Run's lease.
package txntoken

import (
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ploeg-hq/ploeg/pkg/signingkeys"
)

// TokenType is the JOSE "typ" header the draft requires.
const TokenType = "txntoken+jwt"

// Algorithm is the only signing algorithm a transaction token uses.
const Algorithm = signingkeys.Algorithm

// MinLifetime is the shortest lifetime ploegd issues; a Run whose lease ends
// sooner gets none.
const MinLifetime = 5 * time.Second

// Proof says how Ploeg knows the person in "sub".
const (
	// ProofProven: a person token the identity provider signed named them
	// when the work was handed over (ADR-0150).
	ProofProven = "proven"
	// ProofAsserted: an Operator Consumer's header or the tracker's people
	// mapping named them; nothing Ploeg verified.
	ProofAsserted = "asserted"
	// ProofSponsor: no person handed the work over, so the Run acts for its
	// Team's sponsor.
	ProofSponsor = "sponsor"
)

// Scopes name the purpose of a Run's transaction.
const (
	ScopeWrite = "ploeg.run.write"
	ScopeRead  = "ploeg.run.read"
)

// Actor is an RFC 8693 "act" claim: who acts, for a Run the Run's id, and
// the actor before it in the delegation chain.
type Actor struct {
	Sub string `json:"sub"`
	Run string `json:"run,omitempty"`
	Act *Actor `json:"act,omitempty"`
}

// Context is the "tctx" claim: what stays true of the Run for its whole
// life, read from ploegd's records.
type Context struct {
	WorkItem string `json:"workItem,omitempty"`
	Shift    string `json:"shift,omitempty"`
	Team     string `json:"team"`
	Role     string `json:"role,omitempty"`
	// Repository is "<forge>/<owner>/<repo>"; forge is empty for the
	// deployment's default forge.
	Repository string `json:"repository,omitempty"`
	Branch     string `json:"branch,omitempty"`
	// Allowance is the Run's authorized spend in USD as a decimal string.
	Allowance string `json:"allowance,omitempty"`
	// Isolation is the class the Run executes at (ADR-0106); empty while
	// ploegd records none.
	Isolation string `json:"isolation,omitempty"`
	// SubjectProof is ProofProven, ProofAsserted or ProofSponsor.
	SubjectProof string `json:"subjectProof"`
}

// Claims are a transaction token's JWT claims.
type Claims struct {
	jwt.RegisteredClaims
	Txn   string  `json:"txn"`
	Scope string  `json:"scope"`
	ReqWL string  `json:"req_wl"`
	Act   Actor   `json:"act"`
	TCtx  Context `json:"tctx"`
}

// Run is what ploegd signs for one Run.
type Run struct {
	// ID is the Run's id, the "txn" claim and the act's "run".
	ID string
	// Person is the person the Run acts for, Proof how Ploeg knows them and
	// IdentityActor the "act.sub" the identity provider issued, if any.
	Person        string
	Proof         string
	IdentityActor string
	Team          string
	Role          string
	Writes        bool
	// Worker is the requesting workload, "req_wl": the worker id that
	// claimed the Run.
	Worker  string
	Context Context
	// Deadline is when the Run's lease ends unless it is renewed.
	Deadline time.Time
}

// Agent is the "act.sub" ploegd writes for a Team's Role: "<team>/<role>",
// or the Team alone for a Run without a Role.
func Agent(team, role string) string {
	if role == "" {
		return team
	}
	return team + "/" + role
}

// Signer issues transaction tokens with ploegd's current key and publishes
// it, with the keys it retired, as a JWK Set.
type Signer struct {
	issuer   string
	audience string
	key      *ecdsa.PrivateKey
	kid      string
	retired  []*ecdsa.PublicKey
	now      func() time.Time
}

// NewSigner reads a P-256 private key (PEM, PKCS#8 or SEC 1) and any retired
// P-256 public keys (concatenated PEM "PUBLIC KEY" blocks, or empty). issuer
// is the http(s) URL ploegd is reached at, the "iss" claim. audience names
// Ploeg's trust domain, the "aud" claim; empty is the issuer.
func NewSigner(issuer, audience, privatePEM, retiredPEM string) (*Signer, error) {
	u, err := url.Parse(issuer)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("transaction token issuer %q must be the http(s) URL ploegd is reached at", issuer)
	}
	if audience == "" {
		audience = issuer
	}
	if why := ValidAudience(audience); why != "" {
		return nil, fmt.Errorf("transaction token audience %q %s", audience, why)
	}
	key, err := signingkeys.ParsePrivateKey(privatePEM)
	if err != nil {
		return nil, fmt.Errorf("transaction token: %w", err)
	}
	retired, err := signingkeys.ParsePublicKeys(retiredPEM)
	if err != nil {
		return nil, fmt.Errorf("transaction token: %w", err)
	}
	return &Signer{issuer: issuer, audience: audience, key: key, kid: signingkeys.Thumbprint(&key.PublicKey), retired: retired, now: time.Now}, nil
}

// ValidAudience reports why aud cannot name a trust domain, or "": an
// http(s) URL without credentials, query or fragment, or a URN, at most 256
// characters.
func ValidAudience(aud string) string {
	if aud == "" || len(aud) > 256 || strings.ContainsAny(aud, " \t\r\n") {
		return "must be 1 to 256 characters without whitespace"
	}
	if strings.HasPrefix(aud, "urn:") {
		if len(strings.SplitN(aud, ":", 3)) < 3 {
			return "a URN needs a namespace and a name"
		}
		return ""
	}
	u, err := url.Parse(aud)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "must be an http(s) URL or a urn:"
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "must not carry credentials, a query or a fragment"
	}
	return ""
}

// Issuer is the "iss" of every token s signs.
func (s *Signer) Issuer() string { return s.issuer }

// Audience is the trust domain, the "aud" of every token s signs.
func (s *Signer) Audience() string { return s.audience }

// KeyID is the RFC 7638 thumbprint of the current key, the "kid" header.
func (s *Signer) KeyID() string { return s.kid }

// KeySet is the JWK Set ploegd publishes: the current key first, then the
// retired ones.
func (s *Signer) KeySet() signingkeys.JWKS { return signingkeys.Set(&s.key.PublicKey, s.retired) }

// Keys are the public keys that verify what s signs, by kid.
func (s *Signer) Keys() map[string]*ecdsa.PublicKey {
	keys := map[string]*ecdsa.PublicKey{s.kid: &s.key.PublicKey}
	for _, k := range s.retired {
		keys[signingkeys.Thumbprint(k)] = k
	}
	return keys
}

// ErrNoPerson is a Run that names no person: no one handed its work over
// and its Team has no sponsor.
var ErrNoPerson = errors.New("the Run acts for no person: no one handed its work over and its Team has no sponsor")

// Sign issues a transaction token for run that expires at run.Deadline.
func (s *Signer) Sign(run Run) (string, time.Time, error) {
	if run.Person == "" {
		return "", time.Time{}, ErrNoPerson
	}
	switch run.Proof {
	case ProofProven, ProofAsserted, ProofSponsor:
	default:
		return "", time.Time{}, fmt.Errorf("unknown subject proof %q", run.Proof)
	}
	if run.ID == "" || run.Team == "" || run.Worker == "" {
		return "", time.Time{}, errors.New("a transaction token needs the Run's id, Team and worker")
	}
	now := s.now().Truncate(time.Second)
	expires := run.Deadline.Truncate(time.Second)
	if expires.Sub(now) < MinLifetime {
		return "", time.Time{}, errors.New("the Run's lease ends too soon for a transaction token")
	}
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", time.Time{}, err
	}
	act := Actor{Sub: Agent(run.Team, run.Role), Run: run.ID}
	if run.IdentityActor != "" {
		act.Act = &Actor{Sub: run.IdentityActor}
	}
	tctx := run.Context
	tctx.Team, tctx.Role, tctx.SubjectProof = run.Team, run.Role, run.Proof
	scope := ScopeRead
	if run.Writes {
		scope = ScopeWrite
	}
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.issuer,
			Subject:   run.Person,
			Audience:  jwt.ClaimStrings{s.audience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expires),
			ID:        hex.EncodeToString(jti),
		},
		Txn:   run.ID,
		Scope: scope,
		ReqWL: run.Worker,
		Act:   act,
		TCtx:  tctx,
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

// Expect is what a verifier pins before it trusts a transaction token.
type Expect struct {
	// Issuer is compared byte for byte with "iss".
	Issuer string
	// Audience is the verifier's trust domain, which "aud" must name.
	Audience string
	// Leeway is the clock skew tolerated on exp, nbf and iat; at most a
	// minute.
	Leeway time.Duration
	// Now replaces the clock; nil is time.Now.
	Now func() time.Time
}

// Verify checks a transaction token's type, ES256 signature against keys,
// issuer, trust domain and expiry, and that it names its Run, person,
// requesting workload and agent.
func Verify(token string, keys map[string]*ecdsa.PublicKey, want Expect) (Claims, error) {
	var claims Claims
	if want.Issuer == "" || want.Audience == "" {
		return claims, errors.New("pin the issuer and the trust domain before verifying a transaction token")
	}
	if len(token) > 8<<10 {
		return claims, errors.New("the transaction token is too long")
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
	if !parsed.Valid || claims.IssuedAt == nil {
		return Claims{}, errors.New("the transaction token has no issue time")
	}
	if claims.Txn == "" || claims.Subject == "" || claims.ReqWL == "" || claims.Act.Sub == "" || claims.Act.Run != claims.Txn || claims.TCtx.Team == "" {
		return Claims{}, errors.New("the transaction token does not name its Run, person, workload and agent")
	}
	return claims, nil
}

// LooksSigned reports whether token has the three segments of a compact JWS,
// so a verifier can tell a transaction token from the deprecated two-segment
// worker capability without parsing it.
func LooksSigned(token string) bool {
	return strings.Count(token, ".") == 2
}
