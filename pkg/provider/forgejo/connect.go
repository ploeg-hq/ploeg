package forgejo

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/oauthconn"
)

// Connector is Forgejo's runtime connection support (ADR-0195): OAuth with
// PKCE and expiring access tokens with refresh tokens, and a probe of
// GET /api/v1/user. Forgejo offers no endpoint to revoke an OAuth token.
type Connector struct {
	Now func() time.Time
}

func (Connector) PKCE() bool { return true }

func (Connector) AuthorizeURL(inst provider.ConnectionInstance, redirectURI, state, challenge string) (string, error) {
	base := oauthconn.Trim(inst.URL) + "/login/oauth/authorize"
	if inst.OAuth.AuthorizeURL != "" {
		base = inst.OAuth.AuthorizeURL
	}
	return oauthconn.AuthorizeURL(base, map[string]string{"client_id": inst.OAuth.ClientID, "redirect_uri": redirectURI, "response_type": "code",
		"state": state, "scope": strings.Join(inst.OAuth.Scopes, " "), "code_challenge": challenge, "code_challenge_method": "S256"})
}

func tokenURL(inst provider.ConnectionInstance) string {
	if inst.OAuth.TokenURL != "" {
		return inst.OAuth.TokenURL
	}
	return oauthconn.Trim(inst.URL) + "/login/oauth/access_token"
}

func (c Connector) Exchange(ctx context.Context, inst provider.ConnectionInstance, redirectURI, code, verifier string) (provider.OAuthCredential, error) {
	return oauthconn.Token(ctx, tokenURL(inst), url.Values{"client_id": {inst.OAuth.ClientID}, "client_secret": {inst.OAuth.ClientSecret}, "code": {code},
		"grant_type": {"authorization_code"}, "redirect_uri": {redirectURI}, "code_verifier": {verifier}}, c.now())
}

func (c Connector) Refresh(ctx context.Context, inst provider.ConnectionInstance, _, refreshToken string) (provider.OAuthCredential, error) {
	return oauthconn.Token(ctx, tokenURL(inst), url.Values{"client_id": {inst.OAuth.ClientID}, "client_secret": {inst.OAuth.ClientSecret},
		"refresh_token": {refreshToken}, "grant_type": {"refresh_token"}}, c.now())
}

func (Connector) Revoke(context.Context, provider.ConnectionInstance, string) error {
	return provider.ErrNotSupported
}

func (Connector) ProbeConnection(ctx context.Context, inst provider.ConnectionInstance, cred provider.ConnectionCredential) (provider.ConnectionProbe, error) {
	api := oauthconn.Trim(inst.APIURL)
	if api == "" {
		api = oauthconn.Trim(inst.URL) + "/api/v1"
	}
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if err := oauthconn.GetJSON(ctx, api+"/user", "Bearer "+cred.Token, &user); err != nil {
		return provider.ConnectionProbe{}, err
	}
	canMint := false
	return provider.ConnectionProbe{Account: provider.ConnectionAccount{ID: strconv.FormatInt(user.ID, 10), Login: user.Login}, CanMint: &canMint}, nil
}

func (c Connector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// ListGrantedRepositories lists the repositories the consenting account can
// reach, with whether it may push to each.
func (Connector) ListGrantedRepositories(ctx context.Context, inst provider.ConnectionInstance, cred provider.ConnectionCredential, page, perPage int) ([]provider.GrantedRepository, bool, error) {
	api := oauthconn.Trim(inst.APIURL)
	if api == "" {
		api = oauthconn.Trim(inst.URL) + "/api/v1"
	}
	var out []struct {
		ID          int64  `json:"id"`
		FullName    string `json:"full_name"`
		Private     bool   `json:"private"`
		Permissions struct {
			Push bool `json:"push"`
		} `json:"permissions"`
	}
	if err := oauthconn.GetJSON(ctx, api+"/user/repos?limit="+strconv.Itoa(perPage)+"&page="+strconv.Itoa(page), "Bearer "+cred.Token, &out); err != nil {
		return nil, false, err
	}
	repos := make([]provider.GrantedRepository, 0, len(out))
	for _, r := range out {
		repos = append(repos, provider.GrantedRepository{ID: strconv.FormatInt(r.ID, 10), FullName: r.FullName, Private: r.Private, CanWrite: r.Permissions.Push})
	}
	return repos, len(out) == perPage, nil
}
