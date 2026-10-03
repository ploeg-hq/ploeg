package httpapi

import (
	"context"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

const (
	cardRarityBatch   = 25
	cardRarityRecheck = time.Hour
)

func (s *Server) cardRarity() *store.RarityOptions {
	return &store.RarityOptions{Matchers: s.OperatorConfig.RarityMatchers}
}

// SweepCardRarity assembles the cards of up to 25 Work Items with a merged
// play whose rarity is not revealed yet, and not checked in the last hour,
// so a released card is revealed and frozen even when nobody reads it
// (ADR-0056). Failures are logged.
func (s *Server) SweepCardRarity(ctx context.Context) {
	now := s.cardClock()
	ids, err := s.Store.RarityCandidates(ctx, now.Add(-cardRarityRecheck), cardRarityBatch)
	if err != nil {
		s.Log.Error("card rarity sweep failed", "err", err)
		return
	}
	revealed := 0
	for _, id := range ids {
		card, err := s.Store.OperatorCard(ctx, id, nil, store.CardOptions{Bots: s.ForgeBots,
			ReleaseEnvironments: s.OperatorConfig.ReleaseEnvironments, HotfixLabels: s.cardHotfixLabels(), Rarity: s.cardRarity(), Now: now})
		if err != nil {
			s.Log.Warn("card rarity not checked", "work_item", id, "err", err)
			continue
		}
		if card.Rarity != nil && card.Rarity.Revealed != nil {
			revealed++
			continue
		}
		if err := s.Store.MarkRarityChecked(ctx, id, now); err != nil {
			s.Log.Warn("card rarity check not recorded", "work_item", id, "err", err)
		}
	}
	if revealed > 0 {
		s.Log.Info("card rarity sweep revealed cards", "cards", revealed)
	}
}
