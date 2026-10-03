package httpapi

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

const cardListLoginBytes = 256

func (s *Server) handleOperatorCards(w http.ResponseWriter, r *http.Request) {
	if !operatorGET(w, r) {
		return
	}
	filter, ok := cardListFilter(w, r)
	if !ok {
		return
	}
	page, err := s.Store.OperatorCards(r.Context(), filter,
		store.CardOptions{Bots: s.ForgeBots, ReleaseEnvironments: s.OperatorConfig.ReleaseEnvironments, Live: s.liveCardUsage(r.Context()),
			HotfixLabels: s.cardHotfixLabels(), Rarity: s.cardRarity(), Flow: s.cardFlow()})
	if err != nil {
		operatorReadError(w, err)
		return
	}
	for i := range page.Cards {
		page.Cards[i].Style = s.cardStyle(page.Cards[i].Target)
	}
	var next *string
	if page.NextBefore != nil {
		cursor := page.NextBefore.String()
		next = &cursor
	}
	operatorJSON(w, http.StatusOK, map[string]any{"schemaVersion": 1, "cards": page.Cards, "nextBefore": next})
}

func cardListFilter(w http.ResponseWriter, r *http.Request) (store.CardListFilter, bool) {
	principal, _ := OperatorPrincipalFromContext(r.Context())
	f := store.CardListFilter{Teams: principal.Teams, Limit: store.DefaultCardListLimit}
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
	if len(members) == 0 || len(members) > store.CardListMembers {
		operatorError(w, 400, "invalid_request", "Name between 1 and 20 member logins.")
		return f, false
	}
	for _, m := range members {
		if !validCardListLogin(m) {
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
		cursor, err := store.ParseCardCursor(before)
		if err != nil {
			operatorError(w, 400, "invalid_request", "Invalid cursor.")
			return f, false
		}
		f.Before = &cursor
	}
	if limit := q.Get("limit"); limit != "" {
		f.Limit, err = strconv.Atoi(limit)
		if err != nil || f.Limit < 1 || f.Limit > store.CardListLimit {
			operatorError(w, 400, "invalid_request", "Limit must be between 1 and 50.")
			return f, false
		}
	}
	return f, true
}

func validCardListLogin(login string) bool {
	return len(login) <= cardListLoginBytes && utf8.ValidString(login) && strings.TrimSpace(login) == login &&
		!strings.ContainsFunc(login, unicode.IsControl)
}
