// Package provider defines Ploeg's SPI. Everything vendor-specific lives
// behind these two interfaces; the core never assumes a vendor.
// Stability of this package is the project's compatibility promise.
// See docs/design.md §4.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// TrackerEventKind is a normalized tracker event.
type TrackerEventKind string

const (
	TrackerAssigned   TrackerEventKind = "assigned"
	TrackerUpdated    TrackerEventKind = "updated"
	TrackerUnassigned TrackerEventKind = "unassigned"
	// TrackerClosed is an item marked done or closed in the tracker.
	TrackerClosed TrackerEventKind = "closed"
)

// TrackerEvent is the normalized result of parsing a tracker webhook.
type TrackerEvent struct {
	Kind       TrackerEventKind
	ExternalID string
	Team       string // resolved assignee → team name, when applicable
	// Scope is the vendor's own container for this item — a Vikunja project
	// id, a Jira project key, a GitHub repository. It is OPAQUE to the core,
	// which only ever compares it for equality; interpreting it here would be
	// the vendor leak R7 forbids. Empty when the provider has no such concept.
	Scope Scope
	// Item is the payload's snapshot of the tracker item — the fallback when
	// FetchItem cannot supply authoritative state (thin-payload rule: the
	// webhook is a trigger, the provider read is the truth).
	Item *work.WorkItem
	// Actor is the tracker username that caused the event, when the
	// payload names one.
	Actor string
	// At is when the tracker says the change happened; zero when the
	// payload does not say.
	At time.Time
}

// BoardStatus is where a tracker item sits on its board when read
// (ADR-0051). Scope is the container id, as in TrackerEvent.Scope.
// Statuses holds every status or bucket title the item is in; Labels holds
// its label or tag titles. Created is when the tracker says the item was
// created, zero when it did not say. Estimates is true when the tracker
// keeps a time estimate, and EstimateSeconds is then the item's estimate
// or nil when none is set (ADR-0057).
type BoardStatus struct {
	Scope           string
	Statuses        []string
	Labels          []string
	Created         time.Time
	Estimates       bool
	EstimateSeconds *int64
}

// BoardComment is one comment on a tracker item.
type BoardComment struct {
	Text   string
	Author string
	At     time.Time
}

// BoardReader is implemented by a TrackerProvider that can read an item's
// board status and comments, which Ploeg needs to record gate transitions
// and bounce reasons (ADR-0051). A provider without it records no gates.
type BoardReader interface {
	BoardStatus(ctx context.Context, externalID string) (BoardStatus, error)
	BoardComments(ctx context.Context, externalID string) ([]BoardComment, error)
}

// Scope is a provider-scoped container id: the vendor's own answer to "which
// body of work does this belong to". Ploeg compares Scopes; it never parses
// them.
type Scope struct {
	Kind string // provider-defined: "project", "board", "repo", "list"
	ID   string // provider-scoped, opaque
	Name string // human label for audit and logs — NEVER a routing key
}

// TaskLinker is implemented by a TrackerProvider that can name the page a
// person opens for one of its items. TaskURL returns "" when it cannot.
type TaskLinker interface {
	TaskURL(externalID string) string
}

// TrackerProvider adapts one task-management system (reference: Vikunja).
type TrackerProvider interface {
	Name() string
	// ParseWebhook verifies the request signature and returns normalized events.
	ParseWebhook(r *http.Request) ([]TrackerEvent, error)
	// FetchItem reads the authoritative item for mirroring into a WorkItem.
	FetchItem(ctx context.Context, externalID string) (work.WorkItem, error)
	// Comment writes an audited comment back to the tracker item.
	Comment(ctx context.Context, externalID, htmlBody string) error
	// SetStatus applies the provider's mapping of Ploeg states (label, column…).
	SetStatus(ctx context.Context, externalID string, state work.State) error
}

// ForgeEventKind is a normalized forge event relevant to follow-up routing.
type ForgeEventKind string

const (
	ForgeReviewSubmitted ForgeEventKind = "review_submitted"
	ForgeCheckFailed     ForgeEventKind = "check_failed"
	ForgeMergeStateDirty ForgeEventKind = "merge_state_dirty"
	// ForgePRMerged reports that a pull request was merged.
	ForgePRMerged ForgeEventKind = "pr_merged"
	// ForgePRClosed reports that a pull request was closed without merging.
	ForgePRClosed ForgeEventKind = "pr_closed"
	// ForgePROpened reports that a pull request was opened or reopened.
	ForgePROpened ForgeEventKind = "pr_opened"
	// ForgePRSynchronized reports that new commits reached a pull request's
	// head branch.
	ForgePRSynchronized ForgeEventKind = "pr_synchronized"
)

