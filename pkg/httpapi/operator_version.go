package httpapi

import (
	"net/http"

	"github.com/ploeg-hq/ploeg/pkg/contracts"
)

const (
	unknownPloegVersion = "unknown"
	operatorPrefix      = "/api/v1/operator/"
)

type versionResponse struct {
	SchemaVersion string               `json:"schemaVersion"`
	Ploeg         string               `json:"ploeg"`
	Contracts     []contracts.Contract `json:"contracts"`
}

func (s *Server) registerOperatorVersion(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/operator/version", s.handleOperatorVersion)
}

func (s *Server) handleOperatorVersion(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		operatorError(w, 400, "invalid_request", "Version does not accept query parameters.")
		return
	}
	release := s.Version
	if release == "" {
		release = unknownPloegVersion
	}
	operatorJSON(w, 200, versionResponse{SchemaVersion: "1.0", Ploeg: release, Contracts: contracts.Served()})
}

// ServesRoute reports whether ploegd routes a request with this method to this
// concrete path, without running a handler. A consumer's recorded
// expectations are checked against it (ADR-0147).
func (s *Server) ServesRoute(method, path string) bool {
	r, err := http.NewRequest(method, path, nil)
	if err != nil {
		return false
	}
	_, pattern := s.routes().Handler(r)
	if pattern == operatorPrefix {
		_, pattern = s.operatorRoutes().Handler(r)
	}
	return pattern != ""
}
