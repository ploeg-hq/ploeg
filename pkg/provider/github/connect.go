package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"

	"github.com/ploeg-hq/ploeg/pkg/githubapi"
	"github.com/ploeg-hq/ploeg/pkg/githubapp"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/oauthconn"
)

// Connector is GitHub's runtime connection support (ADR-0195): an App
// created from a manifest, installed, probed through an installation token
// and uninstalled. Every manifest it builds has public false.
type Connector struct{}

// ForgeAppPermissions are what a forge App Ploeg creates asks for: what a
// writing Run pushes with and what ploegd reads and comments with.
func ForgeAppPermissions() githubapp.Permissions {
	perms := githubapp.WriterPermissions()
	for k, v := range githubapp.ForgePermissions() {
		if perms[k] != "write" {
			perms[k] = v
		}
	}
	return perms
}

// TrackerAppPermissions are what a tracker App Ploeg creates asks for.
func TrackerAppPermissions() githubapp.Permissions {
	return githubapp.Permissions{"issues": "write", "metadata": "read"}
}

var forgeEvents = []string{"pull_request", "pull_request_review", "check_run", "status"}
var trackerEvents = []string{"issues", "issue_comment"}

func permissionsFor(port string) githubapp.Permissions {
	if port == "tracker" {
		return TrackerAppPermissions()
	}
	return ForgeAppPermissions()
}

func webBase(inst provider.ConnectionInstance) string {
	if inst.URL != "" {
		return oauthconn.Trim(inst.URL)
	}
	return "https://github.com"
}

func apiBase(inst provider.ConnectionInstance) string {
	if inst.APIURL != "" {
		return oauthconn.Trim(inst.APIURL)
	}
	return githubapi.APIURL(webBase(inst))
}

func (Connector) Manifest(_ provider.ConnectionInstance, req provider.ManifestRequest) (string, error) {
	events := forgeEvents
	if req.Port == "tracker" {
		events = trackerEvents
	}
	manifest := map[string]any{
		"name":                req.Name,
		"url":                 req.HomeURL,
		"hook_attributes":     map[string]any{"url": req.HookURL, "active": true},
		"redirect_url":        req.RedirectURL,
		"setup_url":           req.SetupURL,
		"setup_on_update":     true,
		"public":              false,
		"default_events":      events,
		"default_permissions": permissionsFor(req.Port),
	}
	raw, err := json.Marshal(manifest)
	return string(raw), err
}

func (Connector) ManifestPostURL(inst provider.ConnectionInstance, owner, state string) string {
	path := "/settings/apps/new"
	if owner != "" {
		path = "/organizations/" + url.PathEscape(owner) + "/settings/apps/new"
	}
	return webBase(inst) + path + "?state=" + url.QueryEscape(state)
}

func (Connector) InstallURL(inst provider.ConnectionInstance, slug, state string) string {
	return webBase(inst) + "/apps/" + url.PathEscape(slug) + "/installations/new?state=" + url.QueryEscape(state)
}

func (Connector) SettingsURL(inst provider.ConnectionInstance, app provider.ManifestApp) string {
	if app.HTMLURL != "" {
		return app.HTMLURL
	}
	return webBase(inst) + "/settings/apps/" + url.PathEscape(app.Slug)
}

func (Connector) Convert(ctx context.Context, inst provider.ConnectionInstance, code string) (provider.ManifestApp, error) {
	var out struct {
		ID            int64  `json:"id"`
		Slug          string `json:"slug"`
		ClientID      string `json:"client_id"`
		ClientSecret  string `json:"client_secret"`
		WebhookSecret string `json:"webhook_secret"`
		PEM           string `json:"pem"`
		HTMLURL       string `json:"html_url"`
		Owner         struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
		} `json:"owner"`
	}
	if err := oauthconn.Do(ctx, http.MethodPost, apiBase(inst)+"/app-manifests/"+url.PathEscape(code)+"/conversions", "", nil, &out); err != nil {
		return provider.ManifestApp{}, err
	}
	if out.ID == 0 || out.PEM == "" || out.Slug == "" {
		return provider.ManifestApp{}, fmt.Errorf("github: manifest conversion returned no App: %w", provider.ErrCredentialRefused)
	}
	return provider.ManifestApp{AppID: strconv.FormatInt(out.ID, 10), Slug: out.Slug, ClientID: out.ClientID, ClientSecret: out.ClientSecret,
		WebhookSecret: out.WebhookSecret, PrivateKey: out.PEM, HTMLURL: out.HTMLURL,
		Owner: provider.ConnectionAccount{ID: strconv.FormatInt(out.Owner.ID, 10), Login: out.Owner.Login}}, nil
}

func appAuth(appID, privateKey string) (string, error) {
	key, err := githubapp.ParsePrivateKey([]byte(privateKey))
	if err != nil {
		return "", fmt.Errorf("github: the App's private key is unreadable: %w", provider.ErrCredentialRefused)
	}
	jwt, err := (&githubapp.Minter{AppID: appID, Key: key}).JWT()
	if err != nil {
		return "", err
	}
	return "Bearer " + jwt, nil
}

func (Connector) SetWebhookSecret(ctx context.Context, inst provider.ConnectionInstance, appID, privateKey, secret string) error {
	auth, err := appAuth(appID, privateKey)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"secret": secret})
	return oauthconn.Do(ctx, http.MethodPatch, apiBase(inst)+"/app/hook/config", auth, bytes.NewReader(body), nil)
}

