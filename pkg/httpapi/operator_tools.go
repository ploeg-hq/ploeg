package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/connection"
	"github.com/ploeg-hq/ploeg/pkg/secretstore"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/toolregistry"
)

const (
	maxToolBody          = 64 * 1024
	toolProbesPerMin     = 30
	toolRequestTimeout   = 90 * time.Second
	toolRegistryRequires = "toolRegistry.requireClaims"
)

// ToolRegistry serves the MCP server, skill source and Team tools routes of
// ADR-0199. Nil, or a nil Service, refuses every change with
// tool_registry_not_accepted and reads an empty registry.
type ToolRegistry struct {
	Service       *toolregistry.Service
	RequireClaims map[string][]string
	StepUp        *connection.StepUp

	mu     sync.Mutex
	probes map[string][]time.Time
}

func (t *ToolRegistry) allowProbe(consumer string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.probes == nil {
		t.probes = map[string][]time.Time{}
	}
	recent := t.probes[consumer][:0]
	for _, at := range t.probes[consumer] {
		if now.Sub(at) < time.Minute {
			recent = append(recent, at)
		}
	}
	if len(recent) >= toolProbesPerMin {
		t.probes[consumer] = recent
		return false
	}
	t.probes[consumer] = append(recent, now)
	return true
}

func (s *Server) registerOperatorTools(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/operator/mcp-servers", s.handleListMCPServers)
	mux.HandleFunc("GET /api/v1/operator/mcp-servers/{server}", s.handleGetMCPServer)
	mux.HandleFunc("POST /api/v1/operator/mcp-servers", s.handleCreateMCPServer)
	mux.HandleFunc("PUT /api/v1/operator/mcp-servers/{server}", s.handleReplaceMCPServer)
	mux.HandleFunc("DELETE /api/v1/operator/mcp-servers/{server}", s.handleRemoveMCPServer)
	mux.HandleFunc("GET /api/v1/operator/skill-sources", s.handleListSkillSources)
	mux.HandleFunc("GET /api/v1/operator/skill-sources/{source}", s.handleGetSkillSource)
	mux.HandleFunc("POST /api/v1/operator/skill-sources", s.handleCreateSkillSource)
	mux.HandleFunc("PUT /api/v1/operator/skill-sources/{source}", s.handleReplaceSkillSource)
	mux.HandleFunc("DELETE /api/v1/operator/skill-sources/{source}", s.handleRemoveSkillSource)
	mux.HandleFunc("GET /api/v1/operator/teams/{team}/tools", s.handleGetTeamTools)
	mux.HandleFunc("PUT /api/v1/operator/teams/{team}/tools", s.handleReplaceTeamTools)
	mux.HandleFunc("DELETE /api/v1/operator/teams/{team}/tools", s.handleRemoveTeamTools)
}

var toolRoutes = regexp.MustCompile(`^/api/v1/operator/(mcp-servers|skill-sources)(/|$)|^/api/v1/operator/teams/[^/]+/tools$`)

func isToolProbeCall(r *http.Request) bool {
	return (r.Method == http.MethodPost || r.Method == http.MethodPut) && toolRoutes.MatchString(r.URL.Path)
}

func (s *Server) toolService() *toolregistry.Service {
	if s.ToolRegistry == nil {
		return nil
	}
	return s.ToolRegistry.Service
}

func toolError(w http.ResponseWriter, e *toolregistry.Error) {
	body := map[string]any{"code": e.Code, "message": e.Message}
	if e.Field != "" {
		body["field"] = e.Field
	}
	if len(e.Checks) > 0 {
		body["checks"] = e.Checks
	}
	operatorJSON(w, e.Status, map[string]any{"schemaVersion": "1.0", "error": body})
}

