package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

const operatorExecutionTTL = 90 * time.Second

func (s *Server) ReconcileOperatorExecutions(ctx context.Context) error {
	executions, err := s.Store.ExpireOperatorExecutions(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, execution := range executions {
		if !execution.Demo && s.LLMControl != nil {
			if err := s.LLMControl.Block(ctx, execution.RunToken); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}

func (s *Server) registerOperatorExecution(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/operator/executions", s.handleAdmitExecution)
	mux.HandleFunc("GET /api/v1/operator/executions/{execution}", s.handleReadExecution)
	mux.HandleFunc("POST /api/v1/operator/executions/{execution}/commands", s.handleExecutionCommand)
	mux.HandleFunc("GET /api/v1/operator/executions/{execution}/events", s.handleExecutionEvents)
	mux.HandleFunc("POST /api/v1/operator/executions/{execution}/credential", s.handleExecutionCredential)
	mux.HandleFunc("POST /api/v1/operator/executions/{execution}/block", s.handleExecutionBlock)
	mux.HandleFunc("GET /api/v1/operator/executions/{execution}/spend", s.handleExecutionSpend)
}

func executionActor(w http.ResponseWriter, r *http.Request) (OperatorPrincipal, string, bool) {
	p, ok := OperatorPrincipalFromContext(r.Context())
	if !ok || !p.CanExecute {
		operatorError(w, 403, "execution_forbidden", "This consumer cannot control executions.")
		return p, "", false
	}
	actor := r.Header.Get("X-Ploeg-Actor")
	if len(r.Header.Values("X-Ploeg-Actor")) != 1 || !operatorName.MatchString(actor) {
		operatorError(w, 400, "actor_required", "An authenticated actor identity is required.")
		return p, "", false
	}
	acting := r.Header.Values("X-Ploeg-Acting-User")
	if len(acting) > 1 || (len(acting) == 1 && !operatorName.MatchString(acting[0])) {
		operatorError(w, 400, "invalid_acting_user", "Supply one valid authenticated acting identity.")
		return p, "", false
	}
	return p, actor, true
}

func decodeExecution(w http.ResponseWriter, r *http.Request, out any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		operatorError(w, 415, "json_required", "Use application/json.")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(out); err != nil {
		operatorError(w, 400, "invalid_command", "The execution request is invalid or too large.")
		return false
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		operatorError(w, 400, "invalid_command", "Supply one JSON object.")
		return false
	}
	return true
}

func (s *Server) handleAdmitExecution(w http.ResponseWriter, r *http.Request) {
	p, actor, ok := executionActor(w, r)
	if !ok {
		return
	}
	var input store.AdmitOperatorExecution
	if !decodeExecution(w, r, &input) {
		return
	}
	if !operatorName.MatchString(input.SessionID) || !operatorName.MatchString(input.Team) || !operatorName.MatchString(input.RepositoryID) || !operatorName.MatchString(input.CrewID) || len(strings.TrimSpace(input.Title)) < 1 || len(input.Title) > 200 || len(strings.TrimSpace(input.Objective)) < 20 || len(input.Objective) > 110000 || math.IsNaN(input.BudgetUSD) || math.IsInf(input.BudgetUSD, 0) || input.BudgetUSD < 0 || input.BudgetUSD > p.BudgetLimitUSD || (!input.Demo && input.BudgetUSD <= 0) {
		operatorError(w, 400, "invalid_execution", "Choose valid execution details within the consumer budget limit.")
		return
	}
	if !p.AllowsTeam(input.Team) {
		operatorError(w, 403, "team_forbidden", "This team is outside the consumer scope.")
		return
	}
	if input.Source != nil {
		replay, err := s.Store.OperatorAdmissionReplay(r.Context(), p.Name, actor, input)
		if err == nil && !replay {
			err = s.validateOperatorSource(r.Context(), input)
		}
		if err != nil {
			executionError(w, err)
			return
		}
	}
	if !input.Demo {
		parsed, err := url.Parse(input.RepositoryURL)
		if err != nil || parsed.User != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "ssh") || len(input.RepositoryURL) > 2000 {
			operatorError(w, 400, "invalid_repository", "Use a credential-free registered repository URL.")
			return
		}
		if s.LLMControl == nil {
			operatorError(w, 503, "inference_unavailable", "Ploeg managed inference is not configured.")
			return
		}
	}
	e, created, err := s.Store.AdmitOperatorExecution(r.Context(), p.Name, actor, input, operatorExecutionTTL)
	if err != nil {
		executionError(w, err)
		return
	}
	if !e.Demo {
		if err = s.LLMControl.Reserve(r.Context(), e.RunToken); err != nil {
			operatorError(w, 409, "inference_policy", "The execution is recorded, but no matching managed inference policy admitted its budget.")
			return
		}
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	operatorJSON(w, status, map[string]any{"schemaVersion": "1.0", "execution": e, "created": created})
}

func (s *Server) executionForRequest(w http.ResponseWriter, r *http.Request) (OperatorPrincipal, store.OperatorExecution, bool) {
	p, actor, ok := executionActor(w, r)
	if !ok {
		return p, store.OperatorExecution{}, false
	}
	id := r.PathValue("execution")
	if len(id) != 32 {
		operatorError(w, 404, "not_found", "Execution not found.")
		return p, store.OperatorExecution{}, false
	}
	e, err := s.Store.OperatorExecution(r.Context(), id, p.Name, actor)
	if err != nil {
		executionError(w, err)
		return p, e, false
	}
	if !p.AllowsTeam(e.Team) {
		operatorError(w, 404, "not_found", "Execution not found.")
		return p, e, false
	}
	return p, e, true
}

func (s *Server) handleReadExecution(w http.ResponseWriter, r *http.Request) {
	_, e, ok := s.executionForRequest(w, r)
	if !ok {
		return
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "execution": e})
}

func (s *Server) handleExecutionCommand(w http.ResponseWriter, r *http.Request) {
	p, e, ok := s.executionForRequest(w, r)
	if !ok {
		return
	}
	var command store.OperatorExecutionCommand
	if !decodeExecution(w, r, &command) {
		return
	}
	if !operatorName.MatchString(command.ID) || command.ExpectedRevision < 1 || command.Generation < 1 || len(command.Text) > 20000 {
		operatorError(w, 400, "invalid_command", "Use a command identity, revision and bounded text.")
		return
	}
	command.AuthenticatedBy = r.Header.Get("X-Ploeg-Acting-User")
	if command.AuthenticatedBy == "" {
		command.AuthenticatedBy = e.Actor
	}
	e, err := s.Store.CommandOperatorExecution(r.Context(), e.ID, p.Name, e.Actor, command, operatorExecutionTTL)
	if err != nil {
		executionError(w, err)
		return
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "execution": e})
}

func (s *Server) handleExecutionEvents(w http.ResponseWriter, r *http.Request) {
	_, e, ok := s.executionForRequest(w, r)
	if !ok {
		return
	}
	after := int64(0)
	if value := r.URL.Query().Get("after"); value != "" {
		var err error
		after, err = strconv.ParseInt(value, 10, 64)
		if err != nil || after < 0 {
			operatorError(w, 400, "invalid_cursor", "Use a nonnegative revision.")
			return
		}
	}
	events, err := s.Store.OperatorExecutionEvents(r.Context(), e.ID, after, 200)
	if err != nil {
		executionError(w, err)
		return
	}
	cursor := after
	if len(events) > 0 {
		cursor = events[len(events)-1].Revision
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "events": events, "nextCursor": cursor, "consistency": "serialized-execution", "hasMore": cursor < e.Revision})
}

