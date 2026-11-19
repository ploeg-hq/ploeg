package connection

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/secretstore"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *Service) usedBy(ctx context.Context, id string) ([]Use, error) {
	if s.Users == nil {
		return []Use{}, nil
	}
	uses, err := s.Users(ctx, id)
	if uses == nil {
		uses = []Use{}
	}
	return uses, err
}

func (s *Service) view(ctx context.Context, row store.ConnectionRow) View {
	var account provider.ConnectionAccount
	_ = json.Unmarshal(row.Account, &account)
	method := row.Method
	v := View{ID: row.ID, Port: row.Port, Kind: row.Kind, Instance: row.Instance, Method: &method, Account: &account, Source: "runtime",
		State: row.State, InvalidReason: strPtr(row.InvalidReason), Label: row.Label, Revision: row.Revision, App: strPtr(row.App),
		ExpiresAt: row.ExpiresAt, CreatedBy: &row.CreatedBy, CreatedAt: &row.CreatedAt, UpdatedBy: &row.UpdatedBy, UpdatedAt: &row.UpdatedAt,
		ReconnectedBy: strPtr(row.ReconnectedBy), ReconnectedAt: row.ReconnectedAt}
	var field secretsField
	if json.Unmarshal(row.Secrets, &field) == nil {
		v.Secrets = map[string]secretstore.Ref{}
		if field.Credential != nil {
			v.Secrets["credential"] = *field.Credential
		}
		if field.App != nil {
			v.Secrets["app"] = *field.App
		}
	}
	if len(row.LastProbe) > 0 {
		var report ProbeReport
		if json.Unmarshal(row.LastProbe, &report) == nil {
			v.LastProbe = &report
			if row.Port == PortForge {
				v.CanMint = report.CanMint
			}
		}
	}
	if row.Port == PortTracker {
		hand := row.HandOverAgent
		v.HandOverAgent = &hand
	} else if v.CanMint == nil {
		f := false
		v.CanMint = &f
	}
	v.UsedBy, _ = s.usedBy(ctx, row.ID)
	return v
}

func staticView(st Static) View {
	v := View{ID: st.ID, Port: st.Port, Kind: st.Kind, Instance: st.Instance, Source: "static", State: StateActive, UsedBy: []Use{}}
	if st.Port == PortTracker {
		f := false
		v.HandOverAgent = &f
	}
	return v
}

// List answers the live connections of port, static first by id, paged
// after an id.
func (s *Service) List(ctx context.Context, port, after string, limit int) ([]View, *string, *Error) {
	var out []View
	for _, st := range s.Static {
		if st.Port == port {
			out = append(out, staticView(st))
		}
	}
	rows, err := s.Store.LiveConnections(ctx, s.tenant(), port)
	if err != nil {
		return nil, nil, refuse(503, "unavailable", "Ploeg could not read its connections.")
	}
	for _, r := range rows {
		out = append(out, s.view(ctx, r))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	start := 0
	if after != "" {
		start = sort.Search(len(out), func(i int) bool { return out[i].ID > after })
	}
	out = out[start:]
	var next *string
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1].ID
		next = &last
	}
	if out == nil {
		out = []View{}
	}
	return out, next, nil
}

func (s *Service) live(ctx context.Context, port, id string) (store.ConnectionRow, *Error) {
	row, err := s.Store.Connection(ctx, s.tenant(), id)
	if errors.Is(err, store.ErrConnectionNotFound) || (err == nil && (row.Port != port || row.RemovedAt != nil)) {
		return row, refuse(404, "unknown_connection", "No live "+port+" connection has that id.")
	}
	if err != nil {
		return row, refuse(503, "unavailable", "Ploeg could not read the connection.")
	}
	return row, nil
}

// Get answers one connection of port, static or runtime.
func (s *Service) Get(ctx context.Context, port, id string) (View, *Error) {
	for _, st := range s.Static {
		if st.ID == id && st.Port == port {
			return staticView(st), nil
		}
	}
	row, e := s.live(ctx, port, id)
	if e != nil {
		return View{}, e
	}
	return s.view(ctx, row), nil
}

func (s *Service) refuseStatic(port, id string) *Error {
	for _, st := range s.Static {
		if st.ID == id && st.Port == port {
			return refuse(409, "static_connection", "A static connection is configured through the environment or Helm and cannot be changed through the API.")
		}
	}
	return nil
}

