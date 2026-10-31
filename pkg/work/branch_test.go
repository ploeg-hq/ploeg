package work

import "testing"

func TestBranchDependsOnTrackerAndKeepsVikunjaFormat(t *testing.T) {
	for _, tc := range []struct {
		provider, externalID, want string
	}{
		{"vikunja", "585", "agent/vik-585"},
		{"", "585", "agent/vik-585"},
		{"clickup", "86c0abc12", "agent/clickup-86c0abc12"},
		{"clickup", "ENG-12", "agent/clickup-ENG-12"},
		{"clickup", "a/b..c d~^:?*[\\", "agent/clickup-a-b--c-d-------"},
	} {
		if got := Branch(WorkItem{Provider: tc.provider, ExternalID: tc.externalID}); got != tc.want {
			t.Errorf("Branch(%q, %q) = %q, want %q", tc.provider, tc.externalID, got, tc.want)
		}
	}
}

func TestReferenceDependsOnTrackerAndKeepsVikunjaFormat(t *testing.T) {
	for _, tc := range []struct {
		provider, externalID, want string
	}{
		{"vikunja", "585", "VIK-585"},
		{"", "585", "VIK-585"},
		{"clickup", "86c0abc12", "clickup-86c0abc12"},
		{"clickup", "a/b c", "clickup-a-b-c"},
	} {
		if got := Reference(WorkItem{Provider: tc.provider, ExternalID: tc.externalID}); got != tc.want {
			t.Errorf("Reference(%q, %q) = %q, want %q", tc.provider, tc.externalID, got, tc.want)
		}
	}
}

func TestARegisteredReferenceNamerNamesTheBranchAndReferenceAndLeavesOthersAlone(t *testing.T) {
	RegisterReferenceNamer("tracker-x", func(id string) string { return "TX-" + id })
	t.Cleanup(func() { RegisterReferenceNamer("tracker-x", nil) })
	for _, tc := range []struct {
		item          WorkItem
		branch, refer string
	}{
		{WorkItem{Provider: "tracker-x", ExternalID: "86c0abc12"}, "agent/TX-86c0abc12", "TX-86c0abc12"},
		{WorkItem{Provider: "tracker-x", ExternalID: "a/b c"}, "agent/TX-a-b-c", "TX-a-b-c"},
		{WorkItem{Provider: "tracker-x", ExternalID: "1", SourceBranch: "agent/old-1"}, "agent/old-1", "TX-1"},
		{WorkItem{Provider: "vikunja", ExternalID: "585"}, "agent/vik-585", "VIK-585"},
		{WorkItem{Provider: "other", ExternalID: "7"}, "agent/other-7", "other-7"},
	} {
		if got := Branch(tc.item); got != tc.branch {
			t.Errorf("Branch(%+v) = %q, want %q", tc.item, got, tc.branch)
		}
		if got := Reference(tc.item); got != tc.refer {
			t.Errorf("Reference(%+v) = %q, want %q", tc.item, got, tc.refer)
		}
	}
}
