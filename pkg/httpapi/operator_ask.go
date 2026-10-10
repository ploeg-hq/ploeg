package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

// MaxAskQuestionRunes bounds the question an operator consumer sends with an
// Ask. Ploeg keeps only its SHA-256 (ADR-0082).
const MaxAskQuestionRunes = 4000

func (s *Server) registerOperatorAsk(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/operator/work-items/{id}/asks", s.handleAdmitAsk)
	mux.HandleFunc("GET /api/v1/operator/work-items/{id}/asks/{ask}", s.handleReadAsk)
	mux.HandleFunc("POST /api/v1/operator/work-items/{id}/asks/{ask}/finish", s.handleFinishAsk)
}

type askCredential struct {
	Key       string    `json:"key"`
	Alias     string    `json:"alias"`
	Models    []string  `json:"models"`
	BudgetUSD float64   `json:"budgetUsd"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func askWorkItemID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		operatorError(w, 404, "not_found", "The resource was not found in the consumer's scope.")
		return 0, false
	}
	return id, true
}

func (s *Server) handleAdmitAsk(w http.ResponseWriter, r *http.Request) {
	p, actor, ok := executionActor(w, r)
	if !ok {
		return
	}
	workItemID, ok := askWorkItemID(w, r)
	if !ok {
		return
	}
	var input struct {
		AskID    string `json:"askId"`
		Question string `json:"question"`
	}
	if !decodeExecution(w, r, &input) {
		return
	}
	if !operatorName.MatchString(input.AskID) || strings.TrimSpace(input.Question) == "" || !utf8.ValidString(input.Question) ||
		utf8.RuneCountInString(input.Question) > MaxAskQuestionRunes {
		operatorError(w, 400, "invalid_ask", "Use an askId and a question of 1 to 4000 characters.")
		return
	}
	team, err := s.Store.AskWorkItemTeam(r.Context(), workItemID, p.Teams)
	if err != nil {
		askError(w, err)
		return
	}
	if s.LLMControl == nil {
		operatorError(w, 503, "inference_unavailable", "Ploeg managed inference is not configured.")
		return
	}
	policy, budget, ok := s.askBudget(team)
	if !ok {
		operatorError(w, 409, "inference_policy", "This team has no managed inference policy for the ask Role.")
		return
	}
	ttl, err := time.ParseDuration(policy.TTL)
	if err != nil {
		operatorError(w, 409, "inference_policy", "This team has no managed inference policy for the ask Role.")
		return
	}
	digest := sha256.Sum256([]byte(input.Question))
	ask, created, err := s.Store.AdmitAsk(r.Context(), p.Name, actor, store.AdmitAsk{AskID: input.AskID, WorkItemID: workItemID, Team: team,
		QuestionSHA256: hex.EncodeToString(digest[:]), AskedBy: r.Header.Get("X-Ploeg-Acting-User"),
		BudgetUSD: budget, LimitUSD: s.askAllowanceUSD(), TTL: ttl})
	var exhausted *store.AllowanceExhaustedError
	if errors.As(err, &exhausted) {
		operatorJSON(w, 402, map[string]any{"schemaVersion": "1.0",
			"error":     map[string]string{"code": "allowance_exhausted", "message": "The Ask Allowance cannot cover another Ask until it resets."},
			"allowance": s.allowanceView(exhausted.Allowance)})
		return
	}
	if err != nil {
		askError(w, err)
		return
	}
	if ask.Spend.KeyState == "none" {
		if err := s.LLMControl.Reserve(r.Context(), ask.RunToken); err != nil {
			operatorError(w, 409, "inference_policy", "The Ask is recorded, but no matching managed inference policy admitted its budget.")
			return
		}
	}
	var credential *askCredential
	if ask.State == "open" {
		if account, err := s.Store.LLMAccount(r.Context(), ask.RunToken); err == nil && account.State == "reserved" {
			issued, err := s.LLMControl.Issue(r.Context(), ask.RunToken)
			switch {
			case err == nil:
				credential = &askCredential{Key: issued.APIKey, Alias: issued.Alias, Models: account.Models, BudgetUSD: account.Authorized, ExpiresAt: ask.ExpiresAt}
			case !errors.Is(err, store.ErrLLMAccountState):
				operatorError(w, 409, "credential_unresolved", "Credential issuance is unresolved. Finish this Ask and ask again under a new askId.")
				return
			}
		}
	}
	if ask, err = s.Store.Ask(r.Context(), p.Name, actor, input.AskID); err != nil {
		askError(w, err)
		return
	}
	allowance, err := s.Store.Allowance(r.Context(), store.AllowancePurposeAsk, ask.ScopeKind, ask.ScopeID, ask.PeriodStart, s.askAllowanceUSD())
	if err != nil {
		askError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	operatorJSON(w, status, map[string]any{"schemaVersion": "1.0", "created": created, "ask": ask, "credential": credential,
		"allowance": s.allowanceView(allowance)})
}

func (s *Server) askForRequest(w http.ResponseWriter, r *http.Request) (OperatorPrincipal, string, store.Ask, bool) {
	p, actor, ok := executionActor(w, r)
	if !ok {
		return p, actor, store.Ask{}, false
	}
	workItemID, ok := askWorkItemID(w, r)
	if !ok {
		return p, actor, store.Ask{}, false
	}
	askID := r.PathValue("ask")
	if !operatorName.MatchString(askID) {
		operatorError(w, 404, "not_found", "Ask not found.")
		return p, actor, store.Ask{}, false
	}
	ask, err := s.Store.Ask(r.Context(), p.Name, actor, askID)
	if err != nil {
		askError(w, err)
		return p, actor, ask, false
	}
	if ask.WorkItemID != strconv.FormatInt(workItemID, 10) || !p.AllowsTeam(ask.Team) {
		operatorError(w, 404, "not_found", "Ask not found.")
		return p, actor, ask, false
	}
	return p, actor, ask, true
}

func (s *Server) handleReadAsk(w http.ResponseWriter, r *http.Request) {
	p, actor, ask, ok := s.askForRequest(w, r)
	if !ok {
		return
	}
	switch ask.Spend.KeyState {
	case "issued", "unknown", "blocked":
		if s.LLMControl != nil {
			if _, err := s.LLMControl.Spend(r.Context(), ask.RunToken); err == nil {
				if read, err := s.Store.Ask(r.Context(), p.Name, actor, ask.AskID); err == nil {
					ask = read
				}
			}
		}
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "ask": ask})
}

func (s *Server) handleFinishAsk(w http.ResponseWriter, r *http.Request) {
	p, actor, ask, ok := s.askForRequest(w, r)
	if !ok {
		return
	}
	ask, err := s.Store.FinishAsk(r.Context(), p.Name, actor, ask.AskID)
	if err != nil {
		askError(w, err)
		return
	}
	if ask.Spend.KeyState != "none" {
		if s.LLMControl == nil {
			operatorError(w, 503, "inference_unavailable", "Managed inference is unavailable.")
			return
		}
		if err := s.LLMControl.Block(r.Context(), ask.RunToken); err != nil {
			operatorError(w, 503, "block_unconfirmed", "The Ask is finished, but blocking its key is not confirmed. Ploeg retries the block.")
			return
		}
		if ask, err = s.Store.Ask(r.Context(), p.Name, actor, ask.AskID); err != nil {
			askError(w, err)
			return
		}
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "ask": ask, "blocked": true})
}

func askError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrWorkItemNotFound):
		operatorError(w, 404, "not_found", "The resource was not found in the consumer's scope.")
	case errors.Is(err, store.ErrAskNotFound):
		operatorError(w, 404, "not_found", "Ask not found.")
	case errors.Is(err, store.ErrAskConflict):
		operatorError(w, 409, "ask_conflict", "This askId was used for another Work Item, question or actor.")
	default:
		operatorError(w, 503, "ask_unavailable", "Ploeg could not confirm the Ask operation.")
	}
}
