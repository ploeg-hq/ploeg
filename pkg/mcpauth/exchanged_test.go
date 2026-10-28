package mcpauth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/ploeg-hq/ploeg/pkg/mcpauth"
)

func exchangedVerifier(t *testing.T, is *issuer) *mcpauth.Verifier {
	t.Helper()
	v, err := mcpauth.NewVerifier(mcpauth.Config{Issuer: is.url, Audiences: []string{"ploeg"}, AuthorizedParties: []string{"acting-app"}, ScopeOptional: true,
		MaxLifetime: 5 * time.Minute, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func exchangedClaims(is *issuer) jwt.MapClaims {
	return jwt.MapClaims{"iss": is.url, "sub": "person", "aud": "ploeg", "azp": "acting-app", "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix()}
}

func TestATokenExchangedForAnAudienceNamesItsClientAndActor(t *testing.T) {
	is := newIssuer(t)
	v := exchangedVerifier(t, is)
	c := exchangedClaims(is)
	c["act"] = map[string]any{"sub": "team-bronze"}
	info, err := v.Verify(context.Background(), is.sign("rsa-1", c), nil)
	if err != nil {
		t.Fatal(err)
	}
	if info.UserID != "person" || info.Extra["client"] != "acting-app" || info.Extra["act"] != "team-bronze" {
		t.Fatalf("token info %+v", info)
	}
	c = exchangedClaims(is)
	delete(c, "azp")
	c["client_id"] = "acting-app"
	if info, err := v.Verify(context.Background(), is.sign("rsa-1", c), nil); err != nil || info.Extra["client"] != "acting-app" || info.Extra["act"] != "" {
		t.Fatalf("client_id without azp: %+v %v", info, err)
	}
}

func TestAVerifiedTokenCarriesEveryPayloadClaim(t *testing.T) {
	is := newIssuer(t)
	v := exchangedVerifier(t, is)
	c := exchangedClaims(is)
	c["groups"] = []string{"ploeg-operators"}
	c["department"] = "platform"
	info, err := v.Verify(context.Background(), is.sign("rsa-1", c), nil)
	if err != nil {
		t.Fatal(err)
	}
	claims, _ := info.Extra["claims"].(map[string]any)
	groups, _ := claims["groups"].([]any)
	if claims["department"] != "platform" || len(groups) != 1 || groups[0] != "ploeg-operators" || claims["sub"] != "person" {
		t.Fatalf("claims = %#v", info.Extra["claims"])
	}
}

func TestATokenExchangedForAnotherAudienceOrClientIsRefused(t *testing.T) {
	is := newIssuer(t)
	v := exchangedVerifier(t, is)
	for name, edit := range map[string]func(jwt.MapClaims){
		"the audience's own ID token": func(c jwt.MapClaims) { c["azp"] = "ploeg" },
		"another client":              func(c jwt.MapClaims) { c["azp"] = "stranger" },
		"another audience":            func(c jwt.MapClaims) { c["aud"] = "acting-app" },
		"an act without sub":          func(c jwt.MapClaims) { c["act"] = map[string]any{} },
		"an act that is a string":     func(c jwt.MapClaims) { c["act"] = "team" },
		"a lifetime over five":        func(c jwt.MapClaims) { c["exp"] = now.Add(6 * time.Minute).Unix() },
	} {
		c := exchangedClaims(is)
		edit(c)
		if _, err := v.Verify(context.Background(), is.sign("rsa-1", c), nil); !errors.Is(err, auth.ErrInvalidToken) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAnOptionalScopeNeedsPartiesApartFromTheAudience(t *testing.T) {
	for _, cfg := range []mcpauth.Config{
		{Issuer: "https://id.example/", Audiences: []string{"ploeg"}, ScopeOptional: true},
		{Issuer: "https://id.example/", Audiences: []string{"ploeg"}, AuthorizedParties: []string{"ploeg"}, ScopeOptional: true},
		{Issuer: "https://id.example/", Audiences: []string{"ploeg"}, AuthorizedParties: []string{""}},
	} {
		if _, err := mcpauth.NewVerifier(cfg); err == nil {
			t.Errorf("%+v accepted", cfg)
		}
	}
}

func TestATokenIssuedToItsOwnAudienceNeedsAScopeClaim(t *testing.T) {
	is := newIssuer(t)
	v, err := mcpauth.NewVerifier(mcpauth.Config{Issuer: is.url, Audiences: []string{"ploeg", "ploeg-console"}, AuthorizedParties: []string{"acting-app"},
		SelfIssuedAudiences: []string{"ploeg-console"}, ScopeOptional: true, MaxLifetime: 5 * time.Minute, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	c := exchangedClaims(is)
	c["aud"], c["azp"], c["scope"] = "ploeg-console", "ploeg-console", ""
	info, err := v.Verify(context.Background(), is.sign("rsa-1", c), nil)
	if err != nil {
		t.Fatal(err)
	}
	if aud, _ := info.Extra["audiences"].([]string); info.Extra["client"] != "ploeg-console" || len(aud) != 1 || aud[0] != "ploeg-console" {
		t.Fatalf("token info %+v", info)
	}
	for name, edit := range map[string]func(jwt.MapClaims){
		"no scope claim, the shape of an ID token": func(c jwt.MapClaims) { delete(c, "scope") },
		"no azp":                                 func(c jwt.MapClaims) { delete(c, "azp") },
		"Ploeg's shared audience as its own azp": func(c jwt.MapClaims) { c["aud"], c["azp"] = "ploeg", "ploeg" },
		"a self-issued audience for another aud": func(c jwt.MapClaims) { c["aud"] = "ploeg" },
	} {
		c := exchangedClaims(is)
		c["aud"], c["azp"], c["scope"] = "ploeg-console", "ploeg-console", ""
		edit(c)
		if _, err := v.Verify(context.Background(), is.sign("rsa-1", c), nil); !errors.Is(err, auth.ErrInvalidToken) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := mcpauth.NewVerifier(mcpauth.Config{Issuer: is.url, Audiences: []string{"ploeg"}, SelfIssuedAudiences: []string{"ploeg-console"}, ScopeOptional: true}); err == nil {
		t.Fatal("a self-issued audience that is not an audience was accepted")
	}
}
