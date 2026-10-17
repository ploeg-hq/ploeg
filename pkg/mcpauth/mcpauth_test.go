package mcpauth_test

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/ploeg-hq/ploeg/pkg/mcpauth"
)

type issuer struct {
	t       *testing.T
	srv     *httptest.Server
	url     string
	mu      sync.Mutex
	keys    map[string]crypto.Signer
	fetches atomic.Int32
	down    atomic.Bool
	delay   atomic.Int64
}

func newIssuer(t *testing.T) *issuer {
	t.Helper()
	is := &issuer{t: t, keys: map[string]crypto.Signer{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/application/o/ploeg-mcp/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": is.url, "jwks_uri": is.srv.URL + "/application/o/ploeg-mcp/jwks/"})
	})
	mux.HandleFunc("/application/o/ploeg-mcp/jwks/", func(w http.ResponseWriter, r *http.Request) {
		is.fetches.Add(1)
		time.Sleep(time.Duration(is.delay.Load()))
		if is.down.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		is.mu.Lock()
		defer is.mu.Unlock()
		var keys []map[string]string
		for kid, k := range is.keys {
			keys = append(keys, jwk(kid, k.Public()))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
	})
	is.srv = httptest.NewServer(mux)
	t.Cleanup(is.srv.Close)
	is.url = is.srv.URL + "/application/o/ploeg-mcp/"
	is.addRSA("rsa-1")
	return is
}

func (is *issuer) addRSA(kid string) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		is.t.Fatal(err)
	}
	is.mu.Lock()
	is.keys[kid] = k
	is.mu.Unlock()
}

func (is *issuer) addEC(kid string) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		is.t.Fatal(err)
	}
	is.mu.Lock()
	is.keys[kid] = k
	is.mu.Unlock()
}

func jwk(kid string, pub crypto.PublicKey) map[string]string {
	b := func(i *big.Int) string { return base64.RawURLEncoding.EncodeToString(i.Bytes()) }
	switch k := pub.(type) {
	case *rsa.PublicKey:
		return map[string]string{"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256", "n": b(k.N), "e": b(big.NewInt(int64(k.E)))}
	case *ecdsa.PublicKey:
		raw, _ := k.Bytes()
		size := (len(raw) - 1) / 2
		return map[string]string{"kty": "EC", "kid": kid, "use": "sig", "crv": "P-256", "x": base64.RawURLEncoding.EncodeToString(raw[1 : 1+size]), "y": base64.RawURLEncoding.EncodeToString(raw[1+size:])}
	}
	panic("unknown key")
}

var now = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func (is *issuer) accessClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"iss": is.url, "sub": "hashed-subject", "aud": "ploeg-mcp", "azp": "ploeg-mcp",
		"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(), "auth_time": now.Unix(),
		"scope": "openid ploeg:read offline_access", "groups": []string{"ploeg-readers", "homelab-admins"},
		"preferred_username": "ryan", "jti": "j1", "uid": "u1",
	}
}

func (is *issuer) sign(kid string, c jwt.MapClaims) string {
	is.t.Helper()
	is.mu.Lock()
	key := is.keys[kid]
	is.mu.Unlock()
	method := jwt.SigningMethod(jwt.SigningMethodRS256)
	if _, ok := key.(*ecdsa.PrivateKey); ok {
		method = jwt.SigningMethodES256
	}
	tok := jwt.NewWithClaims(method, c)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(key)
	if err != nil {
		is.t.Fatal(err)
	}
	return s
}

