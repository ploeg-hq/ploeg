package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/connection"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

const maxConnectionBody = 64 * 1024

func (s *Server) registerOperatorConnections(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/operator/connection-flows", s.handleConnectionFlowStart)
	mux.HandleFunc("POST /api/v1/operator/connection-flows/complete", s.handleConnectionFlowComplete)
	mux.HandleFunc("GET /api/v1/operator/connection-flows/{flow}", s.handleConnectionFlow)
	for _, port := range []string{connection.PortTracker, connection.PortForge} {
		base := "/api/v1/operator/" + port + "-connections"
		mux.HandleFunc("GET "+base, s.handleConnectionList(port))
		mux.HandleFunc("GET "+base+"/{connection}", s.handleConnectionGet(port))
		mux.HandleFunc("POST "+base+"/{connection}/probe", s.handleConnectionProbe(port))
		mux.HandleFunc("PATCH "+base+"/{connection}", s.handleConnectionPatch(port))
		mux.HandleFunc("DELETE "+base+"/{connection}", s.handleConnectionRemove(port))
	}
	mux.HandleFunc("GET /api/v1/operator/forge-connections/{connection}/repositories", s.handleConnectionRepositories)
	mux.HandleFunc("GET /api/v1/operator/connection-apps", s.handleConnectionApps)
	mux.HandleFunc("GET /api/v1/operator/connection-apps/{app}", s.handleConnectionApp)
	mux.HandleFunc("DELETE /api/v1/operator/connection-apps/{app}", s.handleConnectionAppRemove)
}

func isConnectionVendorCall(r *http.Request) bool {
	p := r.URL.Path
	if !strings.HasPrefix(p, "/api/v1/operator/") {
		return false
	}
	return (r.Method == http.MethodPost && strings.HasPrefix(p, "/api/v1/operator/connection-flows")) ||
		(strings.Contains(p, "-connections/") && (r.Method == http.MethodDelete || strings.HasSuffix(p, "/probe") || strings.HasSuffix(p, "/repositories"))) ||
		(r.Method == http.MethodDelete && strings.HasPrefix(p, "/api/v1/operator/connection-apps/"))
}

func connectionError(w http.ResponseWriter, e *connection.Error) {
	body := map[string]any{"code": e.Code, "message": e.Message}
	if len(e.Checks) > 0 {
		body["checks"] = e.Checks
	}
	if len(e.UsedBy) > 0 {
		body["usedBy"] = e.UsedBy
	}
	if e.Connection != "" {
		body["connection"] = e.Connection
	}
	operatorJSON(w, e.Status, map[string]any{"schemaVersion": "1.0", "error": body})
}

func (s *Server) connectionsOn(w http.ResponseWriter) bool {
	if s.ConnectionService == nil {
		operatorError(w, 422, "connections_not_accepted", "This installation does not accept runtime connections (connections.enabled).")
		return false
	}
	return true
}

func connectionReader(w http.ResponseWriter, r *http.Request) bool {
	p, ok := OperatorPrincipalFromContext(r.Context())
	if !ok || !p.CanManageConnections {
		operatorError(w, 403, "permission_required", "Reading connections needs the consumer's manageConnections permission.")
		return false
	}
	return true
}

