package toolregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/secretstore"
)

// DefaultTenant is the Tenant of an installation that names none.
const DefaultTenant = "default"

// Store is what the registry keeps in ploegd's database. Implemented by
// *store.Store through an adapter in ploegd.
type Store interface {
	LiveMCPServers(ctx context.Context, tenant string) ([]Server, error)
	CreateMCPServer(ctx context.Context, in Server, max int, audit Audit) (Server, bool, error)
	ReplaceMCPServer(ctx context.Context, in Server, revision int64, audit Audit) (Server, Server, error)
	RemoveMCPServer(ctx context.Context, tenant, id string, audit Audit) (Server, error)
	LiveSkillSources(ctx context.Context, tenant string) ([]SkillSource, error)
	CreateSkillSource(ctx context.Context, in SkillSource, max int, audit Audit) (SkillSource, bool, error)
	ReplaceSkillSource(ctx context.Context, in SkillSource, revision int64, audit Audit) (SkillSource, SkillSource, error)
	RemoveSkillSource(ctx context.Context, tenant, id string, audit Audit) (SkillSource, error)
	PutSkillBundle(ctx context.Context, digest string, archive []byte, skills []string) error
	SkillBundle(ctx context.Context, digest string) ([]byte, error)
	TeamTools(ctx context.Context, tenant string, team Team) (Attachment, error)
	LiveTeamTools(ctx context.Context, tenant string) ([]Attachment, error)
	ReplaceTeamTools(ctx context.Context, tenant string, team Team, roles map[string]RoleTools, revision *int64, audit Audit) (Attachment, error)
	RemoveTeamTools(ctx context.Context, tenant string, team Team, audit Audit) error
	ToolRegistryRefused(ctx context.Context, code, route string, audit Audit) error
}

// Audit is the attribution of a change, opaque to the registry.
type Audit any

// Errors a Store returns that the registry maps to operator refusals.
var (
	ErrNotFound = errors.New("toolregistry: not found")
	ErrLimit    = errors.New("toolregistry: limit reached")
)

// ExistsError is a key a live entry holds with another body.
type ExistsError struct{ Revision int64 }

func (e *ExistsError) Error() string { return "toolregistry: key taken" }

// StaleError is a revision that is no longer current.
type StaleError struct{ Revision int64 }

func (e *StaleError) Error() string { return "toolregistry: revision stale" }

// InUseError is a removal refused while attachments name the entry.
type InUseError struct{ Teams []string }

func (e *InUseError) Error() string { return "toolregistry: in use" }

// Teams resolves a Team by id, key or alias.
type Teams interface {
	Resolve(ref string) (Team, bool)
}

// Service registers servers, sources and attachments and builds grants.
type Service struct {
	Store   Store
	Secrets secretstore.Store
	Teams   Teams
	Policy  Policy
	Prober  *Prober
	Fetcher *Fetcher
	// Accepting reports whether runtime changes are switched on.
	Accepting bool
	Limit     int
	Tenant    string
	// StaticServers, StaticSources and StaticTools are the configuration
	// file's entries, which win on a key conflict.
	StaticServers []Server
	StaticSources []SkillSource
	StaticTools   map[string]map[string]RoleTools
	// StaticSourceEnv names, by static source key, the environment
	// variable holding its pull credential.
	StaticSourceEnv map[string]string
	Env             func(string) string
	Log             *slog.Logger
	Now             func() time.Time
}

func (s *Service) tenant() string {
	if s.Tenant == "" {
		return DefaultTenant
	}
	return s.Tenant
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) env(name string) string {
	if s.Env != nil {
		return s.Env(name)
	}
	return os.Getenv(name)
}

func (s *Service) limit() int {
	if s.Limit <= 0 {
		return 50
	}
	return s.Limit
}

func storeFault(err error) *Error {
	return refuse(503, "store_unavailable", "The registry could not be read or written; nothing changed.")
}