// PullRequestState is a forge's answer to "what happened to this pull request".
type PullRequestState string

const (
	PullRequestOpen   PullRequestState = "open"
	PullRequestMerged PullRequestState = "merged"
	PullRequestClosed PullRequestState = "closed"
)

// ForgeReviewState is a submitted review's verdict, normalized from the
// forge's own vocabulary.
type ForgeReviewState string

const (
	ForgeReviewApproved         ForgeReviewState = "approved"
	ForgeReviewChangesRequested ForgeReviewState = "changes_requested"
	ForgeReviewCommented        ForgeReviewState = "commented"
)

// ForgeEvent is the normalized result of parsing a forge webhook.
type ForgeEvent struct {
	Kind   ForgeEventKind
	Repo   string
	PR     int
	Branch string
	Body   string // feedback payload for classification
	// Actor is the forge login that caused the event. Empty when the forge
	// did not say.
	Actor string
	// Review is the verdict of a ForgeReviewSubmitted event. Empty when the
	// provider cannot classify it.
	Review ForgeReviewState
	// PullRequest is what the payload said about the pull request itself:
	// its head for every event, and the merge or close facts for
	// ForgePRMerged and ForgePRClosed (ADR-0045).
	PullRequest PullRequestFacts
}

// PullRequestFacts is what a forge reports about one pull request. A zero
// field is a fact the forge did not report.
type PullRequestFacts struct {
	State          PullRequestState
	HeadSHA        string
	MergeCommitSHA string
	MergedAt       *time.Time
	MergedBy       string
	ClosedAt       *time.Time
	// Additions, Deletions and ChangedFiles are the diff size (ADR-0046).
	// Nil is a figure the forge did not report; a reported zero is zero.
	Additions    *int
	Deletions    *int
	ChangedFiles *int
	// OpenedAt is when the forge says the pull request was created, Author
	// the login that opened it and Draft whether it is a draft (or WIP) now
	// (ADR-0058). Nil and empty are facts the forge did not report.
	OpenedAt *time.Time
	Author   string
	Draft    *bool
}

// CommitState is the combined result of the checks on one commit.
type CommitState string

const (
	CommitSuccess CommitState = "success"
	CommitFailure CommitState = "failure"
	CommitPending CommitState = "pending"
	CommitError   CommitState = "error"
)

// CommitCheck is one named check on a commit, in the CommitState vocabulary.
type CommitCheck struct {
	Context string
	State   CommitState
}

// CommitStatus is the combined state of every check a forge reports on one
// commit, with each check. A commit with no checks has no CommitStatus.
type CommitStatus struct {
	SHA    string
	State  CommitState
	Checks []CommitCheck
}

// CommitStatusReader is implemented by a ForgeProvider that can read the
// combined commit status at a pull request's head (ADR-0046). A forge without
// it leaves CI unknown.
type CommitStatusReader interface {
	// CommitStatus reads the combined status of sha in repo. ok is false
	// when the forge reports no check on the commit.
	CommitStatus(ctx context.Context, repo, sha string) (status CommitStatus, ok bool, err error)
}

// ReadCommitStatus reads the combined status at sha when fp can. ok is false
// when fp cannot read statuses, sha is empty or the commit has no check.
func ReadCommitStatus(ctx context.Context, fp ForgeProvider, repo, sha string) (CommitStatus, bool, error) {
	reader, can := fp.(CommitStatusReader)
	if !can || sha == "" || repo == "" {
		return CommitStatus{}, false, nil
	}
	return reader.CommitStatus(ctx, repo, sha)
}

// AncestryReader is implemented by a ForgeProvider that can tell, through
// its compare API, whether one commit is in the history of another
// (ADR-0047). A forge without it never marks a pull request as deployed.
type AncestryReader interface {
	// IsAncestor reports whether ancestor is reachable from descendant in
	// repo. A commit is its own ancestor.
	IsAncestor(ctx context.Context, repo, ancestor, descendant string) (bool, error)
}

// ErrNoAncestry is returned by IsAncestor when the forge cannot compare
// commits.
var ErrNoAncestry = errors.New("forge cannot compare commits")

