package plan

// ReviewOnly reduces the plan to one Round holding the reading Roles of its
// last Round that has a reader who is neither a shadow nor a planner, with a
// pool of those Roles' caps and no fix loop (ADR-0106). It is the plan a
// Shift runs when it explains a pull request someone else opened: nothing may
// write to that branch, and no verdict re-opens a writer. It reports false
// when the plan has no such Round. A reading Role without a cap leaves the
// pool unmetered, as the Team configured it; otherwise the pool is the sum of
// the caps, bounded by the Team's own pool.
func (p TeamPlan) ReviewOnly() (TeamPlan, bool) {
	for i := len(p.Rounds) - 1; i >= 0; i-- {
		round := p.Rounds[i]
		acting := false
		for _, r := range round.Roles {
			if !r.Writes && !r.Shadow && !r.Planner {
				acting = true
			}
		}
		if !acting {
			continue
		}
		var roles []Role
		pool, unmetered := 0.0, false
		for _, r := range round.Roles {
			if r.Writes || r.Planner {
				continue
			}
			roles = append(roles, r)
			if r.CapUSD() <= 0 {
				unmetered = true
			}
			pool += r.CapUSD()
		}
		out := TeamPlan{Rounds: []Round{{Roles: roles}}}
		if !unmetered {
			if p.PoolUSD() > 0 && p.PoolUSD() < pool {
				pool = p.PoolUSD()
			}
			out.Pool = money(pool)
		}
		return out, true
	}
	return TeamPlan{}, false
}

// PoolUSD is the plan's Shift pool in US dollars; zero is unmetered.
func (p TeamPlan) PoolUSD() float64 { return float64(p.Pool) }