func (Connector) Installation(ctx context.Context, inst provider.ConnectionInstance, appID, privateKey, installationID string) (provider.AppInstallation, error) {
	auth, err := appAuth(appID, privateKey)
	if err != nil {
		return provider.AppInstallation{}, err
	}
	var out struct {
		ID          int64   `json:"id"`
		AppID       int64   `json:"app_id"`
		SuspendedAt *string `json:"suspended_at"`
		Account     struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
		} `json:"account"`
	}
	if err := oauthconn.GetJSON(ctx, apiBase(inst)+"/app/installations/"+url.PathEscape(installationID), auth, &out); err != nil {
		return provider.AppInstallation{}, err
	}
	if strconv.FormatInt(out.AppID, 10) != appID {
		return provider.AppInstallation{}, provider.ErrForeignInstallation
	}
	id := strconv.FormatInt(out.ID, 10)
	return provider.AppInstallation{ID: id, AppID: appID, Suspended: out.SuspendedAt != nil,
		Account: provider.ConnectionAccount{ID: strconv.FormatInt(out.Account.ID, 10), Login: out.Account.Login, InstallationID: id}}, nil
}

func (Connector) Uninstall(ctx context.Context, inst provider.ConnectionInstance, appID, privateKey, installationID string) error {
	auth, err := appAuth(appID, privateKey)
	if err != nil {
		return err
	}
	return oauthconn.Do(ctx, http.MethodDelete, apiBase(inst)+"/app/installations/"+url.PathEscape(installationID), auth, nil, nil)
}

// ProbeConnection mints an installation token, compares its permissions
// with what a forge or tracker App needs, and revokes the token again.
func (c Connector) ProbeConnection(ctx context.Context, inst provider.ConnectionInstance, cred provider.ConnectionCredential) (provider.ConnectionProbe, error) {
	installation, err := c.Installation(ctx, inst, cred.AppID, cred.PrivateKey, cred.InstallationID)
	if err != nil {
		return provider.ConnectionProbe{}, err
	}
	if installation.Suspended {
		return provider.ConnectionProbe{Account: installation.Account}, provider.ErrInstallationSuspended
	}
	auth, err := appAuth(cred.AppID, cred.PrivateKey)
	if err != nil {
		return provider.ConnectionProbe{}, err
	}
	var token struct {
		Token       string            `json:"token"`
		Permissions map[string]string `json:"permissions"`
	}
	if err := oauthconn.Do(ctx, http.MethodPost, apiBase(inst)+"/app/installations/"+url.PathEscape(cred.InstallationID)+"/access_tokens", auth, nil, &token); err != nil {
		return provider.ConnectionProbe{}, err
	}
	if token.Token != "" {
		_ = oauthconn.Do(ctx, http.MethodDelete, apiBase(inst)+"/installation/token", "Bearer "+token.Token, nil, nil)
	}
	var held, missing []string
	for name, level := range token.Permissions {
		held = append(held, name+":"+level)
	}
	for name, level := range permissionsFor(cred.Port) {
		got := token.Permissions[name]
		if got == "" || (level == "write" && got != "write" && got != "admin") {
			missing = append(missing, name+":"+level)
		}
	}
	sort.Strings(held)
	sort.Strings(missing)
	canMint := len(missing) == 0
	return provider.ConnectionProbe{Account: installation.Account, Scopes: held, Missing: missing, CanMint: &canMint}, nil
}

// ListGrantedRepositories lists the repositories the installation was
// granted, through a short-lived installation token it revokes again.
func (c Connector) ListGrantedRepositories(ctx context.Context, inst provider.ConnectionInstance, cred provider.ConnectionCredential, page, perPage int) ([]provider.GrantedRepository, bool, error) {
	auth, err := appAuth(cred.AppID, cred.PrivateKey)
	if err != nil {
		return nil, false, err
	}
	var token struct {
		Token string `json:"token"`
	}
	if err := oauthconn.Do(ctx, http.MethodPost, apiBase(inst)+"/app/installations/"+url.PathEscape(cred.InstallationID)+"/access_tokens", auth, nil, &token); err != nil {
		return nil, false, err
	}
	defer func() {
		_ = oauthconn.Do(context.WithoutCancel(ctx), http.MethodDelete, apiBase(inst)+"/installation/token", "Bearer "+token.Token, nil, nil)
	}()
	var out struct {
		TotalCount   int `json:"total_count"`
		Repositories []struct {
			ID       int64  `json:"id"`
			FullName string `json:"full_name"`
			Private  bool   `json:"private"`
		} `json:"repositories"`
	}
	if err := oauthconn.GetJSON(ctx, fmt.Sprintf("%s/installation/repositories?per_page=%d&page=%d", apiBase(inst), perPage, page), "Bearer "+token.Token, &out); err != nil {
		return nil, false, err
	}
	repos := make([]provider.GrantedRepository, 0, len(out.Repositories))
	for _, r := range out.Repositories {
		repos = append(repos, provider.GrantedRepository{ID: strconv.FormatInt(r.ID, 10), FullName: r.FullName, Private: r.Private, CanWrite: true})
	}
	return repos, page*perPage < out.TotalCount, nil
}
