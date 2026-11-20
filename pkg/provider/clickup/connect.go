package clickup

import (
	"context"
	"net/url"
	"strconv"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/oauthconn"
)

// Connector is ClickUp's runtime connection support (ADR-0195): OAuth
// without PKCE or refresh, and a probe that reads the user and the Lists
// the token can see. ClickUp's access tokens do not expire and ClickUp
// offers no revocation endpoint.
type Connector struct {
	Now func() time.Time
}

func (Connector) PKCE() bool { return false }

func webURL(inst provider.ConnectionInstance) string {
	if inst.URL != "" {
		return oauthconn.Trim(inst.URL)
	}
	return "https://app.clickup.com"
}

func apiURL(inst provider.ConnectionInstance) string {
	if inst.APIURL != "" {
		return oauthconn.Trim(inst.APIURL)
	}
	return "https://api.clickup.com/api"
}

func (Connector) AuthorizeURL(inst provider.ConnectionInstance, redirectURI, state, _ string) (string, error) {
	base := webURL(inst) + "/api"
	if inst.OAuth.AuthorizeURL != "" {
		base = inst.OAuth.AuthorizeURL
	}
	return oauthconn.AuthorizeURL(base, map[string]string{"client_id": inst.OAuth.ClientID, "redirect_uri": redirectURI, "state": state})
}

func (c Connector) Exchange(ctx context.Context, inst provider.ConnectionInstance, _, code, _ string) (provider.OAuthCredential, error) {
	tokenURL := apiURL(inst) + "/v2/oauth/token"
	if inst.OAuth.TokenURL != "" {
		tokenURL = inst.OAuth.TokenURL
	}
	form := url.Values{"client_id": {inst.OAuth.ClientID}, "client_secret": {inst.OAuth.ClientSecret}, "code": {code}}
	return oauthconn.Token(ctx, tokenURL, form, c.now())
}

func (Connector) Refresh(context.Context, provider.ConnectionInstance, string, string) (provider.OAuthCredential, error) {
	return provider.OAuthCredential{}, provider.ErrNotSupported
}

func (Connector) Revoke(context.Context, provider.ConnectionInstance, string) error {
	return provider.ErrNotSupported
}

const maxVisibleWalk = 500

// ProbeConnection reads GET /v2/user and walks workspaces, Spaces and
// Folders for the Lists the token can see, stopping after 500.
func (Connector) ProbeConnection(ctx context.Context, inst provider.ConnectionInstance, cred provider.ConnectionCredential) (provider.ConnectionProbe, error) {
	auth := cred.Token
	base := apiURL(inst)
	var user struct {
		User struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
			Email    string `json:"email"`
		} `json:"user"`
	}
	if err := oauthconn.GetJSON(ctx, base+"/v2/user", auth, &user); err != nil {
		return provider.ConnectionProbe{}, err
	}
	login := user.User.Username
	if login == "" {
		login = user.User.Email
	}
	probe := provider.ConnectionProbe{Account: provider.ConnectionAccount{ID: strconv.FormatInt(user.User.ID, 10), Login: login}}
	visible := &provider.VisibleScopes{First: []provider.VisibleScope{}}
	add := func(id, name string) {
		visible.Count++
		if len(visible.First) < 50 {
			visible.First = append(visible.First, provider.VisibleScope{ID: id, Name: name})
		}
	}
	type list struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	var teams struct {
		Teams []struct {
			ID string `json:"id"`
		} `json:"teams"`
	}
	if err := oauthconn.GetJSON(ctx, base+"/v2/team", auth, &teams); err != nil {
		return provider.ConnectionProbe{}, err
	}
	for _, team := range teams.Teams {
		var spaces struct {
			Spaces []struct {
				ID string `json:"id"`
			} `json:"spaces"`
		}
		if err := oauthconn.GetJSON(ctx, base+"/v2/team/"+url.PathEscape(team.ID)+"/space", auth, &spaces); err != nil {
			return provider.ConnectionProbe{}, err
		}
		for _, space := range spaces.Spaces {
			if visible.Count >= maxVisibleWalk {
				break
			}
			var folders struct {
				Folders []struct {
					Lists []list `json:"lists"`
				} `json:"folders"`
			}
			if err := oauthconn.GetJSON(ctx, base+"/v2/space/"+url.PathEscape(space.ID)+"/folder", auth, &folders); err != nil {
				return provider.ConnectionProbe{}, err
			}
			for _, f := range folders.Folders {
				for _, l := range f.Lists {
					add(l.ID, l.Name)
				}
			}
			var lists struct {
				Lists []list `json:"lists"`
			}
			if err := oauthconn.GetJSON(ctx, base+"/v2/space/"+url.PathEscape(space.ID)+"/list", auth, &lists); err != nil {
				return provider.ConnectionProbe{}, err
			}
			for _, l := range lists.Lists {
				add(l.ID, l.Name)
			}
		}
	}
	probe.Visible = visible
	return probe, nil
}

func (c Connector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
