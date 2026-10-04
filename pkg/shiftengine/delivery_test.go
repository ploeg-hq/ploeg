package shiftengine

import (
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

// VIK-1732 (ADR-0059): a writer's pr_* outcome makes a Shift ready for review
// only when its stored delivery record observed that delivery. A Run from an
// older worker, with no record, still counts.
func TestReadyForReviewNeedsAnObservedDelivery(t *testing.T) {
	head := strings.Repeat("a", 40)
	writer := func(outcome string, d *harness.Delivery) []store.RunReport {
		return []store.RunReport{{Role: "builder", Writes: true, Outcome: outcome, Delivery: d}}
	}
	for name, tc := range map[string]struct {
		reports []store.RunReport
		want    bool
	}{
		"opened": {writer("pr_opened", &harness.Delivery{Observed: harness.DeliveryOpened, Number: 1, Head: head}), true},
		"updated": {writer("pr_updated", &harness.Delivery{Observed: harness.DeliveryUpdated, Number: 1, Head: head,
			HeadBefore: strings.Repeat("b", 40)}), true},
		"older worker":  {writer("pr_opened", nil), true},
		"unknown":       {writer("pr_opened", &harness.Delivery{Observed: harness.DeliveryUnknown}), false},
		"none":          {writer("pr_updated", &harness.Delivery{Observed: harness.DeliveryNone, Number: 1}), false},
		"no pr outcome": {writer("no_change_needed", &harness.Delivery{Observed: harness.DeliveryNone}), false},
	} {
		if got := readyForReview(reasonPlanExhausted, tc.reports); got != tc.want {
			t.Errorf("%s: readyForReview = %v, want %v", name, got, tc.want)
		}
	}
}
