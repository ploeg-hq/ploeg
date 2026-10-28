package mcpauth_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ploeg-hq/ploeg/pkg/mcpauth"
)

// loginIssuer is an OpenID Connect provider whose token endpoint answers
// every code with the ID token the test chose.
type loginIssuer struct {
	*issuer
	url     string
	mu      sync.Mutex
	idToken string
	form    url.Values
	user    string
}

func newLoginIssuer(t *testing.T) *loginIssuer {
	t.Helper()
	keys := newIssuer(t)
	li := &loginIssuer{issuer: keys}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	li.url = srv.URL + "/o/confirm/"
	mux.HandleFunc("/o/confirm/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": li.url, "authorization_endpoint": srv.URL + "/authorize?tenant=a", "token_endpoint": srv.URL + "/token", "jwks_uri": srv.URL + "/jwks"})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		keys.mu.Lock()
		defer keys.mu.Unlock()
		var set []map[string]string
		for kid, k := range keys.keys {
			set = append(set, jwk(kid, k.Public()))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": set})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		user, _, _ := r.BasicAuth()
		li.mu.Lock()
		li.form, li.user = r.PostForm, user
		tok := li.idToken
		li.mu.Unlock()
		if tok == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": tok})
	})
	return li
}

func (li *loginIssuer) idClaims() jwt.MapClaims {
	return jwt.MapClaims{"iss": li.url, "sub": "hashed-subject", "aud": "ploeg-confirm", "azp": "ploeg-confirm", "nonce": "the-nonce",
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "auth_time": now.Add(-10 * time.Second).Unix(),
		"acr": "phr", "amr": []string{"hwk", "user"}, "preferred_username": "ryan"}
}

func (li *loginIssuer) answer(c jwt.MapClaims) {
	li.mu.Lock()
	defer li.mu.Unlock()
	li.idToken = li.sign("rsa-1", c)
}

