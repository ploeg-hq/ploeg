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
