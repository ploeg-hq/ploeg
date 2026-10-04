package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// The values of agent_runs.delivery_source (migration 0039).
const (
	DeliverySourceWorker   = "worker"
	DeliverySourceMismatch = "mismatch"
	DeliverySourceLegacy   = "legacy"
)

// ErrCheckpointRefused reports a checkpoint that names another branch than
// the Run's, or a pull request outside the Work Item's repository.
var ErrCheckpointRefused = errors.New("checkpoint refused")

type runBinding struct {
	writes bool
	target work.Target
	branch string
}

func (b runBinding) expected() harness.DeliveryBinding {
	exp := harness.DeliveryBinding{Branch: b.branch}
	if b.target.Resolved() {
		exp.Forge = b.target.Forge
		exp.Repository = b.target.Owner + "/" + b.target.Repo
		exp.Base = b.target.BaseBranch
	}
	return exp
}

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func loadRunBinding(ctx context.Context, q queryRower, runToken string) (runBinding, bool, error) {
	var b runBinding
	var item work.WorkItem
	var shiftBranch string
	err := q.QueryRow(ctx, `
		SELECT r.writes, w.target_forge, w.target_owner, w.target_repo, w.target_base_branch,
		       w.provider, w.external_id, w.source_branch, COALESCE(s.branch, '')
		FROM agent_runs r
		JOIN work_items w ON w.id = r.work_item_id
		LEFT JOIN shifts s ON s.id = r.shift_id
		WHERE r.run_token = $1 AND r.state = 'running'`, runToken).
		Scan(&b.writes, &b.target.Forge, &b.target.Owner, &b.target.Repo, &b.target.BaseBranch,
			&item.Provider, &item.ExternalID, &item.SourceBranch, &shiftBranch)
	if errors.Is(err, pgx.ErrNoRows) {
		return runBinding{}, false, nil
	}
	if err != nil {
		return runBinding{}, false, err
	}
	b.branch = shiftBranch
	if b.branch == "" {
		b.branch = work.Branch(item)
	}
	return b, true, nil
}

func checkpointMismatch(cp work.Checkpoint, b runBinding) string {
	exp := b.expected()
	if cp.Branch != "" && exp.Branch != "" && cp.Branch != exp.Branch {
		return fmt.Sprintf("checkpoint names branch %q, the Run's is %q", cp.Branch, exp.Branch)
	}
	if cp.PRURL != "" && exp.Repository != "" && harness.PullRequestIn(cp.PRURL, exp.Repository) == 0 {
		return fmt.Sprintf("checkpoint pull request %q is not one of %q", cp.PRURL, exp.Repository)
	}
	return ""
}

type deliveryAdmission struct {
	source string
	// changed says why ploegd did not keep the reported outcome; empty when
	// it did.
	changed string
}

// admitDelivery binds a report's delivery facts to the Run (ADR-0059). The
// worker's record is kept when it names the Work Item's forge, repository
// and the Run's branch, and is stored as unknown otherwise. A writing Run's
// pr_opened or pr_updated outcome stands only when the stored record observed
// that delivery; a report with no record is from an older worker and keeps a
// pr_* outcome only with a link to a pull request of the Work Item's
// repository. A reading Run's outcome decides no delivery and is kept. Links
// that name any other pull request are dropped from every Run.
func admitDelivery(rep harnessReport, b runBinding) (harnessReport, deliveryAdmission) {
	exp := b.expected()
	if rep.Delivery == nil {
		a := deliveryAdmission{source: DeliverySourceLegacy}
		if exp.Repository == "" {
			return rep, a
		}
		rep.Links = keepLinks(rep.Links, func(l string) bool {
			return harness.PullRequestNumber(l) == 0 || harness.PullRequestIn(l, exp.Repository) > 0
		})
		if b.writes && rep.Outcome.AssertsDelivery() && !anyPullRequest(rep.Links) {
			a.changed = fmt.Sprintf("the report says %s but names no pull request of %s, and carries no delivery record from the worker",
				rep.Outcome, exp.Repository)
			rep = unconfirmedDelivery(rep, a.changed)
		}
		return rep, a
	}

	d := *rep.Delivery
	a := deliveryAdmission{source: DeliverySourceWorker}
	if why := d.Mismatch(exp); why != "" {
		d = harness.Delivery{Forge: d.Forge, Repository: d.Repository, Branch: d.Branch,
			Observed: harness.DeliveryUnknown, Reason: why}
		a.source = DeliverySourceMismatch
	}
	rep.Delivery = &d
	rep.Links = keepLinks(rep.Links, func(l string) bool {
		return harness.PullRequestNumber(l) == 0 || (d.URL != "" && l == d.URL)
	})

	if !b.writes {
		return rep, a
	}
	switch {
	case d.Observed == harness.DeliveryOpened && rep.Outcome == work.OutcomePRUpdated:
		rep.Outcome = work.OutcomePROpened
	case d.Observed == harness.DeliveryUpdated && rep.Outcome == work.OutcomePROpened:
		rep.Outcome = work.OutcomePRUpdated
	case d.Observed == harness.DeliveryNone && rep.Outcome == work.OutcomePRUpdated && d.Number > 0:
		a.changed = fmt.Sprintf("pull request %d's head commit did not move during the Run", d.Number)
		rep.Outcome = work.OutcomeNoChangeNeeded
		rep.Summary = appendSentence(rep.Summary, "Nothing was pushed: "+a.changed+".")
	case d.Observed == harness.DeliveryUnknown &&
		(rep.Outcome.AssertsDelivery() || rep.Outcome == work.OutcomeNoChangeNeeded):
		a.changed = "Ploeg could not confirm on the forge whether this Run pushed or opened a pull request: " + d.Reason
		rep = unconfirmedDelivery(rep, a.changed)
	case rep.Outcome.AssertsDelivery() && !d.Observed.Delivered():
		a.changed = fmt.Sprintf("the report says %s but the worker observed %q on the forge", rep.Outcome, d.Observed)
		rep = unconfirmedDelivery(rep, a.changed)
	}
	return rep, a
}

func unconfirmedDelivery(rep harnessReport, why string) harnessReport {
	rep.Outcome = work.OutcomeStuck
	rep.StuckReason = appendSentence(why, rep.StuckReason)
	return rep
}

func appendSentence(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "\n\n" + b
}

func keepLinks(links []string, keep func(string) bool) []string {
	out := []string{}
	for _, l := range links {
		if keep(l) {
			out = append(out, l)
		}
	}
	return out
}

func anyPullRequest(links []string) bool {
	for _, l := range links {
		if harness.PullRequestNumber(l) > 0 {
			return true
		}
	}
	return false
}

// RunPullRequest returns the pull request a finished Run's stored facts name:
// the number of its admitted delivery record when it has one, else the last
// link to a pull request of repository ("owner/name"), or of any repository
// when repository is empty because the Work Item has no resolved Target. It
// returns 0 when the Run names none, and for a delivery observed as unknown.
func RunPullRequest(d *harness.Delivery, links []string, repository string) (link string, number int) {
	if d != nil {
		if d.Observed == harness.DeliveryUnknown || d.Number <= 0 {
			return "", 0
		}
		return d.URL, d.Number
	}
	for _, l := range links {
		n := harness.PullRequestNumber(l)
		if repository != "" {
			n = harness.PullRequestIn(l, repository)
		}
		if n > 0 {
			link, number = l, n
		}
	}
	return link, number
}
