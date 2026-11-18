package connection

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/secretstore"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

var digits = regexp.MustCompile(`^[0-9]{1,20}$`)

func validComplete(req CompleteRequest) *Error {
	if req.State == "" || len(req.State) > 256 {
		return refuse(400, "invalid_request", "state is required.")
	}
	given := 0
	for _, v := range []string{req.Code, req.InstallationID, req.Token} {
		if v != "" {
			given++
		}
	}
	if given != 1 {
		return refuse(400, "invalid_request", "Send exactly one of code, installationId or token.")
	}
	if len(req.Code) > 2048 || len(req.Token) > MaxToken {
		return refuse(400, "invalid_request", "code is at most 2048 and token at most 4096 characters.")
	}
	if req.InstallationID != "" {
		if !digits.MatchString(req.InstallationID) {
			return refuse(400, "invalid_request", "installationId is GitHub's numeric installation id.")
		}
		switch req.SetupAction {
		case "install", "update", "request":
		default:
			return refuse(400, "invalid_request", "setupAction is install, update or request.")
		}
	} else if req.SetupAction != "" {
		return refuse(400, "invalid_request", "setupAction comes with installationId.")
	}
	return nil
}

func unknownFlow() *Error {
	return refuse(404, "unknown_flow", "No flow of this consumer and person has that state.")
}

// Complete finishes a flow from what the consumer relays (ADR-0195
// section 3). A code is exchanged at most once.
func (s *Service) Complete(ctx context.Context, caller Caller, req CompleteRequest) (Completed, *Error) {
	if e := validComplete(req); e != nil {
		return Completed{}, e
	}
	flow, err := s.Store.FlowByStateHash(ctx, hashState(req.State))
	if errors.Is(err, store.ErrFlowNotFound) || (err == nil && (flow.Person != caller.Person || flow.Consumer != caller.Consumer || flow.Tenant != s.tenant())) {
		return Completed{}, unknownFlow()
	}
	if err != nil {
		return Completed{}, refuse(503, "unavailable", "Ploeg could not read the flow.")
	}
	return s.complete(ctx, caller, flow, req)
}

func (s *Service) complete(ctx context.Context, caller Caller, flow store.ConnectionFlow, req CompleteRequest) (Completed, *Error) {
	now := s.now()
	switch {
	case flow.Status == "completed":
		if flow.CompletedAt == nil || now.Sub(*flow.CompletedAt) > ReplayWindow || flow.Outcome == nil {
			return Completed{}, unknownFlow()
		}
		return s.replay(ctx, *flow.Outcome)
	case flow.Status == "completing":
		return Completed{}, refuse(409, "flow_in_progress", "This flow is being completed by another call.")
	case flow.Status == "failed":
		return Completed{}, refuse(410, "flow_expired", "This flow failed after its exchange; start a new one.")
	case !now.Before(flow.ExpiresAt):
		return Completed{}, refuse(410, "flow_expired", "This flow expired; start a new one.")
	}
	var start StartRequest
	if err := json.Unmarshal(flow.Request, &start); err != nil {
		return Completed{}, refuse(503, "unavailable", "Ploeg could not read the flow.")
	}
	switch flow.Method {
	case MethodOAuth, MethodGitHubAppManifest:
		if req.Code == "" {
			return Completed{}, refuse(400, "invalid_request", "This flow completes with code only; a token or installationId is refused.")
		}
	case MethodGitHubAppInstallation:
		if req.InstallationID == "" {
			return Completed{}, refuse(400, "invalid_request", "This flow completes with installationId and setupAction only.")
		}
	case MethodToken:
		if req.Token == "" {
			return Completed{}, refuse(400, "invalid_request", "This flow completes with token only.")
		}
	}
	if e := s.Accepting(); e != nil {
		return Completed{}, e
	}
	claimed, err := s.Store.ClaimFlow(ctx, flow.ID)
	if err != nil {
		return Completed{}, refuse(503, "unavailable", "Ploeg could not claim the flow.")
	}
	if !claimed {
		current, err := s.Store.Flow(ctx, flow.ID)
		if err == nil && current.Status != "pending" {
			return s.complete(ctx, caller, current, req)
		}
		return Completed{}, refuse(410, "flow_expired", "This flow expired; start a new one.")
	}
	var out Completed
	var e *Error
	switch flow.Method {
	case MethodOAuth:
		out, e = s.completeOAuth(ctx, caller, flow, start, req.Code)
	case MethodToken:
		out, e = s.finishCredential(ctx, caller, flow, start, credential{values: map[string]string{"access_token": req.Token}, token: req.Token})
	case MethodGitHubAppManifest:
		out, e = s.completeManifest(ctx, caller, flow, start, req.Code)
	case MethodGitHubAppInstallation:
		out, e = s.completeInstallation(ctx, caller, flow, start, req.InstallationID, req.SetupAction)
	}
	if e != nil {
		detail := map[string]any{"flow": flow.ID, "kind": start.Kind, "instance": start.Instance, "method": flow.Method, "code": e.Code}
		if len(e.Checks) > 0 {
			detail["checks"] = e.Checks
		}
		if start.Connection != "" {
			detail["connection"] = start.Connection
		}
		if err := s.Store.FailFlow(ctx, flow.ID, []store.ConnectionAudit{s.audit(caller.Person, "connection.refused", &caller, detail)}); err != nil {
			s.log().Error("connection flow failure not recorded", "flow", flow.ID, "err", err)
		}
		if flow.Verifier != nil {
			_ = s.Secrets.Delete(ctx, "flows/"+flow.ID)
		}
		return Completed{}, e
	}
	if flow.Verifier != nil {
		if err := s.Secrets.Delete(ctx, "flows/"+flow.ID); err != nil {
			s.log().Warn("flow verifier left in the store; the sweep removes it", "flow", flow.ID, "err", err)
		}
	}
	return out, nil
}

