package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// MaxChatMessageRunes bounds the message an operator consumer sends with a
// chat admission. Ploeg keeps only its SHA-256 (ADR-0115).
const MaxChatMessageRunes = 4000

// DefaultChatAllowanceUSD is the monthly Chat Allowance a Team's period opens
// with (PLOEG_CHAT_ALLOWANCE_USD, ADR-0115).
const DefaultChatAllowanceUSD = 2.00

// DefaultChatBudgetUSD is the per-chat Budget (PLOEG_CHAT_BUDGET_USD,
// ADR-0115). It is larger than the per-Ask Budget because one chat message
// may take several tool-calling model calls.
const DefaultChatBudgetUSD = 0.05

func (s *Server) registerOperatorChat(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/operator/teams/{team}/chats", s.handleAdmitChat)
	mux.HandleFunc("GET /api/v1/operator/teams/{team}/chats/{chat}", s.handleReadChat)
	mux.HandleFunc("POST /api/v1/operator/teams/{team}/chats/{chat}/finish", s.handleFinishChat)
}

func (s *Server) chatAllowanceUSD() float64 {
	if s.ChatAllowanceUSD > 0 {
		return s.ChatAllowanceUSD
	}
	return DefaultChatAllowanceUSD
}

// chatBudget is the per-chat Budget for a team: the configured budget,
// lowered by the (team, ask) inference policy's budget when that is smaller.
// ok is false when the team has no such policy and therefore no chats.
func (s *Server) chatBudget(team string) (LLMPolicy, float64, bool) {
	budget := DefaultChatBudgetUSD
	if s.ChatBudgetUSD > 0 {
		budget = s.ChatBudgetUSD
	}
	if s.LLMControl == nil {
		return LLMPolicy{}, budget, false
	}
	policy, ok := s.LLMControl.Policy(team, policyRole(work.ChatRole))
	if !ok {
		return policy, budget, false
	}
	return policy, math.Min(budget, policy.BudgetUSD), true
}

type chatAllowanceView struct {
	store.Allowance
	ChatCount     int64   `json:"chatCount"`
	ChatBudgetUSD float64 `json:"chatBudgetUsd"`
	ChatsEnabled  bool    `json:"chatsEnabled"`
}

func (s *Server) chatAllowanceView(a store.Allowance) chatAllowanceView {
	_, budget, enabled := s.chatBudget(a.ScopeID)
	return chatAllowanceView{Allowance: a, ChatCount: a.Count, ChatBudgetUSD: budget, ChatsEnabled: enabled}
}

func chatTeam(w http.ResponseWriter, r *http.Request, p OperatorPrincipal) (string, bool) {
	team := r.PathValue("team")
	if !operatorName.MatchString(team) || !p.AllowsTeam(team) {
		operatorError(w, 404, "not_found", "The resource was not found in the consumer's scope.")
		return "", false
	}
	return team, true
}

