package main

import (
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider/clickup"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestTheBinaryNamesClickUpItemsTheWayClickUpLinksThem(t *testing.T) {
	registerReferenceNamers()
	t.Cleanup(func() { work.RegisterReferenceNamer("clickup", nil) })
	item := work.WorkItem{Provider: "clickup", ExternalID: "86c0abc12"}
	if got, want := work.Reference(item), clickup.Reference("86c0abc12"); got != want {
		t.Errorf("reference = %q, want %q", got, want)
	}
	if got, want := work.Branch(item), "agent/"+clickup.Reference("86c0abc12"); got != want {
		t.Errorf("branch = %q, want %q", got, want)
	}
	if got := work.Branch(work.WorkItem{Provider: "vikunja", ExternalID: "585"}); got != "agent/vik-585" {
		t.Errorf("vikunja branch = %q", got)
	}
}
