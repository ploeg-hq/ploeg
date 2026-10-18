package harness

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ReviewFinding is one thing a reading Run found, in a shape a later Run can
// answer by id (ADR-0121). ploegd assigns ID when it stores the finding; a
// reader that raises an earlier finding again names that finding's ID.
type ReviewFinding struct {
	ID       string `json:"id,omitempty"`
	Severity string `json:"severity,omitempty"`
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
}

// FindingAnswer is a writing Run's answer to one finding it was briefed with.
type FindingAnswer struct {
	FindingID string `json:"findingId"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
}

// Question is what a Run asks instead of guessing. ploegd assigns ID when it
// stores the question.
type Question struct {
	ID   string `json:"id,omitempty"`
	To   string `json:"to"`
	Text string `json:"text"`
}

// QuestionAnswer is a Run's answer to a question it was briefed with.
type QuestionAnswer struct {
	QuestionID string `json:"questionId"`
	Answer     string `json:"answer"`
}

// The values of ReviewFinding.Severity.
const (
	SeverityBlocking  = "blocking"
	SeverityShouldFix = "should_fix"
	SeverityNit       = "nit"
)

// The values of FindingAnswer.Status.
const (
	AnswerFixed      = "fixed"
	AnswerDisagree   = "disagree"
	AnswerOutOfScope = "out_of_scope"
	AnswerDeferred   = "deferred"
)

// The values of Question.To.
const (
	AskPerson   = "person"
	AskReviewer = "reviewer"
	AskNextRun  = "next_run"
)

// Limits of the blackboard fields of an OutcomeReport.
const (
	MaxFindingItems      = 50
	MaxFindingTitleLen   = 200
	MaxFindingBodyLen    = 4000
	MaxFindingPathLen    = 512
	MaxFindingAnswers    = 100
	MaxAnswerReasonLen   = 2000
	MaxQuestions         = 5
	MaxQuestionTextLen   = 2000
	MaxQuestionAnswers   = 20
	MaxQuestionAnswerLen = 4000
)

// BlackboardIDPattern is the shape of the ids ploegd assigns: f<run>-<n> for a
// finding and q<run>-<n> for a question, where run is the Run's id.
var BlackboardIDPattern = regexp.MustCompile(`^[fq][1-9][0-9]{0,18}-[1-9][0-9]{0,3}$`)

// ValidSeverity reports whether s is empty or a known severity.
func ValidSeverity(s string) bool {
	return s == "" || s == SeverityBlocking || s == SeverityShouldFix || s == SeverityNit
}

// ValidAnswerStatus reports whether s is a known answer status.
func ValidAnswerStatus(s string) bool {
	return s == AnswerFixed || s == AnswerDisagree || s == AnswerOutOfScope || s == AnswerDeferred
}

// ValidQuestionTo reports whether s is a known addressee.
func ValidQuestionTo(s string) bool {
	return s == AskPerson || s == AskReviewer || s == AskNextRun
}

func (f ReviewFinding) validate() error {
	switch {
	case f.ID != "" && !BlackboardIDPattern.MatchString(f.ID):
		return fmt.Errorf("id must be an id from the briefing")
	case !ValidSeverity(f.Severity):
		return fmt.Errorf("severity must be blocking, should_fix or nit")
	case strings.TrimSpace(f.Title) == "" || utf8.RuneCountInString(f.Title) > MaxFindingTitleLen:
		return fmt.Errorf("title must be 1 to %d characters", MaxFindingTitleLen)
	case utf8.RuneCountInString(f.Body) > MaxFindingBodyLen:
		return fmt.Errorf("body exceeds %d characters", MaxFindingBodyLen)
	case utf8.RuneCountInString(f.Path) > MaxFindingPathLen:
		return fmt.Errorf("path exceeds %d characters", MaxFindingPathLen)
	case f.Line < 0:
		return fmt.Errorf("line must not be negative")
	}
	return nil
}

func (a FindingAnswer) validate() error {
	switch {
	case !BlackboardIDPattern.MatchString(a.FindingID) || a.FindingID[0] != 'f':
		return fmt.Errorf("findingId must be a finding id from the briefing")
	case !ValidAnswerStatus(a.Status):
		return fmt.Errorf("status must be fixed, disagree, out_of_scope or deferred")
	case a.Status == AnswerDisagree && strings.TrimSpace(a.Reason) == "":
		return fmt.Errorf("a disagree answer needs a reason")
	case utf8.RuneCountInString(a.Reason) > MaxAnswerReasonLen:
		return fmt.Errorf("reason exceeds %d characters", MaxAnswerReasonLen)
	}
	return nil
}

func (q Question) validate() error {
	switch {
	case q.ID != "":
		return fmt.Errorf("id is assigned by Ploeg")
	case !ValidQuestionTo(q.To):
		return fmt.Errorf("to must be person, reviewer or next_run")
	case strings.TrimSpace(q.Text) == "" || utf8.RuneCountInString(q.Text) > MaxQuestionTextLen:
		return fmt.Errorf("text must be 1 to %d characters", MaxQuestionTextLen)
	}
	return nil
}

func (a QuestionAnswer) validate() error {
	switch {
	case !BlackboardIDPattern.MatchString(a.QuestionID) || a.QuestionID[0] != 'q':
		return fmt.Errorf("questionId must be a question id from the briefing")
	case strings.TrimSpace(a.Answer) == "" || utf8.RuneCountInString(a.Answer) > MaxQuestionAnswerLen:
		return fmt.Errorf("answer must be 1 to %d characters", MaxQuestionAnswerLen)
	}
	return nil
}

// ValidateBlackboard checks a report's findings, answers and questions
// against the contract. ploegd refuses a report that fails it; the worker
// drops the failing entries first (SanitizeBlackboard), so only a report
// that bypassed the worker is refused.
func ValidateBlackboard(r OutcomeReport) error {
	if len(r.FindingItems) > MaxFindingItems {
		return fmt.Errorf("findingItems has %d entries; at most %d are accepted", len(r.FindingItems), MaxFindingItems)
	}
	for i, f := range r.FindingItems {
		if err := f.validate(); err != nil {
			return fmt.Errorf("findingItems[%d]: %w", i, err)
		}
	}
	if len(r.FindingAnswers) > MaxFindingAnswers {
		return fmt.Errorf("findingAnswers has %d entries; at most %d are accepted", len(r.FindingAnswers), MaxFindingAnswers)
	}
	for i, a := range r.FindingAnswers {
		if err := a.validate(); err != nil {
			return fmt.Errorf("findingAnswers[%d]: %w", i, err)
		}
	}
	if len(r.Questions) > MaxQuestions {
		return fmt.Errorf("questions has %d entries; at most %d are accepted", len(r.Questions), MaxQuestions)
	}
	for i, q := range r.Questions {
		if err := q.validate(); err != nil {
			return fmt.Errorf("questions[%d]: %w", i, err)
		}
	}
	if len(r.QuestionAnswers) > MaxQuestionAnswers {
		return fmt.Errorf("questionAnswers has %d entries; at most %d are accepted", len(r.QuestionAnswers), MaxQuestionAnswers)
	}
	for i, a := range r.QuestionAnswers {
		if err := a.validate(); err != nil {
			return fmt.Errorf("questionAnswers[%d]: %w", i, err)
		}
	}
	return nil
}

// SanitizeBlackboard keeps the entries of an agent's report that the
// contract accepts, up to each limit, and returns how many it dropped. An
// agent's malformed entry costs that entry, never the rest of its report.
func SanitizeBlackboard(r OutcomeReport) (OutcomeReport, int) {
	dropped := 0
	r.FindingItems, dropped = keepValid(r.FindingItems, MaxFindingItems, ReviewFinding.validate, dropped)
	r.FindingAnswers, dropped = keepValid(r.FindingAnswers, MaxFindingAnswers, FindingAnswer.validate, dropped)
	for i := range r.Questions {
		r.Questions[i].ID = ""
	}
	r.Questions, dropped = keepValid(r.Questions, MaxQuestions, Question.validate, dropped)
	r.QuestionAnswers, dropped = keepValid(r.QuestionAnswers, MaxQuestionAnswers, QuestionAnswer.validate, dropped)
	return r, dropped
}

func keepValid[T any](items []T, limit int, valid func(T) error, dropped int) ([]T, int) {
	if len(items) == 0 {
		return items, dropped
	}
	kept := make([]T, 0, len(items))
	for _, it := range items {
		if valid(it) != nil || len(kept) == limit {
			dropped++
			continue
		}
		kept = append(kept, it)
	}
	if len(kept) == 0 {
		return nil, dropped
	}
	return kept, dropped
}

// BriefedFinding is an earlier reviewer finding as a later Run of the Shift
// is briefed with it: who raised it, in which Round, and the writer's answer
// after it was last raised. Answer is nil when no writer answered it.
type BriefedFinding struct {
	ReviewFinding
	Role   string         `json:"role"`
	Round  int            `json:"round"`
	Answer *BriefedAnswer `json:"answer,omitempty"`
	// Unanswered is true when a writing Run finished after the finding was
	// last raised without answering it.
	Unanswered bool `json:"unanswered,omitempty"`
}

// BriefedAnswer is a writer's answer as the briefing carries it.
type BriefedAnswer struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Role   string `json:"role"`
	Round  int    `json:"round"`
}

// BriefedQuestion is a question a Run of the Work Item asked, with its answer
// once someone gave one. AnsweredBy is "person" or the answering Run's Role.
type BriefedQuestion struct {
	ID         string `json:"id"`
	To         string `json:"to"`
	Text       string `json:"text"`
	Role       string `json:"role"`
	Round      int    `json:"round"`
	Answer     string `json:"answer,omitempty"`
	AnsweredBy string `json:"answeredBy,omitempty"`
}
