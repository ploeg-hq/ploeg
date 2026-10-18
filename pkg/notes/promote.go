package notes

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// EvidenceKind names what a promotion without a person's approval rests on.
type EvidenceKind string

// A merged pull request of the work the note came from, or a check that
// passed at that pull request's head.
const (
	EvidenceMergedPullRequest EvidenceKind = "merged-pull-request"
	EvidencePassingCheck      EvidenceKind = "passing-check"
)

// Evidence is one observed fact a promotion cites.
type Evidence struct {
	Kind EvidenceKind
	// Ref names the fact: "forgejo/acme/shop#12" for a pull request,
	// "forgejo/acme/shop@<sha>:<check>" for a check.
	Ref string
}

// String is the evidence as the graph stores it.
func (e Evidence) String() string { return string(e.Kind) + ":" + e.Ref }

func (e Evidence) valid() bool {
	return (e.Kind == EvidenceMergedPullRequest || e.Kind == EvidencePassingCheck) && strings.TrimSpace(e.Ref) != ""
}

// PromotedKey is the key of n promoted to level: one per note and level, so
// a promotion applied twice writes the same note.
func PromotedKey(level Level, key string) string {
	return "promoted/" + string(level) + "/" + key
}

// Promote returns n at the wider level to, citing evidence or approvedBy.
//
// A repository note needs evidence or a person's approval. A Client or
// Tenant note needs a person's approval: evidence alone never reaches other
// repositories. The promoted note keeps its author and trust: a person's OK
// lets a Run's claim reach more work, it does not turn the Run's text into
// instructions. A retraction, an expired note or one a person must still
// decide about is not promoted.
func Promote(n Note, to Level, evidence []Evidence, approvedBy string, contested bool, now time.Time) (Note, error) {
	switch {
	case !to.Wider(n.Level):
		return Note{}, fmt.Errorf("%w: %s is not wider than %s", ErrNotPromotable, to, n.Level)
	case n.Type == TypeRetraction || n.Expired(now) || contested:
		return Note{}, fmt.Errorf("%w: %s is retracted, expired or contested", ErrNotPromotable, n.Key)
	case to == LevelRepository && n.Repository == "":
		return Note{}, fmt.Errorf("%w: %s names no repository", ErrNotPromotable, n.Key)
	case to == LevelClient && n.Client == "":
		return Note{}, fmt.Errorf("%w: %s belongs to no Client", ErrNotPromotable, n.Key)
	}
	var cited []string
	for _, e := range evidence {
		if e.valid() {
			cited = append(cited, e.String())
		}
	}
	approvedBy = strings.TrimSpace(approvedBy)
	if approvedBy == "" {
		if to != LevelRepository {
			return Note{}, ErrNeedsApproval
		}
		if len(cited) == 0 {
			return Note{}, ErrNeedsEvidence
		}
	}
	p := n
	p.Key = PromotedKey(to, n.Key)
	p.Level = to
	p.PromotedFrom = n.Key
	p.Supersedes = ""
	p.Contradicts = nil
	p.Contested = false
	p.Evidence = cited
	p.ApprovedBy = approvedBy
	p.SourceRefs = append(append([]string(nil), n.SourceRefs...), "note:"+n.Key)
	p.WrittenAt = now
	return p, nil
}

// Outcome is how a person ended a pull request's review.
type Outcome string

// Merged; closed after a reviewer asked for changes (a rejection, the
// strongest signal); or closed without a review asking for changes.
const (
	OutcomeMerged   Outcome = "merged"
	OutcomeRejected Outcome = "rejected"
	OutcomeClosed   Outcome = "closed"
)

// ReviewOutcome is what ploegd observed when a pull request it delivered left
// review. Every field comes from Ploeg's records of the forge, never from
// the pull request's text.
type ReviewOutcome struct {
	// Repository is "<forge>/<owner>/<repo>".
	Repository   string
	Number       int
	Outcome      Outcome
	By           string
	WorkItem     string
	RootWorkItem string
	Client       string
	At           time.Time
}

var loginUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Note is the repository-level note a review outcome becomes. ploegd
// composes its text from its own records, so it is trusted; the people it
// names are forge logins, reduced to login characters.
func (r ReviewOutcome) Note() Note {
	ref := r.Repository + "#" + strconv.Itoa(r.Number)
	by := loginUnsafe.ReplaceAllString(r.By, "")
	if len(by) > 64 {
		by = by[:64]
	}
	var title, body string
	switch r.Outcome {
	case OutcomeMerged:
		title = fmt.Sprintf("Pull request #%d was merged", r.Number)
		body = fmt.Sprintf("Pull request %s for Work Item %s was merged", ref, r.WorkItem)
		if by != "" {
			body += " by " + by
		}
		body += ". Its approach was accepted for this repository."
	case OutcomeRejected:
		title = fmt.Sprintf("Pull request #%d was rejected", r.Number)
		body = fmt.Sprintf("Pull request %s for Work Item %s was closed without merging after a reviewer", ref, r.WorkItem)
		if by != "" {
			body += ", " + by + ","
		}
		body += " asked for changes. A rejection is the strongest signal that the approach did not fit this repository: read the review on the pull request before trying it again."
	default:
		title = fmt.Sprintf("Pull request #%d was closed without merging", r.Number)
		body = fmt.Sprintf("Pull request %s for Work Item %s was closed without merging and without a review asking for changes. Read the pull request for why before trying the same approach.", ref, r.WorkItem)
	}
	refs := []string{"pull-request:" + ref}
	if r.WorkItem != "" {
		refs = append(refs, "work-item:"+r.WorkItem)
	}
	return Note{
		Key:            "review/" + ref + "/" + string(r.Outcome),
		RootWorkItem:   r.RootWorkItem,
		WorkItem:       r.WorkItem,
		Repository:     r.Repository,
		Client:         r.Client,
		Type:           TypeReviewOutcome,
		Title:          title,
		Body:           body,
		Tags:           []string{"review", string(r.Outcome)},
		SuggestedLevel: LevelRepository,
		Level:          LevelRepository,
		Subject:        "review:" + ref,
		AuthorKind:     AuthorPloeg,
		Author:         "ploegd",
		SourceRefs:     refs,
		Trust:          Trusted,
		ObservedAt:     r.At,
		WrittenAt:      r.At,
	}
}