// IsAncestor asks fp whether ancestor is reachable from descendant in repo.
// Equal commits answer true without a forge read; a forge that cannot
// compare returns ErrNoAncestry.
func IsAncestor(ctx context.Context, fp ForgeProvider, repo, ancestor, descendant string) (bool, error) {
	if ancestor == "" || descendant == "" || repo == "" {
		return false, errors.New("ancestry needs a repository and two commits")
	}
	if strings.EqualFold(ancestor, descendant) {
		return true, nil
	}
	reader, can := fp.(AncestryReader)
	if !can {
		return false, ErrNoAncestry
	}
	return reader.IsAncestor(ctx, repo, ancestor, descendant)
}

// CompareListsCommits reads a forge compare response and reports whether it
// lists any commit. It stops at the first answer, a total_commits count or
// the first entry of the commits array, so a long comparison is never read
// whole. A response with neither is an error.
func CompareListsCommits(body io.Reader) (bool, error) {
	dec := json.NewDecoder(body)
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return false, errors.New("compare response is not a JSON object")
	}
	sawCommits := false
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return false, fmt.Errorf("compare response: %w", err)
		}
		switch key, _ := tok.(string); key {
		case "total_commits":
			var total int
			if err := dec.Decode(&total); err != nil {
				return false, fmt.Errorf("compare response total_commits: %w", err)
			}
			return total > 0, nil
		case "commits":
			open, err := dec.Token()
			if err != nil {
				return false, fmt.Errorf("compare response commits: %w", err)
			}
			if open == nil {
				sawCommits = true
				continue
			}
			if open != json.Delim('[') {
				return false, errors.New("compare response commits is not an array")
			}
			if dec.More() {
				return true, nil
			}
			if _, err := dec.Token(); err != nil {
				return false, fmt.Errorf("compare response commits: %w", err)
			}
			sawCommits = true
		default:
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return false, fmt.Errorf("compare response: %w", err)
			}
		}
	}
	if !sawCommits {
		return false, errors.New("compare response has neither total_commits nor commits")
	}
	return false, nil
}

// CombineCommitStates folds the states of several checks into one: any
// failure fails, then any error errors, then anything pending is pending,
// and only all-success succeeds. ok is false when states is empty.
func CombineCommitStates(states []CommitState) (CommitState, bool) {
	if len(states) == 0 {
		return "", false
	}
	seen := map[CommitState]bool{}
	for _, s := range states {
		seen[s] = true
	}
	for _, s := range []CommitState{CommitFailure, CommitError, CommitPending} {
		if seen[s] {
			return s, true
		}
	}
	return CommitSuccess, true
}

// ParseForgeTime reads a forge timestamp in RFC 3339 or GitLab's older
// "2006-01-02 15:04:05 UTC" form. It returns nil for an empty or unreadable
// value, so a timestamp the forge did not give stays unknown.
func ParseForgeTime(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05 MST", "2006-01-02 15:04:05 -0700"} {
		if t, err := time.Parse(layout, s); err == nil {
			utc := t.UTC()
			return &utc
		}
	}
	return nil
}

// Comment is one conversation comment on a pull request, as the forge reports
// it. ID is the forge's own comment identifier, used to edit it in place.
type Comment struct {
	ID   int64
	Body string
}

// ForgeProvider adapts one git forge (reference: Forgejo).
type ForgeProvider interface {
	Name() string
	ParseWebhook(r *http.Request) ([]ForgeEvent, error)
	Comment(ctx context.Context, repo string, pr int, body string) error
	// Comments lists a pull request's conversation comments, oldest-first.
	// Implementations return the WHOLE thread, following pagination to
	// exhaustion: a caller that looks for a marker on any page must not miss
	// one the forge put on a later page.
	Comments(ctx context.Context, repo string, pr int) ([]Comment, error)
	// EditComment replaces the body of one existing comment.
	EditComment(ctx context.Context, repo string, pr int, id int64, body string) error
	// PullRequestState reads whether a pull request is open, merged, or closed
	// without merging. repo is the forge's project path; pr is the number a
	// human sees in the forge's UI.
	PullRequestState(ctx context.Context, repo string, pr int) (PullRequestState, error)
	// PullRequestFacts reads the same pull request with its head and, once
	// it left the open state, its merge or close facts (ADR-0045).
	PullRequestFacts(ctx context.Context, repo string, pr int) (PullRequestFacts, error)
}