// connectionCaller applies the four gates of ADR-0195 section 7 that do not
// depend on the installation: the permission, a proven person in every
// attribution mode, the claims the installation requires and its step-up.
func (s *Server) connectionCaller(w http.ResponseWriter, r *http.Request) (connection.Caller, bool) {
	p, ok := OperatorPrincipalFromContext(r.Context())
	if !ok || !p.CanManageConnections {
		operatorError(w, 403, "permission_required", "Connections need the consumer's manageConnections permission.")
		return connection.Caller{}, false
	}
	person, ok := ProvenPersonFromContext(r.Context())
	if !ok {
		operatorError(w, 403, reasonPersonTokenRequired, "A connection is made, changed or removed only by a proven person: send the person's exchanged token. Nothing changed.")
		return connection.Caller{}, false
	}
	if s.ConnectionService != nil && s.ConnectionService.Config != nil {
		cfg := s.ConnectionService.Config
		names := make([]string, 0, len(cfg.RequireClaims))
		for name := range cfg.RequireClaims {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			if !claimHoldsOneOf(name, person.claims[name], cfg.RequireClaims[name]) {
				operatorError(w, 403, "person_claim_required", fmt.Sprintf("The person token carries none of the values connections.requireClaims names in its %q claim; nothing changed.", name))
				return connection.Caller{}, false
			}
		}
		if missing := unmetStepUp(cfg.StepUp, person.claims, time.Now()); missing != "" {
			challenge := `Bearer realm="ploeg-operator", error="insufficient_user_authentication", error_description="` + missing + `"`
			if len(cfg.StepUp.AcrValues) > 0 {
				challenge += `, acr_values="` + strings.Join(cfg.StepUp.AcrValues, " ") + `"`
			}
			if cfg.StepUp.MaxAge > 0 {
				challenge += `, max_age=` + strconv.Itoa(cfg.StepUp.MaxAge)
			}
			w.Header().Set("WWW-Authenticate", challenge)
			operatorError(w, 401, "insufficient_user_authentication", "The person must authenticate again: "+missing+".")
			return connection.Caller{}, false
		}
	}
	caller := connection.Caller{Consumer: p.Name, Person: person.ID, RedirectURIs: p.ConnectionRedirectURIs}
	if a, ok := store.AttributionFrom(r.Context()); ok {
		caller.Via, caller.Attribution = a.Via, a
	}
	return caller, true
}

func unmetStepUp(step *connection.StepUp, claims map[string]any, now time.Time) string {
	if step == nil {
		return ""
	}
	if len(step.AcrValues) > 0 && !claimHoldsOneOf("acr", claims["acr"], step.AcrValues) {
		return "the authentication context (acr) is not one this installation requires"
	}
	if len(step.AMR) > 0 && !claimHoldsOneOf("amr", claims["amr"], step.AMR) {
		return "the authentication method (amr) is not one this installation requires"
	}
	if step.MaxAge > 0 {
		at, ok := claims["auth_time"].(float64)
		if !ok || now.Sub(time.Unix(int64(at), 0)) > time.Duration(step.MaxAge)*time.Second {
			return "the authentication is older than this installation's maxAge"
		}
	}
	return ""
}

func decodeConnection(w http.ResponseWriter, r *http.Request, out any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		operatorError(w, 415, "json_required", "Use application/json.")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxConnectionBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			operatorError(w, 413, "too_large", "The request exceeds 64 KiB.")
			return false
		}
		operatorError(w, 400, "invalid_request", "The request body is not a valid object of the documented fields; unknown fields are refused.")
		return false
	}
	if decoder.Decode(new(any)) != io.EOF {
		operatorError(w, 400, "invalid_request", "Supply one JSON object.")
		return false
	}
	return true
}

func noQuery(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.RawQuery != "" {
		operatorError(w, 400, "invalid_request", "This resource takes no query parameters; a code, state or token travels only in a request body.")
		return false
	}
	return true
}

func pathName(w http.ResponseWriter, r *http.Request, key string) (string, bool) {
	v := r.PathValue(key)
	if !operatorName.MatchString(v) {
		operatorError(w, 400, "invalid_request", "Invalid identifier.")
		return "", false
	}
	return v, true
}

func (s *Server) handleConnectionFlowStart(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.connectionCaller(w, r)
	if !ok || !noQuery(w, r) || !s.connectionsOn(w) {
		return
	}
	var req connection.StartRequest
	if !decodeConnection(w, r, &req) {
		return
	}
	started, e := s.ConnectionService.Start(r.Context(), caller, req)
	if e != nil {
		connectionError(w, e)
		return
	}
	operatorJSON(w, 201, struct {
		SchemaVersion string `json:"schemaVersion"`
		connection.Started
	}{"1.0", started})
}

func (s *Server) handleConnectionFlowComplete(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.connectionCaller(w, r)
	if !ok || !noQuery(w, r) || !s.connectionsOn(w) {
		return
	}
	var req connection.CompleteRequest
	if !decodeConnection(w, r, &req) {
		return
	}
	done, e := s.ConnectionService.Complete(r.Context(), caller, req)
	if e != nil {
		connectionError(w, e)
		return
	}
	operatorJSON(w, done.Status, struct {
		SchemaVersion string `json:"schemaVersion"`
		connection.Completed
	}{"1.0", done})
}