func (li *loginIssuer) login(t *testing.T) *mcpauth.Login {
	t.Helper()
	l, err := mcpauth.NewLogin(mcpauth.LoginConfig{Issuer: li.url, AllowLoopbackHTTP: true, ClientID: "ploeg-confirm", ClientSecret: "s3cret", RedirectURL: "https://ploeg-mcp.example.com/confirmations/callback",
		Leeway: time.Minute, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestALoginAsksForAFreshSignInWithPKCE(t *testing.T) {
	li := newLoginIssuer(t)
	to, err := li.login(t).AuthorizationURL(context.Background(), mcpauth.LoginRequest{State: "st", Nonce: "no", Challenge: "ch", ACRValues: []string{"phr", "phrh"}})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(to)
	q := u.Query()
	for k, v := range map[string]string{"tenant": "a", "response_type": "code", "client_id": "ploeg-confirm", "redirect_uri": "https://ploeg-mcp.example.com/confirmations/callback",
		"scope": "openid profile", "state": "st", "nonce": "no", "code_challenge": "ch", "code_challenge_method": "S256", "prompt": "login", "max_age": "0", "acr_values": "phr phrh"} {
		if q.Get(k) != v {
			t.Fatalf("%s = %q, want %q in %s", k, q.Get(k), v, to)
		}
	}
}

func TestALoginRedeemsACodeForAVerifiedIDToken(t *testing.T) {
	li := newLoginIssuer(t)
	li.answer(li.idClaims())
	got, err := li.login(t).Redeem(context.Background(), "the-code", "the-verifier", "the-nonce")
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != "hashed-subject" || got.Username != "ryan" || got.ACR != "phr" || strings.Join(got.AMR, " ") != "hwk user" || !got.AuthTime.Equal(now.Add(-10*time.Second).Truncate(time.Second)) {
		t.Fatalf("id token = %+v", got)
	}
	if li.user != "ploeg-confirm" || li.form.Get("code") != "the-code" || li.form.Get("code_verifier") != "the-verifier" || li.form.Get("grant_type") != "authorization_code" ||
		li.form.Get("redirect_uri") != "https://ploeg-mcp.example.com/confirmations/callback" {
		t.Fatalf("token request: user %q form %v", li.user, li.form)
	}
}

func TestIDTokensThatMustNotPassAreRefused(t *testing.T) {
	li := newLoginIssuer(t)
	other := newIssuer(t)
	cases := map[string]func() string{
		"another nonce": func() string { c := li.idClaims(); c["nonce"] = "replayed"; return li.sign("rsa-1", c) },
		"no nonce":      func() string { c := li.idClaims(); delete(c, "nonce"); return li.sign("rsa-1", c) },
		"another audience": func() string {
			c := li.idClaims()
			c["aud"] = "ploeg-mcp"
			c["azp"] = "ploeg-mcp"
			return li.sign("rsa-1", c)
		},
		"an audience without this client": func() string { c := li.idClaims(); c["aud"] = "ploeg-mcp"; return li.sign("rsa-1", c) },
		"another authorized party":        func() string { c := li.idClaims(); c["azp"] = "ploeg-mcp"; return li.sign("rsa-1", c) },
		"two audiences without azp": func() string {
			c := li.idClaims()
			c["aud"] = []string{"ploeg-confirm", "x"}
			delete(c, "azp")
			return li.sign("rsa-1", c)
		},
		"no auth_time": func() string { c := li.idClaims(); delete(c, "auth_time"); return li.sign("rsa-1", c) },
		"no sub":       func() string { c := li.idClaims(); delete(c, "sub"); return li.sign("rsa-1", c) },
		"no iat":       func() string { c := li.idClaims(); delete(c, "iat"); return li.sign("rsa-1", c) },
		"expired": func() string {
			c := li.idClaims()
			c["exp"] = now.Add(-2 * time.Minute).Unix()
			return li.sign("rsa-1", c)
		},
		"another issuer":             func() string { c := li.idClaims(); c["iss"] = other.url; return li.sign("rsa-1", c) },
		"a signature by another key": func() string { return other.sign("rsa-1", li.idClaims()) },
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			tok := token()
			li.mu.Lock()
			li.idToken = tok
			li.mu.Unlock()
			if _, err := li.login(t).Redeem(context.Background(), "code", "verifier", "the-nonce"); !errors.Is(err, mcpauth.ErrLoginRefused) {
				t.Fatalf("err = %v, want ErrLoginRefused", err)
			}
		})
	}
}

func TestARefusedCodeIsALoginRefusal(t *testing.T) {
	li := newLoginIssuer(t)
	if _, err := li.login(t).Redeem(context.Background(), "code", "verifier", "the-nonce"); !errors.Is(err, mcpauth.ErrLoginRefused) || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewLoginRefusesUnsafeConfiguration(t *testing.T) {
	good := mcpauth.LoginConfig{Issuer: "https://id.example.com/o/confirm/", ClientID: "c", ClientSecret: "s", RedirectURL: "https://ploeg-mcp.example.com/confirmations/callback"}
	if _, err := mcpauth.NewLogin(good); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*mcpauth.LoginConfig){
		"an issuer with a query":     func(c *mcpauth.LoginConfig) { c.Issuer = "https://id.example.com/?a=b" },
		"no client id":               func(c *mcpauth.LoginConfig) { c.ClientID = "" },
		"a client id with a colon":   func(c *mcpauth.LoginConfig) { c.ClientID = "a:b" },
		"no client secret":           func(c *mcpauth.LoginConfig) { c.ClientSecret = "" },
		"a redirect with a fragment": func(c *mcpauth.LoginConfig) { c.RedirectURL = "https://ploeg-mcp.example.com/cb#x" },
		"too much leeway":            func(c *mcpauth.LoginConfig) { c.Leeway = time.Hour },
	} {
		c := good
		change(&c)
		if _, err := mcpauth.NewLogin(c); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}
