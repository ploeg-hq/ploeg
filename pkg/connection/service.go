package connection

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/secretstore"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

// Limits of ADR-0195 section 11.
const (
	FlowLifetime         = 15 * time.Minute
	ReplayWindow         = time.Hour
	OpenFlowsPerPerson   = 5
	OpenFlowsPerConsumer = 20
	ProbeInterval        = 30 * time.Second
	MaxLabel             = 200
	MaxToken             = 4096
	DefaultTenant        = "default"
)

const handOverMissing = "The adapter of this kind does not recognise an assignment to a connection's own account, so handOverAgent cannot be true."

// Service makes and manages runtime connections.
type Service struct {
	Store      *store.Store
	Secrets    secretstore.Store
	Config     *Config
	Instances  map[string]provider.ConnectionInstance
	Connectors map[string]provider.Connector
	// Static lists the connections configured through the environment or
	// Helm; their names can never be a runtime id.
	Static []Static
	// PublicWebhookBase is PLOEG_PUBLIC_WEBHOOK_BASE: the HTTPS base at
	// which vendors reach ploegd's /webhooks routes.
	PublicWebhookBase string
	// Users lists the bindings and runtime targets that name a connection.
	// Nil means nothing can use one yet.
	Users func(ctx context.Context, id string) ([]Use, error)
	// OnChange is told the id of a connection that was reconnected,
	// removed or changed state, so what holds its credential lets it go.
	OnChange func(id string)
	Tenant   string
	Log      *slog.Logger
	Now      func() time.Time

	mu          sync.Mutex
	hookSecrets map[string]heldHookSecret
	appSecrets  map[string]string
	heldApps    map[string]bool
	heldAt      time.Time
}

func (s *Service) changed(id string) {
	s.forgetHookSecret(id)
	if s.OnChange != nil {
		s.OnChange(id)
	}
}

func (s *Service) tenant() string {
	if s.Tenant != "" {
		return s.Tenant
	}
	return DefaultTenant
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.DiscardHandler)
}

// Accepting reports the opt-in and store gates of ADR-0195 section 7.
func (s *Service) Accepting() *Error {
	if s == nil || s.Config == nil || !s.Config.Enabled {
		return refuse(422, "connections_not_accepted", "This installation does not accept runtime connections (connections.enabled).")
	}
	if !secretstore.Configured(s.Secrets) {
		return refuse(503, "secret_store_unavailable", "No secret store is configured, so no connection can be made or changed; nothing was written.")
	}
	return nil
}