func (s *Server) handleConnectionFlow(w http.ResponseWriter, r *http.Request) {
	if !connectionReader(w, r) || !noQuery(w, r) || !s.connectionsOn(w) {
		return
	}
	p, _ := OperatorPrincipalFromContext(r.Context())
	person, ok := ProvenPersonFromContext(r.Context())
	if !ok {
		operatorError(w, 403, reasonPersonTokenRequired, "A flow is read only by the proven person who started it.")
		return
	}
	id := r.PathValue("flow")
	status, e := s.ConnectionService.FlowStatus(r.Context(), connection.Caller{Consumer: p.Name, Person: person.ID}, id)
	if e != nil {
		connectionError(w, e)
		return
	}
	operatorJSON(w, 200, struct {
		SchemaVersion string `json:"schemaVersion"`
		connection.FlowStatus
	}{"1.0", status})
}

func pageQuery(w http.ResponseWriter, r *http.Request, cursor func(string) bool) (string, int, bool) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		operatorError(w, 400, "invalid_request", "Malformed query parameters.")
		return "", 0, false
	}
	for key, values := range q {
		if (key != "after" && key != "limit") || len(values) != 1 || values[0] == "" {
			operatorError(w, 400, "invalid_request", "Only after and limit are accepted, once each.")
			return "", 0, false
		}
	}
	limit := 50
	if raw := q.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 200 {
			operatorError(w, 400, "invalid_request", "limit must be between 1 and 200.")
			return "", 0, false
		}
	}
	after := q.Get("after")
	if after != "" && !cursor(after) {
		operatorError(w, 400, "invalid_request", "Invalid cursor.")
		return "", 0, false
	}
	return after, limit, true
}

func (s *Server) handleConnectionList(port string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !connectionReader(w, r) || !s.connectionsOn(w) {
			return
		}
		after, limit, ok := pageQuery(w, r, operatorName.MatchString)
		if !ok {
			return
		}
		views, next, e := s.ConnectionService.List(r.Context(), port, after, limit)
		if e != nil {
			connectionError(w, e)
			return
		}
		operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "connections": views, "nextCursor": next})
	}
}

func revisionETag(v connection.View) string { return `"` + strconv.FormatInt(v.Revision, 10) + `"` }

func (s *Server) handleConnectionGet(port string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !connectionReader(w, r) || !noQuery(w, r) || !s.connectionsOn(w) {
			return
		}
		id, ok := pathName(w, r, "connection")
		if !ok {
			return
		}
		view, e := s.ConnectionService.Get(r.Context(), port, id)
		if e != nil {
			connectionError(w, e)
			return
		}
		if view.Source == "runtime" {
			w.Header().Set("ETag", revisionETag(view))
		}
		operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "connection": view})
	}
}

func (s *Server) handleConnectionProbe(port string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller, ok := s.connectionCaller(w, r)
		if !ok || !noQuery(w, r) || !s.connectionsOn(w) {
			return
		}
		id, ok := pathName(w, r, "connection")
		if !ok {
			return
		}
		view, report, e := s.ConnectionService.Probe(r.Context(), caller, port, id)
		if e != nil {
			connectionError(w, e)
			return
		}
		operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "connection": view, "probe": report})
	}
}

func (s *Server) handleConnectionPatch(port string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller, ok := s.connectionCaller(w, r)
		if !ok || !noQuery(w, r) || !s.connectionsOn(w) {
			return
		}
		id, ok := pathName(w, r, "connection")
		if !ok {
			return
		}
		match := r.Header.Values("If-Match")
		if len(match) != 1 {
			operatorError(w, 428, "if_match_required", `Send If-Match: "<revision>".`)
			return
		}
		revision, err := strconv.ParseInt(strings.Trim(match[0], `"`), 10, 64)
		if err != nil || !strings.HasPrefix(match[0], `"`) || !strings.HasSuffix(match[0], `"`) {
			operatorError(w, 412, "revision_stale", `If-Match must be "<revision>".`)
			return
		}
		var req connection.PatchRequest
		if !decodeConnection(w, r, &req) {
			return
		}
		view, e := s.ConnectionService.Patch(r.Context(), caller, port, id, revision, req)
		if e != nil {
			connectionError(w, e)
			return
		}
		w.Header().Set("ETag", revisionETag(view))
		operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "connection": view})
	}
}

