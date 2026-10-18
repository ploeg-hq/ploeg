// Package notes holds the rules every note in a Tenant's notes graph follows
// (ADR-0122): who wrote it and from what, whether its text may be trusted,
// which Runs may read it, when it stops being read, when a newer note
// replaces it, when two notes disagree, and when a note may reach a wider
// level. It does no I/O: ploegd's notes graph client stores what these rules
// decide, and the briefing reads a Run's notes through ForRun.
package notes

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Level says which Runs read a note.
type Level string

// The levels, narrowest first. Work Item and repository notes live in the
// notes graph; Client and Tenant knowledge only ever lives in the reviewed
// knowledge graph, through a promotion a person approved.
const (
	LevelWorkItem   Level = "work-item"
	LevelRepository Level = "repository"
	LevelClient     Level = "client"
	LevelTenant     Level = "tenant"
)

var levelRank = map[Level]int{LevelWorkItem: 0, LevelRepository: 1, LevelClient: 2, LevelTenant: 3}

// Valid reports whether l is one of the four levels.
func (l Level) Valid() bool {
	_, ok := levelRank[l]
	return ok
}

// Wider reports whether l reaches more Runs than other.
func (l Level) Wider(other Level) bool {
	return l.Valid() && other.Valid() && levelRank[l] > levelRank[other]
}

// InNotesGraph reports whether a note at l is stored in the notes graph.
func (l Level) InNotesGraph() bool {
	return l == LevelWorkItem || l == LevelRepository
}

// AuthorKind says what wrote a note.
type AuthorKind string

// A Run's agent, a person through an operator consumer, or ploegd itself
// recording what it observed.
const (
	AuthorRun    AuthorKind = "run"
	AuthorPerson AuthorKind = "person"
	AuthorPloeg  AuthorKind = "ploeg"
)

// Valid reports whether k is a known author kind.
func (k AuthorKind) Valid() bool {
	return k == AuthorRun || k == AuthorPerson || k == AuthorPloeg
}

// Trust says how a note's text reaches an agent.
type Trust string

// Untrusted text is only ever rendered as quoted data. Trusted text is a
// person's own words or a sentence ploegd composed from its records.
const (
	Trusted   Trust = "trusted"
	Untrusted Trust = "untrusted"
)

// TypeRetraction is the type of the note a person's decision appends to take
// an older note out of every briefing. It is history, never briefed.
const TypeRetraction = "Retraction"

// TypeReviewOutcome is the type of the note ploegd writes when a pull
// request is merged, rejected or closed.
const TypeReviewOutcome = "ReviewOutcome"

// Note is one note with its provenance. Contested is the only field the
// graph does not store: ForRun sets it from the open review flags.
type Note struct {
	Key          string
	RootWorkItem string
	WorkItem     string
	Repository   string
	Client       string

	Type        string
	Title       string
	Description string
	Resource    string
	Tags        []string
	Body        string

	SuggestedLevel Level
	Level          Level
	Subject        string

	AuthorKind AuthorKind
	// Author is the Run's trace alias, the person's id, or "ploegd".
	Author     string
	SourceRefs []string
	Trust      Trust
	ObservedAt time.Time
	WrittenAt  time.Time
	// ExpiresAt is zero when the note does not expire.
	ExpiresAt time.Time

	Supersedes   string
	Contradicts  []string
	PromotedFrom string
	Evidence     []string
	ApprovedBy   string

	Contested bool
}

// Normalize fills what a note written before ADR-0122 lacks: its level,
// author, trust, observation time and subject. writtenBy is the legacy
// "<kind>:<trace>" attribution.
func (n Note) Normalize(writtenBy string) Note {
	if n.Level == "" {
		n.Level = LevelWorkItem
	}
	if n.AuthorKind == "" {
		kind, author, _ := strings.Cut(writtenBy, ":")
		switch AuthorKind(kind) {
		case AuthorPloeg:
			n.AuthorKind = AuthorPloeg
		case AuthorPerson:
			n.AuthorKind = AuthorPerson
		default:
			n.AuthorKind = AuthorRun
		}
		if n.Author == "" {
			n.Author = author
		}
	}
	if n.Trust == "" {
		n.Trust = Untrusted
	}
	if n.ObservedAt.IsZero() {
		n.ObservedAt = n.WrittenAt
	}
	if n.Subject == "" {
		n.Subject = SubjectOf(n.Type, n.Title)
	}
	return n
}

