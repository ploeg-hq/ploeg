package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const (
	renewalStrikes   = 3
	minRenewInterval = 5 * time.Second
)

func renewInterval(ttl time.Duration) time.Duration {
	return max(ttl/4, minRenewInterval)
}

func leaseStopMargin(ttl time.Duration) time.Duration {
	return ttl / 6
}

type leaseClock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

func (wallClock) AfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}

func (w *Worker) leaseClock() leaseClock {
	if w.clock != nil {
		return w.clock
	}
	return wallClock{}
}

func (w *Worker) renewTicks(interval time.Duration) (<-chan time.Time, func()) {
	if w.RenewTicks != nil {
		return w.RenewTicks(interval)
	}
	t := time.NewTicker(interval)
	return t.C, t.Stop
}

type leaseGuard struct {
	clock  leaseClock
	margin time.Duration
	cancel context.CancelCauseFunc
	log    *slog.Logger

	mu   sync.Mutex
	stop func() bool
}

func (g *leaseGuard) holdUntil(leaseEnds time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stop != nil {
		g.stop()
	}
	g.stop = g.clock.AfterFunc(leaseEnds.Add(-g.margin).Sub(g.clock.Now()), g.expire)
}

func (g *leaseGuard) expire() {
	g.log.Error("the lease ends before a renewal could extend it; cancelling run", "margin", g.margin)
	g.cancel(errLeaseLost)
}

func (g *leaseGuard) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stop != nil {
		g.stop()
	}
}

func renewLoop(ctx context.Context, cancel context.CancelCauseFunc, api *APIClient, token string, ticks <-chan time.Time,
	guard *leaseGuard, claimValidity time.Duration, log *slog.Logger, yield func()) {
	strikes := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			sent := guard.clock.Now()
			renewal, err := api.Renew(ctx, token)
			switch {
			case ctx.Err() != nil:
				return
			case renewal.Gone:
				log.Error("lease no longer ours; cancelling run")
				cancel(errLeaseLost)
				return
			case err != nil:
				strikes++
				log.Warn("lease renewal failed", "strikes", strikes, "err", err)
				if strikes >= renewalStrikes {
					log.Error("renewal failed repeatedly; cancelling run", "strikes", strikes)
					cancel(errLeaseLost)
					return
				}
			default:
				strikes = 0
				guard.holdUntil(sent.Add(renewal.validFor(claimValidity)))
				if renewal.Yield && yield != nil {
					log.Info("a person asked the Run to yield; stopping at the next safe point")
					yield()
				}
			}
		}
	}
}
