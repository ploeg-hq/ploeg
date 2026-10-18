// Package findings derives the findings ledger of a Work Item (ADR-0121):
// which reviewer findings were raised by id, how each writer answered them,
// which questions Runs asked and who answered, and where a shadow reviewer's
// verdict differed from the acting reviewers'. Everything is derived from the
// Runs' stored reports, never counted, so a restart or a replay reads the
// same ledger.
package findings

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

// Run is the part of one finished Run's report the ledger reads. Its
// FindingItems and Questions carry the ids ploegd assigned.
type Run struct {
	ID              int64
	ShiftID         int64
	Role            string
	Round           int
	Writes          bool
	Shadow          bool
	Finished        bool
	Verdict         string
	FindingItems    []harness.ReviewFinding
	FindingAnswers  []harness.FindingAnswer
	Questions       []harness.Question
	QuestionAnswers []harness.QuestionAnswer
}

// PersonAnswer is a person's answer to a question addressed to a person.
type PersonAnswer struct {
	Answer string
	Actor  string
}

// MaxBriefedPersonQuestions caps the person questions a briefing carries,
// newest first.
const MaxBriefedPersonQuestions = 10

// AssignFindingIDs gives each finding of run its id: an entry that names a
// finding in known keeps that id, and every other entry gets f<run>-<n>.
func AssignFindingIDs(runID int64, items []harness.ReviewFinding, known map[string]bool) []harness.ReviewFinding {
	if len(items) == 0 {
		return nil
	}
	out := make([]harness.ReviewFinding, 0, len(items))
	seen := map[string]bool{}
	n := 0
	for _, it := range items {
		if it.ID == "" || !known[it.ID] || seen[it.ID] {
			n++
			it.ID = fmt.Sprintf("f%d-%d", runID, n)
		}
		seen[it.ID] = true
		out = append(out, it)
	}
	return out
}

// AssignQuestionIDs gives each question of run its id q<run>-<n>.
func AssignQuestionIDs(runID int64, qs []harness.Question) []harness.Question {
	if len(qs) == 0 {
		return nil
	}
	out := make([]harness.Question, len(qs))
	for i, q := range qs {
		q.ID = fmt.Sprintf("q%d-%d", runID, i+1)
		out[i] = q
	}
	return out
}

// thread is one finding as the ledger follows it through a Shift.
type thread struct {
	finding    harness.ReviewFinding
	shiftID    int64
	runID      int64
	role       string
	round      int
	lastRaised int
	shadow     bool
	answer     *answer
	unanswered bool
}

type answer struct {
	harness.FindingAnswer
	runID int64
	role  string
	round int
}

func chronological(runs []Run) []Run {
	out := slices.Clone(runs)
	slices.SortStableFunc(out, func(a, b Run) int {
		return cmp.Or(cmp.Compare(a.ShiftID, b.ShiftID), cmp.Compare(a.Round, b.Round), cmp.Compare(a.ID, b.ID))
	})
	return out
}

// threads follows every finding raised by id through the Runs, in Shift,
// Round and claim order. A raise again replaces the finding's text and
// clears its answer; a writer's answer counts only after the last raise; a
// writer that finished after the last raise without answering marks it
// unanswered until a later writer answers.
func threads(runs []Run) []*thread {
	byID := map[string]*thread{}
	var out []*thread
	for _, r := range chronological(runs) {
		if !r.Finished {
			continue
		}
		if !r.Writes {
			for _, f := range r.FindingItems {
				if f.ID == "" {
					continue
				}
				t, ok := byID[f.ID]
				if !ok {
					t = &thread{shiftID: r.ShiftID, runID: r.ID, role: r.Role, round: r.Round, shadow: r.Shadow}
					byID[f.ID] = t
					out = append(out, t)
				} else if t.shadow || r.Shadow {
					continue
				}
				t.finding, t.shiftID, t.lastRaised, t.answer, t.unanswered = f, r.ShiftID, r.Round, nil, false
			}
			continue
		}
		answered := map[string]bool{}
		for _, a := range r.FindingAnswers {
			t, ok := byID[a.FindingID]
			if !ok || t.shadow || t.shiftID != r.ShiftID || r.Round <= t.lastRaised {
				continue
			}
			t.answer = &answer{FindingAnswer: a, runID: r.ID, role: r.Role, round: r.Round}
			t.unanswered = false
			answered[a.FindingID] = true
		}
		for _, t := range out {
			if !t.shadow && t.shiftID == r.ShiftID && r.Round > t.lastRaised && t.answer == nil && !answered[t.finding.ID] {
				t.unanswered = true
			}
		}
	}
	return out
}

