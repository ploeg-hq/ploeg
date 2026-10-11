package githubapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeGitHub struct {
	mu        sync.Mutex
	key       *rsa.PublicKey
	mints     int
	lookups   int
	requested []map[string]any
	installed map[string]int64
}

func (f *fakeGitHub) verifyJWT(t *testing.T, r *http.Request) {
	t.Helper()
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Errorf("Authorization is not a JWT: %q", token)
		return
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Errorf("signature encoding: %v", err)
		return
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(f.key, crypto.SHA256, digest[:], sig); err != nil {
		t.Errorf("JWT signature does not verify: %v", err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil || claims.Iss != "1234" || claims.Exp-claims.Iat > 600 {
		t.Errorf("claims %+v (err %v); want iss 1234 and a lifetime of at most ten minutes", claims, err)
	}
}

func newFake(t *testing.T, key *rsa.PrivateKey) (*fakeGitHub, *httptest.Server) {
	f := &fakeGitHub{key: &key.PublicKey, installed: map[string]int64{"webgrip/ploeg": 77}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.verifyJWT(t, r)
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/installation"):
			f.lookups++
			repo := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"), "/installation"))
			id, ok := f.installed[repo]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]int64{"id": id})
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/77/access_tokens":
			f.mints++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.requested = append(f.requested, body)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_fixture", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusTeapot)
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestTokenIsRepositoryScopedWithRequestedPermissionsAndCached(t *testing.T) {
	key := testKey(t)
	f, srv := newFake(t, key)
	m := &Minter{AppID: "1234", Key: key, APIURL: srv.URL, Permissions: Permissions{"issues": "write", "metadata": "read"}}

	for range 3 {
		token, err := m.Token(context.Background(), "WebGrip", "Ploeg")
		if err != nil || token != "ghs_fixture" {
			t.Fatalf("Token = %q, %v", token, err)
		}
	}
	if f.mints != 1 || f.lookups != 1 {
		t.Fatalf("minted %d tokens after %d installation lookups; want one of each for three calls", f.mints, f.lookups)
	}
	repos, _ := f.requested[0]["repositories"].([]any)
	perms, _ := f.requested[0]["permissions"].(map[string]any)
	if len(repos) != 1 || repos[0] != "Ploeg" || perms["issues"] != "write" || perms["metadata"] != "read" || len(perms) != 2 {
		t.Fatalf("token request %+v; want one repository and exactly issues:write, metadata:read", f.requested[0])
	}

	m.Forget("webgrip", "ploeg")
	if _, err := m.Token(context.Background(), "webgrip", "ploeg"); err != nil {
		t.Fatal(err)
	}
	if f.mints != 2 || f.lookups != 1 {
		t.Fatalf("after Forget: %d mints, %d lookups; want a fresh token from the cached installation", f.mints, f.lookups)
	}
}

func TestTokenNearExpiryIsReminted(t *testing.T) {
	key := testKey(t)
	f, srv := newFake(t, key)
	now := time.Now()
	m := &Minter{AppID: "1234", Key: key, APIURL: srv.URL, Now: func() time.Time { return now }}
	if _, err := m.Token(context.Background(), "webgrip", "ploeg"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(56 * time.Minute)
	if _, err := m.Token(context.Background(), "webgrip", "ploeg"); err != nil {
		t.Fatal(err)
	}
	if f.mints != 2 {
		t.Fatalf("minted %d tokens; a token four minutes from expiry must be replaced", f.mints)
	}
}

func TestTokenForUninstalledRepositoryNamesIt(t *testing.T) {
	key := testKey(t)
	_, srv := newFake(t, key)
	m := &Minter{AppID: "1234", Key: key, APIURL: srv.URL}
	_, err := m.Token(context.Background(), "someone", "else")
	if err == nil || !strings.Contains(err.Error(), "not installed on someone/else") {
		t.Fatalf("err = %v; want it to say the App is not installed on the repository", err)
	}
}

func TestParsePrivateKeyAcceptsPKCS1AndPKCS8(t *testing.T) {
	key := testKey(t)
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	for name, raw := range map[string][]byte{"pkcs1": pkcs1, "pkcs8": pkcs8} {
		got, err := ParsePrivateKey(raw)
		if err != nil || !got.Equal(key) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := ParsePrivateKey([]byte("not a key")); err == nil {
		t.Error("a non-PEM key parsed")
	}
}
