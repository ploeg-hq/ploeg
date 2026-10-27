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
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/ploeg-hq/ploeg/pkg/mcpauth"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

type OperatorPrincipal struct {
	Name           string
	Teams          []string
	CanExecute     bool
	CanVerify      bool
	CanReadContent bool
	BudgetLimitUSD float64
}

// AllTeams is the team scope that grants an Operator Consumer every Team. A
// consumer that names no team reads none.
const AllTeams = "*"

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
	clientIDs []string
	audiences []string
}

type OperatorConfig struct {
	Consumers        []OperatorConsumer
	PersonTokens     *PersonTokens
	Teams            map[string][]string
	TeamAssignees    map[string][]string
	TeamScopes       map[string][]string
	DeliveryPolicies map[string]DeliveryPolicy
	TeamHarnesses    TeamHarnesses
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
		ReadContent  *bool    `json:"readContent"`
		MaxBudgetUSD *float64 `json:"maxBudgetUsd"`
		ClientIDs    []string `json:"clientIds"`
		Audiences    []string `json:"audiences"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&configs); err != nil {
		return nil, errors.New("operator consumers must be a JSON array of name, tokenEnv, teams, execute, verify, readContent, maxBudgetUsd, clientIds and audiences")
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
		teams, err := consumerTeamScope(cfg.Name, cfg.Teams)
		if err != nil {
			return nil, err
		}
		readContent := cfg.Execute
		if cfg.ReadContent != nil {
			readContent = *cfg.ReadContent
		}
		token, ok := lookup(cfg.TokenEnv)
		if !ok || len(token) < 32 || len(token) > 4096 || strings.ContainsAny(token, " \t\r\n") {
			return nil, fmt.Errorf("operator consumer %s requires a token of 32 to 4096 non-whitespace bytes in its configured environment reference", cfg.Name)
		}
		hash := sha256.Sum256([]byte(token))
		if hashes[hash] {
			return nil, errors.New("operator consumers must not share a bearer credential")
		}
		for field, values := range map[string][]string{"clientIds": cfg.ClientIDs, "audiences": cfg.Audiences} {
			if len(values) > 20 {
				return nil, fmt.Errorf("operator consumer %s has more than 20 %s", cfg.Name, field)
			}
			seen := map[string]bool{}
			for _, id := range values {
				if id == "" || len(id) > 256 || seen[id] || strings.ContainsFunc(id, func(r rune) bool { return r <= 0x20 || r >= 0x7f }) {
					return nil, fmt.Errorf("operator consumer %s has an empty, duplicated or non-printable value in %s", cfg.Name, field)
				}
				seen[id] = true
			}
		}
		consumers = append(consumers, OperatorConsumer{Principal: OperatorPrincipal{Name: cfg.Name, Teams: teams, CanExecute: cfg.Execute, CanVerify: cfg.Verify, CanReadContent: readContent, BudgetLimitUSD: budgetLimit}, tokenHash: hash, clientIDs: cfg.ClientIDs, audiences: cfg.Audiences})
		names[cfg.Name], hashes[hash] = true, true
	}
	return consumers, nil
}

func consumerTeamScope(name string, configured []string) ([]string, error) {
	if slices.Equal(configured, []string{AllTeams}) {
		return nil, nil
	}
	teams := []string{}
	seen := map[string]bool{}
	for _, team := range configured {
		if !operatorName.MatchString(team) || seen[team] {
			return nil, fmt.Errorf("operator consumer %s has an invalid or duplicated team scope; %q grants every Team only on its own", name, AllTeams)
		}
		seen[team] = true
		teams = append(teams, team)
	}
	return teams, nil
}

// OperatorConsumersNameClientIDs reports whether any consumer accepts person
// tokens through clientIds or audiences, so a deployment that names either
// without an issuer is refused.
func OperatorConsumersNameClientIDs(consumers []OperatorConsumer) bool {
	for _, c := range consumers {
		if len(c.clientIDs) > 0 || len(c.audiences) > 0 {
			return true
		}
	}
	return false
}

func (s *Server) operatorAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		principal, proven, refusal := s.operatorCaller(r)
		if refusal != nil {
			if refusal.status == http.StatusUnauthorized {
				challenge := `Bearer realm="ploeg-operator"`
				if refusal.code == "invalid_token" {
					challenge += `, error="invalid_token"`
				}
				w.Header().Set("WWW-Authenticate", challenge)
			}
			operatorError(w, refusal.status, refusal.code, refusal.message)
			return
		}
		if proven != nil {
			for _, name := range []string{"X-Ploeg-Actor", "X-Ploeg-Acting-User"} {
				for _, named := range r.Header.Values(name) {
					if named != proven.ID {
						operatorError(w, http.StatusForbidden, "actor_conflict", name+" names someone other than the person the person token proves.")
						return
					}
				}
			}
			r = r.Clone(context.WithValue(r.Context(), provenPersonKey{}, *proven))
			r.Header.Set("X-Ploeg-Actor", proven.ID)
			r.Header.Del("X-Ploeg-Acting-User")
		}
		if len(r.URL.RawQuery) > 8192 {
			operatorError(w, http.StatusRequestURITooLong, "invalid_request", "Operator query exceeds 8 KiB.")
			return
		}
		timeout := 5 * time.Second
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/operator/executions/") && strings.HasSuffix(r.URL.Path, "/credential") {
			timeout = 30 * time.Second
		}
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/operator/work-items/") && strings.HasSuffix(r.URL.Path, "/asks") {
			timeout = 30 * time.Second
		}
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/operator/teams/") && strings.HasSuffix(r.URL.Path, "/chats") {
			timeout = 30 * time.Second
		}
		if isContextUpload(r) {
			timeout = 60 * time.Second
		}
		ctx := context.WithValue(r.Context(), operatorPrincipalKey{}, *principal)
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			attribution, bad := operatorAttribution(r, principal.Name)
			if bad != nil {
				operatorError(w, http.StatusBadRequest, bad.code, bad.message)
				return
			}
			if attribution.Actor != "" {
				ctx = store.WithAttribution(ctx, attribution)
			}
		}
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type callerRefusal struct {
	status        int
	code, message string
}

// operatorCaller authenticates the Operator Consumer of r (ADR-0150). The
// bearer is the consumer's static credential, or a person token that belongs
// to exactly one consumer: the one whose audiences its aud names, or else the
// one whose clientIds name its azp. A person token may instead ride in
// X-Ploeg-Person-Token next to the static credential; it must then belong to
// that consumer. A presented person token that does not verify refuses the
// request.
func (s *Server) operatorCaller(r *http.Request) (*OperatorPrincipal, *ProvenPerson, *callerRefusal) {
	unauthorized := &callerRefusal{http.StatusUnauthorized, "unauthorized", "A configured operator bearer credential is required."}
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return nil, nil, unauthorized
	}
	scheme, credential, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || len(credential) < 32 || len(credential) > maxPersonTokenBytes || strings.ContainsAny(credential, " \t\r\n,") {
		return nil, nil, unauthorized
	}
	extra := r.Header.Values(PersonTokenHeader)
	if len(extra) > 1 {
		return nil, nil, &callerRefusal{http.StatusUnauthorized, "invalid_token", "Send at most one " + PersonTokenHeader + "."}
	}
	var consumer *OperatorConsumer
	if len(credential) <= 4096 {
		hash := sha256.Sum256([]byte(credential))
		for i := range s.OperatorConfig.Consumers {
			if subtle.ConstantTimeCompare(hash[:], s.OperatorConfig.Consumers[i].tokenHash[:]) == 1 {
				consumer = &s.OperatorConfig.Consumers[i]
			}
		}
	}
	personToken := ""
	switch {
	case consumer != nil && len(extra) == 1:
		personToken = extra[0]
	case consumer == nil && len(extra) == 0 && s.OperatorConfig.PersonTokens != nil && looksLikeJWT(credential):
		personToken = credential
	case consumer == nil:
		return nil, nil, unauthorized
	}
	if personToken == "" {
		principal := consumer.Principal
		return &principal, nil, nil
	}
	if s.OperatorConfig.PersonTokens == nil {
		return nil, nil, &callerRefusal{http.StatusUnauthorized, "invalid_token", "This deployment accepts no person tokens."}
	}
	if !looksLikeJWT(personToken) {
		return nil, nil, &callerRefusal{http.StatusUnauthorized, "invalid_token", "The person token was refused: it is not a JWT."}
	}
	proven, err := s.OperatorConfig.PersonTokens.verify(r.Context(), personToken)
	if errors.Is(err, mcpauth.ErrKeysUnavailable) {
		return nil, nil, &callerRefusal{http.StatusServiceUnavailable, "unavailable", "The identity provider's signing keys are unavailable; try again shortly."}
	}
	if err != nil {
		reason := strings.TrimPrefix(err.Error(), auth.ErrInvalidToken.Error()+": ")
		return nil, nil, &callerRefusal{http.StatusUnauthorized, "invalid_token", "The person token was refused: " + reason + "."}
	}
	consumer, refusal := s.personTokenOwner(proven, consumer)
	if refusal != "" {
		return nil, nil, &callerRefusal{http.StatusUnauthorized, "invalid_token", refusal}
	}
	principal := consumer.Principal
	return &principal, &proven, nil
}

// personTokenOwner finds the consumer a verified person token belongs to.
// caller is the consumer whose static credential came with the token, or nil
// when the token is the bearer.
func (s *Server) personTokenOwner(proven ProvenPerson, caller *OperatorConsumer) (*OperatorConsumer, string) {
	var owner *OperatorConsumer
	for i := range s.OperatorConfig.Consumers {
		c := &s.OperatorConfig.Consumers[i]
		if slices.ContainsFunc(c.audiences, func(aud string) bool { return slices.Contains(proven.audiences, aud) }) {
			if owner != nil {
				return nil, "The person token's audience names more than one consumer."
			}
			owner = c
		}
	}
	if owner != nil {
		if caller != nil && caller != owner {
			return nil, "The person token was issued for another consumer than the credential it came with."
		}
		if !slices.Contains(owner.audiences, proven.Client) && !slices.Contains(owner.clientIDs, proven.Client) {
			return nil, "The person token was issued to a client its consumer does not name."
		}
		return owner, ""
	}
	if s.OperatorConfig.PersonTokens.audience == "" || !slices.Contains(proven.audiences, s.OperatorConfig.PersonTokens.audience) {
		return nil, "The person token is meant for an audience no consumer names."
	}
	if caller != nil {
		if !slices.Contains(caller.clientIDs, proven.Client) {
			return nil, "The person token was issued to a client this consumer does not name."
		}
		return caller, ""
	}
	for i := range s.OperatorConfig.Consumers {
		if slices.Contains(s.OperatorConfig.Consumers[i].clientIDs, proven.Client) {
			if owner != nil {
				return nil, "The person token's client is named by more than one consumer; send the consumer's own credential with the person token in " + PersonTokenHeader + "."
			}
			owner = &s.OperatorConfig.Consumers[i]
		}
	}
	if owner == nil {
		return nil, "The person token was issued to a client no consumer names."
	}
	return owner, ""
}

func contentReader(w http.ResponseWriter, r *http.Request) (OperatorPrincipal, bool) {
	p, ok := OperatorPrincipalFromContext(r.Context())
	if !ok || !p.CanReadContent {
		operatorError(w, 403, "content_forbidden", "This consumer reads Work Item metadata only; transcripts, briefings, context and notes need readContent.")
		return p, false
	}
	return p, true
}

func (s *Server) operatorHandler() http.Handler {
	return s.operatorAuth(s.operatorRoutes())
}

func (s *Server) operatorRoutes() *http.ServeMux {
	mux := http.NewServeMux()
	s.registerOperatorVersion(mux)
	mux.HandleFunc("GET /api/v1/operator/teams", s.handleOperatorTeams)
	mux.HandleFunc("GET /api/v1/operator/work-items", s.handleOperatorItems)
	mux.HandleFunc("GET /api/v1/operator/work-items/lookup", s.handleOperatorSourceLookup)
	mux.HandleFunc("GET /api/v1/operator/work-items/{id}", s.handleOperatorItem)
	mux.HandleFunc("POST /api/v1/operator/work-items/{id}/cancel", s.handleOperatorCancel)
	mux.HandleFunc("POST /api/v1/operator/work-items/{id}/requeue", s.handleOperatorRequeue)
	mux.HandleFunc("POST /api/v1/operator/work-items/{id}/notes", s.handleOperatorNote)
	mux.HandleFunc("POST /api/v1/operator/work-items/{id}/take-over", s.handleOperatorTakeOver)
	mux.HandleFunc("POST /api/v1/operator/work-items/{id}/claim", s.handleClientClaim)
	mux.HandleFunc("POST /api/v1/operator/work-items/{id}/questions/{question}/answer", s.handleOperatorQuestionAnswer)
	mux.HandleFunc("GET /api/v1/operator/summary", s.handleOperatorSummary)
	mux.HandleFunc("GET /api/v1/operator/unsettled-accounts", s.handleOperatorUnsettledAccounts)
	mux.HandleFunc("POST /api/v1/operator/unsettled-accounts/{id}/settle", s.handleOperatorSettleFromEvidence)
	mux.HandleFunc("GET /api/v1/operator/runs", s.handleOperatorRuns)
	mux.HandleFunc("GET /api/v1/operator/runs/{id}", s.handleOperatorRun)
	mux.HandleFunc("GET /api/v1/operator/runs/{id}/accounting", s.handleOperatorRunAccounting)
	mux.HandleFunc("GET /api/v1/operator/runs/{id}/transcript", s.handleOperatorRunTranscript)
	mux.HandleFunc("GET /api/v1/operator/runs/{id}/briefing", s.handleOperatorRunBriefing)
	mux.HandleFunc("GET /api/v1/operator/events", s.handleOperatorEvents)
	mux.HandleFunc("GET /api/v1/operator/route-refusals", s.handleOperatorRouteRefusals)
	mux.HandleFunc("GET /api/v1/operator/connections", s.handleOperatorConnections)
	mux.HandleFunc("GET /api/v1/operator/allowances", s.handleOperatorAllowance)
	s.registerOperatorExecution(mux)
	s.registerOperatorWorkAdmission(mux)
	s.registerOperatorAsk(mux)
	s.registerOperatorChat(mux)
	s.registerOperatorDelivery(mux)
	s.registerOperatorProposed(mux)
	s.registerContext(mux)
	s.registerFacts(mux)
	s.registerOperatorKnowledge(mux)
	return mux
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
	if err := s.describeRoles(r.Context(), teams); err != nil {
		operatorReadError(w, err)
		return
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
	for i := range items {
		s.linkTask(&items[i])
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
	s.linkTask(&detail.Item)
	detail.Resume = s.resumeOffer(r.Context(), id)
	detail.TakeOver = s.takeOverDetail(r.Context(), id)
	operatorJSON(w, 200, struct {
		SchemaVersion string `json:"schemaVersion"`
		store.OperatorDetail
	}{SchemaVersion: "1.0", OperatorDetail: detail})
}

func (s *Server) linkTask(item *store.OperatorItem) {
	if item.URL == "" {
		item.URL = s.taskURL(item.Provider, item.ExternalID)
	}
}

func (s *Server) taskURL(providerName, externalID string) string {
	linker, ok := s.Trackers[providerName].(provider.TaskLinker)
	if !ok {
		return ""
	}
	return linker.TaskURL(externalID)
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
	allowed := map[string]bool{"team": true, "after": true, "limit": true, "order": true, "before": true}
	if events {
		allowed["workItemId"], allowed["actor"] = true, true
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
		operatorError(w, 400, "invalid_request", "after pages ascending and before pages descending.")
		return f, false
	}
	if limit := q.Get("limit"); limit != "" {
		f.Limit, err = strconv.Atoi(limit)
		if err != nil || f.Limit < 1 || f.Limit > 200 {
			operatorError(w, 400, "invalid_request", "Limit must be between 1 and 200.")
			return f, false
		}
	}
	if state := q.Get("state"); state != "" {
		seen := map[string]bool{}
		spelled := map[string]bool{}
		for _, one := range strings.Split(state, ",") {
			parsed, ok := work.ParseState(one)
			if !ok {
				operatorError(w, 400, "invalid_request", "Unknown work-item state.")
				return f, false
			}
			if spelled[one] {
				operatorError(w, 400, "invalid_request", "Repeated work-item state.")
				return f, false
			}
			spelled[one] = true
			if !seen[string(parsed)] {
				seen[string(parsed)] = true
				f.States = append(f.States, string(parsed))
			}
		}
	}
	if needs := q.Get("needsHuman"); needs != "" {
		if needs != "true" && needs != "false" {
			operatorError(w, 400, "invalid_request", "needsHuman must be true or false.")
			return f, false
		}
		f.NeedsHuman = needs == "true"
	}
	if f.NeedsHuman && len(f.States) > 0 && (len(f.States) != 1 || f.States[0] != "needs_human") {
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
	if actor := q.Get("actor"); actor != "" {
		if !operatorName.MatchString(actor) {
			operatorError(w, 400, "invalid_request", "actor is an X-Ploeg-Actor or X-Ploeg-Acting-User identity.")
			return f, false
		}
		f.Actor = actor
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
