package store

import (
	"context"

	"github.com/ploeg-hq/ploeg/pkg/spendcap"
)

// PoolClaim marks a claim a runtime Team pool makes (ADR-0198 section 7):
// it selects only a Run of an active runtime Team, under that Team's spend
// cap. A claim without it never selects a runtime Team's Run.
type PoolClaim struct {
	TeamID   string
	SpendCap spendcap.Cap
}

type poolClaimKey struct{}

// WithPoolClaim returns ctx carrying a pool claim.
func WithPoolClaim(ctx context.Context, c PoolClaim) context.Context {
	return context.WithValue(ctx, poolClaimKey{}, c)
}

func poolClaimFrom(ctx context.Context) (PoolClaim, bool) {
	c, ok := ctx.Value(poolClaimKey{}).(PoolClaim)
	return c, ok
}

const (
	staticRunPredicate = `(team_id IS NULL OR NOT EXISTS (SELECT 1 FROM teams rt WHERE rt.id = agent_runs.team_id AND rt.source = 'runtime'))`
	poolRunPredicate   = `EXISTS (SELECT 1 FROM teams rt WHERE rt.id = agent_runs.team_id AND rt.source = 'runtime'
		AND rt.state = 'active' AND rt.removed_at IS NULL)`
)

func teamSourcePredicate(ctx context.Context) string {
	if _, ok := poolClaimFrom(ctx); ok {
		return poolRunPredicate
	}
	return staticRunPredicate
}
