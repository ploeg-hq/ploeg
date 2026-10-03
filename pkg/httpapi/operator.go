package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/playkpi"
	"github.com/ploeg-hq/ploeg/pkg/rarity"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

type OperatorPrincipal struct {
	Name           string
	Teams          []string
	CanExecute     bool
	CanVerify      bool
	BudgetLimitUSD float64
}

func (p OperatorPrincipal) AllowsTeam(team string) bool {
	if p.Teams == nil {
		return true
	}
	for _, allowed := range p.Teams {
		if allowed == team {
			return true
		}
	}
	return false
}

type OperatorConsumer struct {
	Principal OperatorPrincipal
	tokenHash [sha256.Size]byte
}

type OperatorConfig struct {
	Consumers        []OperatorConsumer
	Teams            map[string][]string
	TeamAssignees    map[string][]string
	TeamScopes       map[string][]string
	DeliveryPolicies map[string]DeliveryPolicy
	// CardStyles is the cardStyle of each configured Work Target, keyed by
	// lowercased "owner/name". A Work Target absent here gets
	// store.DefaultCardSkin and no theme.
	CardStyles map[string]store.CardStyle
	// ReleaseEnvironments is the release environment of each configured
	// Work Target that names one, keyed by lowercased "owner/name"
	// (ADR-0047). A Work Target absent here releases in production.
	ReleaseEnvironments map[string]string
	// RarityMatchers holds the rarity path rules of each configured Work
	// Target that sets them, keyed by lowercased "owner/name" (ADR-0056).
	// A Work Target absent here uses the defaults.
	RarityMatchers map[string]rarity.Matcher
	// ShapeMatchers holds the test and documentation path rules of each
	// configured Work Target that sets them, keyed by lowercased
	// "owner/name" (ADR-0058). A Work Target absent here uses the defaults.
	ShapeMatchers map[string]playkpi.Matcher
}

type operatorPrincipalKey struct{}

func OperatorPrincipalFromContext(ctx context.Context) (OperatorPrincipal, bool) {
	p, ok := ctx.Value(operatorPrincipalKey{}).(OperatorPrincipal)
	return p, ok
}

var operatorName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
var operatorEnv = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

