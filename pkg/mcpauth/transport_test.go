package mcpauth_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/ploeg-hq/ploeg/pkg/mcpauth"
)

func TestAnHTTPIdentityProviderIsRefusedWithoutTheLoopbackSetting(t *testing.T) {
	refused := map[string]mcpauth.Config{
		"an http issuer": {Issuer: "http://id.example/application/o/x/", Audiences: []string{"a"}},
		"a loopback http issuer without the setting":  {Issuer: "http://127.0.0.1:9000/application/o/x/", Audiences: []string{"a"}},
		"a remote http issuer with the setting":       {Issuer: "http://id.example/application/o/x/", Audiences: []string{"a"}, AllowLoopbackHTTP: true},
		"a loopback-looking remote host":              {Issuer: "http://127.0.0.1.example/application/o/x/", Audiences: []string{"a"}, AllowLoopbackHTTP: true},
		"an http key set":                             {Issuer: "https://id.example/", JWKSURL: "http://id.example/jwks/", Audiences: []string{"a"}},
		"a loopback http key set without the setting": {Issuer: "https://id.example/", JWKSURL: "http://localhost/jwks/", Audiences: []string{"a"}},
		"another scheme":                              {Issuer: "ftp://id.example/", Audiences: []string{"a"}, AllowLoopbackHTTP: true},
	}
	for name, cfg := range refused {
		if _, err := mcpauth.NewVerifier(cfg); err == nil {
			t.Errorf("verifier with %s: want an error", name)
		}
	}
	accepted := map[string]mcpauth.Config{
		"https": {Issuer: "https://id.example/application/o/x/", JWKSURL: "https://id.example/jwks/", Audiences: []string{"a"}},
		"a loopback http issuer with the setting":  {Issuer: "http://127.0.0.1:9000/application/o/x/", Audiences: []string{"a"}, AllowLoopbackHTTP: true},
		"localhost and IPv6 loopback with setting": {Issuer: "http://localhost:9000/", JWKSURL: "http://[::1]:9000/jwks/", Audiences: []string{"a"}, AllowLoopbackHTTP: true},
	}
	for name, cfg := range accepted {
		if _, err := mcpauth.NewVerifier(cfg); err != nil {
			t.Errorf("verifier with %s: %v", name, err)
		}
	}
	login := mcpauth.LoginConfig{ClientID: "c", ClientSecret: "s", RedirectURL: "https://ploeg-mcp.example.com/confirmations/callback"}
	for issuer, allow := range map[string]bool{"http://id.example/o/confirm/": true, "http://127.0.0.1:9000/o/confirm/": false} {
		cfg := login
		cfg.Issuer, cfg.AllowLoopbackHTTP = issuer, allow
		if _, err := mcpauth.NewLogin(cfg); err == nil {
			t.Errorf("login with issuer %s and the setting %v: want an error", issuer, allow)
		}
	}
	login.Issuer, login.AllowLoopbackHTTP = "http://127.0.0.1:9000/o/confirm/", true
	if _, err := mcpauth.NewLogin(login); err != nil {
		t.Errorf("login with a loopback http issuer and the setting: %v", err)
	}
	exchange := mcpauth.ExchangeConfig{ClientID: "c", ClientSecret: "s", Audience: "ploeg"}
	for tokenURL, allow := range map[string]bool{"http://id.example/token": true, "http://127.0.0.1:9000/token": false} {
		cfg := exchange
		cfg.TokenURL, cfg.AllowLoopbackHTTP = tokenURL, allow
		if _, err := mcpauth.NewExchanger(cfg); err == nil {
			t.Errorf("exchanger with %s and the setting %v: want an error", tokenURL, allow)
		}
	}
	exchange.TokenURL = "https://id.example/token"
	if _, err := mcpauth.NewExchanger(exchange); err != nil {
		t.Errorf("exchanger over https: %v", err)
	}
}

func TestAnHTTPSIssuerVerifiesWithoutTheLoopbackSetting(t *testing.T) {
	is := newIssuerServedBy(t, httptest.NewTLSServer)
	v, err := mcpauth.NewVerifier(mcpauth.Config{Issuer: is.url, Audiences: []string{"ploeg-mcp"}, HTTPClient: is.srv.Client(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(context.Background(), is.sign("rsa-1", is.accessClaims()), nil); err != nil {
		t.Fatalf("a token from an https issuer: %v", err)
	}
}

func TestDiscoveryNamingAnHTTPKeySetIsRefused(t *testing.T) {
	plain := newIssuer(t)
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": srv.URL + "/", "jwks_uri": plain.srv.URL + "/application/o/ploeg-mcp/jwks/"})
	}))
	t.Cleanup(srv.Close)
	ready := func(allow bool) error {
		v, err := mcpauth.NewVerifier(mcpauth.Config{Issuer: srv.URL + "/", Audiences: []string{"a"}, AllowLoopbackHTTP: allow, HTTPClient: srv.Client()})
		if err != nil {
			t.Fatal(err)
		}
		return v.Ready(context.Background())
	}
	if err := ready(false); err == nil || plain.fetches.Load() != 0 {
		t.Fatalf("an http jwks_uri without the setting: want no fetch and an error, got %d fetches and %v", plain.fetches.Load(), err)
	}
	if err := ready(true); err != nil || plain.fetches.Load() != 1 {
		t.Fatalf("a loopback http jwks_uri with the setting: want one fetch, got %d and %v", plain.fetches.Load(), err)
	}
}

