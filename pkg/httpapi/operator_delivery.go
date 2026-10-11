package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

type DeliveryPolicy = store.DeliveryPolicy

var deliveryDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)
var deliveryGitObject = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)
var deliveryIdentity = regexp.MustCompile(`^[a-f0-9]{32}$`)
var deliveryBranch = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)

func ParseDeliveryPolicies(raw string) (map[string]DeliveryPolicy, error) {
	result := map[string]DeliveryPolicy{}
	if strings.TrimSpace(raw) == "" {
		return result, nil
	}
	if len(raw) > 128*1024 {
		return nil, fmt.Errorf("operator delivery policy configuration exceeds limit")
	}
	var policies []DeliveryPolicy
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policies); err != nil {
		return nil, fmt.Errorf("invalid operator delivery policy configuration")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("invalid operator delivery policy configuration")
	}
	for _, p := range policies {
		if !operatorName.MatchString(p.RepositoryID) || !deliveryDigest.MatchString(p.PolicySHA256) || !operatorName.MatchString(p.VerifierID) || p.MinTests < 1 || p.MinTests > 100000 || (p.PublisherLogin != "" && !operatorName.MatchString(p.PublisherLogin)) {
			return nil, fmt.Errorf("invalid operator delivery policy")
		}
		if _, exists := result[p.RepositoryID]; exists {
			return nil, fmt.Errorf("duplicate operator delivery repository policy")
		}
		result[p.RepositoryID] = p
	}
	return result, nil
}

func (s *Server) registerOperatorDelivery(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/operator/executions/{execution}/delivery", s.handleReadDelivery)
	mux.HandleFunc("POST /api/v1/operator/executions/{execution}/delivery/candidates", s.handleAdmitDeliveryCandidate)
	mux.HandleFunc("POST /api/v1/operator/executions/{execution}/delivery/verification", s.handleAdmitVerificationReceipt)
	mux.HandleFunc("POST /api/v1/operator/executions/{execution}/delivery/approval", s.handleApproveDeliveryCandidate)
	mux.HandleFunc("POST /api/v1/operator/executions/{execution}/delivery/publication", s.handleReservePublication)
	mux.HandleFunc("POST /api/v1/operator/executions/{execution}/delivery/publication/{operation}/status", s.handlePublicationStatus)
}

func (s *Server) deliveryForRequest(w http.ResponseWriter, r *http.Request, mode string) (store.OperatorExecution, store.DeliveryAccess, bool) {
	p, ok := OperatorPrincipalFromContext(r.Context())
	a := store.DeliveryAccess{Consumer: p.Name, Actor: r.Header.Get("X-Ploeg-Actor"), AuthenticatedBy: r.Header.Get("X-Ploeg-Acting-User"), Verifier: p.CanVerify}
	if !ok || (mode == "verify" && !p.CanVerify) || (mode == "owner" && !p.CanExecute) || (!p.CanExecute && !p.CanVerify) {
		operatorError(w, 403, "delivery_forbidden", "This consumer cannot perform this delivery operation.")
		return store.OperatorExecution{}, a, false
	}
	if len(r.Header.Values("X-Ploeg-Actor")) != 1 || !operatorName.MatchString(a.Actor) || len(r.Header.Values("X-Ploeg-Acting-User")) > 1 || (a.AuthenticatedBy != "" && !operatorName.MatchString(a.AuthenticatedBy)) {
		operatorError(w, 400, "actor_required", "Supply valid authenticated actor identities.")
		return store.OperatorExecution{}, a, false
	}
	if mode == "owner" {
		a.Verifier = false
	}
	if !deliveryIdentity.MatchString(r.PathValue("execution")) {
		operatorError(w, 404, "not_found", "Execution not found.")
		return store.OperatorExecution{}, a, false
	}
	e, err := s.Store.OperatorDeliveryExecution(r.Context(), r.PathValue("execution"), a)
	if err != nil {
		executionError(w, err)
		return e, a, false
	}
	if !p.AllowsTeam(e.Team) {
		operatorError(w, 404, "not_found", "Execution not found.")
		return e, a, false
	}
	return e, a, true
}

func (s *Server) deliveryPolicy(w http.ResponseWriter, repository string) (DeliveryPolicy, bool) {
	p, ok := s.OperatorConfig.DeliveryPolicies[repository]
	if !ok {
		operatorError(w, 409, "delivery_policy_missing", "A current registered delivery policy is required.")
	}
	return p, ok
}

