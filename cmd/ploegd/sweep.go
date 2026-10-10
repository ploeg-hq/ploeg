package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/forgebroker"
	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/shiftengine"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

func forgeOrphanSweep(ctx context.Context, log *slog.Logger, st *store.Store, sweeper forgebroker.Sweeper) {
	if sweeper == nil {
		return
	}
	n, err := sweeper.SweepOrphans(ctx, st.LeasedRunTokens)
	if n > 0 {
		log.Info("forge orphan sweep: revoked stale push credentials", "count", n)
	}
	if err != nil {
		log.Error("forge orphan sweep failed", "err", err)
		return
	}
	if n == 0 {
		log.Info("forge orphan sweep: no stale push credentials found")
	}
}

// orphanSweep revokes gateway credentials that no longer correspond to a
// live (unfinished) run.
//
// It runs at boot AND on orphanSweepEvery, because boot-only left a real
// hole: a run that finishes normally but whose deferred Revoke call fails is
// marked finished, so neither ExpireLeases nor ExpireRuns will ever look at
// it again — both only see state='running'. Its key then outlived every
// mechanism until the next restart. Observed 2026-07-24..27: eight keys for
// finished runs accumulated and stayed live for about a day.
func orphanSweep(ctx context.Context, log *slog.Logger, st *store.Store, sweeper llmbroker.Sweeper) {
	if sweeper == nil {
		return
	}
	alive, err := st.UnfinishedRunTokens(ctx)
	if err != nil {
		log.Error("orphan sweep: failed to query active runs", "err", err)
		return
	}
	n, err := sweeper.SweepOrphans(ctx, alive)
	if err != nil {
		log.Error("orphan sweep failed", "err", err)
		return
	}
	if n > 0 {
		log.Info("orphan sweep: revoked stale keys", "count", n)
	} else {
		log.Info("orphan sweep: no stale keys found")
	}
}

// sweepLoop is the crash-safety mechanic: nothing depends on an agent
// behaving well at death (design §3). Every expired lease releases the item
// and revokes the run's gateway credentials (when a sweeper is configured).
//
// Shift runs live and die by their own expires_at, not a lease: ExpireRuns
// reclaims dead readers AND writers (a reader holds no lease, so ExpireLeases
// could never see it die), then the engine advances or repairs every live
// Shift — the async half of the fast-path/sweeper split (R2).
func sweepLoop(ctx context.Context, log *slog.Logger, st *store.Store, sweeper llmbroker.Sweeper,
	forgeSweeper forgebroker.Sweeper, engine *shiftengine.Engine, server *httpapi.Server, every, settleAfter time.Duration) {
	var managedCursor, settlementCursor int64
	blockRetries, settlementRetries := newAccountRetries(every), newAccountRetries(every)
	t := time.NewTicker(every)
	defer t.Stop()
	// A second, much slower ticker: reconciling every credential against the
	// database is a two-API-call scan, not something to do every 15 seconds
	// alongside the lease sweep.
	orphans := time.NewTicker(orphanSweepEvery)
	defer orphans.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-orphans.C:
			orphanSweep(ctx, log, st, sweeper)
			forgeOrphanSweep(ctx, log, st, forgeSweeper)
			managedCorrectionSweep(ctx, log, server)
		case <-t.C:
			if err := server.ReconcileOperatorExecutions(ctx); err != nil {
				log.Error("operator execution reconciliation failed")
			}
			expired, err := st.ExpireLeases(ctx)
			if err != nil {
				log.Error("lease sweep failed", "err", err)
				continue
			}
			for _, e := range expired {
				log.Warn("lease expired, item released", "work_item", e.WorkItemID)
				blockExpiredRun(ctx, log, sweeper, server, e.RunToken)
			}

			runs, err := st.ExpireRuns(ctx)
			if err != nil {
				log.Error("run sweep failed", "err", err)
				continue
			}
			for _, e := range runs {
				log.Warn("run deadline expired, run reclaimed",
					"work_item", e.WorkItemID, "shift", e.ShiftID, "role", e.Role, "writes", e.Writes)
				blockExpiredRun(ctx, log, sweeper, server, e.RunToken)
				// The zombie-writer case ADR-0013 tier 2 exists for: the pod
				// may still be alive and partitioned, so take away its ability
				// to push, not just its right to.
				revokeForgeToken(ctx, log, forgeSweeper, e.ForgeTokenID)
			}
			managedCursor = managedBlockSweep(ctx, log, server, managedCursor, blockRetries)
			settlementCursor = managedSettlementSweep(ctx, log, server, settlementCursor, settleAfter, settlementRetries)

			// Delivery ids outlive a forge's retry window by a wide margin;
			// the table exists to survive a restart, not to be an archive.
			if n, err := st.SweepDeliveries(ctx, deliveryRetention); err != nil {
				log.Error("delivery sweep failed", "err", err)
			} else if n > 0 {
				log.Debug("swept forge delivery ids", "count", n)
			}

			if engine != nil {
				engine.EvaluateAll(ctx)
			}
		}
	}
}

// reviewLoop is the slow half of merge detection. It asks the forge about
// every awaiting_review pull request, so a missed webhook delays the move to
// done or needs_human by at most one interval. It runs apart from sweepLoop
// because forge reads must not hold up lease expiry.
func reviewLoop(ctx context.Context, reviews *shiftengine.ReviewWatch, every time.Duration) {
	if every <= 0 {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			reviews.Reconcile(ctx)
		}
	}
}

func deployCheckLoop(ctx context.Context, server *httpapi.Server, every time.Duration) {
	server.SweepDeployChecks(ctx)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			server.SweepDeployChecks(ctx)
		}
	}
}