func (s *Server) handleAdmitChat(w http.ResponseWriter, r *http.Request) {
	p, actor, ok := executionActor(w, r)
	if !ok {
		return
	}
	var input struct {
		ChatID  string `json:"chatId"`
		Message string `json:"message"`
	}
	if !decodeExecution(w, r, &input) {
		return
	}
	if !operatorName.MatchString(input.ChatID) || strings.TrimSpace(input.Message) == "" || !utf8.ValidString(input.Message) ||
		utf8.RuneCountInString(input.Message) > MaxChatMessageRunes {
		operatorError(w, 400, "invalid_chat", "Use a chatId and a message of 1 to 4000 characters.")
		return
	}
	team, ok := chatTeam(w, r, p)
	if !ok {
		return
	}
	if s.LLMControl == nil {
		operatorError(w, 503, "inference_unavailable", "Ploeg managed inference is not configured.")
		return
	}
	policy, budget, ok := s.chatBudget(team)
	if !ok {
		operatorError(w, 409, "inference_policy", "This team has no managed inference policy for the ask Role, which chat messages use.")
		return
	}
	ttl, err := time.ParseDuration(policy.TTL)
	if err != nil {
		operatorError(w, 409, "inference_policy", "This team has no managed inference policy for the ask Role, which chat messages use.")
		return
	}
	digest := sha256.Sum256([]byte(input.Message))
	chat, created, err := s.Store.AdmitChat(r.Context(), p.Name, actor, store.AdmitChat{ChatID: input.ChatID, Team: team,
		MessageSHA256: hex.EncodeToString(digest[:]), AskedBy: r.Header.Get("X-Ploeg-Acting-User"),
		BudgetUSD: budget, LimitUSD: s.chatAllowanceUSD(), TTL: ttl})
	var exhausted *store.AllowanceExhaustedError
	if errors.As(err, &exhausted) {
		operatorJSON(w, 402, map[string]any{"schemaVersion": "1.0",
			"error":     map[string]string{"code": "allowance_exhausted", "message": "The Chat Allowance cannot cover another chat message until it resets."},
			"allowance": s.chatAllowanceView(exhausted.Allowance)})
		return
	}
	if err != nil {
		chatError(w, err)
		return
	}
	s.recordRunConfig(r.Context(), chat.RunToken, team, policyRole(work.ChatRole), false, runHarness{})
	if chat.Spend.KeyState == "none" {
		if err := s.LLMControl.Reserve(r.Context(), chat.RunToken); err != nil {
			operatorError(w, 409, "inference_policy", "The chat message is recorded, but no matching managed inference policy admitted its budget.")
			return
		}
	}
	var credential *askCredential
	if chat.State == "open" {
		if account, err := s.Store.LLMAccount(r.Context(), chat.RunToken); err == nil && account.State == "reserved" {
			issued, err := s.LLMControl.Issue(r.Context(), chat.RunToken)
			switch {
			case err == nil:
				credential = &askCredential{Key: issued.APIKey, Alias: issued.Alias, Models: account.Models, BudgetUSD: account.Authorized, ExpiresAt: chat.ExpiresAt}
			case !errors.Is(err, store.ErrLLMAccountState):
				operatorError(w, 409, "credential_unresolved", "Credential issuance is unresolved. Finish this chat message and send it again under a new chatId.")
				return
			}
		}
	}
	if chat, err = s.Store.Chat(r.Context(), p.Name, actor, input.ChatID); err != nil {
		chatError(w, err)
		return
	}
	allowance, err := s.Store.Allowance(r.Context(), store.AllowancePurposeChat, chat.ScopeKind, chat.ScopeID, chat.PeriodStart, s.chatAllowanceUSD())
	if err != nil {
		chatError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	operatorJSON(w, status, map[string]any{"schemaVersion": "1.0", "created": created, "chat": chat, "credential": credential,
		"allowance": s.chatAllowanceView(allowance)})
}

func (s *Server) chatForRequest(w http.ResponseWriter, r *http.Request) (OperatorPrincipal, string, store.Chat, bool) {
	p, actor, ok := executionActor(w, r)
	if !ok {
		return p, actor, store.Chat{}, false
	}
	team, ok := chatTeam(w, r, p)
	if !ok {
		return p, actor, store.Chat{}, false
	}
	chatID := r.PathValue("chat")
	if !operatorName.MatchString(chatID) {
		operatorError(w, 404, "not_found", "Chat not found.")
		return p, actor, store.Chat{}, false
	}
	chat, err := s.Store.Chat(r.Context(), p.Name, actor, chatID)
	if err != nil {
		chatError(w, err)
		return p, actor, chat, false
	}
	if chat.Team != team {
		operatorError(w, 404, "not_found", "Chat not found.")
		return p, actor, chat, false
	}
	return p, actor, chat, true
}

func (s *Server) handleReadChat(w http.ResponseWriter, r *http.Request) {
	p, actor, chat, ok := s.chatForRequest(w, r)
	if !ok {
		return
	}
	switch chat.Spend.KeyState {
	case "issued", "unknown", "blocked":
		if s.LLMControl != nil {
			if _, err := s.LLMControl.Spend(r.Context(), chat.RunToken); err == nil {
				if read, err := s.Store.Chat(r.Context(), p.Name, actor, chat.ChatID); err == nil {
					chat = read
				}
			}
		}
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "chat": chat})
}

func (s *Server) handleFinishChat(w http.ResponseWriter, r *http.Request) {
	p, actor, chat, ok := s.chatForRequest(w, r)
	if !ok {
		return
	}
	chat, err := s.Store.FinishChat(r.Context(), p.Name, actor, chat.ChatID)
	if err != nil {
		chatError(w, err)
		return
	}
	if chat.Spend.KeyState != "none" {
		if s.LLMControl == nil {
			operatorError(w, 503, "inference_unavailable", "Managed inference is unavailable.")
			return
		}
		if err := s.LLMControl.Block(r.Context(), chat.RunToken); err != nil {
			operatorError(w, 503, "block_unconfirmed", "The chat message is finished, but blocking its key is not confirmed. Ploeg retries the block.")
			return
		}
		if chat, err = s.Store.Chat(r.Context(), p.Name, actor, chat.ChatID); err != nil {
			chatError(w, err)
			return
		}
	}
	operatorJSON(w, 200, map[string]any{"schemaVersion": "1.0", "chat": chat})
}

func chatError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrChatNotFound):
		operatorError(w, 404, "not_found", "Chat not found.")
	case errors.Is(err, store.ErrChatConflict):
		operatorError(w, 409, "chat_conflict", "This chatId was used for another team, message or actor.")
	default:
		operatorError(w, 503, "chat_unavailable", "Ploeg could not confirm the chat operation.")
	}
}
