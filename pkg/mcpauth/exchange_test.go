package mcpauth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/mcpauth"
)

func TestTheExchangerTradesThePersonsTokenForTheAudience(t *testing.T) {
	var form map[string]string
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "ploeg-mcp" || pass != "s3cret" || r.ParseForm() != nil {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		form = map[string]string{}
		for k := range r.PostForm {
			form[k] = r.PostForm.Get(k)
		}
		if r.PostForm.Get("subject_token") == "expired" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"subject token expired"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"exchanged.jwt.value","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"Bearer","expires_in":300}`))
	}))
	t.Cleanup(endpoint.Close)
	ex, err := mcpauth.NewExchanger(mcpauth.ExchangeConfig{TokenURL: endpoint.URL, ClientID: "ploeg-mcp", ClientSecret: "s3cret", Audience: "ploeg"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ex.Exchange(context.Background(), "callers.jwt.token")
	if err != nil || got != "exchanged.jwt.value" {
		t.Fatalf("exchange: %q %v", got, err)
	}
	want := map[string]string{"grant_type": "urn:ietf:params:oauth:grant-type:token-exchange", "subject_token": "callers.jwt.token",
		"subject_token_type": mcpauth.TokenTypeAccessToken, "requested_token_type": mcpauth.TokenTypeAccessToken, "audience": "ploeg"}
	for k, v := range want {
		if form[k] != v {
			t.Errorf("form %s = %q, want %q", k, form[k], v)
		}
	}
	if _, err := ex.Exchange(context.Background(), "expired"); !errors.Is(err, mcpauth.ErrExchangeRefused) || !strings.Contains(err.Error(), "invalid_grant") || strings.Contains(err.Error(), "expired\"") {
		t.Fatalf("refused exchange: %v", err)
	}
	bad, _ := mcpauth.NewExchanger(mcpauth.ExchangeConfig{TokenURL: endpoint.URL, ClientID: "ploeg-mcp", ClientSecret: "wrong", Audience: "ploeg"})
	if _, err := bad.Exchange(context.Background(), "callers.jwt.token"); !errors.Is(err, mcpauth.ErrExchangeRefused) {
		t.Fatalf("wrong client secret: %v", err)
	}
	for _, cfg := range []mcpauth.ExchangeConfig{
		{TokenURL: "ftp://x", ClientID: "a", ClientSecret: "b", Audience: "ploeg"},
		{TokenURL: endpoint.URL, ClientSecret: "b", Audience: "ploeg"},
		{TokenURL: endpoint.URL, ClientID: "a", ClientSecret: "b"},
	} {
		if _, err := mcpauth.NewExchanger(cfg); err == nil {
			t.Errorf("%+v accepted", cfg)
		}
	}
}
