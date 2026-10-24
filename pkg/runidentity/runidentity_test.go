package runidentity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const issuer = "https://ploeg.example"

const audience = "https://observer.example/sessions"

func newKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func publicPEM(t *testing.T, key *ecdsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func signer(t *testing.T, now time.Time) *Signer {
	t.Helper()
	_, priv := newKey(t)
	s, err := NewSigner(issuer, priv, "")
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }
	return s
}

var run = Run{ID: "42", WorkItemID: "7", ShiftID: "3", Round: 2, Role: "builder", Team: "bronze",
	Harness: "claude-code", Branch: "agent/vik-7", Trace: "ploeg-0123456789ab"}

func keysOf(t *testing.T, s *Signer) map[string]*ecdsa.PublicKey {
	t.Helper()
	body, err := json.Marshal(s.KeySet())
	if err != nil {
		t.Fatal(err)
	}
	keys, err := ParseKeySet(body)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func TestSignedIdentityVerifiesAgainstThePublishedKeySet(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	s := signer(t, now)
	token, exp, err := s.Sign(run, audience, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(now.Add(time.Minute)) {
		t.Errorf("expires %s, want the lease deadline", exp)
	}
	got, err := Verify(token, keysOf(t, s), Expect{Issuer: issuer, Audience: audience, Now: func() time.Time { return now.Add(30 * time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	if got.Run != run || got.Subject != "run:42" || got.ID == "" {
		t.Errorf("claims = %+v", got)
	}
	header, _ := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[0])
	var h map[string]string
	_ = json.Unmarshal(header, &h)
	if h["alg"] != "ES256" || h["typ"] != TokenType || h["kid"] != s.KeyID() {
		t.Errorf("header = %v", h)
	}
}

func TestLifetimeIsBoundedByTheLeaseAndByMaxLifetime(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	s := signer(t, now)
	if _, exp, err := s.Sign(run, audience, now.Add(24*time.Hour)); err != nil || !exp.Equal(now.Add(MaxLifetime)) {
		t.Errorf("a long lease gave exp %s, %v; want %s", exp, err, MaxLifetime)
	}
	if _, _, err := s.Sign(run, audience, now.Add(time.Second)); err == nil {
		t.Error("signed an identity for a lease about to end")
	}
}

func TestVerifyRefuses(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	s := signer(t, now)
	keys := keysOf(t, s)
	token, _, err := s.Sign(run, audience, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	at := func(d time.Duration) func() time.Time { return func() time.Time { return now.Add(d) } }
	other := signer(t, now)
	forged, _, _ := other.Sign(run, audience, now.Add(time.Minute))
	otherKeys := keysOf(t, other)

	untyped := jwt.NewWithClaims(jwt.SigningMethodES256, Claims{RegisteredClaims: jwt.RegisteredClaims{Issuer: issuer,
		Subject: "run:42", Audience: jwt.ClaimStrings{audience}, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute))}, Run: run})
	untyped.Header["kid"] = s.KeyID()
	untypedToken, _ := untyped.SignedString(s.key)

	long := jwt.NewWithClaims(jwt.SigningMethodES256, Claims{RegisteredClaims: jwt.RegisteredClaims{Issuer: issuer,
		Subject: "run:42", Audience: jwt.ClaimStrings{audience}, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour))}, Run: run})
	long.Header["kid"], long.Header["typ"] = s.KeyID(), TokenType
	longToken, _ := long.SignedString(s.key)

	cases := map[string]struct {
		token string
		keys  map[string]*ecdsa.PublicKey
		want  Expect
	}{
		"expired":         {token, keys, Expect{Issuer: issuer, Audience: audience, Now: at(3 * time.Minute)}},
		"other audience":  {token, keys, Expect{Issuer: issuer, Audience: "https://elsewhere.example", Now: at(0)}},
		"other issuer":    {token, keys, Expect{Issuer: "https://other-ploeg.example", Audience: audience, Now: at(0)}},
		"unknown key":     {forged, keys, Expect{Issuer: issuer, Audience: audience, Now: at(0)}},
		"wrong signature": {forged, map[string]*ecdsa.PublicKey{s.KeyID(): otherKeys[other.KeyID()]}, Expect{Issuer: issuer, Audience: audience, Now: at(0)}},
		"no typ":          {untypedToken, keys, Expect{Issuer: issuer, Audience: audience, Now: at(0)}},
		"too long lived":  {longToken, keys, Expect{Issuer: issuer, Audience: audience, Now: at(0)}},
		"nothing pinned":  {token, keys, Expect{Now: at(0)}},
		"tampered":        {tamper(token), keys, Expect{Issuer: issuer, Audience: audience, Now: at(0)}},
	}
	for name, tc := range cases {
		if _, err := Verify(tc.token, tc.keys, tc.want); err == nil {
			t.Errorf("%s: verified", name)
		}
	}
}

func tamper(token string) string {
	parts := strings.Split(token, ".")
	body, _ := base64.RawURLEncoding.DecodeString(parts[1])
	body = []byte(strings.Replace(string(body), `"team":"bronze"`, `"team":"gold"`, 1))
	parts[1] = base64.RawURLEncoding.EncodeToString(body)
	return strings.Join(parts, ".")
}

func TestRetiredKeysArePublishedButNeverSign(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	oldKey, oldPriv := newKey(t)
	old, err := NewSigner(issuer, oldPriv, "")
	if err != nil {
		t.Fatal(err)
	}
	old.now = func() time.Time { return now }
	before, _, _ := old.Sign(run, audience, now.Add(time.Minute))

	_, priv := newKey(t)
	rotated, err := NewSigner(issuer, priv, publicPEM(t, oldKey))
	if err != nil {
		t.Fatal(err)
	}
	set := rotated.KeySet()
	if len(set.Keys) != 2 || set.Keys[0].Kid != rotated.KeyID() || set.Keys[1].Kid != Thumbprint(&oldKey.PublicKey) {
		t.Fatalf("key set = %+v", set)
	}
	if _, err := Verify(before, keysOf(t, rotated), Expect{Issuer: issuer, Audience: audience, Now: func() time.Time { return now }}); err != nil {
		t.Errorf("a token signed before the rotation no longer verifies: %v", err)
	}
}

func TestNewSignerRefuses(t *testing.T) {
	_, priv := newKey(t)
	rsaLike := "-----BEGIN RSA PRIVATE KEY-----\nAAAA\n-----END RSA PRIVATE KEY-----\n"
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(p384)
	p384PEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	cases := map[string][3]string{
		"no issuer":       {"", priv, ""},
		"issuer path":     {"ploeg.example", priv, ""},
		"no key":          {issuer, "", ""},
		"rsa":             {issuer, rsaLike, ""},
		"p-384":           {issuer, p384PEM, ""},
		"two keys":        {issuer, priv + priv, ""},
		"retired garbage": {issuer, priv, "not pem"},
		"retired private": {issuer, priv, priv},
	}
	for name, c := range cases {
		if _, err := NewSigner(c[0], c[1], c[2]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestValidAudience(t *testing.T) {
	for _, ok := range []string{"https://observer.example", "https://observer.example/api/sessions", "urn:example:observer"} {
		if why := ValidAudience(ok); why != "" {
			t.Errorf("%s refused: %s", ok, why)
		}
	}
	for _, bad := range []string{"", "http://observer.example", "https://u:p@observer.example", "https://observer.example?x=1",
		"https://observer.example#f", "observer", "urn:x", "https://a b", strings.Repeat("a", 300)} {
		if ValidAudience(bad) == "" {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestUnverifiedExpiry(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	token, exp, _ := signer(t, now).Sign(run, audience, now.Add(time.Minute))
	if got, ok := UnverifiedExpiry(token); !ok || !got.Equal(exp) {
		t.Errorf("expiry %s %v, want %s", got, ok, exp)
	}
	if _, ok := UnverifiedExpiry("not.a-token"); ok {
		t.Error("read an expiry from garbage")
	}
}
