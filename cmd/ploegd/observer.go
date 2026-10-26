package main

import (
	"context"
	"log/slog"
	"time"
)

const observerGrantTimeout = 30 * time.Second

type observerGranter interface {
	GrantObserver(ctx context.Context) (bool, error)
}

func grantObserver(ctx context.Context, store observerGranter, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(ctx, observerGrantTimeout)
	defer cancel()
	granted, err := store.GrantObserver(ctx)
	switch {
	case err != nil:
		log.Warn("ploeg_observer grants not applied; dashboards keep the access they had", "err", err)
	case granted:
		log.Info("ploeg_observer grants applied")
	default:
		log.Info("ploeg_observer role absent; no read grants applied")
	}
}