func resourceVerifier(t *testing.T, is *issuer, resource string) *mcpauth.Verifier {
	t.Helper()
	v, err := mcpauth.NewVerifier(mcpauth.Config{Issuer: is.url, AllowLoopbackHTTP: true, Audiences: []string{"ploeg-mcp", "other-client"}, Resource: resource, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

const mcpResource = "https://ploeg-mcp.example.com/mcp"

func TestATokenForTheResourcePasses(t *testing.T) {
	is := newIssuer(t)
	v := resourceVerifier(t, is, "https://Ploeg-MCP.example.com/mcp")
	for name, aud := range map[string]any{
		"the resource":                              mcpResource,
		"the resource beside a client id":           []string{"ploeg-mcp", mcpResource},
		"the resource, scheme and host in capitals": "HTTPS://PLOEG-MCP.EXAMPLE.COM/mcp",
	} {
		c := is.accessClaims()
		c["aud"] = aud
		info, err := v.Verify(context.Background(), is.sign("rsa-1", c), nil)
		if err != nil {
			t.Errorf("aud %s: %v", name, err)
			continue
		}
		if info.Extra["client"] != "ploeg-mcp" {
			t.Errorf("aud %s: client %v", name, info.Extra["client"])
		}
	}
}

func TestATokenNotForTheResourceIsRefused(t *testing.T) {
	is := newIssuer(t)
	v := resourceVerifier(t, is, mcpResource)
	for name, mutate := range map[string]func(jwt.MapClaims){
		"aud a client id but not the resource": func(c jwt.MapClaims) { c["aud"] = "ploeg-mcp" },
		"aud another resource":                 func(c jwt.MapClaims) { c["aud"] = "https://other.example.com/mcp" },
		"aud the resource with another path":   func(c jwt.MapClaims) { c["aud"] = mcpResource + "/other" },
		"aud the resource, path in capitals":   func(c jwt.MapClaims) { c["aud"] = "https://ploeg-mcp.example.com/MCP" },
		"aud the resource, a trailing slash":   func(c jwt.MapClaims) { c["aud"] = mcpResource + "/" },
		"aud the resource on another port":     func(c jwt.MapClaims) { c["aud"] = "https://ploeg-mcp.example.com:8443/mcp" },
		"aud the resource over http":           func(c jwt.MapClaims) { c["aud"] = "http://ploeg-mcp.example.com/mcp" },
		"the resource, a foreign azp":          func(c jwt.MapClaims) { c["aud"] = mcpResource; c["azp"] = "another-client" },
		"the resource, an ID token":            func(c jwt.MapClaims) { c["aud"] = mcpResource; delete(c, "azp"); delete(c, "scope") },
		"no aud":                               func(c jwt.MapClaims) { delete(c, "aud") },
	} {
		c := is.accessClaims()
		mutate(c)
		_, err := v.Verify(context.Background(), is.sign("rsa-1", c), nil)
		if !errors.Is(err, auth.ErrInvalidToken) {
			t.Errorf("%s: want ErrInvalidToken, got %v", name, err)
		}
	}
	c := is.accessClaims()
	_, err := v.Verify(context.Background(), is.sign("rsa-1", c), nil)
	if err == nil || !strings.Contains(err.Error(), "RFC 8707 resource parameter") {
		t.Errorf("a client-id audience should name the resource parameter, got %v", err)
	}
}

func TestAResourceIsRefusedWhereItCannotHold(t *testing.T) {
	for name, cfg := range map[string]mcpauth.Config{
		"a relative resource":               {Issuer: "https://id.example/", Audiences: []string{"a"}, Resource: "/mcp"},
		"a resource with a fragment":        {Issuer: "https://id.example/", Audiences: []string{"a"}, Resource: mcpResource + "#x"},
		"a resource with credentials":       {Issuer: "https://id.example/", Audiences: []string{"a"}, Resource: "https://u:p@ploeg-mcp.example.com/mcp"},
		"a resource with authorized party":  {Issuer: "https://id.example/", Audiences: []string{"a"}, Resource: mcpResource, AuthorizedParties: []string{"b"}},
		"a resource with self-issued party": {Issuer: "https://id.example/", Audiences: []string{"a"}, Resource: mcpResource, SelfIssuedAudiences: []string{"a"}},
	} {
		if _, err := mcpauth.NewVerifier(cfg); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
