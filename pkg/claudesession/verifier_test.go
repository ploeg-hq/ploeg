package claudesession

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

type fixture struct {
	key     *ecdsa.PrivateKey
	kid     string
	fetches atomic.Int32
	server  *httptest.Server
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{key: key, kid: "k1"}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.fetches.Add(1)
		raw, err := key.PublicKey.Bytes()
		if err != nil {
			t.Error(err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": f.kid, "kty": "EC", "crv": "P-256", "alg": "ES256",
			"x": base64.RawURLEncoding.EncodeToString(raw[1:33]),
			"y": base64.RawURLEncoding.EncodeToString(raw[33:]),
		}}})
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fixture) verifier() *Verifier {
	return &Verifier{JWKSURL: f.server.URL, Now: func() time.Time { return now }}
}

func validClaims() map[string]any {
	return map[string]any{
		"iss": "ccr", "aud": []string{"ccpool_env1"}, "exp": now.Add(time.Hour).Unix(),
		"ccr:role": "session_worker", "ccr:session_id": "cse_abc123", "ccr:pool_id": "ccpool_env1",
		"act": map[string]string{"sub": "user:u1"},
	}
}

func (f *fixture) sign(t *testing.T, header, claims map[string]any, key *ecdsa.PrivateKey) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signing := enc(header) + "." + enc(claims)
	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return TokenPrefix + signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (f *fixture) token(t *testing.T, claims map[string]any) string {
	return f.sign(t, map[string]any{"alg": "ES256", "kid": f.kid, "typ": "JWT"}, claims, f.key)
}

func TestVerify_AcceptsAValidSessionToken(t *testing.T) {
	f := newFixture(t)
	got, err := f.verifier().Verify(context.Background(), f.token(t, validClaims()))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.EnvironmentID != "ccpool_env1" || got.SessionID != "abc123" || got.Creator != "user:u1" {
		t.Errorf("claims = %+v", got)
	}
}

func TestVerify_Refuses(t *testing.T) {
	f := newFixture(t)
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	with := func(k string, v any) map[string]any {
		c := validClaims()
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
		return c
	}
	cases := map[string]string{
		"wrong issuer":           f.token(t, with("iss", "someone-else")),
		"wrong role":             f.token(t, with("ccr:role", "runner")),
		"expired":                f.token(t, with("exp", now.Add(-time.Second).Unix())),
		"no expiry":              f.token(t, with("exp", nil)),
		"environment not in aud": f.token(t, with("aud", []string{"ccpool_other"})),
		"not a pool id":          f.token(t, with("ccr:pool_id", "env1")),
		"no session id":          f.token(t, with("ccr:session_id", nil)),
		"signed by another key":  f.sign(t, map[string]any{"alg": "ES256", "kid": f.kid}, validClaims(), other),
		"alg none":               f.sign(t, map[string]any{"alg": "none", "kid": f.kid}, validClaims(), f.key),
		"hosted-session prefix":  "sk-ant-si-" + f.token(t, validClaims())[len(TokenPrefix):],
		"not a JWT":              TokenPrefix + "abc",
	}
	for name, tok := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := f.verifier().Verify(context.Background(), tok); !errors.Is(err, ErrInvalid) {
				t.Errorf("Verify = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestVerify_AcceptsASingleStringAudience(t *testing.T) {
	f := newFixture(t)
	c := validClaims()
	c["aud"] = "ccpool_env1"
	if _, err := f.verifier().Verify(context.Background(), f.token(t, c)); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestVerify_RefetchesForAnUnknownKidAtMostOncePerMinute(t *testing.T) {
	f := newFixture(t)
	v := f.verifier()
	if _, err := v.Verify(context.Background(), f.token(t, validClaims())); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	forged := f.sign(t, map[string]any{"alg": "ES256", "kid": "unknown"}, validClaims(), f.key)
	for range 5 {
		if _, err := v.Verify(context.Background(), forged); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Verify = %v, want ErrInvalid", err)
		}
	}
	if n := f.fetches.Load(); n != 1 {
		t.Errorf("JWKS fetched %d times, want 1", n)
	}

	f.kid = "k2"
	v.Now = func() time.Time { return now.Add(2 * time.Minute) }
	c := validClaims()
	c["exp"] = now.Add(time.Hour).Unix()
	if _, err := v.Verify(context.Background(), f.token(t, c)); err != nil {
		t.Fatalf("Verify after rotation: %v", err)
	}
	if n := f.fetches.Load(); n != 2 {
		t.Errorf("JWKS fetched %d times after rotation, want 2", n)
	}
}

func TestNormalizeSessionID(t *testing.T) {
	for in, want := range map[string]string{"cse_abc": "abc", "session_abc": "abc", "abc": "", "": ""} {
		if got := NormalizeSessionID(in); got != want {
			t.Errorf("NormalizeSessionID(%q) = %q, want %q", in, got, want)
		}
	}
}
