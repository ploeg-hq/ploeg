// Package claudesession verifies the session token Anthropic gives every
// Claude Code session in a self-hosted environment
// (CLAUDE_CODE_SESSION_ACCESS_TOKEN), so ploegd can trust a request that
// comes from a runner's session hook (ADR-0084).
//
// The token proves that Anthropic issued it for one session in one
// environment. It does not prove which process inside the session presents
// it: anything the session runs can read it.
package claudesession

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DefaultJWKSURL is where Anthropic publishes the keys that sign session
// tokens.
const DefaultJWKSURL = "https://api.anthropic.com/v1/code/.well-known/jwks.json"

// TokenPrefix marks a self-hosted session token. Anthropic-hosted sessions
// carry a different prefix and a different key set, so they are refused.
const TokenPrefix = "sk-ant-cc-"

const (
	issuer        = "ccr"
	sessionRole   = "session_worker"
	maxTokenBytes = 16 << 10
	maxJWKSBytes  = 1 << 20
	// refetchEvery bounds how often an unknown kid can make ploegd fetch the
	// JWKS, so forged tokens cannot turn it into a request amplifier.
	refetchEvery = time.Minute
)

// ErrInvalid is returned for every token that fails verification. The cause
// is wrapped for logs and never shown to the caller.
var ErrInvalid = errors.New("invalid claude session token")

// Claims are the verified facts ploegd uses from a session token.
type Claims struct {
	// EnvironmentID is the token's ccr:pool_id, the ccpool_… id of the
	// environment the session runs in. It is always one of the audiences.
	EnvironmentID string
	// SessionID is the session's id without its cse_ or session_ prefix.
	SessionID string
	// Creator is act.sub: user:<id> for a person, agent:<id> for an
	// organisation's service identity.
	Creator   string
	ExpiresAt time.Time
}

// Verifier checks session tokens against Anthropic's JWKS.
type Verifier struct {
	// JWKSURL defaults to DefaultJWKSURL.
	JWKSURL string
	// HTTP defaults to a client with a 10 second timeout.
	HTTP *http.Client
	// Now defaults to time.Now.
	Now func() time.Time

	mu        sync.Mutex
	keys      map[string]*ecdsa.PublicKey
	fetchedAt time.Time
}

// Verify checks the token's prefix, ES256 signature, issuer, role, expiry and
// that its environment is one of its audiences.
func (v *Verifier) Verify(ctx context.Context, token string) (Claims, error) {
	if len(token) > maxTokenBytes || !strings.HasPrefix(token, TokenPrefix) {
		return Claims{}, fmt.Errorf("%w: not a self-hosted session token", ErrInvalid)
	}
	parts := strings.Split(strings.TrimPrefix(token, TokenPrefix), ".")
	if len(parts) != 3 {
		return Claims{}, fmt.Errorf("%w: not a JWT", ErrInvalid)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeSegment(parts[0], &header); err != nil {
		return Claims{}, fmt.Errorf("%w: header: %v", ErrInvalid, err)
	}
	if header.Alg != "ES256" || header.Kid == "" {
		return Claims{}, fmt.Errorf("%w: alg %q", ErrInvalid, header.Alg)
	}
	key, err := v.key(ctx, header.Kid)
	if err != nil {
		return Claims{}, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return Claims{}, fmt.Errorf("%w: signature encoding", ErrInvalid)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(key, digest[:], r, s) {
		return Claims{}, fmt.Errorf("%w: signature", ErrInvalid)
	}

	var raw struct {
		Iss       string          `json:"iss"`
		Aud       json.RawMessage `json:"aud"`
		Exp       int64           `json:"exp"`
		Role      string          `json:"ccr:role"`
		SessionID string          `json:"ccr:session_id"`
		PoolID    string          `json:"ccr:pool_id"`
		Act       struct {
			Sub string `json:"sub"`
		} `json:"act"`
	}
	if err := decodeSegment(parts[1], &raw); err != nil {
		return Claims{}, fmt.Errorf("%w: claims: %v", ErrInvalid, err)
	}
	if raw.Iss != issuer {
		return Claims{}, fmt.Errorf("%w: issuer %q", ErrInvalid, raw.Iss)
	}
	if raw.Role != sessionRole {
		return Claims{}, fmt.Errorf("%w: role %q", ErrInvalid, raw.Role)
	}
	expires := time.Unix(raw.Exp, 0)
	if raw.Exp == 0 || !v.now().Before(expires) {
		return Claims{}, fmt.Errorf("%w: expired", ErrInvalid)
	}
	if !strings.HasPrefix(raw.PoolID, "ccpool_") || !audienceContains(raw.Aud, raw.PoolID) {
		return Claims{}, fmt.Errorf("%w: environment %q is not an audience", ErrInvalid, raw.PoolID)
	}
	session := NormalizeSessionID(raw.SessionID)
	if session == "" {
		return Claims{}, fmt.Errorf("%w: no session id", ErrInvalid)
	}
	return Claims{EnvironmentID: raw.PoolID, SessionID: session, Creator: raw.Act.Sub, ExpiresAt: expires}, nil
}

// NormalizeSessionID strips the cse_ or session_ prefix: the dispatch CLI
// prints session_…, the runner and its token use cse_…, and both name the
// same session.
func NormalizeSessionID(id string) string {
	for _, p := range []string{"cse_", "session_"} {
		if strings.HasPrefix(id, p) {
			return strings.TrimPrefix(id, p)
		}
	}
	return ""
}

func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

// key returns the signing key for kid, refetching the JWKS once when the kid
// is unknown so a key rotation does not refuse fresh tokens.
func (v *Verifier) key(ctx context.Context, kid string) (*ecdsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	if v.keys != nil && v.now().Sub(v.fetchedAt) < refetchEvery {
		return nil, fmt.Errorf("%w: unknown key %q", ErrInvalid, kid)
	}
	keys, err := v.fetch(ctx)
	if err != nil {
		return nil, fmt.Errorf("claude session keys: %w", err)
	}
	v.keys, v.fetchedAt = keys, v.now()
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("%w: unknown key %q", ErrInvalid, kid)
}

func (v *Verifier) fetch(ctx context.Context) (map[string]*ecdsa.PublicKey, error) {
	url := v.JWKSURL
	if url == "" {
		url = DefaultJWKSURL
	}
	client := v.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("JWKS answered %d", resp.StatusCode)
	}
	var set struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			Crv string `json:"crv"`
			X   string `json:"x"`
			Y   string `json:"y"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJWKSBytes)).Decode(&set); err != nil {
		return nil, fmt.Errorf("JWKS: %w", err)
	}
	keys := map[string]*ecdsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "EC" || k.Crv != "P-256" || k.Kid == "" {
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
	return keys, nil
}

func decodeSegment(seg string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func audienceContains(raw json.RawMessage, want string) bool {
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		for _, a := range many {
			if a == want {
				return true
			}
		}
		return false
	}
	var one string
	return json.Unmarshal(raw, &one) == nil && one == want
}
