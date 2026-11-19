package store

import (
	"context"
	"regexp"

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

// PoolClaimFrom returns the pool claim ctx carries.
func PoolClaimFrom(ctx context.Context) (PoolClaim, bool) { return poolClaimFrom(ctx) }

func poolClaimFrom(ctx context.Context) (PoolClaim, bool) {
	c, ok := ctx.Value(poolClaimKey{}).(PoolClaim)
	return c, ok
}

var teamIDForm = regexp.MustCompile(`^team_[0-9a-hjkmnp-tv-z]{26}$`)

const staticRunPredicate = `(team_id IS NULL OR NOT EXISTS (SELECT 1 FROM teams rt WHERE rt.id = agent_runs.team_id AND rt.source = 'runtime'))`

// teamSourcePredicate keeps a claim on its side: a static Team's worker
// never selects a runtime Team's Run, and a pool selects only a Run of the
// active runtime Team its marker names, by id, so another Tenant's Team
// with the same key is never reached. A marker without a valid id selects
// nothing.
func teamSourcePredicate(ctx context.Context) string {
	pool, ok := poolClaimFrom(ctx)
	if !ok {
		return staticRunPredicate
	}
	if !teamIDForm.MatchString(pool.TeamID) {
		return "false"
	}
	return `agent_runs.team_id = '` + pool.TeamID + `' AND EXISTS (SELECT 1 FROM teams rt WHERE rt.id = agent_runs.team_id
		AND rt.source = 'runtime' AND rt.state = 'active' AND rt.removed_at IS NULL)`
}