func (s *Service) replay(ctx context.Context, outcome store.FlowOutcome) (Completed, *Error) {
	switch outcome.Kind {
	case "connection":
		row, err := s.Store.Connection(ctx, s.tenant(), outcome.ID)
		if err != nil {
			return Completed{}, refuse(503, "unavailable", "Ploeg could not read the connection.")
		}
		v := s.view(ctx, row)
		return Completed{Connection: &v, Replayed: true, Status: 200}, nil
	case "app":
		app, err := s.Store.ConnectionApp(ctx, s.tenant(), outcome.ID)
		if err != nil {
			return Completed{}, refuse(503, "unavailable", "Ploeg could not read the connection app.")
		}
		v := s.appView(ctx, app)
		return Completed{ConnectionApp: &v, Replayed: true, Status: 200}, nil
	}
	return Completed{}, unknownFlow()
}

type credential struct {
	values  map[string]string
	token   string
	expires *time.Time
	field   secretsField
	revoke  bool
}

func (s *Service) completeOAuth(ctx context.Context, caller Caller, flow store.ConnectionFlow, start StartRequest, code string) (Completed, *Error) {
	inst := s.Instances[start.Instance]
	conn := s.Connectors[start.Kind]
	verifier := ""
	if flow.Verifier != nil {
		var ref secretstore.Ref
		if err := json.Unmarshal(flow.Verifier, &ref); err != nil {
			return Completed{}, refuse(503, "secret_store_unavailable", "The flow's PKCE verifier reference is unreadable.")
		}
		values, err := s.Secrets.Get(ctx, ref)
		if err != nil {
			return Completed{}, storeError(err)
		}
		verifier = values["pkce_verifier"]
	}
	cred, err := conn.Authorizer.Exchange(ctx, inst, start.RedirectURI, code, verifier)
	if err != nil {
		if errors.Is(err, provider.ErrVendorUnavailable) {
			return Completed{}, refuse(503, "vendor_unavailable", "The vendor's token endpoint could not be reached.")
		}
		return Completed{}, &Error{Status: 422, Code: "probe_failed", Message: "The vendor refused the authorization code.", Checks: []Check{{Name: "code_exchange", Result: "fail"}}}
	}
	values := map[string]string{"access_token": cred.AccessToken}
	if cred.RefreshToken != "" {
		values["refresh_token"] = cred.RefreshToken
	}
	c := credential{values: values, token: cred.AccessToken, revoke: true}
	if !cred.ExpiresAt.IsZero() {
		expires := cred.ExpiresAt.UTC()
		c.expires = &expires
		c.field = secretsField{Lifetime: int64(cred.ExpiresAt.Sub(s.now()).Seconds()), RedirectURI: start.RedirectURI}
	}
	return s.finishCredential(ctx, caller, flow, start, c)
}