func ParseOperatorConsumers(raw string, lookup func(string) (string, bool)) ([]OperatorConsumer, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if len(raw) > 65536 {
		return nil, errors.New("operator consumer configuration exceeds 64 KiB")
	}
	var configs []struct {
		Name         string   `json:"name"`
		TokenEnv     string   `json:"tokenEnv"`
		Teams        []string `json:"teams"`
		Execute      bool     `json:"execute"`
		Verify       bool     `json:"verify"`
		MaxBudgetUSD *float64 `json:"maxBudgetUsd"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&configs); err != nil {
		return nil, errors.New("operator consumers must be a JSON array of name, tokenEnv, teams and execute")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("operator consumers must contain one JSON value")
	}
	if len(configs) > 100 {
		return nil, errors.New("operator consumers exceeds 100 entries")
	}
	consumers := make([]OperatorConsumer, 0, len(configs))
	names := map[string]bool{}
	hashes := map[[sha256.Size]byte]bool{}
	for _, cfg := range configs {
		budgetLimit := 25.0
		if cfg.MaxBudgetUSD != nil {
			if !cfg.Execute || *cfg.MaxBudgetUSD < 0.01 || *cfg.MaxBudgetUSD > 10000 {
				return nil, errors.New("maxBudgetUsd requires execute permission and a value between 0.01 and 10000")
			}
			budgetLimit = *cfg.MaxBudgetUSD
		}
		if !operatorName.MatchString(cfg.Name) || names[cfg.Name] || !operatorEnv.MatchString(cfg.TokenEnv) {
			return nil, errors.New("operator consumer name or token environment reference is invalid or duplicated")
		}
		if len(cfg.Teams) > 100 {
			return nil, fmt.Errorf("operator consumer %s has too many team scopes", cfg.Name)
		}
		seen := map[string]bool{}
		for _, team := range cfg.Teams {
			if !operatorName.MatchString(team) || seen[team] {
				return nil, fmt.Errorf("operator consumer %s has an invalid or duplicated team scope", cfg.Name)
			}
			seen[team] = true
		}
		token, ok := lookup(cfg.TokenEnv)
		if !ok || len(token) < 32 || len(token) > 4096 || strings.ContainsAny(token, " \t\r\n") {
			return nil, fmt.Errorf("operator consumer %s requires a token of 32 to 4096 non-whitespace bytes in its configured environment reference", cfg.Name)
		}
		hash := sha256.Sum256([]byte(token))
		if hashes[hash] {
			return nil, errors.New("operator consumers must not share a bearer credential")
		}
		consumers = append(consumers, OperatorConsumer{Principal: OperatorPrincipal{Name: cfg.Name, Teams: cfg.Teams, CanExecute: cfg.Execute, CanVerify: cfg.Verify, BudgetLimitUSD: budgetLimit}, tokenHash: hash})
		names[cfg.Name], hashes[hash] = true, true
	}
	return consumers, nil
}

func (s *Server) operatorAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		values := r.Header.Values("Authorization")
		var token string
		if len(values) == 1 {
			scheme, credential, ok := strings.Cut(values[0], " ")
			if ok && strings.EqualFold(scheme, "Bearer") && len(credential) >= 32 && len(credential) <= 4096 && !strings.ContainsAny(credential, " \t\r\n,") {
				token = credential
			}
		}
		hash := sha256.Sum256([]byte(token))
		var principal *OperatorPrincipal
		for i := range s.OperatorConfig.Consumers {
			consumer := &s.OperatorConfig.Consumers[i]
			if subtle.ConstantTimeCompare(hash[:], consumer.tokenHash[:]) == 1 && token != "" {
				copy := consumer.Principal
				principal = &copy
			}
		}
		if principal == nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="ploeg-operator"`)
			operatorError(w, http.StatusUnauthorized, "unauthorized", "A configured operator bearer credential is required.")
			return
		}
		if len(r.URL.RawQuery) > 8192 {
			operatorError(w, http.StatusRequestURITooLong, "invalid_request", "Operator query exceeds 8 KiB.")
			return
		}
		timeout := 5 * time.Second
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/operator/executions/") && strings.HasSuffix(r.URL.Path, "/credential") {
			timeout = 30 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.WithValue(r.Context(), operatorPrincipalKey{}, *principal), timeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) operatorHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/operator/teams", s.handleOperatorTeams)
	mux.HandleFunc("GET /api/v1/operator/work-items", s.handleOperatorItems)
	mux.HandleFunc("GET /api/v1/operator/work-items/lookup", s.handleOperatorSourceLookup)
	mux.HandleFunc("GET /api/v1/operator/work-items/{id}", s.handleOperatorItem)
	mux.HandleFunc("GET /api/v1/operator/work-items/{id}/card", s.handleOperatorCard)
	mux.HandleFunc("GET /api/v1/operator/cards", s.handleOperatorCards)
	mux.HandleFunc("POST /api/v1/operator/work-items/{id}/cancel", s.handleOperatorCancel)
	mux.HandleFunc("GET /api/v1/operator/summary", s.handleOperatorSummary)
	mux.HandleFunc("GET /api/v1/operator/runs", s.handleOperatorRuns)
	mux.HandleFunc("GET /api/v1/operator/runs/{id}", s.handleOperatorRun)
	mux.HandleFunc("GET /api/v1/operator/events", s.handleOperatorEvents)
	mux.HandleFunc("GET /api/v1/operator/route-refusals", s.handleOperatorRouteRefusals)
	s.registerOperatorExecution(mux)
	s.registerOperatorDelivery(mux)
	s.registerOperatorProposed(mux)
	s.registerCracks(mux)
	return s.operatorAuth(mux)
}

func (s *Server) handleOperatorTeams(w http.ResponseWriter, r *http.Request) {
	if !operatorGET(w, r) {
		return
	}
	if r.URL.RawQuery != "" {
		operatorError(w, 400, "invalid_request", "Teams does not accept query parameters.")
		return
	}
	principal, _ := OperatorPrincipalFromContext(r.Context())
	teams, err := s.Store.OperatorTeams(r.Context(), principal.Teams, s.OperatorConfig.Teams)
	if err != nil {
		operatorReadError(w, err)
		return
	}
	for i := range teams {
		if assignees := s.OperatorConfig.TeamAssignees[teams[i].ID]; len(assignees) > 0 {
			teams[i].Assignees = append([]string{}, assignees...)
			sort.Strings(teams[i].Assignees)
		}
		if scopes := s.OperatorConfig.TeamScopes[teams[i].ID]; len(scopes) > 0 {
			teams[i].PinnedScopes = append([]string{}, scopes...)
			sort.Strings(teams[i].PinnedScopes)
		}
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "teams": teams})
}