func (s *Server) handleExecutionCredential(w http.ResponseWriter, r *http.Request) {
	_, e, ok := s.executionForRequest(w, r)
	if !ok {
		return
	}
	var input struct {
		Generation int64 `json:"generation"`
	}
	if !decodeExecution(w, r, &input) {
		return
	}
	if e.Demo || s.LLMControl == nil || e.State != "running" || !e.ExpiresAt.After(time.Now()) || input.Generation != e.Generation {
		operatorError(w, 409, "execution_not_live", "A live admitted execution is required.")
		return
	}
	credential, err := s.LLMControl.IssueOperator(r.Context(), e.RunToken, e.ID, input.Generation)
	if err != nil {
		operatorError(w, 409, "credential_unresolved", "Credential issuance is unresolved. Reconcile before another paid attempt.")
		return
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "credential": map[string]any{"key": credential.APIKey, "alias": credential.Alias, "reference": e.ID, "budgetUsd": e.BudgetUSD}})
}

func (s *Server) handleExecutionBlock(w http.ResponseWriter, r *http.Request) {
	_, e, ok := s.executionForRequest(w, r)
	if !ok {
		return
	}
	if !e.Demo {
		if s.LLMControl == nil {
			operatorError(w, 503, "inference_unavailable", "Managed inference is unavailable.")
			return
		}
		if err := s.LLMControl.Block(r.Context(), e.RunToken); err != nil {
			operatorError(w, 503, "block_unconfirmed", "Inference capability blocking is not confirmed.")
			return
		}
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "blocked": true})
}

func (s *Server) handleExecutionSpend(w http.ResponseWriter, r *http.Request) {
	_, e, ok := s.executionForRequest(w, r)
	if !ok {
		return
	}
	if e.Demo {
		operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "costStatus": "demo", "observedUsd": 0, "capabilityState": "demo"})
		return
	}
	capabilityState := "unknown"
	if account, err := s.Store.LLMAccount(r.Context(), e.RunToken); err == nil {
		capabilityState = account.State
	}
	if s.LLMControl != nil {
		spend, err := s.LLMControl.Spend(r.Context(), e.RunToken)
		if err == nil {
			operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "costStatus": "observed", "observedUsd": spend, "capabilityState": capabilityState})
			return
		}
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "costStatus": "unknown", "observedUsd": nil, "capabilityState": capabilityState})
}

func executionError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrExecutionNotFound) {
		operatorError(w, 404, "not_found", "Execution not found.")
		return
	}
	if errors.Is(err, store.ErrExecutionConflict) {
		operatorError(w, 409, "execution_conflict", "Refresh execution state. A stale or incompatible command cannot run.")
		return
	}
	operatorError(w, 503, "execution_unavailable", "Ploeg could not confirm the execution operation.")
}
