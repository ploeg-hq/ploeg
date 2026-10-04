// Package followup decides what happens to the Work Items a Run proposes
// (Product R12, ADR-0031): which are stored, for which Team, in which state,
// with how much budget, and why any are rejected. It is pure, so every limit
// is testable without a database. pkg/store applies the decisions.
package followup

import (
	"fmt"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// Policy is one Team's limits on the work its Runs create. The zero value
// creates nothing; Default is the conservative starting point.
type Policy struct {
	// AutoDispatch queues accepted Work Items directly. False holds them as
	// proposed until a person approves each one.
	AutoDispatch bool
	// MaxCreatedPerRun bounds how many Work Items one Run may create.
	MaxCreatedPerRun int
	// MaxDepth bounds the chain of created work: a Work Item from a tracker
	// or an operator consumer has depth 0, and a created one has its source's depth plus one.
	MaxDepth int
	// MaxOpen bounds how many created Work Items from this Team's Runs may be
	// open (proposed, queued or leased) at once.
	MaxOpen int
	// ItemBudgetUSD is the Shift pool each created Work Item is allotted.
	ItemBudgetUSD float64
	// PoolUSD bounds the budget allotted to all created Work Items that
	// descend from one root Work Item.
	PoolUSD float64
	// RefinementTeam receives created Work Items that are not Ready. Empty
	// leaves them proposed for a person.
	RefinementTeam string
}

// Default returns the limits a Team gets when its configuration says
// nothing: nothing is dispatched without approval, and every bound is small.
func Default() Policy {
	return Policy{
		MaxCreatedPerRun: 5,
		MaxDepth:         2,
		MaxOpen:          20,
		ItemBudgetUSD:    2,
		PoolUSD:          10,
	}
}

// Source describes the Run that proposed the Work Items and what already
// exists around it.
type Source struct {
	Team    string
	Outcome work.Outcome
	// Depth is the source Work Item's depth.
	Depth int
	// OpenCreated counts open created Work Items from this Team's Runs.
	OpenCreated int
	// PoolAllotted is the budget already allotted to created Work Items in
	// the source's tree.
	PoolAllotted float64
}

// Decision is the fate of one proposed Work Item, in the order proposed.
type Decision struct {
	Index    int
	Proposal harness.CreatedWorkItem
	Accepted bool
	// Reason explains a rejection. Empty when accepted.
	Reason    string
	Team      string
	State     work.State
	Depth     int
	BudgetUSD float64
}

// Decide applies a Policy to a Run's proposals. knownTeam reports whether a
// requested Team exists; a nil func accepts only the source Team.
//
// Accepted work is proposed unless the Policy sets AutoDispatch. Even then,
// work that is not Ready is queued only when a RefinementTeam receives it;
// without one it stays proposed, because no unattended Run can make it Ready.
func Decide(p Policy, src Source, proposals []harness.CreatedWorkItem, knownTeam func(string) bool) []Decision {
	out := make([]Decision, 0, len(proposals))
	accepted := 0
	allotted := src.PoolAllotted
	depth := src.Depth + 1
	for i, prop := range proposals {
		d := Decision{Index: i, Proposal: prop, Depth: depth}
		d.Team = src.Team
		if prop.Team != "" {
			d.Team = prop.Team
		}
		if !prop.Ready && p.RefinementTeam != "" {
			d.Team = p.RefinementTeam
		}
		switch {
		case src.Outcome == work.OutcomeFailed:
			d.Reason = "the source Run failed; its retry may propose the work again"
		case depth > p.MaxDepth:
			d.Reason = fmt.Sprintf("depth %d exceeds maxDepth %d", depth, p.MaxDepth)
		case accepted >= p.MaxCreatedPerRun:
			d.Reason = fmt.Sprintf("maxCreatedPerRun %d reached", p.MaxCreatedPerRun)
		case src.OpenCreated+accepted >= p.MaxOpen:
			d.Reason = fmt.Sprintf("maxOpen %d reached: team %s already has %d open created Work Items", p.MaxOpen, src.Team, src.OpenCreated+accepted)
		case d.Team != src.Team && (knownTeam == nil || !knownTeam(d.Team)):
			d.Reason = fmt.Sprintf("unknown team %q", d.Team)
		case p.ItemBudgetUSD <= 0 || allotted+p.ItemBudgetUSD > p.PoolUSD+1e-9:
			d.Reason = fmt.Sprintf("created-work pool exhausted: %.2f of %.2f USD allotted in this tree, %.2f needed", allotted, p.PoolUSD, p.ItemBudgetUSD)
		default:
			d.Accepted = true
			d.BudgetUSD = p.ItemBudgetUSD
			d.State = stateFor(p, prop.Ready)
			accepted++
			allotted += p.ItemBudgetUSD
		}
		out = append(out, d)
	}
	return out
}

func stateFor(p Policy, ready bool) work.State {
	if !p.AutoDispatch {
		return work.StateProposed
	}
	if !ready && p.RefinementTeam == "" {
		return work.StateProposed
	}
	return work.StateQueued
}
