package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

func (s *Server) handleOperatorCard(w http.ResponseWriter, r *http.Request) {
	if !operatorGET(w, r) {
		return
	}
	id, ok := operatorID(w, r)
	if !ok {
		return
	}
	principal, _ := OperatorPrincipalFromContext(r.Context())
	card, err := s.Store.OperatorCard(r.Context(), id, principal.Teams,
		store.CardOptions{Bots: s.ForgeBots, ReleaseEnvironments: s.OperatorConfig.ReleaseEnvironments, Live: s.liveCardUsage(r.Context()),
			HotfixLabels: s.cardHotfixLabels(), Rarity: s.cardRarity(), Flow: s.cardFlow()})
	if err != nil {
		operatorReadError(w, err)
		return
	}
	card.Style = s.cardStyle(card.Target)
	operatorJSON(w, http.StatusOK, map[string]any{"schemaVersion": 1, "card": card})
}

const cardLiveTimeout = 3 * time.Second

func (s *Server) cardFlow() *store.FlowOptions {
	return &store.FlowOptions{Kinds: s.StatusBoards, Calendars: s.WorkingCalendars}
}

func (s *Server) liveCardUsage(ctx context.Context) func(context.Context, string) (store.LiveUsage, error) {
	if s.LLMControl == nil {
		return nil
	}
	deadline := time.Now().Add(cardLiveTimeout)
	return func(_ context.Context, runToken string) (store.LiveUsage, error) {
		readCtx, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		return s.LLMControl.Live(readCtx, runToken)
	}
}

func (s *Server) cardStyle(target *store.OperatorCardTarget) store.CardStyle {
	style := store.CardStyle{Skin: store.DefaultCardSkin}
	if target == nil {
		return style
	}
	configured, ok := s.OperatorConfig.CardStyles[strings.ToLower(target.Owner+"/"+target.Repo)]
	if !ok {
		return style
	}
	if configured.Skin != "" {
		style.Skin = configured.Skin
	}
	if configured.Theme != nil && *configured.Theme != "" {
		theme := *configured.Theme
		style.Theme = &theme
	}
	return style
}
