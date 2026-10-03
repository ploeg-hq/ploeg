package httpapi

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

var operatorWindows = map[string]time.Duration{"24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}

var operatorOutcome = regexp.MustCompile(`^[a-z][a-z_]{0,63}$`)

func (s *Server) handleOperatorSummary(w http.ResponseWriter, r *http.Request) {
	if !operatorGET(w, r) {
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		operatorError(w, 400, "invalid_request", "Malformed query parameters.")
		return
	}
	for key, values := range q {
		if key != "window" || len(values) != 1 {
			operatorError(w, 400, "invalid_request", "Unknown or repeated query parameter.")
			return
		}
	}
	window := "7d"
	if q.Has("window") {
		window = q.Get("window")
	}
	span, ok := operatorWindows[window]
	if !ok {
		operatorError(w, 400, "invalid_request", "window must be 24h, 7d or 30d.")
		return
	}
	principal, _ := OperatorPrincipalFromContext(r.Context())
	now := time.Now().UTC().Truncate(time.Second)
	teams, err := s.Store.OperatorSummary(r.Context(), principal.Teams, s.OperatorConfig.Teams, now.Add(-span))
	if err != nil {
		operatorReadError(w, err)
		return
	}
	operatorJSON(w, 200, map[string]any{
		"schemaVersion": "1.0", "generatedAt": now, "window": window,
		"teams": teams, "totals": store.OperatorSummaryTotal(teams),
	})
}

func (s *Server) handleOperatorRuns(w http.ResponseWriter, r *http.Request) {
	if !operatorGET(w, r) {
		return
	}
	principal, _ := OperatorPrincipalFromContext(r.Context())
	f := store.OperatorRunFilter{Teams: principal.Teams, Limit: 50}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		operatorError(w, 400, "invalid_request", "Malformed query parameters.")
		return
	}
	allowed := map[string]bool{"team": true, "state": true, "outcome": true, "limit": true, "before": true}
	for key, values := range q {
		if !allowed[key] || len(values) != 1 || values[0] == "" {
			operatorError(w, 400, "invalid_request", "Unknown, empty or repeated query parameter.")
			return
		}
	}
	f.Team = q.Get("team")
	if f.Team != "" && !operatorName.MatchString(f.Team) {
		operatorError(w, 400, "invalid_request", "Invalid team identifier.")
		return
	}
	if f.Team != "" && !principal.AllowsTeam(f.Team) {
		operatorError(w, 403, "forbidden", "The consumer cannot read this team.")
		return
	}
	f.State = q.Get("state")
	switch f.State {
	case "", "pending", "running", "finished":
	default:
		operatorError(w, 400, "invalid_request", "state must be pending, running or finished.")
		return
	}
	f.Outcome = q.Get("outcome")
	if f.Outcome != "" && !operatorOutcome.MatchString(f.Outcome) {
		operatorError(w, 400, "invalid_request", "Invalid outcome.")
		return
	}
	if f.Outcome != "" && f.State != "" && f.State != "finished" {
		operatorError(w, 400, "invalid_request", "Only finished Runs have an outcome.")
		return
	}
	if limit := q.Get("limit"); limit != "" {
		f.Limit, err = strconv.Atoi(limit)
		if err != nil || f.Limit < 1 || f.Limit > 200 {
			operatorError(w, 400, "invalid_request", "Limit must be between 1 and 200.")
			return
		}
	}
	if before := q.Get("before"); before != "" {
		f.Before, err = store.OperatorCursor(before)
		if err != nil || f.Before == 0 {
			operatorError(w, 400, "invalid_request", "Invalid cursor.")
			return
		}
	}
	runs, more, err := s.Store.OperatorRuns(r.Context(), f)
	if err != nil {
		operatorReadError(w, err)
		return
	}
	var next *string
	if more {
		last := runs[len(runs)-1].ID
		next = &last
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "runs": runs, "nextBefore": next})
}