func (s *Server) handleOperatorItems(w http.ResponseWriter, r *http.Request) {
	if !operatorGET(w, r) {
		return
	}
	filter, ok := operatorFilter(w, r, false)
	if !ok {
		return
	}
	items, more, err := s.Store.OperatorItems(r.Context(), filter)
	if err != nil {
		operatorReadError(w, err)
		return
	}
	var cursor *string
	if more {
		last := items[len(items)-1].ID
		cursor = &last
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "items": items, "nextCursor": cursor})
}

func (s *Server) handleOperatorItem(w http.ResponseWriter, r *http.Request) {
	if !operatorGET(w, r) {
		return
	}
	id, ok := operatorID(w, r)
	if !ok {
		return
	}
	principal, _ := OperatorPrincipalFromContext(r.Context())
	detail, err := s.Store.OperatorItem(r.Context(), id, principal.Teams)
	if err != nil {
		operatorReadError(w, err)
		return
	}
	operatorJSON(w, 200, struct {
		SchemaVersion string `json:"schemaVersion"`
		store.OperatorDetail
	}{SchemaVersion: "1.0", OperatorDetail: detail})
}

func (s *Server) handleOperatorRun(w http.ResponseWriter, r *http.Request) {
	if !operatorGET(w, r) {
		return
	}
	id, ok := operatorID(w, r)
	if !ok {
		return
	}
	principal, _ := OperatorPrincipalFromContext(r.Context())
	run, err := s.Store.OperatorRun(r.Context(), id, principal.Teams)
	if err != nil {
		operatorReadError(w, err)
		return
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "run": run})
}

func (s *Server) handleOperatorEvents(w http.ResponseWriter, r *http.Request) {
	if !operatorGET(w, r) {
		return
	}
	filter, ok := operatorFilter(w, r, true)
	if !ok {
		return
	}
	events, more, err := s.Store.OperatorEvents(r.Context(), filter)
	if err != nil {
		operatorReadError(w, err)
		return
	}
	last := strconv.FormatInt(filter.After, 10)
	if len(events) > 0 {
		last = events[len(events)-1].ID
	}
	var cursor *string
	if more {
		next := last
		cursor = &next
	}
	if filter.Desc {
		last = "0"
		if len(events) > 0 {
			last = events[0].ID
		}
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "events": events, "nextCursor": cursor, "lastCursor": last, "hasMore": more, "consistency": "snapshot"})
}

const (
	routeRefusalWindowDays = 14
	routeRefusalLimit      = 50
)

func (s *Server) handleOperatorRouteRefusals(w http.ResponseWriter, r *http.Request) {
	if !operatorGET(w, r) {
		return
	}
	if r.URL.RawQuery != "" {
		operatorError(w, 400, "invalid_request", "Route refusals does not accept query parameters.")
		return
	}
	principal, _ := OperatorPrincipalFromContext(r.Context())
	now := time.Now().UTC()
	refusals, err := s.Store.RecentRouteRefusals(r.Context(), principal.Teams, now.AddDate(0, 0, -routeRefusalWindowDays), routeRefusalLimit)
	if err != nil {
		operatorReadError(w, err)
		return
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "generatedAt": now, "windowDays": routeRefusalWindowDays, "refusals": refusals})
}

