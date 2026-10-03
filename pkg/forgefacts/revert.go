package forgefacts

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// RevertClaim is what a merged pull request says it reverted (ADR-0052).
// Numbers are pull request numbers in the same repository that a title
// beginning with "Revert" names, from the title or the description. SHAs are
// the commits a "This reverts commit <sha>" line names, from the commits or
// the description, lowercased. Both are empty for a pull request that
// reverts nothing.
type RevertClaim struct {
	Numbers []int
	SHAs    []string
}

var (
	revertTitle  = regexp.MustCompile(`(?i)^\s*revert\b`)
	revertSHA    = regexp.MustCompile(`(?i)this reverts commit ([0-9a-f]{7,64})\b`)
	bareRef      = regexp.MustCompile(`(?:^|[^\w/.-])[#!]([0-9]{1,9})\b`)
	qualifiedRef = regexp.MustCompile(`([\w.-]+(?:/[\w.-]+)+)[#!]([0-9]{1,9})\b`)
)

// Reverts reads what change, a merged pull request of repo, reverted.
func Reverts(repo string, change provider.PullRequestChange) RevertClaim {
	var claim RevertClaim
	numbers := map[int]bool{}
	if revertTitle.MatchString(change.Title) {
		for _, text := range []string{change.Title, change.Body} {
			for _, m := range bareRef.FindAllStringSubmatch(text, -1) {
				if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
					numbers[n] = true
				}
			}
			for _, m := range qualifiedRef.FindAllStringSubmatch(text, -1) {
				if !strings.EqualFold(m[1], repo) {
					continue
				}
				if n, err := strconv.Atoi(m[2]); err == nil && n > 0 {
					numbers[n] = true
				}
			}
		}
	}
	shas := map[string]bool{}
	for _, text := range append([]string{change.Body}, change.Commits...) {
		for _, m := range revertSHA.FindAllStringSubmatch(text, -1) {
			shas[strings.ToLower(m[1])] = true
		}
	}
	for n := range numbers {
		claim.Numbers = append(claim.Numbers, n)
	}
	sort.Ints(claim.Numbers)
	for s := range shas {
		claim.SHAs = append(claim.SHAs, s)
	}
	sort.Strings(claim.SHAs)
	return claim
}