// Brief returns what a Run claiming round of shiftID is briefed with: the
// findings of the Shift's last review Round before it, each with the writer's
// answer or marked unanswered, and the questions that reach it. A writer
// answers the findings; a reader checks the answers. Shadow Runs contribute
// nothing (ADR-0096).
func Brief(runs []Run, shiftID int64, round int, writes bool, persons map[string]PersonAnswer) ([]harness.BriefedFinding, []harness.BriefedQuestion) {
	var shift []Run
	lastRead, read := 0, false
	for _, r := range runs {
		if r.ShiftID != shiftID || r.Round >= round || r.Shadow {
			continue
		}
		shift = append(shift, r)
		if !r.Writes && r.Finished && (!read || r.Round > lastRead) {
			lastRead, read = r.Round, true
		}
	}
	var briefed []harness.BriefedFinding
	if read {
		for _, t := range threads(shift) {
			if t.lastRaised != lastRead {
				continue
			}
			bf := harness.BriefedFinding{ReviewFinding: t.finding, Role: t.role, Round: t.lastRaised, Unanswered: t.unanswered}
			if t.answer != nil {
				bf.Answer = &harness.BriefedAnswer{Status: t.answer.Status, Reason: t.answer.Reason, Role: t.answer.role, Round: t.answer.round}
			}
			briefed = append(briefed, bf)
		}
	}
	return briefed, briefQuestions(runs, shiftID, round, writes, persons)
}

func briefQuestions(runs []Run, shiftID int64, round int, writes bool, persons map[string]PersonAnswer) []harness.BriefedQuestion {
	ordered := chronological(runs)
	answers := map[string]harness.BriefedQuestion{}
	for _, r := range ordered {
		if r.Shadow || !r.Finished || (r.ShiftID == shiftID && r.Round >= round) {
			continue
		}
		for _, a := range r.QuestionAnswers {
			answers[a.QuestionID] = harness.BriefedQuestion{Answer: a.Answer, AnsweredBy: r.Role}
		}
	}
	var personQs, shiftQs []harness.BriefedQuestion
	for _, r := range ordered {
		if r.Shadow || !r.Finished {
			continue
		}
		earlier := r.ShiftID != shiftID || r.Round < round
		if !earlier {
			continue
		}
		for _, q := range r.Questions {
			bq := harness.BriefedQuestion{ID: q.ID, To: q.To, Text: q.Text, Role: r.Role, Round: r.Round}
			switch q.To {
			case harness.AskPerson:
				if p, ok := persons[q.ID]; ok {
					bq.Answer, bq.AnsweredBy = p.Answer, "person"
				}
				personQs = append(personQs, bq)
			case harness.AskReviewer, harness.AskNextRun:
				if r.ShiftID != shiftID || (q.To == harness.AskReviewer && writes) {
					continue
				}
				if a, ok := answers[q.ID]; ok {
					bq.Answer, bq.AnsweredBy = a.Answer, a.AnsweredBy
				}
				shiftQs = append(shiftQs, bq)
			}
		}
	}
	if len(personQs) > MaxBriefedPersonQuestions {
		personQs = personQs[len(personQs)-MaxBriefedPersonQuestions:]
	}
	out := append(personQs, shiftQs...)
	if len(out) == 0 {
		return nil
	}
	return out
}

// WaitingForPerson returns the questions to a person that Runs of round in
// shiftID asked and nobody has answered: the Round ends parked on them.
func WaitingForPerson(runs []Run, shiftID int64, round int, persons map[string]PersonAnswer) []harness.BriefedQuestion {
	var out []harness.BriefedQuestion
	for _, r := range chronological(runs) {
		if r.ShiftID != shiftID || r.Round != round || r.Shadow || !r.Finished {
			continue
		}
		for _, q := range r.Questions {
			if _, answered := persons[q.ID]; q.To == harness.AskPerson && !answered {
				out = append(out, harness.BriefedQuestion{ID: q.ID, To: q.To, Text: q.Text, Role: r.Role, Round: r.Round})
			}
		}
	}
	return out
}

// KnownFindingIDs returns the ids of the findings acting readers raised: the
// ids a later reader may raise again and a writer may answer.
func KnownFindingIDs(runs []Run) map[string]bool {
	known := map[string]bool{}
	for _, r := range runs {
		if r.Writes || r.Shadow {
			continue
		}
		for _, f := range r.FindingItems {
			if f.ID != "" {
				known[f.ID] = true
			}
		}
	}
	return known
}

// AnswerableQuestionIDs returns the ids of the questions to a reviewer or
// the next Run that acting Runs asked: the ids a later Run may answer.
func AnswerableQuestionIDs(runs []Run) map[string]bool {
	ids := map[string]bool{}
	for _, r := range runs {
		if r.Shadow {
			continue
		}
		for _, q := range r.Questions {
			if q.To != harness.AskPerson {
				ids[q.ID] = true
			}
		}
	}
	return ids
}