// toolCaller applies ADR-0199 section 6's gates to a change: the
// consumer's manageTools, the installation's opt-in, a proven person in
// every attribution mode and the claims toolRegistry names.
func (s *Server) toolCaller(w http.ResponseWriter, r *http.Request) (OperatorPrincipal, store.ToolAudit, bool) {
	p, ok := OperatorPrincipalFromContext(r.Context())
	if !ok || !p.CanManageTools {
		operatorError(w, 403, "permission_required", "Changing MCP servers, skill sources or a Team's tools needs the consumer's manageTools permission.")
		return p, store.ToolAudit{}, false
	}
	svc := s.toolService()
	if svc == nil || !svc.Accepting {
		operatorError(w, 422, "tool_registry_not_accepted", "This installation does not accept runtime MCP servers, skill sources or attachments (toolRegistry.enabled).")
		return p, store.ToolAudit{}, false
	}
	person, ok := ProvenPersonFromContext(r.Context())
	if !ok {
		operatorError(w, 403, reasonPersonTokenRequired, "MCP servers, skill sources and a Team's tools are changed only by a proven person, in every attribution mode: send the person's exchanged token. Nothing changed.")
		return p, store.ToolAudit{}, false
	}
	if !personHoldsClaims(w, person, s.ToolRegistry.RequireClaims, s.ToolRegistry.StepUp, toolRegistryRequires) {
		return p, store.ToolAudit{}, false
	}
	a, bad := operatorAttribution(r, p.Name)
	if bad != nil {
		operatorError(w, 400, bad.code, bad.message)
		return p, store.ToolAudit{}, false
	}
	if a.Actor != person.ID {
		operatorError(w, 403, reasonPersonTokenRequired, "The change must be attributed to the proven person.")
		return p, store.ToolAudit{}, false
	}
	if !s.ToolRegistry.allowProbe(p.Name, time.Now()) {
		operatorError(w, 429, "probe_rate_limited", "This consumer changed or probed the tool registry too often in the last minute.")
		return p, store.ToolAudit{}, false
	}
	return p, store.ToolAudit{Attribution: a}, true
}

func (s *Server) refuseTool(ctx context.Context, w http.ResponseWriter, r *http.Request, audit store.ToolAudit, e *toolregistry.Error) {
	if svc := s.toolService(); svc != nil && e.Status != 412 {
		_ = svc.Store.ToolRegistryRefused(ctx, e.Code, r.Method+" "+r.Pattern, audit)
	}
	toolError(w, e)
}

func decodeTool(w http.ResponseWriter, r *http.Request, out any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		operatorError(w, 415, "json_required", "Use application/json.")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxToolBody))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(out); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			operatorError(w, 413, "too_large", "A tool registry request is at most 64 KiB.")
			return false
		}
		operatorError(w, 400, "invalid_request", "The request is invalid; any field the route does not know is refused.")
		return false
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		operatorError(w, 400, "invalid_request", "Supply one JSON object.")
		return false
	}
	return true
}

func ifMatchRevision(w http.ResponseWriter, r *http.Request) (int64, bool) {
	match := r.Header.Get("If-Match")
	if match == "" {
		operatorError(w, 428, "if_match_required", "Send If-Match: \"<revision>\".")
		return 0, false
	}
	m := bindingRevision.FindStringSubmatch(match)
	if m == nil {
		operatorError(w, 412, "revision_stale", "If-Match is not a revision.")
		return 0, false
	}
	revision, _ := strconv.ParseInt(m[1], 10, 64)
	return revision, true
}

func setETag(w http.ResponseWriter, revision int64) {
	if revision > 0 {
		w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(revision, 10)))
	}
}