func (s *Service) revokeQuietly(ctx context.Context, kind string, inst provider.ConnectionInstance, token string) {
	conn := s.Connectors[kind]
	if conn.Authorizer == nil || token == "" {
		return
	}
	if err := conn.Authorizer.Revoke(context.WithoutCancel(ctx), inst, token); err != nil && !errors.Is(err, provider.ErrNotSupported) {
		s.log().Warn("connection token revocation failed", "kind", kind, "instance", inst.ID, "err", err)
	}
}

func (s *Service) finishCredential(ctx context.Context, caller Caller, flow store.ConnectionFlow, start StartRequest, c credential) (Completed, *Error) {
	inst := s.Instances[start.Instance]
	discard := func() {
		if c.revoke {
			s.revokeQuietly(ctx, start.Kind, inst, c.token)
		}
	}
	report := s.probeCredential(ctx, start.Port, start.Kind, inst, provider.ConnectionCredential{Port: start.Port, Token: c.token}, nil)
	if report.Result != "pass" {
		discard()
		if report.Unknown {
			return Completed{}, &Error{Status: 503, Code: "vendor_unavailable", Message: "The vendor could not be reached to probe the credential.", Checks: report.Checks}
		}
		return Completed{}, &Error{Status: 422, Code: "probe_failed", Message: "The credential failed its probe; nothing was stored.", Checks: report.Checks}
	}
	row := store.ConnectionRow{Tenant: s.tenant(), Port: start.Port, Kind: start.Kind, Instance: start.Instance, Method: flow.Method,
		AccountID: report.Accounts.ID, State: StateActive, ExpiresAt: c.expires, HandOverAgent: start.HandOverAgent, Label: start.Label,
		CreatedBy: caller.Person, UpdatedBy: caller.Person}
	row.Account, _ = json.Marshal(report.Accounts)
	row.LastProbe, _ = json.Marshal(report)
	at := report.At
	row.LastProbeAt = &at
	return s.writeConnection(ctx, caller, flow, start, row, c.values, c.field, discard)
}

type secretsField struct {
	Credential  *secretstore.Ref `json:"credential,omitempty"`
	App         *secretstore.Ref `json:"app,omitempty"`
	Lifetime    int64            `json:"lifetime,omitempty"`
	RedirectURI string           `json:"redirectUri,omitempty"`
}