func operatorFilter(w http.ResponseWriter, r *http.Request, events bool) (store.OperatorFilter, bool) {
	principal, _ := OperatorPrincipalFromContext(r.Context())
	f := store.OperatorFilter{Teams: principal.Teams, Limit: 50}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		operatorError(w, 400, "invalid_request", "Malformed query parameters.")
		return f, false
	}
	allowed := map[string]bool{"team": true, "after": true, "limit": true}
	if events {
		allowed["workItemId"], allowed["order"], allowed["before"] = true, true, true
	} else {
		allowed["state"], allowed["needsHuman"] = true, true
		allowed["provider"], allowed["externalId"] = true, true
	}
	for key, values := range q {
		if !allowed[key] || len(values) != 1 || values[0] == "" {
			operatorError(w, 400, "invalid_request", "Unknown, empty or repeated query parameter.")
			return f, false
		}
	}
	f.Team = q.Get("team")
	if f.Team != "" && !operatorName.MatchString(f.Team) {
		operatorError(w, 400, "invalid_request", "Invalid team identifier.")
		return f, false
	}
	if f.Team != "" && !principal.AllowsTeam(f.Team) {
		operatorError(w, 403, "forbidden", "The consumer cannot read this team.")
		return f, false
	}
	if after := q.Get("after"); after != "" {
		f.After, err = store.OperatorCursor(after)
		if err != nil {
			operatorError(w, 400, "invalid_request", "Invalid cursor.")
			return f, false
		}
	}
	switch q.Get("order") {
	case "", "asc":
	case "desc":
		f.Desc = true
	default:
		operatorError(w, 400, "invalid_request", "order must be asc or desc.")
		return f, false
	}
	if before := q.Get("before"); before != "" {
		f.Before, err = store.OperatorCursor(before)
		if err != nil || f.Before == 0 {
			operatorError(w, 400, "invalid_request", "Invalid cursor.")
			return f, false
		}
	}
	if (f.Desc && q.Has("after")) || (!f.Desc && q.Has("before")) {
		operatorError(w, 400, "invalid_request", "after pages ascending events and before pages descending events.")
		return f, false
	}
	if limit := q.Get("limit"); limit != "" {
		f.Limit, err = strconv.Atoi(limit)
		if err != nil || f.Limit < 1 || f.Limit > 200 {
			operatorError(w, 400, "invalid_request", "Limit must be between 1 and 200.")
			return f, false
		}
	}
	f.State = q.Get("state")
	if f.State != "" {
		switch f.State {
		case "ingested", "proposed", "queued", "leased", "needs_human", "awaiting_review", "stale", "done", "withdrawn":
		default:
			operatorError(w, 400, "invalid_request", "Unknown work-item state.")
			return f, false
		}
	}
	if needs := q.Get("needsHuman"); needs != "" {
		if needs != "true" && needs != "false" {
			operatorError(w, 400, "invalid_request", "needsHuman must be true or false.")
			return f, false
		}
		f.NeedsHuman = needs == "true"
	}
	if f.NeedsHuman && f.State != "" && f.State != "needs_human" {
		operatorError(w, 400, "invalid_request", "Conflicting state and needsHuman filters.")
		return f, false
	}
	if q.Has("provider") || q.Has("externalId") {
		f.Provider, f.ExternalID = q.Get("provider"), q.Get("externalId")
		if !operatorName.MatchString(f.Provider) || !operatorName.MatchString(f.ExternalID) {
			operatorError(w, 400, "invalid_request", "provider and externalId must be given together as valid tracker identifiers.")
			return f, false
		}
	}
	if item := q.Get("workItemId"); item != "" {
		f.WorkItemID, err = store.OperatorCursor(item)
		if err != nil || f.WorkItemID == 0 {
			operatorError(w, 400, "invalid_request", "Invalid work-item identifier.")
			return f, false
		}
	}
	return f, true
}

func operatorID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	if r.URL.RawQuery != "" {
		operatorError(w, 400, "invalid_request", "This resource does not accept query parameters.")
		return 0, false
	}
	id, err := store.OperatorCursor(r.PathValue("id"))
	if err != nil || id == 0 {
		operatorError(w, 400, "invalid_request", "Invalid resource identifier.")
		return 0, false
	}
	return id, true
}

func operatorGET(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet {
		return true
	}
	w.Header().Set("Allow", "GET")
	operatorError(w, 405, "method_not_allowed", "This operator resource is read-only.")
	return false
}

func operatorReadError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrOperatorNotFound) {
		operatorError(w, 404, "not_found", "The resource was not found in the consumer's scope.")
		return
	}
	operatorError(w, 503, "unavailable", "Ploeg could not read its operator state.")
}

func operatorError(w http.ResponseWriter, status int, code, message string) {
	operatorJSON(w, status, map[string]any{"schemaVersion": "1.0", "error": map[string]string{"code": code, "message": message}})
}

func operatorJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil || len(body) > 16*1024*1024 {
		status = 503
		body = []byte(`{"schemaVersion":"1.0","error":{"code":"unavailable","message":"Operator response exceeded its bounded representation."}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
