package httpapi

import (
	"context"
	"errors"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

const relationObserveTimeout = 10 * time.Second

func (s *Server) observeEpics(ctx context.Context, name string, tp provider.TrackerProvider, externalID string) {
	reader, ok := tp.(provider.RelationReader)
	if !ok || externalID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, relationObserveTimeout)
	defer cancel()
	if _, _, err := s.Store.TrackerWorkItemID(ctx, name, externalID); err != nil {
		if !errors.Is(err, store.ErrWorkItemNotFound) {
			s.Log.Error("work item lookup failed; epics not read", "provider", name, "external_id", externalID, "err", err)
		}
		return
	}
	parents, err := reader.Parents(ctx, externalID)
	if err != nil {
		s.Log.Warn("tracker relations read failed; epics not recorded", "provider", name, "external_id", externalID, "err", err)
		return
	}
	refs := make([]store.EpicRef, 0, len(parents))
	for _, p := range parents {
		refs = append(refs, store.EpicRef{ExternalID: p.ExternalID, Title: p.Title})
	}
	changed, err := s.Store.RecordEpics(ctx, name, externalID, refs)
	if err != nil {
		s.Log.Error("epics not recorded", "provider", name, "external_id", externalID, "err", err)
		return
	}
	if changed {
		s.Log.Info("epics recorded", "provider", name, "external_id", externalID, "epics", len(refs))
	}
}