func blockExpiredRun(ctx context.Context, log *slog.Logger, sweeper llmbroker.Sweeper, server *httpapi.Server, runToken string) {
	if server.LLMControl != nil {
		if _, err := server.Store.LLMAccount(ctx, runToken); err == nil {
			if err := server.LLMControl.Block(ctx, runToken); err != nil {
				log.Error("managed expired run key block unresolved")
			}
			return
		}
	}
	revokeKey(ctx, log, sweeper, runToken)
}

func managedBlockSweep(ctx context.Context, log *slog.Logger, server *httpapi.Server, after int64, retries *accountRetries) int64 {
	if server.LLMControl == nil {
		return 0
	}
	accounts, err := server.Store.PendingLLMBlocks(ctx, after, 100)
	if err != nil {
		log.Error("managed key block queue unavailable")
		return after
	}
	if len(accounts) == 0 {
		return 0
	}
	for _, account := range accounts {
		after = account.RunID
		now := time.Now()
		if !retries.due(account.RunID, now) {
			continue
		}
		if err := server.LLMControl.Block(ctx, account.RunToken); err != nil {
			log.Error("managed key block retry unresolved", "run", account.RunID, "err", err, "next_attempt_in", retries.failed(account.RunID, now))
		} else {
			retries.succeeded(account.RunID)
		}
		if ctx.Err() != nil {
			break
		}
	}
	return after
}

func managedSettlementSweep(ctx context.Context, log *slog.Logger, server *httpapi.Server, after int64, quietFor time.Duration, retries *accountRetries) int64 {
	if server.LLMControl == nil {
		return 0
	}
	accounts, err := server.Store.UnsettledLLMAccounts(ctx, after, quietFor, 100)
	if err != nil {
		log.Error("managed settlement queue unavailable")
		return after
	}
	if len(accounts) == 0 {
		return 0
	}
	for _, account := range accounts {
		after = account.RunID
		now := time.Now()
		if !retries.due(account.RunID, now) {
			continue
		}
		if err := server.LLMControl.Settle(ctx, account); err != nil {
			log.Warn("managed settlement unresolved", "alias", account.Alias, "state", account.State, "err", err, "next_attempt_in", retries.failed(account.RunID, now))
		} else {
			retries.succeeded(account.RunID)
			log.Info("managed account settled", "alias", account.Alias)
			// Only after the numbers are durable: the report now shows settled
			// figures. A historical Run with no Shift has no report to
			// refresh, so it is skipped rather than treated as an error.
			refreshUsageReports(ctx, log, server, account)
		}
		if ctx.Err() != nil {
			break
		}
	}
	return after
}

func managedCorrectionSweep(ctx context.Context, log *slog.Logger, server *httpapi.Server) {
	if server.LLMControl == nil {
		return
	}
	var after int64
	for ctx.Err() == nil {
		accounts, err := server.Store.CorrectableLLMAccounts(ctx, after, 100)
		if err != nil {
			log.Error("managed correction queue unavailable")
			return
		}
		for _, account := range accounts {
			after = account.RunID
			changed, err := server.LLMControl.Correct(ctx, account)
			if err != nil {
				log.Warn("managed settlement correction unresolved", "alias", account.Alias, "err", err)
			} else if changed {
				log.Info("managed account corrected", "alias", account.Alias)
				refreshUsageReports(ctx, log, server, account)
			}
			if ctx.Err() != nil {
				return
			}
		}
		if len(accounts) < 100 {
			return
		}
	}
}

// refreshUsageReports re-renders the settled Run's usage report. Best-effort:
// a failure is logged and never fails the settlement that already succeeded.
func refreshUsageReports(ctx context.Context, log *slog.Logger, server *httpapi.Server, account store.UnsettledLLMAccount) {
	if server.Engine == nil || account.ShiftID == nil {
		return
	}
	if err := server.Engine.RefreshUsageReport(ctx, *account.ShiftID); err != nil {
		log.Error("usage report refresh failed", "shift", *account.ShiftID, "alias", account.Alias, "err", err)
	}
}

// forgeIDFromEnv is the forge instance id, and there is exactly one of it.
//
// It is read here rather than inline because it feeds THREE places that must
// agree: the key the forge registry is stored under, the default stamped onto
// every resolved Work Target, and the Engine's DefaultForge. Two of those used
// to call os.Getenv separately and only one applied a default, so the registry
// was keyed "forgejo" while every Target carried "" — and publishRound's
// lookup missed on every Shift, silently discarding every review ever written.
// A function with one caller-visible answer is the fix; the test next to it
// pins the part that actually mattered, which is that the default is never
// empty.
func forgeIDFromEnv() string { return envOr("PLOEG_TARGET_FORGE", "forgejo") }

// deliveryRetention is how long a forge delivery id is remembered for dedup.
// Forgejo gives up retrying long before this.
const deliveryRetention = 48 * time.Hour

// orphanSweepEvery is how often every minted credential is reconciled against
// the runs that should still hold one. Well under the key TTL, so a leak is
// caught by this rather than by expiry, and far enough apart that the scan is
// not a load source.
const orphanSweepEvery = 15 * time.Minute

const deployCheckEvery = time.Minute

func revokeForgeToken(ctx context.Context, log *slog.Logger, sweeper forgebroker.Sweeper, id string) {
	if sweeper == nil || id == "" {
		return
	}
	if err := sweeper.RevokeByID(ctx, id); err != nil {
		log.Error("forge credential revoke failed", "token_id", id, "err", err)
	}
}

func revokeKey(ctx context.Context, log *slog.Logger, sweeper llmbroker.Sweeper, runToken string) {
	if sweeper == nil {
		return
	}
	if err := sweeper.RevokeForRun(ctx, runToken); err != nil {
		log.Error("key revoke failed", "run_token_prefix", prefix12(runToken), "err", err)
	}
}

func prefix12(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
