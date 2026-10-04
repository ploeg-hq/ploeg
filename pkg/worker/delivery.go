package worker

import (
	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func withoutDeliveryClaims(r harness.OutcomeReport) harness.OutcomeReport {
	if r.Outcome.AssertsDelivery() {
		r.Outcome = ""
	}
	r.Links, r.Checkpoint, r.Verification, r.Delivery = nil, nil, nil, nil
	return r
}

func samePullRequest(before, after changeRequest) bool {
	return before.URL != "" && before.URL == after.URL && before.Number == after.Number
}

func observedDelivery(ref harness.RepoRef, branch string, before, after changeRequest, readErr error) *harness.Delivery {
	d := &harness.Delivery{Forge: ref.Forge, Repository: ref.ProjectPath(), Branch: branch}
	if readErr != nil {
		d.Observed, d.Reason = harness.DeliveryUnknown, readErr.Error()
		return d
	}
	if after.URL == "" {
		d.Observed = harness.DeliveryNone
		return d
	}
	d.Number, d.URL, d.Base, d.Head = after.Number, after.URL, after.BaseBranch, after.HeadSHA
	switch {
	case !samePullRequest(before, after):
		d.Observed = harness.DeliveryOpened
	case before.HeadSHA != after.HeadSHA:
		d.Observed, d.HeadBefore = harness.DeliveryUpdated, before.HeadSHA
	default:
		d.Observed, d.HeadBefore = harness.DeliveryNone, before.HeadSHA
	}
	return d
}

func forgeUnreadableBeforeRun(ref harness.RepoRef, branch string, err error) harness.OutcomeReport {
	return harness.OutcomeReport{
		Outcome: work.OutcomeFailed,
		Summary: "could not read the forge before the Run, so it did not start: whether branch " + branch +
			" already has a pull request is unknown: " + err.Error(),
		FailureReason: string(work.FailureInfraNode),
		Delivery:      observedDelivery(ref, branch, changeRequest{}, changeRequest{}, err),
	}
}
