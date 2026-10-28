package main

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func emptyWorkItems(*remote) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = w.Write([]byte(`{"items":[],"nextCursor":null}`))
	})
}

func remoteWithAudience(t *testing.T, tokenAudience string) *remote {
	t.Helper()
	extra := map[string]string{}
	if tokenAudience != "" {
		extra[tokenAudienceEnv] = tokenAudience
	}
	return newRemoteTokens(t, emptyWorkItems, `[{"group":"ploeg-readers","tokenEnv":"READERS_TOKEN"}]`, randomToken(t), randomToken(t), extra)
}

func (r *remote) tokenForAudience(aud any) string {
	r.t.Helper()
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": r.issuer, "sub": "subject-1", "aud": aud, "azp": "ploeg-mcp", "scope": "openid ploeg:read",
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "groups": []string{"ploeg-readers"},
	})
	tok.Header["kid"] = "k1"
	s, err := tok.SignedString(r.key)
	if err != nil {
		r.t.Fatal(err)
	}
	return s
}

const toolsList = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`

func TestATokenIsAcceptedOnlyWhenItsAudienceNamesTheResource(t *testing.T) {
	r := remoteWithAudience(t, "")
	for name, aud := range map[string]any{
		"the resource":                    r.url,
		"the resource beside a client id": []string{"ploeg-mcp", r.url},
	} {
		if resp, body := r.post(r.tokenForAudience(aud), "", toolsList); resp.StatusCode != http.StatusOK {
			t.Errorf("aud %s: want 200, got %d %s", name, resp.StatusCode, body)
		}
	}
	for name, aud := range map[string]any{
		"only an accepted client id":            "ploeg-mcp",
		"another resource":                      "https://other.example/mcp",
		"the resource with a trailing slash":    r.url + "/",
		"another resource beside the client id": []string{"ploeg-mcp", "https://other.example/mcp"},
	} {
		resp, _ := r.post(r.tokenForAudience(aud), "", toolsList)
		if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(resp.Header.Get("WWW-Authenticate"), `error="invalid_token"`) {
			t.Errorf("aud %s: want 401 invalid_token, got %d %q", name, resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
		}
	}
	if slices.Contains(r.cfg.warnings, clientIDAudienceNote) {
		t.Error("the resource audience logged the client-id warning")
	}
}

func TestTheClientIDAudienceIsAnExplicitSettingThatWarns(t *testing.T) {
	r := remoteWithAudience(t, audienceClientID)
	if resp, body := r.post(r.tokenForAudience("ploeg-mcp"), "", toolsList); resp.StatusCode != http.StatusOK {
		t.Fatalf("aud client id with %s=%s: want 200, got %d %s", tokenAudienceEnv, audienceClientID, resp.StatusCode, body)
	}
	if resp, _ := r.post(r.tokenForAudience("another-client"), "", toolsList); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("aud of a client not in PLOEG_MCP_CLIENT_IDS: want 401, got %d", resp.StatusCode)
	}
	if !slices.Contains(r.cfg.warnings, clientIDAudienceNote) {
		t.Fatalf("want the client-id warning at start, got %q", r.cfg.warnings)
	}
}

func TestHTTPIdentityProviderURLsNeedTheLoopbackSetting(t *testing.T) {
	good := map[string]string{
		"PLOEG_URL":            "http://ploeg:8080",
		"PLOEG_MCP_RESOURCE":   "https://mcp.example/mcp",
		"PLOEG_MCP_ISSUER":     "https://id.example/application/o/ploeg-mcp/",
		"PLOEG_MCP_CLIENT_IDS": "ploeg-mcp",
		"PLOEG_MCP_GROUPS":     `[{"group":"ploeg-readers","tokenEnv":"READERS_TOKEN"}]`,
		"READERS_TOKEN":        strings.Repeat("a", 64),
	}
	load := func(changes map[string]string) (*httpConfig, error) {
		env := map[string]string{}
		for k, v := range good {
			env[k] = v
		}
		for k, v := range changes {
			env[k] = v
		}
		return loadHTTPConfig(func(k string) string { return env[k] }, func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	}
	exchange := map[string]string{"PLOEG_MCP_EXCHANGE_CLIENT_ID": "ploeg-mcp-exchange", "PLOEG_MCP_EXCHANGE_CLIENT_SECRET": "s", "PLOEG_MCP_EXCHANGE_AUDIENCE": "ploeg"}
	withExchange := func(tokenURL string, more map[string]string) map[string]string {
		out := map[string]string{"PLOEG_MCP_EXCHANGE_TOKEN_URL": tokenURL}
		for k, v := range exchange {
			out[k] = v
		}
		for k, v := range more {
			out[k] = v
		}
		return out
	}
	for name, changes := range map[string]map[string]string{
		"an http issuer": {"PLOEG_MCP_ISSUER": "http://id.example/application/o/ploeg-mcp/"},
		"a loopback http issuer without the setting":    {"PLOEG_MCP_ISSUER": "http://127.0.0.1:9000/application/o/ploeg-mcp/"},
		"a remote http issuer with the setting":         {"PLOEG_MCP_ISSUER": "http://id.example/application/o/ploeg-mcp/", allowLoopbackHTTPEnv: "true"},
		"an http key set":                               {"PLOEG_MCP_JWKS_URL": "http://id.example/jwks/"},
		"an http token endpoint":                        withExchange("http://id.example/token", nil),
		"a remote http token endpoint with the setting": withExchange("http://id.example/token", map[string]string{allowLoopbackHTTPEnv: "true"}),
		"an unknown loopback setting":                   {allowLoopbackHTTPEnv: "yes"},
		"an unknown token audience":                     {tokenAudienceEnv: "aud"},
	} {
		if _, err := load(changes); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	for name, changes := range map[string]map[string]string{
		"https throughout":                        nil,
		"a loopback http issuer with the setting": {"PLOEG_MCP_ISSUER": "http://127.0.0.1:9000/application/o/ploeg-mcp/", "PLOEG_MCP_JWKS_URL": "http://localhost:9000/jwks/", allowLoopbackHTTPEnv: "true"},
		"a loopback http token endpoint":          withExchange("http://[::1]:9000/token", map[string]string{allowLoopbackHTTPEnv: "true"}),
		"an https token endpoint":                 withExchange("https://id.example/token", nil),
		"the resource audience named":             {tokenAudienceEnv: audienceResource},
		"the setting off":                         {allowLoopbackHTTPEnv: "false"},
	} {
		cfg, err := load(changes)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if cfg.loopbackHTTP != slices.Contains(cfg.warnings, loopbackHTTPNote) {
			t.Errorf("%s: the loopback setting and its warning disagree: %v %q", name, cfg.loopbackHTTP, cfg.warnings)
		}
	}
}