// WrittenBy is the legacy attribution older readers of the graph still use.
func (n Note) WrittenBy() string {
	return string(n.AuthorKind) + ":" + n.Author
}

// Errors Validate and Promote return.
var (
	ErrInvalid          = errors.New("note is invalid")
	ErrNeedsApproval    = errors.New("a promotion to this level needs a person's approval")
	ErrNeedsEvidence    = errors.New("a promotion needs evidence or a person's approval")
	ErrNotPromotable    = errors.New("note cannot be promoted")
	ErrUntrustedByRun   = errors.New("a Run's text is never trusted")
	ErrLevelNeedsReview = errors.New("client and tenant knowledge is written only by an approved promotion to the knowledge graph")
)

// MaxExpiry bounds how far ahead a note may set its own expiry.
const MaxExpiry = 366 * 24 * time.Hour

// Validate checks the provenance rules a note must meet before it is stored.
func (n Note) Validate() error {
	invalid := func(why string) error { return fmt.Errorf("%w: %s %s", ErrInvalid, n.Key, why) }
	switch {
	case n.Key == "":
		return invalid("has no key")
	case strings.TrimSpace(n.Type) == "" || strings.TrimSpace(n.Title) == "" || strings.TrimSpace(n.Body) == "":
		return invalid("lacks a type, title or body")
	case !n.Level.Valid():
		return invalid("has no valid level")
	case !n.Level.InNotesGraph():
		return ErrLevelNeedsReview
	case !n.AuthorKind.Valid() || n.Author == "":
		return invalid("names no author")
	case n.Trust != Trusted && n.Trust != Untrusted:
		return invalid("has no trust")
	case n.AuthorKind == AuthorRun && n.Trust == Trusted:
		return ErrUntrustedByRun
	case n.ObservedAt.IsZero() || n.WrittenAt.IsZero():
		return invalid("has no observation or write time")
	case !n.ExpiresAt.IsZero() && !n.ExpiresAt.After(n.ObservedAt):
		return invalid("expires before it was observed")
	case !n.ExpiresAt.IsZero() && n.ExpiresAt.Sub(n.WrittenAt) > MaxExpiry:
		return invalid("expires more than a year ahead")
	case n.Level == LevelWorkItem && n.RootWorkItem == "":
		return invalid("is a Work Item note without a root Work Item")
	case n.Level == LevelRepository && n.Repository == "":
		return invalid("is a repository note without a repository")
	case n.Level == LevelRepository && n.AuthorKind == AuthorRun && len(n.Evidence) == 0 && n.ApprovedBy == "":
		return ErrNeedsEvidence
	case n.Supersedes == n.Key:
		return invalid("supersedes itself")
	}
	return nil
}

var subjectUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// SubjectOf is the subject of a note that names none: its type and title,
// folded to lower case words, so a later note with the same title is about
// the same thing.
func SubjectOf(typ, title string) string {
	slug := strings.Trim(subjectUnsafe.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(slug) > 80 {
		slug = strings.Trim(slug[:80], "-")
	}
	return strings.ToLower(strings.TrimSpace(typ)) + ":" + slug
}

// Expired reports whether no Run reads n at now.
func (n Note) Expired(now time.Time) bool {
	return !n.ExpiresAt.IsZero() && !now.Before(n.ExpiresAt)
}

// sameScope reports whether a and b are read by the same Runs.
func sameScope(a, b Note) bool {
	if a.Level != b.Level || a.Client != b.Client {
		return false
	}
	switch a.Level {
	case LevelWorkItem:
		return a.RootWorkItem == b.RootWorkItem
	case LevelRepository:
		return a.Repository == b.Repository
	}
	return false
}

func sameText(a, b Note) bool {
	fold := func(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }
	return fold(a.Body) == fold(b.Body) && fold(a.Title) == fold(b.Title)
}