// Servers are every server of the Tenant: static first, then runtime, a
// runtime server whose key a static one takes marked shadowed.
func (s *Service) Servers(ctx context.Context) ([]Server, *Error) {
	runtime, err := s.Store.LiveMCPServers(ctx, s.tenant())
	if err != nil {
		return nil, storeFault(err)
	}
	out := append([]Server{}, s.StaticServers...)
	for _, r := range runtime {
		if s.staticServerKey(r.Key) {
			r.State = "shadowed"
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *Service) staticServerKey(key string) bool {
	for _, st := range s.StaticServers {
		if strings.EqualFold(st.Key, key) {
			return true
		}
	}
	return false
}

func (s *Service) staticSourceKey(key string) bool {
	for _, st := range s.StaticSources {
		if strings.EqualFold(st.Key, key) {
			return true
		}
	}
	return false
}

// Sources are every skill source of the Tenant, as Servers orders them.
func (s *Service) Sources(ctx context.Context) ([]SkillSource, *Error) {
	runtime, err := s.Store.LiveSkillSources(ctx, s.tenant())
	if err != nil {
		return nil, storeFault(err)
	}
	out := append([]SkillSource{}, s.StaticSources...)
	for _, r := range runtime {
		if s.staticSourceKey(r.Key) {
			r.State = "shadowed"
		}
		out = append(out, r)
	}
	return out, nil
}

// Server returns the server named ref (an id or a key).
func (s *Service) Server(ctx context.Context, ref string) (Server, *Error) {
	all, e := s.Servers(ctx)
	if e != nil {
		return Server{}, e
	}
	if srv, ok := findServer(all, ref); ok {
		return srv, nil
	}
	return Server{}, refuse(404, "unknown_server", "No live server has this id or key.")
}

// Source returns the skill source named ref.
func (s *Service) Source(ctx context.Context, ref string) (SkillSource, *Error) {
	all, e := s.Sources(ctx)
	if e != nil {
		return SkillSource{}, e
	}
	if src, ok := findSource(all, ref); ok {
		return src, nil
	}
	return SkillSource{}, refuse(404, "unknown_source", "No live skill source has this id or key.")
}

// ServerRequest is a create or replace of a server. Credential nil keeps a
// stored one on replace.
type ServerRequest struct {
	Key        string
	Name       string
	URL        string
	Transport  string
	Auth       Auth
	Tools      []Tool
	Adapter    string
	Credential *string
}

// ServerResult is a change's outcome, or a dry run's plan.
type ServerResult struct {
	Server  Server
	Created bool
	Checks  []Check
	Probe   *ProbeResult
}

func (r ServerRequest) input(hasStored bool) ServerInput {
	in := ServerInput{Key: r.Key, Name: r.Name, URL: r.URL, Transport: r.Transport, Auth: r.Auth, Tools: r.Tools, Adapter: r.Adapter,
		HasCredential: r.Credential != nil && *r.Credential != "" || r.Credential == nil && hasStored}
	if r.Credential != nil {
		in.CredentialLen = len(*r.Credential)
	}
	return in
}

func (s *Service) refuseRuntime() *Error {
	if !s.Accepting {
		return refuse(422, "tool_registry_not_accepted", "This installation does not accept runtime MCP servers, skill sources or attachments (toolRegistry.enabled).")
	}
	return nil
}

// CreateServer validates, probes and stores a server; dryRun stops after
// the probe and writes nothing.
func (s *Service) CreateServer(ctx context.Context, audit Audit, req ServerRequest, dryRun bool) (ServerResult, *Error) {
	if e := s.refuseRuntime(); e != nil {
		return ServerResult{}, e
	}
	if e := s.Policy.CheckServer(ctx, req.input(false)); e != nil {
		return ServerResult{}, e
	}
	if s.staticServerKey(req.Key) {
		return ServerResult{}, refuseField(409, "conflicts_with_static", "key", "The configuration file registers a server with this key; static wins.")
	}
	credential := ""
	if req.Credential != nil {
		credential = *req.Credential
	}
	result := ServerResult{Server: Server{Tenant: s.tenant(), Key: req.Key, Name: req.Name, URL: req.URL, Transport: req.Transport, Auth: req.Auth,
		Tools: req.Tools, Adapter: req.Adapter, Source: FromRuntime, State: "active"}}
	checks, probe, e := s.probe(ctx, result.Server, credential)
	result.Checks, result.Probe = checks, probe
	if e != nil || dryRun {
		return result, e
	}
	if req.Credential != nil && !secretstore.Configured(s.Secrets) {
		return result, refuse(422, "store_unavailable", "No secret store is configured, so a credential cannot be kept; nothing changed.")
	}
	id, err := newID("mcp", s.now())
	if err != nil {
		return result, storeFault(err)
	}
	result.Server.ID = id
	path := ""
	if credential != "" {
		path = "mcp-servers/" + id
		ref, err := s.Secrets.Put(ctx, path, map[string]string{"value": credential})
		if err != nil {
			return result, refuse(422, "store_unavailable", "The secret store refused the credential; nothing changed.")
		}
		result.Server.Credential = ref
	}
	out, created, err := s.Store.CreateMCPServer(ctx, result.Server, s.limit(), audit)
	if path != "" && (err != nil || !created) {
		_ = s.Secrets.Delete(ctx, path)
	}
	if err != nil {
		return result, s.mapStoreError(err, "server")
	}
	result.Server, result.Created = out, created
	return result, nil
}

// ReplaceServer replaces a runtime server at revision.
func (s *Service) ReplaceServer(ctx context.Context, audit Audit, ref string, revision int64, req ServerRequest, dryRun bool) (ServerResult, *Error) {
	if e := s.refuseRuntime(); e != nil {
		return ServerResult{}, e
	}
	current, e := s.Server(ctx, ref)
	if e != nil {
		return ServerResult{}, e
	}
	if current.Source == FromStatic {
		return ServerResult{}, refuse(409, "conflicts_with_static", "This server is registered in the configuration file; change it there.")
	}
	if e := s.Policy.CheckServer(ctx, req.input(!current.Credential.IsZero())); e != nil {
		return ServerResult{}, e
	}
	if !strings.EqualFold(req.Key, current.Key) && s.staticServerKey(req.Key) {
		return ServerResult{}, refuseField(409, "conflicts_with_static", "key", "The configuration file registers a server with this key; static wins.")
	}
	if current.Revision != revision {
		return ServerResult{}, refuse(412, "revision_stale", fmt.Sprintf("The server's revision is %d.", current.Revision))
	}
	credential := ""
	switch {
	case req.Credential != nil:
		credential = *req.Credential
	case !current.Credential.IsZero() && req.Auth.Kind != AuthNone:
		v, err := s.credentialValue(ctx, current)
		if err != nil {
			return ServerResult{}, refuse(422, "store_unavailable", "The stored credential could not be read; nothing changed.")
		}
		credential = v
	}
	next := Server{ID: current.ID, Tenant: s.tenant(), Key: req.Key, Name: req.Name, URL: req.URL, Transport: req.Transport, Auth: req.Auth,
		Tools: req.Tools, Adapter: req.Adapter, Source: FromRuntime, State: "active"}
	checks, probe, e := s.probe(ctx, next, credential)
	result := ServerResult{Server: next, Checks: checks, Probe: probe}
	if e != nil || dryRun {
		if dryRun && e == nil {
			result.Server.Revision = current.Revision
		}
		return result, e
	}
	if e := s.attachedToolsStillAllowed(ctx, current.ID, next.Tools); e != nil {
		return result, e
	}
	path := "mcp-servers/" + current.ID
	switch {
	case req.Credential != nil && *req.Credential != "":
		ref, err := s.Secrets.Put(ctx, path, map[string]string{"value": *req.Credential})
		if err != nil {
			return result, refuse(422, "store_unavailable", "The secret store refused the credential; nothing changed.")
		}
		next.Credential = ref
	case req.Auth.Kind == AuthNone || req.Adapter != "":
		next.Credential = secretstore.Ref{}
	}
	_, out, err := s.Store.ReplaceMCPServer(ctx, next, revision, audit)
	if err != nil {
		return result, s.mapStoreError(err, "server")
	}
	if (req.Auth.Kind == AuthNone || req.Adapter != "") && !current.Credential.IsZero() {
		_ = s.Secrets.Delete(ctx, path)
	}
	result.Server = out
	return result, nil
}

func (s *Service) attachedToolsStillAllowed(ctx context.Context, serverID string, tools []Tool) *Error {
	next := Server{ID: serverID, Tools: tools}
	for team, roles := range s.StaticTools {
		if e := toolsStillAllowed(team, roles, next); e != nil {
			return e
		}
	}
	live, err := s.Store.LiveTeamTools(ctx, s.tenant())
	if err != nil {
		return storeFault(err)
	}
	for _, a := range live {
		if e := toolsStillAllowed(a.Team, a.Roles, next); e != nil {
			return e
		}
	}
	return nil
}

func toolsStillAllowed(team string, roles map[string]RoleTools, next Server) *Error {
	for role, rt := range roles {
		for _, use := range rt.MCP {
			if use.Server != next.ID {
				continue
			}
			for _, name := range use.Tools {
				if _, ok := next.Tool(name); !ok {
					return refuseField(409, "server_in_use", "tools", fmt.Sprintf("Team %s Role %s is given tool %q; change its attachment first.", team, role, name))
				}
			}
		}
	}
	return nil
}

// RemoveServer removes a runtime server and deletes its stored credential.
func (s *Service) RemoveServer(ctx context.Context, audit Audit, ref string) *Error {
	if e := s.refuseRuntime(); e != nil {
		return e
	}
	current, e := s.Server(ctx, ref)
	if e != nil {
		if e.Code == "unknown_server" {
			return nil
		}
		return e
	}
	if current.Source == FromStatic {
		return refuse(409, "conflicts_with_static", "This server is registered in the configuration file; remove it there.")
	}
	if teams := s.staticTeamsNaming(current.ID); len(teams) > 0 {
		return &Error{Status: 409, Code: "server_in_use", Message: "Attachments name this server: " + strings.Join(teams, ", ") + "."}
	}
	if _, err := s.Store.RemoveMCPServer(ctx, s.tenant(), current.ID, audit); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return s.mapStoreError(err, "server")
	}
	if !current.Credential.IsZero() {
		_ = s.Secrets.Delete(ctx, current.Credential.Path)
	}
	return nil
}

func (s *Service) staticTeamsNaming(id string) []string {
	var out []string
	for team, roles := range s.StaticTools {
		for _, rt := range roles {
			named := false
			for _, u := range rt.MCP {
				named = named || u.Server == id
			}
			for _, k := range rt.Skills {
				named = named || k == id
			}
			if named {
				out = append(out, team)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

func (s *Service) mapStoreError(err error, what string) *Error {
	var exists *ExistsError
	var stale *StaleError
	var inUse *InUseError
	switch {
	case errors.As(err, &exists):
		return refuseField(409, "key_taken", "key", fmt.Sprintf("Another live %s holds this key.", what))
	case errors.As(err, &stale):
		return refuse(412, "revision_stale", fmt.Sprintf("The revision is now %d.", stale.Revision))
	case errors.As(err, &inUse):
		code := "server_in_use"
		if what == "skill source" {
			code = "source_in_use"
		}
		return &Error{Status: 409, Code: code, Message: "Attachments name this " + what + ": " + strings.Join(inUse.Teams, ", ") + "."}
	case errors.Is(err, ErrLimit):
		return refuse(429, "limit_reached", fmt.Sprintf("This Tenant holds its maximum of %d live runtime entries.", s.limit()))
	case errors.Is(err, ErrNotFound):
		return refuse(404, "unknown_"+strings.ReplaceAll(strings.TrimPrefix(what, "skill "), " ", "_"), "Not found.")
	}
	if s.Log != nil {
		s.Log.Error("tool registry store", "err", err)
	}
	return storeFault(err)
}

func (s *Service) probe(ctx context.Context, srv Server, credential string) ([]Check, *ProbeResult, *Error) {
	if srv.Adapter == AdapterGatewayAccessGroups || s.Prober == nil {
		return []Check{{Name: "probe", OK: true, Detail: "not probed: reached with the Run's model key"}}, nil, nil
	}
	token := credential
	if srv.Auth.Kind == AuthOAuthClientCredentials {
		t, e := s.clientCredentialsToken(ctx, srv.Auth, credential)
		if e != nil {
			return []Check{{Name: "token", OK: false, Detail: e.Message}}, nil, e
		}
		token = t.AccessToken
	}
	res, e := s.Prober.Probe(ctx, srv.URL, srv.Auth, token)
	if e != nil {
		return []Check{{Name: "probe", OK: false, Detail: e.Message}}, nil, e
	}
	checks, conflict := CompareProbe(srv.Tools, res)
	return checks, &res, conflict
}

// Token is an access token obtained by client credentials.
type Token struct {
	AccessToken string
	ExpiresAt   time.Time
}

func (s *Service) clientCredentialsToken(ctx context.Context, a Auth, secret string) (Token, *Error) {
	form := url.Values{"grant_type": {"client_credentials"}}
	if len(a.Scopes) > 0 {
		form.Set("scope", strings.Join(a.Scopes, " "))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, refuse(422, "invalid_url", "auth.tokenUrl is not usable.")
	}
	req.SetBasicAuth(url.QueryEscape(a.ClientID), url.QueryEscape(secret))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := s.Prober.Client.Do(req)
	if err != nil {
		return Token{}, refuse(422, "credential_refused", "The token endpoint could not be reached.")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var tok struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &tok) != nil || tok.AccessToken == "" || !strings.EqualFold(tok.TokenType, "bearer") {
		return Token{}, refuse(422, "credential_refused", fmt.Sprintf("The token endpoint refused the client credentials (HTTP %d).", resp.StatusCode))
	}
	exp := s.now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	if tok.ExpiresIn <= 0 {
		exp = s.now().Add(5 * time.Minute)
	}
	return Token{AccessToken: tok.AccessToken, ExpiresAt: exp}, nil
}

func (s *Service) credentialValue(ctx context.Context, srv Server) (string, error) {
	if srv.CredentialEnv != "" {
		v := s.env(srv.CredentialEnv)
		if v == "" {
			return "", fmt.Errorf("toolregistry: %s is empty", srv.CredentialEnv)
		}
		return v, nil
	}
	if srv.Credential.IsZero() {
		return "", nil
	}
	values, err := s.Secrets.Get(ctx, srv.Credential)
	if err != nil {
		return "", err
	}
	return values["value"], nil
}

// Credential is what the credential gateway attaches for g: the stored
// value, or for client credentials an access token obtained now. Only the
// gateway's grant route calls it; the value is never logged or stored.
func (s *Service) Credential(ctx context.Context, g ServerGrant) (string, time.Time, error) {
	srv := Server{Credential: g.Credential, CredentialEnv: g.CredentialEnv}
	v, err := s.credentialValue(ctx, srv)
	if err != nil || g.Auth.Kind != AuthOAuthClientCredentials {
		return v, time.Time{}, err
	}
	t, e := s.clientCredentialsToken(ctx, g.Auth, v)
	if e != nil {
		return "", time.Time{}, e
	}
	return t.AccessToken, t.ExpiresAt, nil
}
