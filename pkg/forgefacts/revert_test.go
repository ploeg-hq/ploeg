package forgefacts

import (
	"reflect"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

func TestReverts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change provider.PullRequestChange
		want   RevertClaim
	}{
		{"a forge revert names the pull request in its body",
			provider.PullRequestChange{Title: `Revert "Add billing"`, Body: "Reverts webgrip/ploeg#68"},
			RevertClaim{Numbers: []int{68}}},
		{"a hand-written revert names it in the title",
			provider.PullRequestChange{Title: "revert #12 and !13", Body: "see other/repo#99"},
			RevertClaim{Numbers: []int{12, 13}}},
		{"a git revert commit names the reverted commit",
			provider.PullRequestChange{Title: "Undo the billing change", Commits: []string{
				"Revert \"Add billing\"\n\nThis reverts commit ABCDEF1234567890abcdef1234567890abcdef12.",
				"This reverts commit abcdef1, reversing changes made to 0000000."}},
			RevertClaim{SHAs: []string{"abcdef1", "abcdef1234567890abcdef1234567890abcdef12"}}},
		{"a pull request that mentions a number but is not a revert claims nothing",
			provider.PullRequestChange{Title: "Fix #12", Body: "follow-up to #11"},
			RevertClaim{}},
		{"a reference to another repository is not this repository's pull request",
			provider.PullRequestChange{Title: "Revert", Body: "Reverts other/ploeg#4"},
			RevertClaim{}},
		{"a too-short sha is not a commit",
			provider.PullRequestChange{Title: "x", Commits: []string{"This reverts commit abc12."}},
			RevertClaim{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Reverts("webgrip/ploeg", tc.change); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Reverts = %+v; want %+v", got, tc.want)
			}
		})
	}
}
