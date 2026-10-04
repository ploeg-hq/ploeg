package harness

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var pullRequestPathRe = regexp.MustCompile(`^(.*?)/(?:-/)?(?:pulls?|merge_requests)/(\d+)/?$`)

func splitPullRequestLink(link string) (repoPath string, number int) {
	link = strings.TrimSpace(link)
	path := link
	if u, err := url.Parse(link); err == nil && u.Path != "" {
		path = u.Path
	}
	m := pullRequestPathRe.FindStringSubmatch(path)
	if m == nil {
		return "", 0
	}
	n, err := strconv.Atoi(m[2])
	if err != nil || n <= 0 {
		return "", 0
	}
	return m[1], n
}

// PullRequestNumber returns the number a forge pull or merge request URL ends
// with, or 0 when link is not one.
func PullRequestNumber(link string) int {
	_, n := splitPullRequestLink(link)
	return n
}

// PullRequestIn returns the number of link when it is a pull or merge request
// of repository ("owner/name", compared without case), and 0 otherwise.
func PullRequestIn(link, repository string) int {
	repoPath, n := splitPullRequestLink(link)
	if n == 0 || repository == "" {
		return 0
	}
	if !strings.HasSuffix(strings.ToLower(repoPath), "/"+strings.ToLower(repository)) {
		return 0
	}
	return n
}

// Valid reports whether o is a known observation.
func (o DeliveryObservation) Valid() bool {
	switch o {
	case DeliveryOpened, DeliveryUpdated, DeliveryNone, DeliveryUnknown:
		return true
	}
	return false
}

// Delivered reports whether o says the Run opened or moved its pull request.
func (o DeliveryObservation) Delivered() bool {
	return o == DeliveryOpened || o == DeliveryUpdated
}

// DeliveryBinding is what ploegd expects a Run's delivery to name: the Work
// Item's forge id (empty when the Work Item names none), repository
// ("owner/name", empty when its Target never resolved), the branch Ploeg
// derived for the Run, and the target's base branch (empty for the
// repository's default).
type DeliveryBinding struct {
	Forge      string
	Repository string
	Branch     string
	Base       string
}

// Mismatch returns why d does not belong to the Run b describes, or "" when
// it does. A pull request it names must be in b's repository, on b's branch,
// and its URL must be that pull request's.
func (d Delivery) Mismatch(b DeliveryBinding) string {
	switch {
	case !d.Observed.Valid():
		return fmt.Sprintf("delivery observation %q is not a known value", d.Observed)
	case b.Forge != "" && d.Forge != "" && d.Forge != b.Forge:
		return fmt.Sprintf("delivery names forge %q, the Work Item's is %q", d.Forge, b.Forge)
	case b.Repository != "" && !strings.EqualFold(d.Repository, b.Repository):
		return fmt.Sprintf("delivery names repository %q, the Work Item's is %q", d.Repository, b.Repository)
	case b.Branch != "" && d.Branch != b.Branch:
		return fmt.Sprintf("delivery names branch %q, the Run's is %q", d.Branch, b.Branch)
	}
	if d.Observed == DeliveryUnknown {
		return ""
	}
	if d.Number == 0 && d.URL == "" {
		if d.Observed.Delivered() {
			return fmt.Sprintf("delivery observed %q but names no pull request", d.Observed)
		}
		return ""
	}
	if d.Number <= 0 || PullRequestIn(d.URL, d.Repository) != d.Number {
		return fmt.Sprintf("delivery URL %q is not pull request %d of %q", d.URL, d.Number, d.Repository)
	}
	if b.Base != "" && d.Base != "" && d.Base != b.Base {
		return fmt.Sprintf("pull request %d targets %q, the Work Item's base branch is %q", d.Number, d.Base, b.Base)
	}
	if d.Observed.Delivered() && d.Head == "" {
		return fmt.Sprintf("delivery observed %q but names no head commit", d.Observed)
	}
	if d.Observed == DeliveryUpdated && (d.HeadBefore == "" || d.HeadBefore == d.Head) {
		return "delivery observed \"updated\" but the head commit did not move"
	}
	return ""
}