func random(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashState(state string) []byte {
	sum := sha256.Sum256([]byte(state))
	return sum[:]
}

func (s *Service) isStatic(id string) bool {
	return slices.ContainsFunc(s.Static, func(st Static) bool { return st.ID == id })
}

func (s *Service) audit(actor, action string, caller *Caller, detail map[string]any) store.ConnectionAudit {
	if detail == nil {
		detail = map[string]any{}
	}
	if caller != nil {
		detail["consumer"] = caller.Consumer
		if caller.Via != "" {
			detail["channel"] = caller.Via
		}
		if caller.Attribution != nil {
			detail[store.AuditAttributionKey] = caller.Attribution
		}
	}
	return store.ConnectionAudit{Actor: actor, Action: action, Detail: detail}
}

func (s *Service) refused(ctx context.Context, caller *Caller, e *Error, detail map[string]any) *Error {
	if detail == nil {
		detail = map[string]any{}
	}
	detail["code"] = e.Code
	if len(e.Checks) > 0 {
		detail["checks"] = e.Checks
	}
	if err := s.Store.AuditConnection(ctx, s.audit(caller.Person, "connection.refused", caller, detail)); err != nil {
		s.log().Error("connection refusal audit failed", "err", err)
	}
	return e
}

// Start begins a flow (ADR-0195 section 3).
func (s *Service) Start(ctx context.Context, caller Caller, req StartRequest) (Started, *Error) {
	if e := s.Accepting(); e != nil {
		return Started{}, e
	}
	detail := map[string]any{"kind": req.Kind, "instance": req.Instance, "method": req.Method, "port": req.Port}
	fail := func(e *Error) (Started, *Error) { return Started{}, s.refused(ctx, &caller, e, detail) }
	if e := s.checkStart(ctx, caller, &req); e != nil {
		return fail(e)
	}
	inst := s.Instances[req.Instance]
	conn := s.Connectors[req.Kind]
	flowID := "flow_" + random(16)
	state := random(32)
	expires := s.now().Add(FlowLifetime).UTC()
	view := FlowView{ID: flowID, Method: req.Method, Port: req.Port, Kind: req.Kind, Instance: req.Instance, Status: "pending", ExpiresAt: expires, State: state}
	var next Next
	var verifierRef json.RawMessage
	switch req.Method {
	case MethodOAuth:
		challenge := ""
		if conn.Authorizer.PKCE() {
			verifier := random(48)
			ref, err := s.Secrets.Put(ctx, "flows/"+flowID, map[string]string{"pkce_verifier": verifier})
			if err != nil {
				return fail(storeError(err))
			}
			verifierRef, _ = json.Marshal(ref)
			challenge = pkceChallenge(verifier)
		}
		u, err := conn.Authorizer.AuthorizeURL(inst, req.RedirectURI, state, challenge)
		if err != nil {
			return fail(refuse(422, "capability_missing", "The instance's OAuth application cannot build an authorize URL."))
		}
		next.AuthorizeURL = u
	case MethodGitHubAppManifest:
		hookPort := "forge"
		if req.Port == PortTracker {
			hookPort = "tracker"
		}
		name := "ploeg-" + strings.ToLower(random(4))
		manifest, err := conn.Manifest.Manifest(inst, provider.ManifestRequest{Port: req.Port, Name: name, HomeURL: strings.TrimRight(s.PublicWebhookBase, "/"),
			HookURL: strings.TrimRight(s.PublicWebhookBase, "/") + "/webhooks/" + hookPort + "/" + req.Kind, RedirectURL: req.RedirectURI, SetupURL: req.RedirectURI})
		if err != nil {
			return fail(refuse(422, "capability_missing", "The adapter could not build an App manifest."))
		}
		next.Manifest = manifest
		next.PostURL = conn.Manifest.ManifestPostURL(inst, req.Owner, state)
	case MethodGitHubAppInstallation:
		app, err := s.Store.ConnectionApp(ctx, s.tenant(), req.App)
		if err != nil || app.RemovedAt != nil {
			return fail(refuse(404, "unknown_app", "No connection app has that id."))
		}
		next.InstallURL = conn.Manifest.InstallURL(inst, app.Slug, state)
	}
	raw, _ := json.Marshal(req)
	flow := store.ConnectionFlow{ID: flowID, StateHash: hashState(state), Tenant: s.tenant(), Person: caller.Person, Consumer: caller.Consumer,
		Method: req.Method, Request: raw, Verifier: verifierRef, ExpiresAt: expires}
	detail["flow"] = flowID
	if req.Connection != "" {
		detail["connection"] = req.Connection
	}
	ok, err := s.Store.CreateFlow(ctx, flow, OpenFlowsPerPerson, OpenFlowsPerConsumer, []store.ConnectionAudit{s.audit(caller.Person, "connection.flow_started", &caller, detail)})
	if err != nil {
		return Started{}, refuse(503, "unavailable", "Ploeg could not record the flow.")
	}
	if !ok {
		if verifierRef != nil {
			_ = s.Secrets.Delete(ctx, "flows/"+flowID)
		}
		return fail(refuse(429, "flow_limit", fmt.Sprintf("At most %d open flows per person and %d per consumer.", OpenFlowsPerPerson, OpenFlowsPerConsumer)))
	}
	return Started{Flow: view, Next: next}, nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func storeError(err error) *Error {
	if errors.Is(err, secretstore.ErrNotConfigured) || errors.Is(err, secretstore.ErrUnavailable) {
		return refuse(503, "secret_store_unavailable", "The secret store could not be written; nothing was recorded.")
	}
	return refuse(503, "secret_store_unavailable", "The secret store refused the write; nothing was recorded.")
}

func (s *Service) checkStart(ctx context.Context, caller Caller, req *StartRequest) *Error {
	if req.Port != PortTracker && req.Port != PortForge {
		return refuse(400, "invalid_request", "port is tracker or forge.")
	}
	if !slices.Contains(methods, req.Method) {
		return refuse(400, "invalid_request", "method is oauth, github_app_manifest, github_app_installation, token or gitlab_service_account.")
	}
	for _, v := range []string{req.Kind, req.Instance} {
		if !namePattern.MatchString(v) {
			return refuse(400, "invalid_request", "kind and instance are identifiers, never URLs.")
		}
	}
	for _, v := range []string{req.App, req.Owner, req.Connection, req.ID} {
		if v != "" && !namePattern.MatchString(v) {
			return refuse(400, "invalid_request", "app, owner, connection and id are identifiers.")
		}
	}
	if len([]rune(req.Label)) > MaxLabel {
		return refuse(400, "invalid_request", "label is at most 200 characters.")
	}
	if req.HandOverAgent && req.Port != PortTracker {
		return refuse(400, "invalid_request", "handOverAgent is for tracker connections only.")
	}
	allow, ok := s.Config.allowed(req.Port, req.Kind, req.Instance)
	if !ok {
		return refuse(422, "kind_not_allowed", "This installation does not allow that port, kind and instance at runtime.")
	}
	if !slices.Contains(allow.Methods, req.Method) {
		return refuse(422, "method_not_allowed", "This installation does not allow that method for that instance.")
	}
	inst := s.Instances[req.Instance]
	conn, ok := s.Connectors[req.Kind]
	if !ok || conn.Prober == nil {
		return refuse(422, "capability_missing", "The adapter of this kind cannot be connected at runtime (ConnectionProber).")
	}
	if req.HandOverAgent && !conn.HandOverAgent {
		return refuse(422, "capability_missing", handOverMissing)
	}
	switch req.Method {
	case MethodOAuth:
		if conn.Authorizer == nil || inst.OAuth == nil {
			return refuse(422, "capability_missing", "This kind or instance has no OAuth flow (ConnectionAuthorizer).")
		}
		if !slices.Contains(caller.RedirectURIs, req.RedirectURI) || !slices.Contains(inst.OAuth.RedirectURIs, req.RedirectURI) {
			return refuse(422, "redirect_uri_not_allowed", "redirectUri must be one of the consumer's connectionRedirectUris and of the instance's OAuth redirect URIs.")
		}
	case MethodGitHubAppManifest, MethodGitHubAppInstallation:
		if conn.Manifest == nil {
			return refuse(422, "capability_missing", "This kind has no App manifest flow (AppManifest).")
		}
		if !slices.Contains(caller.RedirectURIs, req.RedirectURI) {
			return refuse(422, "redirect_uri_not_allowed", "redirectUri must be one of the consumer's connectionRedirectUris.")
		}
		if req.Method == MethodGitHubAppManifest {
			if s.PublicWebhookBase == "" {
				return refuse(422, "webhook_base_missing", "PLOEG_PUBLIC_WEBHOOK_BASE is unset, so an App manifest cannot name Ploeg's webhook URL.")
			}
			if req.Connection != "" || req.App != "" {
				return refuse(400, "invalid_request", "A manifest flow creates an App; it names no connection or app.")
			}
		} else if req.App == "" {
			return refuse(400, "invalid_request", "An installation flow names the connection app to install.")
		}
	case MethodGitLabServiceAccount:
		if req.RedirectURI != "" {
			return refuse(400, "invalid_request", "A gitlab_service_account flow has no redirectUri.")
		}
		if req.Port != PortForge || conn.ServiceAccounts == nil {
			return refuse(422, "capability_missing", "This kind or port cannot bootstrap a service account (ServiceAccountBootstrapper).")
		}
	case MethodToken:
		if req.RedirectURI != "" {
			return refuse(400, "invalid_request", "A token flow has no redirectUri.")
		}
		if !conn.TokenOnly && inst.OAuth != nil {
			return refuse(422, "oauth_required", "This instance has an OAuth application, so a pasted token is not accepted (ADR-0184 rule 1).")
		}
	}
	if req.Method != MethodGitHubAppInstallation && req.App != "" {
		return refuse(400, "invalid_request", "app is for github_app_installation flows only.")
	}
	if req.Owner != "" && req.Method != MethodGitHubAppManifest {
		return refuse(400, "invalid_request", "owner is for github_app_manifest flows only.")
	}
	if req.Connection != "" {
		if s.isStatic(req.Connection) {
			return refuse(409, "static_connection", "A static connection cannot be reconnected through the API.")
		}
		row, err := s.Store.Connection(ctx, s.tenant(), req.Connection)
		if err != nil || row.RemovedAt != nil || row.Port != req.Port {
			return refuse(404, "unknown_connection", "No live connection of that port has that id.")
		}
		if row.Kind != req.Kind || row.Instance != req.Instance {
			return refuse(400, "invalid_request", "A reconnect keeps the connection's port, kind and instance.")
		}
		if req.ID != "" && req.ID != req.Connection {
			return refuse(400, "invalid_request", "A reconnect keeps the connection's id.")
		}
		return nil
	}
	if req.Method == MethodGitHubAppManifest {
		return nil
	}
	if req.ID != "" {
		if e := s.idFree(ctx, req.ID); e != nil {
			return e
		}
	}
	n, err := s.Store.CountLiveConnections(ctx, s.tenant())
	if err != nil {
		return refuse(503, "unavailable", "Ploeg could not read its connections.")
	}
	if n >= s.Config.Max {
		return refuse(429, "connection_limit", fmt.Sprintf("This Tenant already has %d live runtime connections (connections.max).", n))
	}
	return nil
}

func (s *Service) idFree(ctx context.Context, id string) *Error {
	if s.isStatic(id) {
		return refuse(409, "id_taken", "That id names a static connection.")
	}
	taken, err := s.Store.ConnectionIDTaken(ctx, s.tenant(), id)
	if err != nil {
		return refuse(503, "unavailable", "Ploeg could not read its connections.")
	}
	if taken {
		return refuse(409, "id_taken", "That id is or was a connection's; a removed id is never reused.")
	}
	return nil
}

func (s *Service) deriveID(ctx context.Context, kind, login string) (string, error) {
	base := strings.ToLower(kind + "-" + login)
	var b strings.Builder
	for i, r := range base {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (i > 0 && strings.ContainsRune("_.:-", r))
		if ok {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	id := b.String()
	if len(id) > 120 {
		id = id[:120]
	}
	for n := 1; n < 1000; n++ {
		candidate := id
		if n > 1 {
			candidate = fmt.Sprintf("%s-%d", id, n)
		}
		if s.isStatic(candidate) {
			continue
		}
		taken, err := s.Store.ConnectionIDTaken(ctx, s.tenant(), candidate)
		if err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
	}
	return "", errors.New("no free connection id")
}

func newHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
