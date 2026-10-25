package txntoken

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ploeg-hq/ploeg/pkg/signingkeys"
)

const (
	issuer = "https://ploeg.example"
	domain = "urn:ploeg:example"
)

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

func signerAt(t *testing.T, now time.Time, retired string) *Signer {
	t.Helper()
	_, priv := newKey(t)
	s, err := NewSigner(issuer, domain, priv, retired)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }
	return s
}

func publishedKeys(t *testing.T, s *Signer) map[string]*ecdsa.PublicKey {
	t.Helper()
	body, err := json.Marshal(s.KeySet())
	if err != nil {
		t.Fatal(err)
	}
	keys, err := signingkeys.ParseKeySet(body)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

var now = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

func aRun() Run {
	return Run{ID: "42", Person: "oidc-0123", Proof: ProofProven, Team: "bronze", Role: "builder", Writes: true,
		Worker: "pod-a", Deadline: now.Add(10 * time.Minute),
		Context: Context{WorkItem: "7", Shift: "3", Repository: "/webgrip/ploeg", Branch: "agent/vik-7", Allowance: "2.5000"}}
}

func expect(at time.Time) Expect {
	return Expect{Issuer: issuer, Audience: domain, Now: func() time.Time { return at }}
}

func TestATransactionTokenCarriesTheDraftsClaimsAndVerifiesAgainstThePublishedKeySet(t *testing.T) {
	s := signerAt(t, now, "")
	token, expires, err := s.Sign(aRun())
	if err != nil {
		t.Fatal(err)
	}
	if !expires.Equal(now.Add(10 * time.Minute)) {
		t.Fatalf("expires %v, want the lease deadline", expires)
	}
	header, _ := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[0])
	var h map[string]string
	_ = json.Unmarshal(header, &h)
	if h["typ"] != "txntoken+jwt" || h["alg"] != "ES256" || h["kid"] != s.KeyID() {
		t.Fatalf("header %v", h)
	}
	c, err := Verify(token, publishedKeys(t, s), expect(now.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if c.Issuer != issuer || len(c.Audience) != 1 || c.Audience[0] != domain || c.Subject != "oidc-0123" || c.Txn != "42" ||
		c.Scope != ScopeWrite || c.ReqWL != "pod-a" || c.ID == "" || !c.ExpiresAt.Equal(expires) {
		t.Fatalf("claims %+v", c)
	}
	if c.Act.Sub != "bronze/builder" || c.Act.Run != "42" || c.Act.Act != nil {
		t.Fatalf("act %+v", c.Act)
	}
	want := Context{WorkItem: "7", Shift: "3", Team: "bronze", Role: "builder", Repository: "/webgrip/ploeg",
		Branch: "agent/vik-7", Allowance: "2.5000", SubjectProof: ProofProven}
	if c.TCtx != want {
		t.Fatalf("tctx %+v, want %+v", c.TCtx, want)
	}
	reader := aRun()
	reader.Writes = false
	token, _, _ = s.Sign(reader)
	if c, _ := Verify(token, s.Keys(), expect(now)); c.Scope != ScopeRead {
		t.Fatalf("a reader's scope %q", c.Scope)
	}
}

func TestTheIdentityProvidersActorIsNestedInsideTheRunsActor(t *testing.T) {
	s := signerAt(t, now, "")
	run := aRun()
	run.IdentityActor = "team-bronze"
	token, _, err := s.Sign(run)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Verify(token, s.Keys(), expect(now))
	if err != nil {
		t.Fatal(err)
	}
	if c.Act.Sub != "bronze/builder" || c.Act.Act == nil || c.Act.Act.Sub != "team-bronze" || c.Act.Act.Act != nil {
		t.Fatalf("act %+v", c.Act)
	}
	payload, _ := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[1])
	if !strings.Contains(string(payload), `"act":{"sub":"bronze/builder","run":"42","act":{"sub":"team-bronze"}}`) {
		t.Fatalf("payload %s", payload)
	}
}

func TestATransactionTokenExpiresAtTheRunsDeadline(t *testing.T) {
	s := signerAt(t, now, "")
	run := aRun()
	run.Deadline = now.Add(90 * time.Second)
	token, _, err := s.Sign(run)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(token, s.Keys(), Expect{Issuer: issuer, Audience: domain, Leeway: time.Nanosecond,
		Now: func() time.Time { return now.Add(89 * time.Second) }}); err != nil {
		t.Fatalf("before the deadline: %v", err)
	}
	if _, err := Verify(token, s.Keys(), Expect{Issuer: issuer, Audience: domain, Leeway: time.Nanosecond,
		Now: func() time.Time { return now.Add(91 * time.Second) }}); !errors.Is(err, jwt.ErrTokenExpired) {
		t.Fatalf("after the deadline: %v", err)
	}
	run.Deadline = now.Add(2 * time.Second)
	if _, _, err := s.Sign(run); err == nil {
		t.Fatal("a lease ending in two seconds got a token")
	}
}

func TestSignRefusesARunThatNamesNoPerson(t *testing.T) {
	s := signerAt(t, now, "")
	run := aRun()
	run.Person = ""
	if _, _, err := s.Sign(run); !errors.Is(err, ErrNoPerson) {
		t.Fatalf("no person: %v", err)
	}
	run = aRun()
	run.Proof = ""
	if _, _, err := s.Sign(run); err == nil {
		t.Fatal("an unmarked subject was signed")
	}
	for _, mutate := range []func(*Run){func(r *Run) { r.ID = "" }, func(r *Run) { r.Team = "" }, func(r *Run) { r.Worker = "" }} {
		run = aRun()
		mutate(&run)
		if _, _, err := s.Sign(run); err == nil {
			t.Fatalf("incomplete run signed: %+v", run)
		}
	}
}

func TestVerifyRefuses(t *testing.T) {
	s := signerAt(t, now, "")
	token, _, err := s.Sign(aRun())
	if err != nil {
		t.Fatal(err)
	}
	other := signerAt(t, now, "")
	otherToken, _, _ := other.Sign(aRun())
	parts := strings.Split(token, ".")
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(strings.Replace(string(payload), "oidc-0123", "oidc-9999", 1))) + "." + parts[2]

	untyped := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{"iss": issuer, "aud": domain, "sub": "x", "txn": "42",
		"iat": now.Unix(), "exp": now.Add(time.Minute).Unix(), "req_wl": "pod", "act": map[string]any{"sub": "bronze", "run": "42"},
		"tctx": map[string]any{"team": "bronze"}})
	untyped.Header["kid"] = s.KeyID()
	untypedToken, _ := untyped.SignedString(s.key)

	noAct := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{"iss": issuer, "aud": domain, "sub": "x", "txn": "42",
		"iat": now.Unix(), "exp": now.Add(time.Minute).Unix(), "req_wl": "pod", "tctx": map[string]any{"team": "bronze"}})
	noAct.Header["kid"], noAct.Header["typ"] = s.KeyID(), TokenType
	noActToken, _ := noAct.SignedString(s.key)

	for name, tc := range map[string]struct {
		token string
		want  Expect
	}{
		"another key":          {otherToken, expect(now)},
		"another trust domain": {token, Expect{Issuer: issuer, Audience: "urn:ploeg:elsewhere", Now: expect(now).Now}},
		"another issuer":       {token, Expect{Issuer: "https://evil.example", Audience: domain, Now: expect(now).Now}},
		"a tampered subject":   {tampered, expect(now)},
		"no typ":               {untypedToken, expect(now)},
		"no act":               {noActToken, expect(now)},
		"nothing pinned":       {token, Expect{}},
		"an expired token":     {token, expect(now.Add(time.Hour))},
		"the HMAC capability":  {"eyJhdWQiOiJwbG9lZy13b3JrZXItcnVuIn0.c2ln", expect(now)},
	} {
		if _, err := Verify(tc.token, s.Keys(), tc.want); err == nil {
			t.Errorf("%s verified", name)
		}
	}
}

