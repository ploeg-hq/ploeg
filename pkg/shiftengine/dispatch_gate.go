package shiftengine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/premise"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// PremiseChecker runs one premise check (ADR-0123).
type PremiseChecker interface {
	Check(ctx context.Context, model string, in premise.Input) (premise.Result, error)
}

// DefaultDependencyRecheck is how often a held Work Item's tracker
// dependencies are read again when Engine.DependencyRecheck is zero.
const DefaultDependencyRecheck = 5 * time.Minute

const (
	dependencyReadTimeout = 10 * time.Second
	premiseCheckTimeout   = 2 * time.Minute
	premiseGiveUpAfter    = 3
	// premiseChecksPerSweep bounds the classifier calls one sweep makes, so
	// a burst of new Work Items cannot hold a sweep for minutes.
	premiseChecksPerSweep = 3
)

// sweepAllowance is how many premise checks the current sweep may still
// run. A nil allowance is the fast path, which never calls a model.
type sweepAllowance struct{ premiseChecks int }

// admit decides whether a queued Work Item without a live Shift may get one
// now: none of its tracker dependencies is open, and its Team's premise
// check, when it has one, has a verdict (ADR-0123).
func (e *Engine) admit(ctx context.Context, workItemID int64, item work.WorkItem, sweep *sweepAllowance) (bool, error) {
	held, err := e.heldForDependencies(ctx, workItemID, item)
	if err != nil || held {
		return false, err
	}
	return e.premiseAdmits(ctx, workItemID, item, sweep)
}

func (e *Engine) heldForDependencies(ctx context.Context, workItemID int64, item work.WorkItem) (bool, error) {
	if !e.HoldDependencies {
		return false, nil
	}
	reader, ok := e.Trackers[item.Provider].(provider.DependencyReader)
	if !ok || item.ExternalID == "" {
		return false, nil
	}
	deps, err := e.Store.WorkItemDependencies(ctx, workItemID)
	if err != nil {
		return false, err
	}
	every := e.DependencyRecheck
	if every <= 0 {
		every = DefaultDependencyRecheck
	}
	if deps.CheckedAt == nil || time.Since(*deps.CheckedAt) >= every {
		readCtx, cancel := context.WithTimeout(ctx, dependencyReadTimeout)
		read, rerr := reader.Dependencies(readCtx, item.ExternalID)
		cancel()
		if rerr != nil {
			e.Log.Warn("tracker dependencies not read; the last reading stands", "work_item", workItemID, "provider", item.Provider, "err", rerr)
		} else {
			rows := make([]store.Dependency, 0, len(read))
			for _, d := range read {
				rows = append(rows, store.Dependency{ExternalID: d.ExternalID, Title: d.Title, Kind: string(d.Kind), Done: d.Done})
			}
			if _, err := e.Store.RecordDependencies(ctx, workItemID, item.Provider, rows); err != nil {
				return false, err
			}
			if deps, err = e.Store.WorkItemDependencies(ctx, workItemID); err != nil {
				return false, err
			}
		}
	}
	open := deps.Open()
	if len(open) == 0 {
		return false, nil
	}
	ids := make([]string, 0, len(open))
	for _, d := range open {
		ids = append(ids, d.ExternalID)
	}
	e.Log.Debug("dispatch held: waiting for dependencies", "work_item", workItemID, "waiting_for", strings.Join(ids, ","))
	return true, nil
}

func (e *Engine) premiseAdmits(ctx context.Context, workItemID int64, item work.WorkItem, sweep *sweepAllowance) (bool, error) {
	model, on := e.PremiseModels[item.Team]
	if !on || e.Premise == nil || item.Origin != work.OriginAssignment {
		return true, nil
	}
	check, err := e.Store.WorkItemPremiseCheck(ctx, workItemID)
	if err != nil {
		return false, err
	}
	if check != nil && check.Verdict != "" {
		return true, nil
	}
	if sweep == nil || sweep.premiseChecks <= 0 {
		return false, nil
	}
	sweep.premiseChecks--
	in := premise.Input{Title: item.Title, Description: item.Description}
	if item.Target != nil {
		in.Repository = item.Target.Owner + "/" + item.Target.Repo
	}
	checkCtx, cancel := context.WithTimeout(ctx, premiseCheckTimeout)
	result, err := e.Premise.Check(checkCtx, model, in)
	cancel()
	if err != nil {
		giveUp := premiseGiveUpAfter
		if premise.Permanent(err) {
			giveUp = 1
		}
		recorded, rerr := e.Store.RecordPremiseFailure(ctx, workItemID, item.Team, model, err.Error(), giveUp)
		if rerr != nil {
			return false, rerr
		}
		e.Log.Warn("premise check failed", "work_item", workItemID, "team", item.Team, "model", model,
			"attempts", recorded.Attempts, "skipped", recorded.Verdict == store.PremiseSkipped, "err", err)
		return recorded.Verdict == store.PremiseSkipped, nil
	}
	recorded, parked, err := e.Store.RecordPremiseVerdict(ctx, workItemID, item.Team, model,
		store.PremiseCheck{Verdict: result.Verdict, Question: result.Question, Reason: result.Reason})
	if err != nil {
		return false, err
	}
	e.Log.Info("premise checked", "work_item", workItemID, "team", item.Team, "model", model, "verdict", recorded.Verdict, "parked", parked)
	if parked {
		e.notifyPremisePark(ctx, item, recorded)
	}
	return recorded.Verdict == store.PremiseProceed || recorded.Verdict == store.PremiseSkipped, nil
}

func (e *Engine) notifyPremisePark(ctx context.Context, item work.WorkItem, check store.PremiseCheck) {
	if item.Provider == work.ProviderPloeg {
		return
	}
	tp, ok := e.Trackers[item.Provider]
	if !ok {
		return
	}
	if err := tp.Comment(ctx, item.ExternalID, premiseMessage(check)); err != nil {
		e.Log.Error("premise park: tracker comment failed", "external_id", item.ExternalID, "err", err)
	}
	if err := tp.SetStatus(ctx, item.ExternalID, work.StateNeedsHuman); err != nil {
		e.Log.Error("premise park: tracker status write failed", "external_id", item.ExternalID, "err", err)
	}
}

// premiseMessage is what the board is told when the premise check parks a
// Work Item. Tracker text the question quotes stays inside a quote block.
func premiseMessage(check store.PremiseCheck) string {
	var b strings.Builder
	if check.Verdict == store.PremiseTooBig {
		b.WriteString("Ploeg did not start this item: its premise check thinks it is too big for one pull request.\n\n")
		fmt.Fprintf(&b, "**Suggested split:**\n> %s\n\n", quoteLines(check.Question))
	} else {
		b.WriteString("Ploeg did not start this item: its premise check needs an answer first.\n\n")
		fmt.Fprintf(&b, "**Question:**\n> %s\n\n", quoteLines(check.Question))
	}
	if check.Reason != "" {
		fmt.Fprintf(&b, "**Why:** %s\n\n", check.Reason)
	}
	b.WriteString("No writer ran and nothing was spent. Answer here or update the description, then assign the task again to start it; the premise check does not run a second time.\n")
	fmt.Fprintf(&b, "\n<sub>Premise check by %s.</sub>", check.Model)
	return b.String()
}

func quoteLines(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n> ") }
