// Package playkpi derives the pull request, CI and change-shape figures of a
// Run card's plays from the facts a forge reported (ADR-0058): when a play
// was opened, became ready, got its first feedback and approval and merged;
// how its CI runs went; and how large, tested and indented its change was.
// Every function is deterministic, an unknown fact stays nil, and none of
// these figures feeds a card's grade or rarity.
package playkpi

import (
	"sort"
	"strings"
	"time"
)

// Review is one review recorded for a play from a forge webhook.
type Review struct {
	Reviewer string
	State    string
	HeadSHA  string
	At       time.Time
}

// Event is one stored conversation event of a play: Kind is comment,
// review_comment, review, push, force_push, ready or draft.
type Event struct {
	Kind    string
	Actor   string
	At      time.Time
	State   string
	HeadSHA string
}

// Event kinds.
const (
	KindComment       = "comment"
	KindReviewComment = "review_comment"
	KindReview        = "review"
	KindPush          = "push"
	KindForcePush     = "force_push"
	KindReady         = "ready"
	KindDraft         = "draft"
)

// Play is what Ploeg stored about one pull request. ActivityCapturedAt is
// nil until a forge activity read succeeded, and Events, Commits,
// FirstCommitAt and ForcePushes are unknown until then. CICapturedAt is nil
// until a CI read succeeded. HeadSHA is the merged head of a merged play and
// the current head otherwise.
type Play struct {
	OpenedAt           *time.Time
	Author             string
	Draft              *bool
	MergedAt           *time.Time
	HeadSHA            string
	Reviews            []Review
	ActivityCapturedAt *time.Time
	ActivityTruncated  bool
	Events             []Event
	Commits            *int
	FirstCommitAt      *time.Time
	ForcePushes        *int
	CICapturedAt       *time.Time
	CISource           string
	CITruncated        bool
	Runs               []Run
}

// Timeline is when a play moved from opened to merged and how its review
// went (ADR-0058). Durations are whole seconds; a duration measured from
// readiness never goes below zero. Comments counts the comments and inline
// review comments of humans other than the author, and Reviewers the
// distinct such humans who submitted a review. ReviewRounds counts the
// distinct head commits humans reviewed, as the grade does (ADR-0050).
// ResponseSeconds is the median time from a request for changes to the
// author's next push. CapturedAt is when the forge's activity was last read,
// nil when only webhook reviews are known, and Truncated says the activity
// read reached its bound.
type Timeline struct {
	OpenedAt        *time.Time `json:"openedAt"`
	ReadyAt         *time.Time `json:"readyAt"`
	FirstFeedbackAt *time.Time `json:"firstFeedbackAt"`
	FirstApprovalAt *time.Time `json:"firstApprovalAt"`
	LastApprovalAt  *time.Time `json:"lastApprovalAt"`
	MergedAt        *time.Time `json:"mergedAt"`
	ToFirstFeedback *int64     `json:"toFirstFeedbackSeconds"`
	ToFirstApproval *int64     `json:"toFirstApprovalSeconds"`
	ApprovalToMerge *int64     `json:"approvalToMergeSeconds"`
	OpenToMerge     *int64     `json:"openToMergeSeconds"`
	ReviewRounds    int        `json:"reviewRounds"`
	Comments        *int       `json:"comments"`
	Reviewers       int        `json:"reviewers"`
	ResponseSeconds *int64     `json:"responseSeconds"`
	Commits         *int       `json:"commits"`
	FirstCommitAt   *time.Time `json:"firstCommitAt"`
	ForcePushes     *int       `json:"forcePushes"`
	CodingSeconds   *int64     `json:"codingSeconds"`
	Truncated       bool       `json:"truncated"`
	CapturedAt      *time.Time `json:"capturedAt"`
}

// Derive computes a play's Timeline and CI. human reports whether a forge
// login is a person, not a login Ploeg acts as. The Timeline is nil when
// nothing about the play's conversation is known, and CI is nil until CI
// was read.
func Derive(p Play, human func(string) bool) (*Timeline, *CI) {
	t := timeline(p, human)
	var readyAt *time.Time
	if t != nil {
		readyAt = t.ReadyAt
	}
	return t, deriveCI(p, readyAt)
}

type verdict struct {
	reviewer, state string
	at              time.Time
}