func (s *Server) existingDeliveryPolicy(w http.ResponseWriter, r *http.Request, e store.OperatorExecution, a store.DeliveryAccess) (DeliveryPolicy, bool) {
	d, err := s.Store.OperatorDelivery(r.Context(), e.ID, a)
	if err != nil {
		executionError(w, err)
		return DeliveryPolicy{}, false
	}
	if d.Candidate == nil {
		operatorError(w, 409, "candidate_required", "An immutable delivery candidate is required.")
		return DeliveryPolicy{}, false
	}
	return s.deliveryPolicy(w, d.Candidate.RepositoryID)
}

func (s *Server) handleReadDelivery(w http.ResponseWriter, r *http.Request) {
	e, a, ok := s.deliveryForRequest(w, r, "read")
	if !ok {
		return
	}
	d, err := s.Store.OperatorDelivery(r.Context(), e.ID, a)
	if err != nil {
		executionError(w, err)
		return
	}
	repository, err := s.Store.DeliveryRepositoryID(r.Context(), e.ID)
	if err != nil {
		executionError(w, err)
		return
	}
	enabled := s.OperatorConfig.DeliveryPolicies[repository].PublicationEnabled && repository != ""
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "delivery": d, "lifecycle": "single-completed-execution", "publicationEnabled": enabled})
}

func (s *Server) handleAdmitDeliveryCandidate(w http.ResponseWriter, r *http.Request) {
	e, a, ok := s.deliveryForRequest(w, r, "verify")
	if !ok {
		return
	}
	var in store.AdmitDeliveryCandidate
	if !decodeExecution(w, r, &in) {
		return
	}
	parsed, err := url.Parse(in.RepositoryURL)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "https" && parsed.Scheme != "ssh") || len(in.RepositoryURL) > 2000 || !operatorName.MatchString(in.RepositoryID) || in.Generation < 1 || !deliveryGitObject.MatchString(in.BaseSHA) || !deliveryGitObject.MatchString(in.CanonicalSHA) || !deliveryGitObject.MatchString(in.TreeSHA) || !deliveryDigest.MatchString(in.ArtifactSHA256) || !deliveryDigest.MatchString(in.PolicySHA256) {
		operatorError(w, 400, "invalid_candidate", "Supply complete canonical candidate bindings.")
		return
	}
	p, ok := s.deliveryPolicy(w, in.RepositoryID)
	if !ok {
		return
	}
	c, created, err := s.Store.AdmitDeliveryCandidate(r.Context(), e.ID, a, p, in)
	if err != nil {
		executionError(w, err)
		return
	}
	status := 200
	if created {
		status = 201
	}
	operatorJSON(w, status, map[string]any{"schemaVersion": "1.0", "candidate": c, "created": created})
}

func (s *Server) handleAdmitVerificationReceipt(w http.ResponseWriter, r *http.Request) {
	e, a, ok := s.deliveryForRequest(w, r, "verify")
	if !ok {
		return
	}
	var in store.AdmitVerificationReceipt
	if !decodeExecution(w, r, &in) {
		return
	}
	if !deliveryIdentity.MatchString(in.CandidateID) || !deliveryDigest.MatchString(in.PolicySHA256) || !deliveryDigest.MatchString(in.ArtifactSHA256) || !deliveryDigest.MatchString(in.EvidenceSHA256) || !deliveryGitObject.MatchString(in.CanonicalSHA) || !deliveryGitObject.MatchString(in.TreeSHA) || !operatorName.MatchString(in.VerifierID) || in.TestCount < 0 || in.TestCount > 100000 {
		operatorError(w, 400, "invalid_receipt", "Supply complete verifier evidence bindings.")
		return
	}
	p, ok := s.existingDeliveryPolicy(w, r, e, a)
	if !ok {
		return
	}
	receipt, created, err := s.Store.AdmitVerificationReceipt(r.Context(), e.ID, a, p, in)
	if err != nil {
		executionError(w, err)
		return
	}
	status := 200
	if created {
		status = 201
	}
	operatorJSON(w, status, map[string]any{"schemaVersion": "1.0", "receipt": receipt, "created": created})
}