func verifier(t *testing.T, is *issuer, clock *time.Time) *mcpauth.Verifier {
	t.Helper()
	v, err := mcpauth.NewVerifier(mcpauth.Config{Issuer: is.url, Audiences: []string{"ploeg-mcp"}, Leeway: time.Minute, Now: func() time.Time { return *clock }})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestAnAccessTokenFromTheIssuerIsAccepted(t *testing.T) {
	is := newIssuer(t)
	clock := now.Add(time.Minute)
	v := verifier(t, is, &clock)
	info, err := v.Verify(context.Background(), is.sign("rsa-1", is.accessClaims()), nil)
	if err != nil {
		t.Fatal(err)
	}
	groups, _ := info.Extra["groups"].([]string)
	if info.UserID != "hashed-subject" || !strings.Contains(strings.Join(info.Scopes, " "), "ploeg:read") || len(groups) != 2 || info.Extra["username"] != "ryan" {
		t.Fatalf("unexpected token info %+v", info)
	}
}

func TestAnECSignedAccessTokenIsAccepted(t *testing.T) {
	is := newIssuer(t)
	is.addEC("ec-1")
	clock := now
	if _, err := verifier(t, is, &clock).Verify(context.Background(), is.sign("ec-1", is.accessClaims()), nil); err != nil {
		t.Fatal(err)
	}
}

func TestTokensThatMustNotPassAreInvalid(t *testing.T) {
	is := newIssuer(t)
	cases := map[string]func(jwt.MapClaims){
		"an ID token, which has neither azp nor scope": func(c jwt.MapClaims) { delete(c, "azp"); delete(c, "scope") },
		"no scope":                     func(c jwt.MapClaims) { delete(c, "scope") },
		"no azp":                       func(c jwt.MapClaims) { delete(c, "azp") },
		"another issuer":               func(c jwt.MapClaims) { c["iss"] = is.url + "x/" },
		"an issuer differing by slash": func(c jwt.MapClaims) { c["iss"] = strings.TrimSuffix(is.url, "/") },
		"another audience":             func(c jwt.MapClaims) { c["aud"] = "another-client"; c["azp"] = "another-client" },
		"an azp outside the clients":   func(c jwt.MapClaims) { c["azp"] = "another-client" },
		"no aud":                       func(c jwt.MapClaims) { delete(c, "aud") },
		"no exp":                       func(c jwt.MapClaims) { delete(c, "exp") },
		"no iat":                       func(c jwt.MapClaims) { delete(c, "iat") },
		"no sub":                       func(c jwt.MapClaims) { delete(c, "sub") },
		"a lifetime over ten minutes":  func(c jwt.MapClaims) { c["exp"] = now.Add(time.Hour).Unix() },
		"expiry beyond the leeway": func(c jwt.MapClaims) {
			c["exp"] = now.Add(-2 * time.Minute).Unix()
			c["iat"] = now.Add(-5 * time.Minute).Unix()
		},
		"an iat in the future": func(c jwt.MapClaims) { c["iat"] = now.Add(5 * time.Minute).Unix() },
		"a future nbf":         func(c jwt.MapClaims) { c["nbf"] = now.Add(5 * time.Minute).Unix() },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			clock := now
			c := is.accessClaims()
			mutate(c)
			_, err := verifier(t, is, &clock).Verify(context.Background(), is.sign("rsa-1", c), nil)
			if !errors.Is(err, auth.ErrInvalidToken) {
				t.Fatalf("want ErrInvalidToken, got %v", err)
			}
		})
	}
}

func TestExpiryWithinTheLeewayIsTolerated(t *testing.T) {
	is := newIssuer(t)
	clock := now.Add(10*time.Minute + 30*time.Second)
	if _, err := verifier(t, is, &clock).Verify(context.Background(), is.sign("rsa-1", is.accessClaims()), nil); err != nil {
		t.Fatal(err)
	}
}

