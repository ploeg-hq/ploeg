package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/flow"
)

// FlowOptions is what a card's flow figures take from configuration
// (ADR-0057).
type FlowOptions struct {
	// Kinds holds the status kinds of every board that records its
	// statuses. A board absent here uses flow.DefaultKind.
	Kinds flow.Boards
	// Calendars are the working calendars by team. A team absent here uses
	// flow.DefaultCalendar.
	Calendars map[string]flow.Calendar
}

type cardFlowFacts struct {
	provider, scope string
	firstSeen       time.Time
	trackerCreated  *time.Time
	estimate        *int64
	admitted        *time.Time
	statuses        []flow.Entry
	truncated       bool
}

func (c *OperatorCard) loadFlow(ctx context.Context, tx pgx.Tx, id int64) error {
	var admitted time.Time
	err := tx.QueryRow(ctx, `SELECT at FROM audit_log WHERE work_item_id = $1 AND action IN ('work_item.queued', 'work_item.approved')
		ORDER BY id LIMIT 1`, id).Scan(&admitted)
	switch {
	case err == nil:
		at := admitted.UTC()
		c.flowFacts.admitted = &at
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return err
	}
	c.flowFacts.statuses, c.flowFacts.truncated, err = cardStatusEntries(ctx, tx, id)
	return err
}

func (c *OperatorCard) flow(opts CardOptions) *flow.Flow {
	if opts.Flow == nil {
		return nil
	}
	ff := c.flowFacts
	kinds, _ := opts.Flow.Kinds.Lookup(ff.provider, ff.scope)
	calendar, ok := opts.Flow.Calendars[c.Team]
	if !ok {
		calendar = flow.DefaultCalendar()
	}
	facts := flow.Facts{
		Now:             cardNow(opts).UTC(),
		Statuses:        ff.statuses,
		Truncated:       ff.truncated,
		TrackerCreated:  ff.trackerCreated,
		FirstSeen:       ff.firstSeen,
		Admitted:        ff.admitted,
		Open:            c.State != "withdrawn" && c.State != "closed",
		EstimateSeconds: ff.estimate,
		Kinds:           kinds,
		Calendar:        calendar,
	}
	for _, r := range c.started() {
		facts.Runs = append(facts.Runs, flow.Run{Started: r.startedAt.UTC(), Finished: utcPtr(r.finishedAt)})
	}
	for _, p := range c.Plays {
		if !p.openedAt.IsZero() && (facts.FirstPlay == nil || p.openedAt.Before(*facts.FirstPlay)) {
			opened := p.openedAt
			facts.FirstPlay = &opened
		}
	}
	if play := c.latestMerged(); play != nil && play.MergedAt != nil {
		facts.Merge = utcPtr(play.MergedAt)
		facts.ReleaseEnvironment = releaseEnvironment(play, opts.ReleaseEnvironments)
		for _, d := range play.Deployments {
			facts.Deploys = append(facts.Deploys, flow.Deploy{Environment: d.Environment, At: d.FirstDeployedAt})
		}
	}
	if c.Release != nil {
		facts.Release = utcPtr(&c.Release.At)
	}
	for _, k := range c.cracks {
		if k.crack.Mended != nil {
			facts.Restores = append(facts.Restores, flow.Restore{CrackID: k.crack.ID, Confirmed: k.crack.ConfirmedAt, Mended: k.crack.Mended.At})
		}
	}
	out := flow.Compute(facts)
	return &out
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	at := t.UTC()
	return &at
}