func (s *Server) handleApproveDeliveryCandidate(w http.ResponseWriter, r *http.Request) {
	e, a, ok := s.deliveryForRequest(w, r, "owner")
	if !ok {
		return
	}
	var in store.ApproveDeliveryCandidate
	if !decodeExecution(w, r, &in) {
		return
	}
	if !deliveryIdentity.MatchString(in.CandidateID) || !deliveryIdentity.MatchString(in.ReceiptID) || !deliveryDigest.MatchString(in.PolicySHA256) {
		operatorError(w, 400, "invalid_approval", "Bind approval to a candidate, receipt and policy.")
		return
	}
	p, ok := s.existingDeliveryPolicy(w, r, e, a)
	if !ok {
		return
	}
	approval, created, err := s.Store.ApproveDeliveryCandidate(r.Context(), e.ID, a, p, in)
	if err != nil {
		executionError(w, err)
		return
	}
	status := 200
	if created {
		status = 201
	}
	operatorJSON(w, status, map[string]any{"schemaVersion": "1.0", "approval": approval, "created": created})
}

func validDeliveryBranch(branch string) bool {
	if !deliveryBranch.MatchString(branch) || strings.Contains(branch, "..") || strings.Contains(branch, "//") || strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".") {
		return false
	}
	for _, part := range strings.Split(branch, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func (s *Server) handleReservePublication(w http.ResponseWriter, r *http.Request) {
	e, a, ok := s.deliveryForRequest(w, r, "owner")
	if !ok {
		return
	}
	var in store.ReservePublication
	if !decodeExecution(w, r, &in) {
		return
	}
	if !operatorName.MatchString(in.OperationID) || !deliveryIdentity.MatchString(in.CandidateID) || !deliveryIdentity.MatchString(in.ReceiptID) || !deliveryIdentity.MatchString(in.ApprovalID) || !deliveryDigest.MatchString(in.PolicySHA256) || !validDeliveryBranch(in.Branch) {
		operatorError(w, 400, "invalid_publication", "Bind publication to approved evidence and a safe branch.")
		return
	}
	p, ok := s.existingDeliveryPolicy(w, r, e, a)
	if !ok {
		return
	}
	operation, authorized, err := s.Store.ReservePublication(r.Context(), e.ID, a, p, in)
	if err != nil {
		executionError(w, err)
		return
	}
	status := 200
	if authorized {
		status = 201
	}
	operatorJSON(w, status, map[string]any{"schemaVersion": "1.0", "operation": operation, "effectAuthorized": authorized})
}

func (s *Server) handlePublicationStatus(w http.ResponseWriter, r *http.Request) {
	e, a, ok := s.deliveryForRequest(w, r, "verify")
	if !ok {
		return
	}
	var in store.PublicationStatus
	if !decodeExecution(w, r, &in) {
		return
	}
	if !operatorName.MatchString(r.PathValue("operation")) || !deliveryGitObject.MatchString(in.CanonicalSHA) || !validDeliveryBranch(in.Branch) || (in.State != "unknown" && in.State != "published") {
		operatorError(w, 400, "invalid_publication_status", "Use an exact operation identity and positive publication evidence or unknown state.")
		return
	}
	if in.State == "published" {
		d, err := s.Store.OperatorDelivery(r.Context(), e.ID, a)
		if err != nil {
			executionError(w, err)
			return
		}
		if d.Operation == nil {
			operatorError(w, 404, "not_found", "Publication operation not found.")
			return
		}
		remote, err := url.Parse(in.RemoteURL)
		repo, repoErr := url.Parse(d.Operation.RepositoryURL)
		if err != nil || repoErr != nil || remote.Scheme != "https" || remote.User != nil || remote.Host != repo.Host || remote.RawQuery != "" || remote.Fragment != "" || !strings.HasPrefix(remote.Path, strings.TrimSuffix(repo.Path, ".git")+"/") || !operatorName.MatchString(in.RemoteID) || len(in.RemoteURL) > 2000 {
			operatorError(w, 400, "invalid_publication_evidence", "Positive publication evidence must identify the registered repository.")
			return
		}
		if d.Operation.ID == r.PathValue("operation") && d.Operation.State != "published" {
			if d.Candidate == nil {
				operatorError(w, 409, "candidate_required", "An immutable delivery candidate is required.")
				return
			}
			policy, ok := s.deliveryPolicy(w, d.Candidate.RepositoryID)
			if !ok {
				return
			}
			if refusal := s.verifyPublication(r.Context(), *d.Operation, policy, remote); refusal != nil {
				refusal.write(w)
				return
			}
		}
	}
	operation, changed, err := s.Store.RecordPublicationStatus(r.Context(), e.ID, r.PathValue("operation"), a, in)
	if err != nil {
		executionError(w, err)
		return
	}
	if changed && operation.State == "published" {
		s.notifyPublished(r.Context(), e.WorkItemID, operation)
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "operation": operation})
}