type credentialRequest struct {
	Value        string `json:"value,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
}

func (c *credentialRequest) secret() *string {
	if c == nil {
		return nil
	}
	v := c.Value
	if v == "" {
		v = c.ClientSecret
	}
	return &v
}

type serverRequest struct {
	Key        string              `json:"key"`
	Name       string              `json:"name"`
	URL        string              `json:"url"`
	Transport  string              `json:"transport"`
	Auth       toolregistry.Auth   `json:"auth"`
	Tools      []toolregistry.Tool `json:"tools"`
	Adapter    string              `json:"adapter,omitempty"`
	Credential *credentialRequest  `json:"credential,omitempty"`
}

func (q serverRequest) service() toolregistry.ServerRequest {
	transport := q.Transport
	if transport == "" {
		transport = toolregistry.TransportStreamableHTTP
	}
	return toolregistry.ServerRequest{Key: q.Key, Name: q.Name, URL: q.URL, Transport: transport, Auth: q.Auth, Tools: q.Tools,
		Adapter: q.Adapter, Credential: q.Credential.secret()}
}

type credentialView struct {
	Set bool             `json:"set"`
	Ref *secretstore.Ref `json:"ref,omitempty"`
	Env string           `json:"env,omitempty"`
}

type serverView struct {
	ID         string              `json:"id"`
	Key        string              `json:"key"`
	Name       string              `json:"name"`
	Source     string              `json:"source"`
	State      string              `json:"state"`
	Revision   int64               `json:"revision"`
	URL        string              `json:"url,omitempty"`
	Host       string              `json:"host"`
	Transport  string              `json:"transport"`
	Auth       toolregistry.Auth   `json:"auth"`
	Credential credentialView      `json:"credential"`
	Tools      []toolregistry.Tool `json:"tools"`
	Adapter    string              `json:"adapter,omitempty"`
}

func viewServer(s toolregistry.Server, full bool) serverView {
	v := serverView{ID: s.ID, Key: s.Key, Name: s.Name, Source: s.Source, State: stateOr(s.State), Revision: s.Revision, Transport: s.Transport,
		Auth: s.Auth, Tools: s.Tools, Adapter: s.Adapter, Credential: credentialView{Set: s.HasCredential()}}
	if v.Tools == nil {
		v.Tools = []toolregistry.Tool{}
	}
	if u, err := url.Parse(s.URL); err == nil {
		v.Host = u.Host
		if full {
			v.URL = s.URL
		} else {
			u.RawQuery = ""
			v.URL = u.String()
		}
	}
	if full && !s.Credential.IsZero() {
		ref := s.Credential
		v.Credential.Ref = &ref
	}
	if full {
		v.Credential.Env = s.CredentialEnv
	}
	return v
}

func stateOr(state string) string {
	if state == "" {
		return "active"
	}
	return state
}

func (s *Server) visibleToolIDs(ctx context.Context, p OperatorPrincipal) map[string]bool {
	if p.CanManageTools {
		return nil
	}
	ids := map[string]bool{}
	svc := s.toolService()
	if svc == nil {
		return ids
	}
	for team := range s.OperatorConfig.Teams {
		if !p.AllowsTeam(team) {
			continue
		}
		view, e := svc.TeamTools(ctx, team)
		if e != nil {
			continue
		}
		for _, rt := range view.Attachment.Roles {
			for _, u := range rt.MCP {
				ids[u.Server] = true
			}
			for _, k := range rt.Skills {
				ids[k] = true
			}
		}
	}
	return ids
}

func (s *Server) handleListMCPServers(w http.ResponseWriter, r *http.Request) {
	if !noQuery(w, r) {
		return
	}
	p, _ := OperatorPrincipalFromContext(r.Context())
	views := []serverView{}
	if svc := s.toolService(); svc != nil {
		servers, e := svc.Servers(r.Context())
		if e != nil {
			toolError(w, e)
			return
		}
		visible := s.visibleToolIDs(r.Context(), p)
		for _, srv := range servers {
			if visible == nil || visible[srv.ID] {
				views = append(views, viewServer(srv, visible == nil))
			}
		}
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "mcpServers": views})
}

func (s *Server) handleGetMCPServer(w http.ResponseWriter, r *http.Request) {
	ref, ok := pathName(w, r, "server")
	if !ok || !noQuery(w, r) {
		return
	}
	svc := s.toolService()
	if svc == nil {
		operatorError(w, 404, "unknown_server", "No live server has this id or key.")
		return
	}
	srv, e := svc.Server(r.Context(), ref)
	p, _ := OperatorPrincipalFromContext(r.Context())
	visible := s.visibleToolIDs(r.Context(), p)
	if e == nil && visible != nil && !visible[srv.ID] {
		e = &toolregistry.Error{Status: 404, Code: "unknown_server", Message: "No live server has this id or key."}
	}
	if e != nil {
		toolError(w, e)
		return
	}
	setETag(w, srv.Revision)
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "mcpServer": viewServer(srv, visible == nil)})
}

func (s *Server) answerServer(w http.ResponseWriter, res toolregistry.ServerResult, dryRun bool, status int) {
	body := map[string]any{"schemaVersion": "1.0", "mcpServer": viewServer(res.Server, true), "checks": orEmptyChecks(res.Checks)}
	if res.Probe != nil {
		body["offered"] = res.Probe.Tools
		body["protocolVersion"] = res.Probe.ProtocolVersion
	}
	if dryRun {
		body["dryRun"] = true
	} else {
		body["created"] = res.Created
		setETag(w, res.Server.Revision)
	}
	operatorJSON(w, status, body)
}

func orEmptyChecks(c []toolregistry.Check) []toolregistry.Check {
	if c == nil {
		return []toolregistry.Check{}
	}
	return c
}

func (s *Server) handleCreateMCPServer(w http.ResponseWriter, r *http.Request) {
	dryRun, ok := dryRunQuery(w, r)
	if !ok {
		return
	}
	_, audit, ok := s.toolCaller(w, r)
	if !ok {
		return
	}
	var req serverRequest
	if !decodeTool(w, r, &req) {
		return
	}
	res, e := s.toolService().CreateServer(r.Context(), audit, req.service(), dryRun)
	if e != nil {
		s.refuseTool(r.Context(), w, r, audit, e)
		return
	}
	status := 200
	if res.Created {
		status = 201
	}
	s.answerServer(w, res, dryRun, status)
}

func (s *Server) handleReplaceMCPServer(w http.ResponseWriter, r *http.Request) {
	dryRun, ok := dryRunQuery(w, r)
	if !ok {
		return
	}
	_, audit, ok := s.toolCaller(w, r)
	if !ok {
		return
	}
	ref, ok := pathName(w, r, "server")
	if !ok {
		return
	}
	revision, ok := ifMatchRevision(w, r)
	if !ok {
		return
	}
	var req serverRequest
	if !decodeTool(w, r, &req) {
		return
	}
	res, e := s.toolService().ReplaceServer(r.Context(), audit, ref, revision, req.service(), dryRun)
	if e != nil {
		s.refuseTool(r.Context(), w, r, audit, e)
		return
	}
	s.answerServer(w, res, dryRun, 200)
}

func (s *Server) handleRemoveMCPServer(w http.ResponseWriter, r *http.Request) {
	if !noQuery(w, r) {
		return
	}
	_, audit, ok := s.toolCaller(w, r)
	if !ok {
		return
	}
	ref, ok := pathName(w, r, "server")
	if !ok {
		return
	}
	if e := s.toolService().RemoveServer(r.Context(), audit, ref); e != nil {
		s.refuseTool(r.Context(), w, r, audit, e)
		return
	}
	w.WriteHeader(204)
}

type sourceRequest struct {
	Key        string             `json:"key"`
	Name       string             `json:"name"`
	Kind       string             `json:"kind"`
	Ref        string             `json:"ref,omitempty"`
	URL        string             `json:"url,omitempty"`
	Commit     string             `json:"commit,omitempty"`
	Digest     string             `json:"digest,omitempty"`
	Credential *credentialRequest `json:"credential,omitempty"`
}

func (q sourceRequest) service() toolregistry.SourceRequest {
	return toolregistry.SourceRequest{Key: q.Key, Name: q.Name, Kind: q.Kind, Ref: q.Ref, URL: q.URL, Commit: q.Commit, Digest: q.Digest,
		Credential: q.Credential.secret()}
}

type sourceView struct {
	ID           string         `json:"id"`
	Key          string         `json:"key"`
	Name         string         `json:"name"`
	Source       string         `json:"source"`
	State        string         `json:"state"`
	Revision     int64          `json:"revision"`
	Kind         string         `json:"kind"`
	Ref          string         `json:"ref,omitempty"`
	URL          string         `json:"url,omitempty"`
	Commit       string         `json:"commit,omitempty"`
	Digest       string         `json:"digest"`
	BundleDigest string         `json:"bundleDigest,omitempty"`
	Skills       []string       `json:"skills"`
	Credential   credentialView `json:"credential"`
}

func viewSource(s toolregistry.SkillSource, full bool) sourceView {
	v := sourceView{ID: s.ID, Key: s.Key, Name: s.Name, Source: s.Source, State: stateOr(s.State), Revision: s.Revision, Kind: s.Kind,
		Ref: s.Ref, Commit: s.Commit, Digest: s.Digest, BundleDigest: s.BundleDigest, Skills: s.Skills,
		Credential: credentialView{Set: !s.Credential.IsZero()}}
	if v.Skills == nil {
		v.Skills = []string{}
	}
	if u, err := url.Parse(s.URL); err == nil && s.URL != "" {
		if !full {
			u.RawQuery = ""
		}
		v.URL = u.String()
	}
	if full && !s.Credential.IsZero() {
		ref := s.Credential
		v.Credential.Ref = &ref
	}
	return v
}

func (s *Server) handleListSkillSources(w http.ResponseWriter, r *http.Request) {
	if !noQuery(w, r) {
		return
	}
	p, _ := OperatorPrincipalFromContext(r.Context())
	views := []sourceView{}
	if svc := s.toolService(); svc != nil {
		sources, e := svc.Sources(r.Context())
		if e != nil {
			toolError(w, e)
			return
		}
		visible := s.visibleToolIDs(r.Context(), p)
		for _, src := range sources {
			if visible == nil || visible[src.ID] {
				views = append(views, viewSource(src, visible == nil))
			}
		}
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "skillSources": views})
}

func (s *Server) handleGetSkillSource(w http.ResponseWriter, r *http.Request) {
	ref, ok := pathName(w, r, "source")
	if !ok || !noQuery(w, r) {
		return
	}
	svc := s.toolService()
	if svc == nil {
		operatorError(w, 404, "unknown_source", "No live skill source has this id or key.")
		return
	}
	src, e := svc.Source(r.Context(), ref)
	p, _ := OperatorPrincipalFromContext(r.Context())
	visible := s.visibleToolIDs(r.Context(), p)
	if e == nil && visible != nil && !visible[src.ID] {
		e = &toolregistry.Error{Status: 404, Code: "unknown_source", Message: "No live skill source has this id or key."}
	}
	if e != nil {
		toolError(w, e)
		return
	}
	setETag(w, src.Revision)
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "skillSource": viewSource(src, visible == nil)})
}

func (s *Server) answerSource(w http.ResponseWriter, res toolregistry.SourceResult, dryRun bool, status int) {
	body := map[string]any{"schemaVersion": "1.0", "skillSource": viewSource(res.Source, true), "checks": orEmptyChecks(res.Checks)}
	if dryRun {
		body["dryRun"] = true
	} else {
		body["created"] = res.Created
		setETag(w, res.Source.Revision)
	}
	operatorJSON(w, status, body)
}

func (s *Server) handleCreateSkillSource(w http.ResponseWriter, r *http.Request) {
	dryRun, ok := dryRunQuery(w, r)
	if !ok {
		return
	}
	_, audit, ok := s.toolCaller(w, r)
	if !ok {
		return
	}
	var req sourceRequest
	if !decodeTool(w, r, &req) {
		return
	}
	res, e := s.toolService().CreateSource(r.Context(), audit, req.service(), dryRun)
	if e != nil {
		s.refuseTool(r.Context(), w, r, audit, e)
		return
	}
	status := 200
	if res.Created {
		status = 201
	}
	s.answerSource(w, res, dryRun, status)
}

func (s *Server) handleReplaceSkillSource(w http.ResponseWriter, r *http.Request) {
	dryRun, ok := dryRunQuery(w, r)
	if !ok {
		return
	}
	_, audit, ok := s.toolCaller(w, r)
	if !ok {
		return
	}
	ref, ok := pathName(w, r, "source")
	if !ok {
		return
	}
	revision, ok := ifMatchRevision(w, r)
	if !ok {
		return
	}
	var req sourceRequest
	if !decodeTool(w, r, &req) {
		return
	}
	res, e := s.toolService().ReplaceSource(r.Context(), audit, ref, revision, req.service(), dryRun)
	if e != nil {
		s.refuseTool(r.Context(), w, r, audit, e)
		return
	}
	s.answerSource(w, res, dryRun, 200)
}

func (s *Server) handleRemoveSkillSource(w http.ResponseWriter, r *http.Request) {
	if !noQuery(w, r) {
		return
	}
	_, audit, ok := s.toolCaller(w, r)
	if !ok {
		return
	}
	ref, ok := pathName(w, r, "source")
	if !ok {
		return
	}
	if e := s.toolService().RemoveSource(r.Context(), audit, ref); e != nil {
		s.refuseTool(r.Context(), w, r, audit, e)
		return
	}
	w.WriteHeader(204)
}

type grantView struct {
	Servers []grantServerView         `json:"servers"`
	Skills  []toolregistry.SkillGrant `json:"skills"`
}

type grantServerView struct {
	ID    string                         `json:"id"`
	Key   string                         `json:"key"`
	Tools map[string]toolregistry.Access `json:"tools"`
}

func viewTeamTools(v toolregistry.TeamToolsView) map[string]any {
	grants := map[string]grantView{}
	for role, g := range v.Grants {
		gv := grantView{Servers: []grantServerView{}, Skills: g.Skills}
		for _, srv := range g.Servers {
			gv.Servers = append(gv.Servers, grantServerView{ID: srv.ID, Key: srv.Key, Tools: srv.Tools})
		}
		grants[role] = gv
	}
	roles := v.Attachment.Roles
	if roles == nil {
		roles = map[string]toolregistry.RoleTools{}
	}
	source := v.Attachment.Source
	if source == "" {
		source = toolregistry.FromRuntime
	}
	team := map[string]any{"key": v.Team.Key}
	if v.Team.ID != "" {
		team["id"] = v.Team.ID
	}
	return map[string]any{"team": team, "source": source, "exists": v.Exists, "revision": v.Attachment.Revision, "roles": roles, "grants": grants}
}

func (s *Server) teamRef(w http.ResponseWriter, r *http.Request) (string, bool) {
	ref, ok := pathName(w, r, "team")
	if !ok {
		return "", false
	}
	p, _ := OperatorPrincipalFromContext(r.Context())
	if !p.CanManageTools && !p.AllowsTeam(ref) {
		operatorError(w, 404, "unknown_team", "No Team has this id, key or alias.")
		return "", false
	}
	return ref, true
}

func (s *Server) handleGetTeamTools(w http.ResponseWriter, r *http.Request) {
	if !noQuery(w, r) {
		return
	}
	ref, ok := s.teamRef(w, r)
	if !ok {
		return
	}
	svc := s.toolService()
	if svc == nil {
		operatorError(w, 404, "unknown_team", "No Team has this id, key or alias.")
		return
	}
	view, e := svc.TeamTools(r.Context(), ref)
	if e != nil {
		toolError(w, e)
		return
	}
	setETag(w, view.Attachment.Revision)
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "teamTools": viewTeamTools(view)})
}

type teamToolsRequest struct {
	Roles map[string]toolregistry.RoleTools `json:"roles"`
}

func (s *Server) handleReplaceTeamTools(w http.ResponseWriter, r *http.Request) {
	dryRun, ok := dryRunQuery(w, r)
	if !ok {
		return
	}
	_, audit, ok := s.toolCaller(w, r)
	if !ok {
		return
	}
	ref, ok := pathName(w, r, "team")
	if !ok {
		return
	}
	var revision *int64
	switch {
	case r.Header.Get("If-None-Match") == "*" && r.Header.Get("If-Match") == "":
	case r.Header.Get("If-None-Match") != "":
		operatorError(w, 400, "invalid_request", "If-None-Match takes only *.")
		return
	default:
		rev, ok := ifMatchRevision(w, r)
		if !ok {
			return
		}
		revision = &rev
	}
	var req teamToolsRequest
	if !decodeTool(w, r, &req) {
		return
	}
	view, e := s.toolService().ReplaceTeamTools(r.Context(), audit, ref, req.Roles, revision, dryRun)
	if e != nil {
		s.refuseTool(r.Context(), w, r, audit, e)
		return
	}
	body := map[string]any{"schemaVersion": "1.0", "teamTools": viewTeamTools(view)}
	if dryRun {
		body["dryRun"] = true
	} else {
		setETag(w, view.Attachment.Revision)
	}
	operatorJSON(w, 200, body)
}

func (s *Server) handleRemoveTeamTools(w http.ResponseWriter, r *http.Request) {
	if !noQuery(w, r) {
		return
	}
	_, audit, ok := s.toolCaller(w, r)
	if !ok {
		return
	}
	ref, ok := pathName(w, r, "team")
	if !ok {
		return
	}
	if e := s.toolService().RemoveTeamTools(r.Context(), audit, ref); e != nil {
		s.refuseTool(r.Context(), w, r, audit, e)
		return
	}
	w.WriteHeader(204)
}