func timeline(p Play, human func(string) bool) *Timeline {
	if p.OpenedAt == nil && len(p.Reviews) == 0 && p.ActivityCapturedAt == nil {
		return nil
	}
	other := func(login string) bool {
		return human(login) && (p.Author == "" || !strings.EqualFold(login, p.Author))
	}
	t := &Timeline{OpenedAt: utc(p.OpenedAt), MergedAt: utc(p.MergedAt), CapturedAt: utc(p.ActivityCapturedAt),
		Truncated: p.ActivityTruncated}
	captured := p.ActivityCapturedAt != nil

	var verdicts []verdict
	rounds := map[string]bool{}
	for _, r := range p.Reviews {
		if human(r.Reviewer) {
			rounds[r.HeadSHA] = true
		}
		if other(r.Reviewer) {
			verdicts = append(verdicts, verdict{r.Reviewer, r.State, r.At})
		}
	}
	t.ReviewRounds = len(rounds)

	comments := 0
	var feedback []time.Time
	for _, e := range p.Events {
		switch e.Kind {
		case KindReview:
			if other(e.Actor) && !recorded(verdicts, e) {
				verdicts = append(verdicts, verdict{e.Actor, e.State, e.At})
			}
		case KindComment, KindReviewComment:
			if other(e.Actor) {
				comments++
				feedback = append(feedback, e.At)
			}
		}
	}
	if captured {
		t.Comments = &comments
	}
	reviewers := map[string]bool{}
	for _, v := range verdicts {
		feedback = append(feedback, v.at)
		reviewers[strings.ToLower(v.reviewer)] = true
		if v.state != "approved" {
			continue
		}
		if t.FirstApprovalAt == nil || v.at.Before(*t.FirstApprovalAt) {
			t.FirstApprovalAt = utc(&v.at)
		}
		if p.MergedAt != nil && v.at.After(*p.MergedAt) {
			continue
		}
		if t.LastApprovalAt == nil || v.at.After(*t.LastApprovalAt) {
			t.LastApprovalAt = utc(&v.at)
		}
	}
	t.Reviewers = len(reviewers)
	for _, at := range feedback {
		if t.FirstFeedbackAt == nil || at.Before(*t.FirstFeedbackAt) {
			t.FirstFeedbackAt = utc(&at)
		}
	}

	if captured {
		t.ReadyAt = readyAt(p)
		t.ResponseSeconds = responseSeconds(p, verdicts)
		t.Commits, t.FirstCommitAt, t.ForcePushes = p.Commits, utc(p.FirstCommitAt), p.ForcePushes
	}
	t.ToFirstFeedback = sinceReady(t.ReadyAt, t.FirstFeedbackAt)
	t.ToFirstApproval = sinceReady(t.ReadyAt, t.FirstApprovalAt)
	t.ApprovalToMerge = between(t.LastApprovalAt, t.MergedAt)
	t.OpenToMerge = between(t.OpenedAt, t.MergedAt)
	t.CodingSeconds = sinceReady(t.FirstCommitAt, t.ReadyAt)
	return t
}

// SameReview is how far apart a webhook's record of a review and the
// forge's own timestamp of it may be and still be one review.
const SameReview = 2 * time.Minute

func recorded(verdicts []verdict, e Event) bool {
	for _, v := range verdicts {
		gap := v.at.Sub(e.At)
		if gap < 0 {
			gap = -gap
		}
		if strings.EqualFold(v.reviewer, e.Actor) && v.state == e.State && gap <= SameReview {
			return true
		}
	}
	return false
}

func readyAt(p Play) *time.Time {
	if p.OpenedAt == nil {
		return nil
	}
	var toggles []Event
	for _, e := range p.Events {
		if e.Kind == KindReady || e.Kind == KindDraft {
			toggles = append(toggles, e)
		}
	}
	openedDraft := p.Draft != nil && *p.Draft
	if len(toggles) > 0 {
		openedDraft = toggles[0].Kind == KindReady
	}
	if !openedDraft {
		return utc(p.OpenedAt)
	}
	for _, e := range toggles {
		if e.Kind == KindReady {
			return utc(&e.At)
		}
	}
	return nil
}

func responseSeconds(p Play, verdicts []verdict) *int64 {
	var pushes []Event
	for _, e := range p.Events {
		if (e.Kind == KindPush || e.Kind == KindForcePush) && (p.Author == "" || e.Actor == "" || strings.EqualFold(e.Actor, p.Author)) {
			pushes = append(pushes, e)
		}
	}
	var waits []int64
	for _, v := range verdicts {
		if v.state != "changes_requested" {
			continue
		}
		for _, push := range pushes {
			if push.At.After(v.at) {
				waits = append(waits, int64(push.At.Sub(v.at)/time.Second))
				break
			}
		}
	}
	return median(waits)
}

func median(values []int64) *int64 {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	mid := len(sorted) / 2
	m := sorted[mid]
	if len(sorted)%2 == 0 {
		m = (sorted[mid-1] + sorted[mid]) / 2
	}
	return &m
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func between(from, to *time.Time) *int64 {
	if from == nil || to == nil || to.Before(*from) {
		return nil
	}
	s := int64(to.Sub(*from) / time.Second)
	return &s
}

func sinceReady(from, to *time.Time) *int64 {
	if from == nil || to == nil {
		return nil
	}
	s := max(int64(0), int64(to.Sub(*from)/time.Second))
	return &s
}