func TestForgedAlgorithmsAreRefused(t *testing.T) {
	is := newIssuer(t)
	clock := now
	v := verifier(t, is, &clock)
	unsigned, err := jwt.NewWithClaims(jwt.SigningMethodNone, is.accessClaims()).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	is.mu.Lock()
	pub := is.keys["rsa-1"].Public().(*rsa.PublicKey)
	is.mu.Unlock()
	hs := jwt.NewWithClaims(jwt.SigningMethodHS256, is.accessClaims())
	hs.Header["kid"] = "rsa-1"
	confused, err := hs.SignedString(pub.N.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	foreign := jwt.NewWithClaims(jwt.SigningMethodRS256, is.accessClaims())
	foreign.Header["kid"] = "rsa-1"
	forged, _ := foreign.SignedString(other)
	for name, tok := range map[string]string{"alg none": unsigned, "HS256 keyed with the public key": confused, "a foreign signature": forged, "garbage": "not.a.jwt"} {
		if _, err := v.Verify(context.Background(), tok, nil); !errors.Is(err, auth.ErrInvalidToken) {
			t.Errorf("%s: want ErrInvalidToken, got %v", name, err)
		}
	}
}

func TestARotatedKeyIsFetchedOnceAndUnknownKeysDoNotHammerTheIssuer(t *testing.T) {
	is := newIssuer(t)
	clock := now
	v := verifier(t, is, &clock)
	if _, err := v.Verify(context.Background(), is.sign("rsa-1", is.accessClaims()), nil); err != nil {
		t.Fatal(err)
	}
	if got := is.fetches.Load(); got != 1 {
		t.Fatalf("want 1 key fetch, got %d", got)
	}
	is.addRSA("rsa-2")
	clock = clock.Add(31 * time.Second)
	if _, err := v.Verify(context.Background(), is.sign("rsa-2", is.accessClaims()), nil); err != nil {
		t.Fatalf("rotated key: %v", err)
	}
	for i := 0; i < 20; i++ {
		c := is.accessClaims()
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
		tok.Header["kid"] = "unknown"
		k, _ := rsa.GenerateKey(rand.Reader, 2048)
		s, _ := tok.SignedString(k)
		if _, err := v.Verify(context.Background(), s, nil); !errors.Is(err, auth.ErrInvalidToken) {
			t.Fatalf("unknown kid: %v", err)
		}
	}
	if got := is.fetches.Load(); got != 2 {
		t.Fatalf("want 2 key fetches after junk tokens, got %d", got)
	}
}

func TestAnUnreachableKeySetIsNotBlamedOnTheToken(t *testing.T) {
	is := newIssuer(t)
	is.down.Store(true)
	clock := now
	v := verifier(t, is, &clock)
	_, err := v.Verify(context.Background(), is.sign("rsa-1", is.accessClaims()), nil)
	if !errors.Is(err, mcpauth.ErrKeysUnavailable) || errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("want ErrKeysUnavailable, got %v", err)
	}
	if err := v.Ready(context.Background()); !errors.Is(err, mcpauth.ErrKeysUnavailable) {
		t.Fatalf("Ready: %v", err)
	}
	is.down.Store(false)
	clock = clock.Add(3 * time.Second)
	if err := v.Ready(context.Background()); err != nil {
		t.Fatalf("Ready after recovery: %v", err)
	}
	is.down.Store(true)
	clock = clock.Add(2 * time.Hour)
	if _, err := v.Verify(context.Background(), is.sign("rsa-1", is.accessClaims()), nil); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("stale keys should still be used while the issuer is down; got %v", err)
	}
}

func TestASlowIssuerStillBecomesReadyBehindAProbeThatGivesUpFirst(t *testing.T) {
	is := newIssuer(t)
	is.delay.Store(int64(time.Second))
	clock := now
	v := verifier(t, is, &clock)
	probe := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		return v.Ready(ctx)
	}
	if err := probe(); !errors.Is(err, mcpauth.ErrKeysUnavailable) {
		t.Fatalf("first probe: want ErrKeysUnavailable while the keys load, got %v", err)
	}
	if err := probe(); !errors.Is(err, mcpauth.ErrKeysUnavailable) {
		t.Fatalf("second probe: want ErrKeysUnavailable while the keys load, got %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for probe() != nil {
		if time.Now().After(deadline) {
			t.Fatal("the keys never loaded behind probes shorter than the issuer's answer")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := is.fetches.Load(); got != 1 {
		t.Fatalf("want one key fetch shared by every probe, got %d", got)
	}
}

func TestNewVerifierRefusesUnsafeConfiguration(t *testing.T) {
	for name, cfg := range map[string]mcpauth.Config{
		"no issuer":        {Audiences: []string{"a"}},
		"no audience":      {Issuer: "https://id.example/application/o/x/"},
		"symmetric alg":    {Issuer: "https://id.example/", Audiences: []string{"a"}, Algorithms: []string{"HS256"}},
		"leeway too large": {Issuer: "https://id.example/", Audiences: []string{"a"}, Leeway: 5 * time.Minute},
	} {
		if _, err := mcpauth.NewVerifier(cfg); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func guarded(t *testing.T, verify auth.TokenVerifier) *httptest.Server {
	t.Helper()
	g := mcpauth.Guard{Verify: verify, ResourceMetadataURL: "https://mcp.example/.well-known/oauth-protected-resource/mcp", RequiredScopes: []string{"ploeg:read"}, ChallengeScopes: []string{"openid", "ploeg:read", "offline_access"}}
	srv := httptest.NewServer(g.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(mcpauth.TokenInfoFrom(r.Context()).UserID))
	})))
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, url, authorization string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	b.Write(buf[:n])
	return resp, b.String()
}