// Probe runs every check now and records the state (ADR-0195 section 8).
func (s *Service) Probe(ctx context.Context, caller Caller, port, id string) (View, ProbeReport, *Error) {
	if e := s.Accepting(); e != nil {
		return View{}, ProbeReport{}, e
	}
	if e := s.refuseStatic(port, id); e != nil {
		return View{}, ProbeReport{}, refuse(422, "capability_missing", "A static connection is not probed through this route.")
	}
	row, e := s.live(ctx, port, id)
	if e != nil {
		return View{}, ProbeReport{}, e
	}
	ok, err := s.Store.ClaimProbe(ctx, s.tenant(), id, ProbeInterval)
	if err != nil {
		return View{}, ProbeReport{}, refuse(503, "unavailable", "Ploeg could not record the probe.")
	}
	if !ok {
		return View{}, ProbeReport{}, refuse(429, "probe_rate_limited", "At most one probe per connection every 30 seconds.")
	}
	report := s.probeRow(ctx, row)
	if err := s.applyProbe(ctx, row, report, "connection:"+row.ID, &caller); err != nil {
		return View{}, ProbeReport{}, refuse(503, "unavailable", "Ploeg could not record the probe.")
	}
	fresh, _ := s.Store.Connection(ctx, s.tenant(), id)
	return s.view(ctx, fresh), report, nil
}

// Patch changes label and handOverAgent at revision.
func (s *Service) Patch(ctx context.Context, caller Caller, port, id string, revision int64, req PatchRequest) (View, *Error) {
	if e := s.Accepting(); e != nil {
		return View{}, e
	}
	if e := s.refuseStatic(port, id); e != nil {
		return View{}, e
	}
	if req.Label != nil && len([]rune(*req.Label)) > MaxLabel {
		return View{}, refuse(400, "invalid_request", "label is at most 200 characters.")
	}
	if req.HandOverAgent != nil && port != PortTracker {
		return View{}, refuse(400, "invalid_request", "handOverAgent is for tracker connections only.")
	}
	if req.HandOverAgent != nil && *req.HandOverAgent {
		return View{}, refuse(422, "capability_missing", "No adapter recognises an assignment to a per-connection agent yet, so handOverAgent cannot be true.")
	}
	row, e := s.live(ctx, port, id)
	if e != nil {
		return View{}, e
	}
	detail := map[string]any{"connection": id, "kind": row.Kind, "instance": row.Instance}
	if req.Label != nil {
		detail["label"] = *req.Label
	}
	if req.HandOverAgent != nil {
		detail["handOverAgent"] = *req.HandOverAgent
	}
	saved, err := s.Store.PatchConnection(ctx, s.tenant(), id, revision, req.Label, req.HandOverAgent, caller.Person,
		[]store.ConnectionAudit{s.audit(caller.Person, "connection.updated", &caller, detail)})
	if errors.Is(err, store.ErrRevisionStale) {
		return View{}, refuse(412, "revision_stale", "The connection changed since that revision; read it again.")
	}
	if err != nil {
		return View{}, refuse(503, "unavailable", "Ploeg could not record the change.")
	}
	return s.view(ctx, saved), nil
}

type removal struct {
	Revoked        bool   `json:"revoked"`
	SecretsDeleted bool   `json:"secretsDeleted"`
	LeftAtVendor   []Left `json:"leftAtVendor"`
}

// Remove disconnects a connection nothing uses (ADR-0195 section 8). It is
// idempotent and continues a partial removal. The answer is the things
// left at the vendor; removed reports an id that was already removed.
func (s *Service) Remove(ctx context.Context, caller Caller, port, id string) ([]Left, *Error) {
	if e := s.Accepting(); e != nil {
		return nil, e
	}
	defer s.changed(id)
	if e := s.refuseStatic(port, id); e != nil {
		return nil, e
	}
	row, err := s.Store.Connection(ctx, s.tenant(), id)
	if errors.Is(err, store.ErrConnectionNotFound) || (err == nil && row.Port != port) {
		return nil, refuse(404, "unknown_connection", "No "+port+" connection has that id.")
	}
	if err != nil {
		return nil, refuse(503, "unavailable", "Ploeg could not read the connection.")
	}
	if row.RemovedAt != nil {
		return nil, nil
	}
	uses, err := s.usedBy(ctx, id)
	if err != nil {
		return nil, refuse(503, "unavailable", "Ploeg could not read what uses the connection.")
	}
	if len(uses) > 0 {
		return nil, &Error{Status: 409, Code: "connection_in_use", Message: "Bindings or runtime targets name this connection; remove them first.", UsedBy: uses}
	}
	var done removal
	_ = json.Unmarshal(row.Removal, &done)
	if done.LeftAtVendor == nil {
		done.LeftAtVendor = []Left{}
	}
	record := func() { _ = s.Store.RecordRemoval(ctx, s.tenant(), id, mustJSON(done)) }
	if !done.Revoked {
		left, err := s.revokeGrant(ctx, row)
		if err != nil {
			record()
			return nil, refuse(503, "vendor_unavailable", "The vendor could not be reached to revoke the grant; retry to continue.")
		}
		done.LeftAtVendor = append(done.LeftAtVendor, left...)
		done.Revoked = true
		record()
	}
	if !done.SecretsDeleted {
		if row.Method != MethodGitHubAppInstallation {
			if err := s.Secrets.Delete(ctx, "connections/"+id); err != nil {
				record()
				return nil, storeError(err)
			}
		}
		done.SecretsDeleted = true
		record()
	}
	var field secretsField
	_ = json.Unmarshal(row.Secrets, &field)
	detail := map[string]any{"connection": id, "kind": row.Kind, "instance": row.Instance, "method": row.Method,
		"account": json.RawMessage(row.Account), "secrets": field, "leftAtVendor": done.LeftAtVendor}
	if err := s.Store.MarkConnectionRemoved(ctx, s.tenant(), id, caller.Person, mustJSON(done),
		[]store.ConnectionAudit{s.audit(caller.Person, "connection.removed", &caller, detail)}); err != nil {
		return nil, refuse(503, "unavailable", "Ploeg could not record the removal; retry to continue.")
	}
	return done.LeftAtVendor, nil
}