func leftAnswer(w http.ResponseWriter, left []connection.Left) {
	if len(left) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "leftAtVendor": left})
}

func (s *Server) handleConnectionRemove(port string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller, ok := s.connectionCaller(w, r)
		if !ok || !noQuery(w, r) || !s.connectionsOn(w) {
			return
		}
		id, ok := pathName(w, r, "connection")
		if !ok {
			return
		}
		left, e := s.ConnectionService.Remove(r.Context(), caller, port, id)
		if e != nil {
			connectionError(w, e)
			return
		}
		leftAnswer(w, left)
	}
}

func (s *Server) handleConnectionRepositories(w http.ResponseWriter, r *http.Request) {
	if !connectionReader(w, r) || !s.connectionsOn(w) {
		return
	}
	id, ok := pathName(w, r, "connection")
	if !ok {
		return
	}
	after, limit, ok := pageQuery(w, r, func(c string) bool { n, err := strconv.Atoi(c); return err == nil && n >= 1 && n <= 100000 })
	if !ok {
		return
	}
	page := 1
	if after != "" {
		page, _ = strconv.Atoi(after)
	}
	if limit > 100 {
		limit = 100
	}
	repos, next, e := s.ConnectionService.Repositories(r.Context(), id, page, limit, s.RegisteredRepository)
	if e != nil {
		connectionError(w, e)
		return
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "repositories": repos, "nextCursor": next})
}

func (s *Server) handleConnectionApps(w http.ResponseWriter, r *http.Request) {
	if !connectionReader(w, r) || !noQuery(w, r) || !s.connectionsOn(w) {
		return
	}
	apps, e := s.ConnectionService.Apps(r.Context())
	if e != nil {
		connectionError(w, e)
		return
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "connectionApps": apps})
}

func (s *Server) handleConnectionApp(w http.ResponseWriter, r *http.Request) {
	if !connectionReader(w, r) || !noQuery(w, r) || !s.connectionsOn(w) {
		return
	}
	id, ok := pathName(w, r, "app")
	if !ok {
		return
	}
	app, e := s.ConnectionService.App(r.Context(), id)
	if e != nil {
		connectionError(w, e)
		return
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "connectionApp": app})
}

func (s *Server) handleConnectionAppRemove(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.connectionCaller(w, r)
	if !ok || !noQuery(w, r) || !s.connectionsOn(w) {
		return
	}
	id, ok := pathName(w, r, "app")
	if !ok {
		return
	}
	left, e := s.ConnectionService.RemoveApp(r.Context(), caller, id)
	if e != nil {
		connectionError(w, e)
		return
	}
	leftAnswer(w, left)
}

func checkConnectionRedirectURIs(consumer string, uris []string) error {
	if len(uris) > 10 {
		return fmt.Errorf("operator consumer %s has more than 10 connectionRedirectUris", consumer)
	}
	seen := map[string]bool{}
	for _, u := range uris {
		if !connection.ValidRedirectURI(u) || seen[u] {
			return fmt.Errorf("operator consumer %s has a duplicated or non-HTTPS value in connectionRedirectUris", consumer)
		}
		seen[u] = true
	}
	return nil
}

func (s *Server) handleGrant(r *http.Request, forge string, grant provider.GrantEvent) {
	s.Log.Info("forge grant changed", "provider", forge, "event", grant.Event, "action", grant.Action, "installation", grant.InstallationID)
	if svc := s.targetService(); svc != nil {
		if err := svc.HandleGrant(r.Context(), grant); err != nil {
			s.Log.Error("forge grant not applied to runtime targets; the hourly reconcile catches it", "provider", forge, "installation", grant.InstallationID, "err", err)
		}
	}
	if s.ConnectionService == nil {
		return
	}
	if err := s.ConnectionService.HandleGrant(r.Context(), grant); err != nil {
		s.Log.Error("forge grant not applied to connections", "provider", forge, "installation", grant.InstallationID, "err", err)
	}
}