func TestTheGuardAnswersPerRFC6750(t *testing.T) {
	is := newIssuer(t)
	clock := now
	v := verifier(t, is, &clock)
	srv := guarded(t, v.Verify)
	metadata := `resource_metadata="https://mcp.example/.well-known/oauth-protected-resource/mcp"`

	resp, _ := get(t, srv.URL, "")
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") != `Bearer scope="openid ploeg:read offline_access", `+metadata {
		t.Fatalf("no token: %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}

	resp, body := get(t, srv.URL, "Bearer not.a.jwt")
	if h := resp.Header.Get("WWW-Authenticate"); resp.StatusCode != http.StatusUnauthorized || !strings.HasPrefix(h, `Bearer error="invalid_token", error_description="the token is not a JWT"`) || !strings.Contains(h, metadata) || strings.Contains(body, "not.a.jwt") {
		t.Fatalf("bad token: %d %q %s", resp.StatusCode, h, body)
	}

	c := is.accessClaims()
	c["scope"] = "openid profile"
	resp, _ = get(t, srv.URL, "Bearer "+is.sign("rsa-1", c))
	if h := resp.Header.Get("WWW-Authenticate"); resp.StatusCode != http.StatusForbidden || !strings.HasPrefix(h, `Bearer error="insufficient_scope"`) || !strings.Contains(h, `scope="openid ploeg:read offline_access"`) || !strings.Contains(h, metadata) {
		t.Fatalf("missing scope: %d %q", resp.StatusCode, h)
	}

	resp, body = get(t, srv.URL, "bearer "+is.sign("rsa-1", is.accessClaims()))
	if resp.StatusCode != http.StatusOK || body != "hashed-subject" {
		t.Fatalf("valid token: %d %s", resp.StatusCode, body)
	}

	is.down.Store(true)
	cold := verifier(t, is, &clock)
	resp, _ = get(t, guarded(t, cold.Verify).URL, "Bearer "+is.sign("rsa-1", is.accessClaims()))
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("WWW-Authenticate") != "" {
		t.Fatalf("keys unavailable: %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
}

func TestResourceMetadataIsPublicAndNamesTheIssuer(t *testing.T) {
	srv := httptest.NewServer(mcpauth.ResourceMetadata("https://mcp.example/mcp", []string{"https://id.example/application/o/ploeg-mcp/"}, []string{"ploeg:read"}, "Ploeg"))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var doc map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&doc)
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" || doc["resource"] != "https://mcp.example/mcp" || doc["authorization_servers"].([]any)[0] != "https://id.example/application/o/ploeg-mcp/" {
		t.Fatalf("metadata: %v %v", resp.Header, doc)
	}
	if got := mcpauth.MetadataPath("/mcp"); got != "/.well-known/oauth-protected-resource/mcp" {
		t.Fatal(got)
	}
	if got := mcpauth.MetadataPath(""); got != "/.well-known/oauth-protected-resource" {
		t.Fatal(got)
	}
}
