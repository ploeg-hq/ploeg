package connection

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/secretstore"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

type appSecrets struct {
	App *secretstore.Ref `json:"app"`
}

func (s *Service) completeManifest(ctx context.Context, caller Caller, flow store.ConnectionFlow, start StartRequest, code string) (Completed, *Error) {
	inst := s.Instances[start.Instance]
	conn := s.Connectors[start.Kind]
	app, err := conn.Manifest.Convert(ctx, inst, code)
	if err != nil {
		if errors.Is(err, provider.ErrVendorUnavailable) {
			return Completed{}, refuse(503, "vendor_unavailable", "The vendor could not be reached to convert the manifest code.")
		}
		return Completed{}, &Error{Status: 422, Code: "probe_failed", Message: "The vendor refused the manifest code.", Checks: []Check{{Name: "manifest_conversion", Result: "fail"}}}
	}
	if app.WebhookSecret == "" {
		app.WebhookSecret = newHex(32)
		if err := conn.Manifest.SetWebhookSecret(ctx, inst, app.AppID, app.PrivateKey, app.WebhookSecret); err != nil {
			return Completed{}, refuse(503, "vendor_unavailable", "The App was created but its webhook secret could not be set; start again.")
		}
	}
	id := strings.ToLower(app.Slug)
	if !namePattern.MatchString(id) {
		id = "app-" + app.AppID
	}
	values := map[string]string{"private_key": app.PrivateKey, "webhook_secret": app.WebhookSecret}
	if app.ClientSecret != "" {
		values["client_secret"] = app.ClientSecret
	}
	ref, err := s.Secrets.Put(ctx, "apps/"+id, values)
	if err != nil {
		return Completed{}, storeError(err)
	}
	row := store.ConnectionAppRow{ID: id, Tenant: s.tenant(), Port: start.Port, Kind: start.Kind, Instance: start.Instance, AppID: app.AppID,
		Slug: app.Slug, ClientID: app.ClientID, Owner: mustJSON(app.Owner), HTMLURL: app.HTMLURL, Secrets: mustJSON(appSecrets{App: &ref}), CreatedBy: caller.Person}
	state := random(32)
	expires := s.now().Add(FlowLifetime).UTC()
	nextStart := StartRequest{Port: start.Port, Kind: start.Kind, Instance: start.Instance, Method: MethodGitHubAppInstallation,
		RedirectURI: start.RedirectURI, App: id, ID: start.ID, Label: start.Label}
	nextFlow := store.ConnectionFlow{ID: "flow_" + random(16), StateHash: hashState(state), Tenant: s.tenant(), Person: caller.Person, Consumer: caller.Consumer,
		Method: MethodGitHubAppInstallation, Request: mustJSON(nextStart), ExpiresAt: expires}
	detail := map[string]any{"app": id, "appId": app.AppID, "slug": app.Slug, "kind": start.Kind, "instance": start.Instance, "owner": app.Owner,
		"secrets": appSecrets{App: &ref}, "flow": flow.ID}
	if err := s.Store.CreateConnectionApp(ctx, row, flow.ID, store.FlowOutcome{Kind: "app", ID: id, Status: 201}, &nextFlow,
		[]store.ConnectionAudit{s.audit(caller.Person, "connection_app.created", &caller, detail)}); err != nil {
		_ = s.Secrets.Delete(context.WithoutCancel(ctx), "apps/"+id)
		return Completed{}, refuse(503, "unavailable", "Ploeg could not record the App; its secrets were deleted from the store.")
	}
	s.forgetAppSecret(app.AppID)
	saved, err := s.Store.ConnectionApp(ctx, s.tenant(), id)
	if err != nil {
		return Completed{}, refuse(503, "unavailable", "Ploeg recorded the App but could not read it back.")
	}
	view := s.appView(ctx, saved)
	next := &Next{InstallURL: conn.Manifest.InstallURL(inst, app.Slug, state)}
	fv := &FlowView{ID: nextFlow.ID, Method: MethodGitHubAppInstallation, Port: start.Port, Kind: start.Kind, Instance: start.Instance, Status: "pending", ExpiresAt: expires, State: state}
	return Completed{ConnectionApp: &view, Flow: fv, Next: next, Status: 201}, nil
}

