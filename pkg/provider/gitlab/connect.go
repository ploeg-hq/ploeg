package gitlab

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/oauthconn"
)

// Connector is GitLab's runtime connection support (ADR-0195, ADR-0203):
// OAuth with PKCE, expiring access tokens with rotating refresh tokens,
// revocation, a group service account bootstrapped from an Owner's token,
// self-rotation, project webhooks, and a probe of GET /api/v4/user and the
// token's scopes. A personal access token with api scope and Maintainer reach
// can mint a project access token per Run; an OAuth token acts as a person
// and mints none.
type Connector struct {
	Now func() time.Time
}

// NeededScopes are the OAuth scopes a GitLab forge connection must hold.
var NeededScopes = []string{"api"}

func (Connector) PKCE() bool { return true }

func (Connector) AuthorizeURL(inst provider.ConnectionInstance, redirectURI, state, challenge string) (string, error) {
	base := oauthconn.Trim(inst.URL) + "/oauth/authorize"
	if inst.OAuth.AuthorizeURL != "" {
		base = inst.OAuth.AuthorizeURL
	}
	scopes := inst.OAuth.Scopes
	if len(scopes) == 0 {
		scopes = NeededScopes
	}
	return oauthconn.AuthorizeURL(base, map[string]string{"client_id": inst.OAuth.ClientID, "redirect_uri": redirectURI, "response_type": "code",
		"state": state, "scope": strings.Join(scopes, " "), "code_challenge": challenge, "code_challenge_method": "S256"})
}

func tokenURL(inst provider.ConnectionInstance) string {
	if inst.OAuth.TokenURL != "" {
		return inst.OAuth.TokenURL
	}
	return oauthconn.Trim(inst.URL) + "/oauth/token"
}

func (c Connector) Exchange(ctx context.Context, inst provider.ConnectionInstance, redirectURI, code, verifier string) (provider.OAuthCredential, error) {
	return oauthconn.Token(ctx, tokenURL(inst), url.Values{"client_id": {inst.OAuth.ClientID}, "client_secret": {inst.OAuth.ClientSecret}, "code": {code},
		"grant_type": {"authorization_code"}, "redirect_uri": {redirectURI}, "code_verifier": {verifier}}, c.now())
}

func (c Connector) Refresh(ctx context.Context, inst provider.ConnectionInstance, redirectURI, refreshToken string) (provider.OAuthCredential, error) {
	return oauthconn.Token(ctx, tokenURL(inst), url.Values{"client_id": {inst.OAuth.ClientID}, "client_secret": {inst.OAuth.ClientSecret},
		"refresh_token": {refreshToken}, "grant_type": {"refresh_token"}, "redirect_uri": {redirectURI}}, c.now())
}

func (Connector) Revoke(ctx context.Context, inst provider.ConnectionInstance, token string) error {
	form := url.Values{"client_id": {inst.OAuth.ClientID}, "client_secret": {inst.OAuth.ClientSecret}, "token": {token}}
	return oauthconn.PostForm(ctx, oauthconn.Trim(inst.URL)+"/oauth/revoke", form)
}

func (Connector) ProbeConnection(ctx context.Context, inst provider.ConnectionInstance, cred provider.ConnectionCredential) (provider.ConnectionProbe, error) {
	var user struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	if err := oauthconn.GetJSON(ctx, apiRoot(inst)+"/user", "Bearer "+cred.Token, &user); err != nil {
		return provider.ConnectionProbe{}, err
	}
	scopes, err := tokenScopes(ctx, inst, cred)
	if err != nil {
		return provider.ConnectionProbe{}, err
	}
	canMint := false
	if personalToken(cred) && slices.Contains(scopes, "api") {
		if canMint, err = maintainerReach(ctx, inst, cred.Token); err != nil {
			return provider.ConnectionProbe{}, err
		}
	}
	return provider.ConnectionProbe{Account: provider.ConnectionAccount{ID: strconv.FormatInt(user.ID, 10), Login: user.Username},
		Scopes: scopes, Missing: oauthconn.Missing(NeededScopes, scopes), CanMint: &canMint}, nil
}

// oauthMethod is the connection method whose token is an OAuth access
// token. An empty method is one too, as before methods were told apart;
// every other GitLab method holds a personal access token.
const oauthMethod = "oauth"

func personalToken(cred provider.ConnectionCredential) bool {
	return cred.Method != "" && cred.Method != oauthMethod
}

// tokenScopes reads the scopes of an OAuth token from /oauth/token/info and
// of a personal access token from /personal_access_tokens/self, which
// answers only for a personal access token.
func tokenScopes(ctx context.Context, inst provider.ConnectionInstance, cred provider.ConnectionCredential) ([]string, error) {
	if !personalToken(cred) {
		var info struct {
			Scope []string `json:"scope"`
		}
		if err := oauthconn.GetJSON(ctx, oauthconn.Trim(inst.URL)+"/oauth/token/info", "Bearer "+cred.Token, &info); err != nil {
			return nil, err
		}
		return info.Scope, nil
	}
	tok, err := tokenSelf(ctx, inst, cred.Token)
	if err != nil {
		return nil, err
	}
	if tok.Revoked || (!tok.Active && tok.ID != 0) {
		return nil, fmt.Errorf("gitlab: the token is revoked or expired: %w", provider.ErrCredentialRefused)
	}
	return tok.Scopes, nil
}

func (c Connector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// ListGrantedRepositories lists the projects the consenting account is a
// member of, with whether it may push (Developer or above).
func (Connector) ListGrantedRepositories(ctx context.Context, inst provider.ConnectionInstance, cred provider.ConnectionCredential, page, perPage int) ([]provider.GrantedRepository, bool, error) {
	api := oauthconn.Trim(inst.APIURL)
	if api == "" {
		api = oauthconn.Trim(inst.URL) + "/api/v4"
	}
	var out []struct {
		ID                int64  `json:"id"`
		PathWithNamespace string `json:"path_with_namespace"`
		Visibility        string `json:"visibility"`
		Permissions       struct {
			Project *struct {
				AccessLevel int `json:"access_level"`
			} `json:"project_access"`
			Group *struct {
				AccessLevel int `json:"access_level"`
			} `json:"group_access"`
		} `json:"permissions"`
	}
	if err := oauthconn.GetJSON(ctx, api+"/projects?membership=true&simple=false&per_page="+strconv.Itoa(perPage)+"&page="+strconv.Itoa(page), "Bearer "+cred.Token, &out); err != nil {
		return nil, false, err
	}
	repos := make([]provider.GrantedRepository, 0, len(out))
	for _, r := range out {
		level := 0
		if r.Permissions.Project != nil {
			level = r.Permissions.Project.AccessLevel
		}
		if r.Permissions.Group != nil && r.Permissions.Group.AccessLevel > level {
			level = r.Permissions.Group.AccessLevel
		}
		repos = append(repos, provider.GrantedRepository{ID: strconv.FormatInt(r.ID, 10), FullName: r.PathWithNamespace, Private: r.Visibility != "public", CanWrite: level >= 30})
	}
	return repos, len(out) == perPage, nil
}

// ConnectionAPIURL is the REST root of a GitLab connection instance.
func ConnectionAPIURL(inst provider.ConnectionInstance) string { return apiRoot(inst) }
