package shiftengine

import (
	"context"
	"fmt"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

const reasonHarnessConfig = string(work.FailureHarnessConfig)

func harnessConfigParkMessage(r store.RunReport) string {
	said := strings.Join(strings.Fields(r.Summary), " ")
	if _, line, found := strings.Cut(said, ": "); found {
		said = line
	}
	if runes := []rune(said); len(runes) > maxParkCause {
		said = string(runes[:maxParkCause]) + "…"
	}
	if said == "" {
		said = "no error line was recorded"
	}
	return fmt.Sprintf("shift stopped: the %s's harness rejected its configuration or crashed before calling the model in round %d. "+
		"This is a system configuration problem, not the ticket, so Ploeg parked the item instead of retrying: "+
		"fix the harness image or its configuration, then restart the item. The harness said: %s", r.Role, r.Round, said)
}

func (e *Engine) parkForHarnessConfig(ctx context.Context, si store.ShiftInfo, r store.RunReport, reports []store.RunReport) error {
	e.Log.Error("a harness rejected its configuration; parking the shift for a person",
		"shift", si.ID, "work_item", si.WorkItemID, "team", si.Team, "round", r.Round, "role", r.Role, "summary", r.Summary)
	return e.close(ctx, si, reasonHarnessConfig, harnessConfigParkMessage(r), false, reports)
}
