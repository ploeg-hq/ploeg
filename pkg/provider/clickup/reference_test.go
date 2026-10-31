package clickup

import (
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

func TestARegisteredClickUpReferenceNamesTheBranchSoClickUpLinksThePullRequest(t *testing.T) {
	work.RegisterReferenceNamer("clickup", Reference)
	t.Cleanup(func() { work.RegisterReferenceNamer("clickup", nil) })
	item := work.WorkItem{Provider: "clickup", ExternalID: "86c0abc12"}
	if got := work.Branch(item); got != "agent/CU-86c0abc12" {
		t.Errorf("branch = %q", got)
	}
	if got := work.Reference(item); got != "CU-86c0abc12" {
		t.Errorf("reference = %q", got)
	}
	if got := (&Provider{}).Reference("86c0abc12"); got != "CU-86c0abc12" {
		t.Errorf("provider reference = %q", got)
	}
}