func (s *Service) revokeGrant(ctx context.Context, row store.ConnectionRow) ([]Left, error) {
	inst := s.Instances[row.Instance]
	conn := s.Connectors[row.Kind]
	cred, values, err := s.storedCredential(ctx, row)
	if errors.Is(err, secretstore.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	switch row.Method {
	case MethodGitHubAppInstallation:
		if conn.Manifest == nil {
			return []Left{{Kind: "installation", ID: row.InstallationID}}, nil
		}
		err := conn.Manifest.Uninstall(ctx, inst, cred.AppID, cred.PrivateKey, row.InstallationID)
		switch {
		case errors.Is(err, provider.ErrVendorUnavailable):
			return nil, err
		case err != nil:
			return []Left{{Kind: "installation", ID: row.InstallationID}}, nil
		}
	case MethodGitLabServiceAccount:
		left := []Left{{Kind: "service_account", ID: accountLogin(row)}}
		if conn.ServiceAccounts == nil {
			return append(left, Left{Kind: "token", ID: values["token_id"]}), nil
		}
		err := conn.ServiceAccounts.RevokeOwnToken(ctx, inst, values["access_token"])
		switch {
		case errors.Is(err, provider.ErrVendorUnavailable):
			return nil, err
		case err != nil:
			return append(left, Left{Kind: "token", ID: values["token_id"]}), nil
		}
		return left, nil
	case MethodOAuth:
		if conn.Authorizer == nil || inst.OAuth == nil {
			return nil, nil
		}
		for _, token := range []string{values["refresh_token"], values["access_token"]} {
			if token == "" {
				continue
			}
			err := conn.Authorizer.Revoke(ctx, inst, token)
			switch {
			case errors.Is(err, provider.ErrNotSupported):
				return nil, nil
			case errors.Is(err, provider.ErrVendorUnavailable):
				return nil, err
			case err != nil:
				return []Left{{Kind: "grant", ID: row.ID}}, nil
			}
		}
	}
	return nil, nil
}

func accountLogin(row store.ConnectionRow) string {
	var account provider.ConnectionAccount
	_ = json.Unmarshal(row.Account, &account)
	return firstNonEmpty(account.Login, row.AccountID)
}

// Apps lists the connection apps.
func (s *Service) Apps(ctx context.Context) ([]AppView, *Error) {
	rows, err := s.Store.LiveConnectionApps(ctx, s.tenant())
	if err != nil {
		return nil, refuse(503, "unavailable", "Ploeg could not read its connection apps.")
	}
	out := []AppView{}
	for _, r := range rows {
		out = append(out, s.appView(ctx, r))
	}
	return out, nil
}

// App answers one connection app.
func (s *Service) App(ctx context.Context, id string) (AppView, *Error) {
	app, err := s.Store.ConnectionApp(ctx, s.tenant(), id)
	if err != nil || app.RemovedAt != nil {
		return AppView{}, refuse(404, "unknown_app", "No connection app has that id.")
	}
	return s.appView(ctx, app), nil
}

// RemoveApp deletes a connection app's secrets. GitHub offers no API to
// delete an App, so the answer names it as left at the vendor.
func (s *Service) RemoveApp(ctx context.Context, caller Caller, id string) ([]Left, *Error) {
	if e := s.Accepting(); e != nil {
		return nil, e
	}
	app, err := s.Store.ConnectionApp(ctx, s.tenant(), id)
	if err != nil {
		return nil, refuse(404, "unknown_app", "No connection app has that id.")
	}
	view := s.appView(ctx, app)
	left := []Left{{Kind: "app", ID: app.Slug, URL: view.SettingsURL}}
	if app.RemovedAt != nil {
		return left, nil
	}
	if len(view.Installations) > 0 {
		return nil, &Error{Status: 409, Code: "app_in_use", Message: "A live connection uses an installation of this App; remove it first."}
	}
	if err := s.Secrets.Delete(ctx, "apps/"+id); err != nil {
		return nil, storeError(err)
	}
	s.forgetAppSecret(app.AppID)
	detail := map[string]any{"app": id, "appId": app.AppID, "slug": app.Slug, "kind": app.Kind, "instance": app.Instance, "secrets": json.RawMessage(app.Secrets), "leftAtVendor": left}
	if err := s.Store.RemoveConnectionApp(ctx, s.tenant(), id, []store.ConnectionAudit{s.audit(caller.Person, "connection_app.removed", &caller, detail)}); err != nil {
		return nil, refuse(503, "unavailable", "Ploeg could not record the removal.")
	}
	return left, nil
}

// FlowStatus answers a flow to the consumer and person that started it.
func (s *Service) FlowStatus(ctx context.Context, caller Caller, flowID string) (FlowStatus, *Error) {
	flow, err := s.Store.Flow(ctx, flowID)
	if err != nil || flow.Person != caller.Person || flow.Consumer != caller.Consumer {
		return FlowStatus{}, unknownFlow()
	}
	var start StartRequest
	_ = json.Unmarshal(flow.Request, &start)
	status := flow.Status
	if status == "pending" && !s.now().Before(flow.ExpiresAt) {
		status = "expired"
	}
	out := FlowStatus{Flow: FlowView{ID: flow.ID, Method: flow.Method, Port: start.Port, Kind: start.Kind, Instance: start.Instance, Status: status, ExpiresAt: flow.ExpiresAt.UTC()}}
	if flow.Status == "completed" && flow.Outcome != nil {
		done, e := s.replay(ctx, *flow.Outcome)
		if e == nil {
			out.Connection, out.ConnectionApp = done.Connection, done.ConnectionApp
		}
	}
	return out, nil
}

// Repositories lists what a forge connection's grant reaches, one page at
// a time, with each repository's registered key (ADR-0193 section 3).
func (s *Service) Repositories(ctx context.Context, id string, page, limit int, registered func(forge, repo string) (string, bool)) ([]Repository, *string, *Error) {
	row, e := s.live(ctx, PortForge, id)
	if e != nil {
		if s.refuseStatic(PortForge, id) != nil {
			return nil, nil, refuse(422, "capability_missing", "A static forge connection's grant is not listed through this route.")
		}
		return nil, nil, e
	}
	conn := s.Connectors[row.Kind]
	if conn.Repos == nil {
		return nil, nil, refuse(422, "capability_missing", "The adapter of this kind cannot list its grant.")
	}
	cred, _, err := s.credentialFor(ctx, row)
	if err != nil {
		if errors.Is(err, secretstore.ErrNotFound) {
			return nil, nil, refuse(503, "secret_store_unavailable", "The connection's credential is missing from the store.")
		}
		return nil, nil, refuse(503, "secret_store_unavailable", "The secret store could not be read.")
	}
	repos, more, err := conn.Repos.ListGrantedRepositories(ctx, s.Instances[row.Instance], cred, page, limit)
	if errors.Is(err, provider.ErrCredentialRefused) {
		_ = s.CredentialRefused(ctx, id)
		return nil, nil, refuse(422, "probe_failed", "The vendor refused the connection's credential.")
	}
	if err != nil {
		return nil, nil, refuse(503, "vendor_unavailable", "The vendor could not be reached.")
	}
	out := make([]Repository, 0, len(repos))
	for _, r := range repos {
		repo := Repository{GrantedRepository: r}
		if registered != nil {
			if key, ok := registered(row.Instance, r.FullName); ok {
				repo.Registered, repo.Key = true, &key
			}
		}
		out = append(out, repo)
	}
	var next *string
	if more {
		n := strconv.Itoa(page + 1)
		next = &n
	}
	return out, next, nil
}

// Repository is one repository of a forge connection's grant.
type Repository struct {
	provider.GrantedRepository
	Registered bool    `json:"registered"`
	Key        *string `json:"key"`
}
