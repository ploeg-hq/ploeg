package httpapi

import (
	"net/http"
	"time"
)

func (s *Server) handleOperatorRunAccounting(w http.ResponseWriter, r *http.Request) {
	if !operatorGET(w, r) {
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		operatorError(w, 400, "invalid_request", "Run accounting does not accept query parameters.")
		return
	}
	id, ok := operatorID(w, r)
	if !ok {
		return
	}
	principal, _ := OperatorPrincipalFromContext(r.Context())
	accounting, err := s.Store.OperatorRunAccounting(r.Context(), id, principal.Teams)
	if err != nil {
		operatorReadError(w, err)
		return
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "generatedAt": time.Now().UTC().Truncate(time.Second), "accounting": accounting})
}
