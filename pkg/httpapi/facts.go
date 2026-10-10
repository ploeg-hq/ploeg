package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

const (
	factsLoginBytes  = 256
	factsLiveTimeout = 3 * time.Second
)

func (s *Server) registerFacts(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/operator/work-items/{id}/facts", s.handleWorkItemFacts)
	mux.HandleFunc("GET /api/v1/operator/facts", s.handleFactsList)
	mux.HandleFunc("PUT /api/v1/operator/work-items/{id}/pull-request-comments/{key}", s.handlePutPullRequestComment)
	mux.HandleFunc("DELETE /api/v1/operator/work-items/{id}/pull-request-comments/{key}", s.handleDeletePullRequestComment)
	mux.HandleFunc("GET /api/v1/operator/card-legacy-export", s.handleLegacyExport)
}

func (s *Server) factsOptions(ctx context.Context) store.FactsOptions {
	return store.FactsOptions{Bots: s.ForgeBots, Live: s.liveRunUsage(ctx), TaskURL: s.taskURL}
}

func (s *Server) liveRunUsage(ctx context.Context) func(context.Context, string) (store.LiveUsage, error) {
	if s.LLMControl == nil {
		return nil
	}
	deadline := time.Now().Add(factsLiveTimeout)
	return func(_ context.Context, runToken string) (store.LiveUsage, error) {
		readCtx, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		return s.LLMControl.Live(readCtx, runToken)
	}
}

func (s *Server) handleWorkItemFacts(w http.ResponseWriter, r *http.Request) {
	if !operatorGET(w, r) {
		return
	}
	id, ok := operatorID(w, r)
	if !ok {
		return
	}
	principal, _ := OperatorPrincipalFromContext(r.Context())
	facts, err := s.Store.WorkItemFacts(r.Context(), id, principal.Teams, s.factsOptions(r.Context()))
	if err != nil {
		operatorReadError(w, err)
		return
	}
	operatorJSON(w, http.StatusOK, map[string]any{"schemaVersion": "1.0", "facts": facts})
}

func (s *Server) handleFactsList(w http.ResponseWriter, r *http.Request) {
	if !operatorGET(w, r) {
		return
	}
	filter, ok := factsListFilter(w, r)
	if !ok {
		return
	}
	page, err := s.Store.WorkItemFactsList(r.Context(), filter, s.factsOptions(r.Context()))
	if err != nil {
		operatorReadError(w, err)
		return
	}
	var next *string
	if page.NextBefore != nil {
		cursor := page.NextBefore.String()
		next = &cursor
	}
	operatorJSON(w, http.StatusOK, map[string]any{"schemaVersion": "1.0", "facts": page.Facts, "nextBefore": next})
}

func factsListFilter(w http.ResponseWriter, r *http.Request) (store.FactsListFilter, bool) {
	principal, _ := OperatorPrincipalFromContext(r.Context())
	f := store.FactsListFilter{Teams: principal.Teams, Limit: store.DefaultFactsListLimit}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		operatorError(w, 400, "invalid_request", "Malformed query parameters.")
		return f, false
	}
	allowed := map[string]bool{"member": true, "since": true, "team": true, "before": true, "limit": true}
	for key, values := range q {
		if !allowed[key] || (key != "member" && len(values) != 1) {
			operatorError(w, 400, "invalid_request", "Unknown or repeated query parameter.")
			return f, false
		}
		for _, v := range values {
			if v == "" {
				operatorError(w, 400, "invalid_request", "Empty query parameter.")
				return f, false
			}
		}
	}
	members := q["member"]
	if len(members) > store.FactsListMembers {
		operatorError(w, 400, "invalid_request", "Name at most 20 member logins.")
		return f, false
	}
	for _, m := range members {
		if !validFactsLogin(m) {
			operatorError(w, 400, "invalid_request", "Invalid member login.")
			return f, false
		}
	}
	f.Members = members
	f.Team = q.Get("team")
	if f.Team != "" && !operatorName.MatchString(f.Team) {
		operatorError(w, 400, "invalid_request", "Invalid team identifier.")
		return f, false
	}
	if f.Team != "" && !principal.AllowsTeam(f.Team) {
		operatorError(w, 403, "forbidden", "The consumer cannot read this team.")
		return f, false
	}
	if since := q.Get("since"); since != "" {
		at, err := time.Parse(time.RFC3339, since)
		if err != nil {
			operatorError(w, 400, "invalid_request", "since must be an RFC 3339 timestamp.")
			return f, false
		}
		f.Since = &at
	}
	if before := q.Get("before"); before != "" {
		cursor, err := store.ParseFactsCursor(before)
		if err != nil {
			operatorError(w, 400, "invalid_request", "Invalid cursor.")
			return f, false
		}
		f.Before = &cursor
	}
	if limit := q.Get("limit"); limit != "" {
		f.Limit, err = strconv.Atoi(limit)
		if err != nil || f.Limit < 1 || f.Limit > store.FactsListLimit {
			operatorError(w, 400, "invalid_request", "Limit must be between 1 and 25.")
			return f, false
		}
	}
	return f, true
}

func validFactsLogin(login string) bool {
	return len(login) <= factsLoginBytes && utf8.ValidString(login) && strings.TrimSpace(login) == login &&
		!strings.ContainsFunc(login, unicode.IsControl)
}

func (s *Server) handleLegacyExport(w http.ResponseWriter, r *http.Request) {
	if !operatorGET(w, r) {
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		operatorError(w, 400, "invalid_request", "Malformed query parameters.")
		return
	}
	for key, values := range q {
		if (key != "after" && key != "limit") || len(values) != 1 || values[0] == "" {
			operatorError(w, 400, "invalid_request", "Unknown, repeated or empty query parameter.")
			return
		}
	}
	after, err := store.OperatorCursor(q.Get("after"))
	if err != nil {
		operatorError(w, 400, "invalid_request", "after must be a Work Item id.")
		return
	}
	limit := store.DefaultLegacyExportLimit
	if raw := q.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > store.LegacyExportLimit {
			operatorError(w, 400, "invalid_request", "Limit must be between 1 and 200.")
			return
		}
	}
	principal, _ := OperatorPrincipalFromContext(r.Context())
	page, err := s.Store.LegacyExport(r.Context(), principal.Teams, after, limit)
	if err != nil {
		operatorReadError(w, err)
		return
	}
	w.Header().Set("Deprecation", "true")
	operatorJSON(w, http.StatusOK, map[string]any{"schemaVersion": "1.0", "deprecated": true, "items": page.Items, "nextAfter": page.NextAfter})
}
