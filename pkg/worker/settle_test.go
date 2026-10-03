package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
)

// meterStub returns a scripted sequence of spend readings, then repeats the
// last one forever — which is what a gateway that has finished writing its
// spend logs looks like.
type meterStub struct {
	readings []float64
	err      error
	calls    int
}

func (m *meterStub) Spend(context.Context, llmbroker.Credential) (float64, error) {
	if m.err != nil {
		return 0, m.err
	}
	i := m.calls
	m.calls++
	if i >= len(m.readings) {
		i = len(m.readings) - 1
	}
	return m.readings[i], nil
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// LiteLLM writes spend asynchronously, so the first read after a run ends is
// routinely stale. Settling on the first non-zero figure would truncate the
// cost at whatever partial value happened to be written first; this waits for
// it to stop moving.
func TestSettleSpend_WaitsForTheFigureToStopMoving(t *testing.T) {
	m := &meterStub{readings: []float64{0, 0.004, 0.0137}}
	got, err := observeSpend(context.Background(), m, llmbroker.Credential{APIKey: "sk-x"}, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	if got != 0.0137 {
		t.Errorf("spend = %v, want 0.0137 (the settled figure, not the first reading)", got)
	}
}

// A run that genuinely spent nothing — the exec harness makes no LLM call at
// all — must settle immediately rather than burning the whole poll budget.
// This is why the loop keys on "stopped changing" and not on "non-zero".
func TestSettleSpend_ZeroCostRunSettlesImmediately(t *testing.T) {
	m := &meterStub{readings: []float64{0}}
	got, err := observeSpend(context.Background(), m, llmbroker.Credential{APIKey: "sk-x"}, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Errorf("spend = %v, want 0", got)
	}
	if m.calls > 2 {
		t.Errorf("polled %d times for a zero-cost run; two equal reads is enough", m.calls)
	}
}

// The first read failing means no figure at all, and the caller must be told
// so it can log "cost not settled" rather than record a confident 0.00 — a
// silent zero is exactly how shifts.spent stayed 0.0000 for the system's
// entire life.
func TestSettleSpend_FirstReadFailureIsReported(t *testing.T) {
	m := &meterStub{err: errors.New("gateway down")}
	if _, err := observeSpend(context.Background(), m, llmbroker.Credential{APIKey: "sk-x"}, quietLog()); err == nil {
		t.Fatal("expected the error to surface, got a silent zero")
	}
}