func TestARetiredKeyIsPublishedAndStillVerifiesButNeverSigns(t *testing.T) {
	old := signerAt(t, now, "")
	oldToken, _, err := old.Sign(aRun())
	if err != nil {
		t.Fatal(err)
	}
	rotated := signerAt(t, now, publicPEM(t, old.key))
	set := rotated.KeySet()
	if len(set.Keys) != 2 || set.Keys[0].Kid != rotated.KeyID() || set.Keys[1].Kid != old.KeyID() {
		t.Fatalf("key set %+v", set)
	}
	if _, err := Verify(oldToken, publishedKeys(t, rotated), expect(now)); err != nil {
		t.Fatalf("a token signed before the rotation: %v", err)
	}
	newToken, _, _ := rotated.Sign(aRun())
	if _, err := Verify(newToken, publishedKeys(t, old), expect(now)); err == nil {
		t.Fatal("the old key set verified a token of the new key")
	}
}

func TestNewSignerRefuses(t *testing.T) {
	_, priv := newKey(t)
	for name, tc := range map[string]struct{ issuer, audience, key, retired string }{
		"no issuer":              {"", "", priv, ""},
		"an issuer with a query": {"https://ploeg.example?x=1", "", priv, ""},
		"a bad audience":         {issuer, "not a domain", priv, ""},
		"no key":                 {issuer, domain, "", ""},
		"a malformed retired":    {issuer, domain, priv, "nonsense"},
	} {
		if _, err := NewSigner(tc.issuer, tc.audience, tc.key, tc.retired); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	s, err := NewSigner(issuer, "", priv, "")
	if err != nil || s.Audience() != issuer {
		t.Fatalf("an empty audience is the issuer: %v %q", err, s.Audience())
	}
}

func TestLooksSigned(t *testing.T) {
	if LooksSigned("a.b") || !LooksSigned("a.b.c") {
		t.Fatal("segment count")
	}
}