// LedgerFinding is one finding of a Work Item as a person reads it.
type LedgerFinding struct {
	harness.ReviewFinding
	ShiftID         int64
	RunID           int64
	Role            string
	Round           int
	LastRaisedRound int
	Shadow          bool
	Answer          *LedgerAnswer
	Unanswered      bool
}

// LedgerAnswer is the writer's answer after a finding was last raised.
type LedgerAnswer struct {
	harness.FindingAnswer
	RunID int64
	Role  string
	Round int
}

// LedgerQuestion is one question of a Work Item and its answer, if any.
type LedgerQuestion struct {
	harness.Question
	ShiftID int64
	RunID   int64
	Role    string
	Round   int
	Answer  string
	// AnsweredBy is "person" or the answering Run's Role; empty when
	// unanswered.
	AnsweredBy string
	// AnsweringRunID is the Run that answered; zero for a person.
	AnsweringRunID int64
	PersonActor    string
}

// Disagreement is a review Round whose shadow reviewer gave another verdict
// than the acting reviewers combined (ADR-0096): request_changes when any
// asked for changes, else approve when one approved, else none.
type Disagreement struct {
	ShiftID        int64
	Round          int
	ShadowRunID    int64
	ShadowRole     string
	PrimaryVerdict string
	ShadowVerdict  string
}

// Ledger is everything the operator API shows of a Work Item's findings,
// questions and shadow disagreements.
type Ledger struct {
	Findings      []LedgerFinding
	Questions     []LedgerQuestion
	Disagreements []Disagreement
}

// BuildLedger derives the ledger of a Work Item from all its Runs.
func BuildLedger(runs []Run, persons map[string]PersonAnswer) Ledger {
	var l Ledger
	for _, t := range threads(runs) {
		lf := LedgerFinding{ReviewFinding: t.finding, ShiftID: t.shiftID, RunID: t.runID, Role: t.role, Round: t.round,
			LastRaisedRound: t.lastRaised, Shadow: t.shadow, Unanswered: t.unanswered}
		if t.answer != nil {
			lf.Answer = &LedgerAnswer{FindingAnswer: t.answer.FindingAnswer, RunID: t.answer.runID, Role: t.answer.role, Round: t.answer.round}
		}
		l.Findings = append(l.Findings, lf)
	}
	ordered := chronological(runs)
	type runAnswer struct {
		text  string
		role  string
		runID int64
	}
	answers := map[string]runAnswer{}
	for _, r := range ordered {
		if r.Shadow || !r.Finished {
			continue
		}
		for _, a := range r.QuestionAnswers {
			answers[a.QuestionID] = runAnswer{a.Answer, r.Role, r.ID}
		}
	}
	for _, r := range ordered {
		if r.Shadow || !r.Finished {
			continue
		}
		for _, q := range r.Questions {
			lq := LedgerQuestion{Question: q, ShiftID: r.ShiftID, RunID: r.ID, Role: r.Role, Round: r.Round}
			if q.To == harness.AskPerson {
				if p, ok := persons[q.ID]; ok {
					lq.Answer, lq.AnsweredBy, lq.PersonActor = p.Answer, "person", p.Actor
				}
			} else if a, ok := answers[q.ID]; ok {
				lq.Answer, lq.AnsweredBy, lq.AnsweringRunID = a.text, a.role, a.runID
			}
			l.Questions = append(l.Questions, lq)
		}
	}
	l.Disagreements = disagreements(ordered)
	return l
}

func verdictOrNone(v string) string {
	if v == harness.VerdictApprove || v == harness.VerdictRequestChanges {
		return v
	}
	return "none"
}

func disagreements(ordered []Run) []Disagreement {
	type key struct {
		shift int64
		round int
	}
	primary := map[key]string{}
	for _, r := range ordered {
		if r.Writes || r.Shadow || !r.Finished {
			continue
		}
		k := key{r.ShiftID, r.Round}
		switch {
		case r.Verdict == harness.VerdictRequestChanges:
			primary[k] = harness.VerdictRequestChanges
		case r.Verdict == harness.VerdictApprove && primary[k] != harness.VerdictRequestChanges:
			primary[k] = harness.VerdictApprove
		}
	}
	var out []Disagreement
	for _, r := range ordered {
		if !r.Shadow || !r.Finished {
			continue
		}
		p := verdictOrNone(primary[key{r.ShiftID, r.Round}])
		if s := verdictOrNone(r.Verdict); s != p {
			out = append(out, Disagreement{ShiftID: r.ShiftID, Round: r.Round, ShadowRunID: r.ID, ShadowRole: r.Role, PrimaryVerdict: p, ShadowVerdict: s})
		}
	}
	return out
}