func (s *Service) completeInstallation(ctx context.Context, caller Caller, flow store.ConnectionFlow, start StartRequest, installationID, setupAction string) (Completed, *Error) {
	inst := s.Instances[start.Instance]
	conn := s.Connectors[start.Kind]
	app, err := s.Store.ConnectionApp(ctx, s.tenant(), start.App)
	if err != nil || app.RemovedAt != nil {
		return Completed{}, refuse(404, "unknown_app", "The connection app this flow installs was removed.")
	}
	if existing, err := s.Store.LiveConnectionsByInstallation(ctx, installationID); err == nil && len(existing) > 0 && existing[0].ID != start.Connection {
		return Completed{}, &Error{Status: 409, Code: "already_connected", Message: "That installation is already a live connection.", Connection: existing[0].ID}
	}
	var refs appSecrets
	_ = json.Unmarshal(app.Secrets, &refs)
	if refs.App == nil {
		return Completed{}, storeError(secretstore.ErrNotFound)
	}
	values, err := s.Secrets.Get(ctx, *refs.App)
	if err != nil {
		return Completed{}, storeError(err)
	}
	row := store.ConnectionRow{Tenant: s.tenant(), Port: start.Port, Kind: start.Kind, Instance: start.Instance, Method: MethodGitHubAppInstallation,
		App: app.ID, InstallationID: installationID, HandOverAgent: start.HandOverAgent, Label: start.Label, CreatedBy: caller.Person, UpdatedBy: caller.Person,
		Secrets: mustJSON(secretsField{App: refs.App})}
	installation, err := conn.Manifest.Installation(ctx, inst, app.AppID, values["private_key"], installationID)
	switch {
	case setupAction == "request" && err != nil && !errors.Is(err, provider.ErrVendorUnavailable) && !errors.Is(err, provider.ErrForeignInstallation):
		row.State, row.AccountID = StateAwaitingConsent, "installation:"+installationID
		row.Account = mustJSON(provider.ConnectionAccount{InstallationID: installationID})
		return s.writeConnection(ctx, caller, flow, start, row, nil, secretsField{}, func() {})
	case errors.Is(err, provider.ErrForeignInstallation), errors.Is(err, provider.ErrCredentialRefused):
		return Completed{}, &Error{Status: 422, Code: "probe_failed", Message: "That installation is not an installation of this App.", Checks: []Check{{Name: "installation_belongs_to_app", Result: "fail"}}}
	case err != nil:
		return Completed{}, refuse(503, "vendor_unavailable", "The vendor could not be reached to read the installation.")
	}
	if setupAction == "request" {
		row.State, row.AccountID = StateAwaitingConsent, installation.Account.ID
		row.Account = mustJSON(installation.Account)
		return s.writeConnection(ctx, caller, flow, start, row, nil, secretsField{}, func() {})
	}
	cred := provider.ConnectionCredential{Port: start.Port, AppID: app.AppID, PrivateKey: values["private_key"], InstallationID: installationID}
	report := s.probeCredential(ctx, start.Port, start.Kind, inst, cred, nil)
	if report.Result != "pass" {
		if report.Unknown {
			return Completed{}, &Error{Status: 503, Code: "vendor_unavailable", Message: "The vendor could not be reached to probe the installation.", Checks: report.Checks}
		}
		return Completed{}, &Error{Status: 422, Code: "probe_failed", Message: "The installation failed its probe; nothing was stored.", Checks: report.Checks}
	}
	row.State, row.AccountID = StateActive, report.Accounts.ID
	row.Account = mustJSON(report.Accounts)
	row.LastProbe = mustJSON(report)
	at := report.At
	row.LastProbeAt = &at
	return s.writeConnection(ctx, caller, flow, start, row, nil, secretsField{}, func() {})
}

func (s *Service) appView(ctx context.Context, app store.ConnectionAppRow) AppView {
	var owner provider.ConnectionAccount
	_ = json.Unmarshal(app.Owner, &owner)
	var refs appSecrets
	_ = json.Unmarshal(app.Secrets, &refs)
	secrets := map[string]secretstore.Ref{}
	if refs.App != nil {
		secrets["app"] = *refs.App
	}
	v := AppView{ID: app.ID, Port: app.Port, Kind: app.Kind, Instance: app.Instance, AppID: app.AppID, Slug: app.Slug, ClientID: app.ClientID,
		Owner: owner, Installations: []string{}, Secrets: secrets, CreatedBy: app.CreatedBy, CreatedAt: app.CreatedAt}
	if conn := s.Connectors[app.Kind]; conn.Manifest != nil {
		v.SettingsURL = conn.Manifest.SettingsURL(s.Instances[app.Instance], provider.ManifestApp{Slug: app.Slug, HTMLURL: app.HTMLURL})
	}
	if rows, err := s.Store.LiveConnections(ctx, s.tenant(), ""); err == nil {
		for _, r := range rows {
			if r.App == app.ID && r.InstallationID != "" {
				v.Installations = append(v.Installations, r.InstallationID)
			}
		}
	}
	return v
}
