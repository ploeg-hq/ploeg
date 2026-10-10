package httpapi

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/gate"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

const gateObserveTimeout = 10 * time.Second

func (s *Server) observeGate(ctx context.Context, name string, tp provider.TrackerProvider, ev provider.TrackerEvent) {
	if !s.Gates.Has(name) {
		return
	}
	reader, ok := tp.(provider.BoardReader)
	if !ok {
		return
	}
	if _, gated := s.Gates.Lookup(name, ev.Scope.ID); ev.Scope.ID != "" && !gated {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, gateObserveTimeout)
	defer cancel()
	pos, err := s.Store.GatePosition(ctx, name, ev.ExternalID)
	if errors.Is(err, store.ErrWorkItemNotFound) {
		return
	}
	if err != nil {
		s.Log.Error("gate position read failed", "provider", name, "external_id", ev.ExternalID, "err", err)
		return
	}
	status, err := reader.BoardStatus(ctx, ev.ExternalID)
	if err != nil {
		s.Log.Warn("board status read failed; gate move not recorded", "provider", name, "external_id", ev.ExternalID, "err", err)
		return
	}
	board, gated := s.Gates.Lookup(name, status.Scope)
	if !gated {
		return
	}
	s.recordTrackerFacts(ctx, name, ev.ExternalID, status)
	s.recordStatus(ctx, name, ev, status.Statuses, board)
	to, matched, ok := board.Resolve(status.Statuses)
	if !ok {
		s.Log.Info("tracker status maps to no single gate", "provider", name, "external_id", ev.ExternalID, "statuses", status.Statuses)
		return
	}
	if to == pos.Gate {
		return
	}
	move := store.GateMove{Provider: name, ExternalID: ev.ExternalID, Gate: to, Status: matched, Actor: ev.Actor, At: ev.At}
	if move.At.Before(pos.Entered) {
		move.At = time.Time{}
	}
	if gate.IsBounce(pos.Gate, to) {
		move.Reason = s.bounceReason(ctx, name, reader, ev.ExternalID, status.Labels, pos.Entered)
	}
	recorded, err := s.Store.RecordGateMove(ctx, move)
	if err != nil {
		s.Log.Error("gate move not recorded", "provider", name, "external_id", ev.ExternalID, "gate", to, "err", err)
		return
	}
	if recorded {
		s.Log.Info("gate move recorded", "provider", name, "external_id", ev.ExternalID, "from", pos.Gate, "to", to,
			"status", matched, "bounce", gate.IsBounce(pos.Gate, to), "reason", move.Reason)
	}
}

func (s *Server) recordTrackerFacts(ctx context.Context, name, externalID string, status provider.BoardStatus) {
	if status.Created.IsZero() && !status.Estimates {
		return
	}
	err := s.Store.RecordTrackerFacts(ctx, store.TrackerFacts{Provider: name, ExternalID: externalID, Created: status.Created,
		Estimates: status.Estimates, EstimateSeconds: status.EstimateSeconds})
	if err != nil && !errors.Is(err, store.ErrWorkItemNotFound) {
		s.Log.Warn("tracker creation time and estimate not recorded", "provider", name, "external_id", externalID, "err", err)
	}
}

func (s *Server) recordStatus(ctx context.Context, name string, ev provider.TrackerEvent, statuses []string, board gate.Map) {
	status, ok := currentStatus(statuses, board)
	if !ok {
		if len(statuses) > 0 {
			s.Log.Info("tracker reports several statuses; status move not recorded", "provider", name, "external_id", ev.ExternalID,
				"statuses", statuses)
		}
		return
	}
	g, _, _ := board.Resolve([]string{status})
	recorded, err := s.Store.RecordStatusMove(ctx, store.StatusMove{Provider: name, ExternalID: ev.ExternalID, Status: status, Gate: g, At: ev.At})
	if err != nil {
		s.Log.Error("status move not recorded", "provider", name, "external_id", ev.ExternalID, "status", status, "err", err)
		return
	}
	if recorded {
		s.Log.Info("status move recorded", "provider", name, "external_id", ev.ExternalID, "status", status, "gate", g)
	}
}

func currentStatus(statuses []string, board gate.Map) (string, bool) {
	if _, matched, ok := board.Resolve(statuses); ok {
		return matched, true
	}
	distinct := map[string]bool{}
	first := ""
	for _, st := range statuses {
		if key := strings.ToLower(strings.TrimSpace(st)); key != "" {
			if first == "" {
				first = st
			}
			distinct[key] = true
		}
	}
	return first, len(distinct) == 1
}

func (s *Server) bounceReason(ctx context.Context, name string, reader provider.BoardReader, externalID string, labels []string, since time.Time) gate.Reason {
	comments, err := reader.BoardComments(ctx, externalID)
	if err != nil {
		s.Log.Warn("comment read failed; bounce reason taken from labels only", "provider", name, "external_id", externalID, "err", err)
	}
	var newest time.Time
	var fromComment gate.Reason
	for _, c := range comments {
		if !c.At.IsZero() && c.At.Before(since) {
			continue
		}
		reason, ok := gate.ParseReason(c.Text)
		if ok && (fromComment == "" || !c.At.Before(newest)) {
			fromComment, newest = reason, c.At
		}
	}
	if fromComment != "" {
		return fromComment
	}
	var fromLabel gate.Reason
	for _, l := range labels {
		reason, ok := gate.ParseReason(l)
		if !ok {
			continue
		}
		if fromLabel != "" && fromLabel != reason {
			return gate.ReasonUnknown
		}
		fromLabel = reason
	}
	if fromLabel != "" {
		return fromLabel
	}
	return gate.ReasonUnknown
}