func (s *Service) writeConnection(ctx context.Context, caller Caller, flow store.ConnectionFlow, start StartRequest, row store.ConnectionRow, values map[string]string, field secretsField, discard func()) (Completed, *Error) {
	var old store.ConnectionRow
	reconnect := start.Connection != ""
	if reconnect {
		defer s.changed(start.Connection)
		var err error
		old, err = s.Store.Connection(ctx, s.tenant(), start.Connection)
		if err != nil || old.RemovedAt != nil {
			discard()
			return Completed{}, refuse(404, "unknown_connection", "The connection to reconnect was removed.")
		}
		if !sameAccount(old, row) {
			discard()
			return Completed{}, refuse(409, "account_mismatch", "A reconnect must act as the same vendor account; nothing changed.")
		}
		row.ID = old.ID
	} else {
		if existing, err := s.Store.LiveConnectionByAccount(ctx, s.tenant(), row.Port, row.Kind, row.Instance, row.AccountID); err == nil {
			discard()
			return Completed{}, &Error{Status: 409, Code: "already_connected", Message: "That account is already a live connection.", Connection: existing.ID}
		}
		if start.ID != "" {
			if e := s.idFree(ctx, start.ID); e != nil {
				discard()
				return Completed{}, e
			}
			row.ID = start.ID
		} else {
			var login provider.ConnectionAccount
			_ = json.Unmarshal(row.Account, &login)
			id, err := s.deriveID(ctx, row.Kind, firstNonEmpty(login.Login, login.ID, login.InstallationID))
			if err != nil {
				discard()
				return Completed{}, refuse(503, "unavailable", "Ploeg could not derive a connection id.")
			}
			row.ID = id
		}
		if n, err := s.Store.CountLiveConnections(ctx, s.tenant()); err != nil || n >= s.Config.Max {
			discard()
			return Completed{}, refuse(429, "connection_limit", "This Tenant has reached connections.max live runtime connections.")
		}
	}
	var oldValues map[string]string
	if values != nil {
		var oldField secretsField
		if reconnect && json.Unmarshal(old.Secrets, &oldField) == nil && oldField.Credential != nil {
			var err error
			oldValues, err = s.Secrets.Get(ctx, *oldField.Credential)
			if err != nil && !errors.Is(err, secretstore.ErrNotFound) {
				discard()
				return Completed{}, storeError(err)
			}
		}
		ref, err := s.Secrets.Put(ctx, "connections/"+row.ID, values)
		if err != nil {
			discard()
			return Completed{}, storeError(err)
		}
		field.Credential = &ref
	} else {
		field.App = appRefOf(row)
	}
	row.Secrets, _ = json.Marshal(field)
	detail := map[string]any{"connection": row.ID, "kind": row.Kind, "instance": row.Instance, "method": row.Method,
		"account": json.RawMessage(row.Account), "secrets": field, "flow": flow.ID}
	var err error
	status := 201
	if reconnect {
		status = 200
		var oldField secretsField
		_ = json.Unmarshal(old.Secrets, &oldField)
		detail["previousSecrets"] = oldField
		err = s.Store.ReconnectConnection(ctx, row, flow.ID, store.FlowOutcome{Kind: "connection", ID: row.ID, Status: status},
			[]store.ConnectionAudit{s.audit(caller.Person, "connection.reconnected", &caller, detail)})
	} else {
		err = s.Store.CreateConnection(ctx, row, flow.ID, store.FlowOutcome{Kind: "connection", ID: row.ID, Status: status},
			[]store.ConnectionAudit{s.audit(caller.Person, "connection.created", &caller, detail)})
	}
	if err != nil {
		if values != nil {
			s.undoCredentialWrite(ctx, row.ID, reconnect, oldValues)
		}
		discard()
		if errors.Is(err, store.ErrConnectionConflict) {
			return Completed{}, refuse(409, "already_connected", "That account or id is already a live connection.")
		}
		return Completed{}, refuse(503, "unavailable", "Ploeg could not record the connection; what it stored was deleted.")
	}
	if reconnect && oldValues["access_token"] != "" && oldValues["access_token"] != values["access_token"] {
		s.revokeQuietly(ctx, row.Kind, s.Instances[row.Instance], oldValues["access_token"])
	}
	saved, err := s.Store.Connection(ctx, s.tenant(), row.ID)
	if err != nil {
		return Completed{}, refuse(503, "unavailable", "Ploeg recorded the connection but could not read it back.")
	}
	v := s.view(ctx, saved)
	return Completed{Connection: &v, Status: status}, nil
}

// undoCredentialWrite puts the store back as it was when the row that
// should have referenced a new credential was not written: a new
// connection's path is deleted, and a reconnected one gets its previous
// values back as the newest version, so the live connection never reads a
// token that is about to be revoked, also in a store that keeps one version.
func (s *Service) undoCredentialWrite(ctx context.Context, id string, reconnect bool, oldValues map[string]string) {
	ctx = context.WithoutCancel(ctx)
	path := "connections/" + id
	if !reconnect || oldValues == nil {
		if err := s.Secrets.Delete(ctx, path); err != nil {
			s.log().Error("connection credential not cleaned up after a failed write", "connection", id, "err", err)
		}
		return
	}
	if _, err := s.Secrets.Put(ctx, path, oldValues); err != nil {
		s.log().Error("previous connection credential not restored after a failed reconnect", "connection", id, "err", err)
	}
}

func sameAccount(old, next store.ConnectionRow) bool {
	if strings.HasPrefix(old.AccountID, "installation:") && next.InstallationID != "" {
		return old.AccountID == "installation:"+next.InstallationID || old.InstallationID == next.InstallationID
	}
	return old.AccountID == next.AccountID
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return "connection"
}

func appRefOf(row store.ConnectionRow) *secretstore.Ref {
	var f secretsField
	if json.Unmarshal(row.Secrets, &f) == nil {
		return f.App
	}
	return nil
}
