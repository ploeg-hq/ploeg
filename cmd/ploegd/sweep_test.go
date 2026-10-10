package main

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// fakeEngine records the Shift ids its usage report was refreshed for.
type fakeEngine struct {
	refreshed []int64
	err       error
}

func (f *fakeEngine) EnsureShift(context.Context, int64, work.WorkItem) error { return nil }
func (f *fakeEngine) EvaluateItem(context.Context, int64) error               { return nil }
func (f *fakeEngine) RefreshUsageReport(_ context.Context, shiftID int64) error {
	f.refreshed = append(f.refreshed, shiftID)
	return f.err
}

func shiftIDPtr(v int64) *int64 { return &v }

// A settled account in a Shift refreshes that Shift's report; a historical Run
// with no Shift is skipped, not treated as an error.
func TestRefreshUsageReports_CallsTheEngineForAShiftOnly(t *testing.T) {
	eng := &fakeEngine{}
	srv := &httpapi.Server{Engine: eng}
	log := slog.New(slog.DiscardHandler)

	refreshUsageReports(context.Background(), log, srv, store.UnsettledLLMAccount{Alias: "ploeg-aaaaaaaaaaaa", ShiftID: nil})
	if len(eng.refreshed) != 0 {
		t.Fatalf("a Shift-less account triggered a refresh: %v", eng.refreshed)
	}

	refreshUsageReports(context.Background(), log, srv, store.UnsettledLLMAccount{Alias: "ploeg-bbbbbbbbbbbb", ShiftID: shiftIDPtr(42)})
	if len(eng.refreshed) != 1 || eng.refreshed[0] != 42 {
		t.Fatalf("refreshed = %v, want [42]", eng.refreshed)
	}
}

// With no engine configured the refresh is a silent no-op.
func TestRefreshUsageReports_NoEngineIsSilent(t *testing.T) {
	srv := &httpapi.Server{}
	refreshUsageReports(context.Background(), slog.New(slog.DiscardHandler), srv,
		store.UnsettledLLMAccount{ShiftID: shiftIDPtr(7)})
}

// A refresh failure is logged and swallowed; the settlement that already
// succeeded is not undone.
func TestRefreshUsageReports_SwallowsEngineFailure(t *testing.T) {
	eng := &fakeEngine{err: errors.New("forge down")}
	srv := &httpapi.Server{Engine: eng}
	refreshUsageReports(context.Background(), slog.New(slog.DiscardHandler), srv,
		store.UnsettledLLMAccount{ShiftID: shiftIDPtr(9)})
	if len(eng.refreshed) != 1 {
		t.Fatalf("engine was not called: %v", eng.refreshed)
	}
}

// The settlement sweep is a no-op with no LLMControl configured.
func TestManagedSettlementSweep_WithoutControlIsANoop(t *testing.T) {
	srv := &httpapi.Server{}
	if got := managedSettlementSweep(context.Background(), slog.New(slog.DiscardHandler), srv, 0, 0, newAccountRetries(time.Second)); got != 0 {
		t.Errorf("cursor = %d, want 0", got)
	}
}

func TestManagedCorrectionSweep_WithoutControlIsANoop(t *testing.T) {
	managedCorrectionSweep(context.Background(), slog.New(slog.DiscardHandler), &httpapi.Server{})
}

func TestCardSweeps_DoNothingWhenCardsAreDisabled(t *testing.T) {
	server := &httpapi.Server{CardsDisabled: true, Log: slog.New(slog.DiscardHandler)}
	cardSweeps(context.Background(), slog.New(slog.DiscardHandler), nil, server)
}
