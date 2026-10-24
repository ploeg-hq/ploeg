package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ploeg-hq/ploeg/pkg/findings"
	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// Findings by id, their answers and questions (ADR-0121).

const (
	AuditQuestionAnswered  = "question.answered"
	MaxQuestionAnswerRunes = 4096
)

var (
	ErrQuestionNotFound = errors.New("no question to a person with this id on the work item")
	ErrQuestionAnswered = errors.New("the question already has another answer")
	ErrAnswerConflict   = errors.New("command id was used for a different answer")
)

// WithBlackboard attaches a Run's findings by id, its answers to findings,
// its questions and its answers to questions. ReportOutcome assigns the ids
// and keeps only what the Run's kind may report.
func (r harnessReport) WithBlackboard(items []harness.ReviewFinding, answers []harness.FindingAnswer,
	questions []harness.Question, questionAnswers []harness.QuestionAnswer) harnessReport {
	r.FindingItems, r.FindingAnswers, r.Questions, r.QuestionAnswers = items, answers, questions, questionAnswers
	return r
}

const findingRunColumns = `id, COALESCE(shift_id, 0), role, round, writes, shadow, state = 'finished', verdict,
	finding_items, finding_answers, questions, question_answers`

func findingRuns(ctx context.Context, q querier, workItemID int64) ([]findings.Run, error) {
	rows, err := q.Query(ctx, `SELECT `+findingRunColumns+` FROM agent_runs WHERE work_item_id = $1 ORDER BY id`, workItemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []findings.Run
	for rows.Next() {
		var r findings.Run
		var items, answers, questions, questionAnswers []byte
		if err := rows.Scan(&r.ID, &r.ShiftID, &r.Role, &r.Round, &r.Writes, &r.Shadow, &r.Finished, &r.Verdict,
			&items, &answers, &questions, &questionAnswers); err != nil {
			return nil, err
		}
		for _, field := range []struct {
			raw  []byte
			into any
		}{{items, &r.FindingItems}, {answers, &r.FindingAnswers}, {questions, &r.Questions}, {questionAnswers, &r.QuestionAnswers}} {
			if field.raw == nil {
				continue
			}
			if err := json.Unmarshal(field.raw, field.into); err != nil {
				return nil, fmt.Errorf("decode run %d findings: %w", r.ID, err)
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func personAnswers(ctx context.Context, q querier, workItemID int64) (map[string]findings.PersonAnswer, error) {
	rows, err := q.Query(ctx, `SELECT question_id, answer, actor FROM question_answers WHERE work_item_id = $1`, workItemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]findings.PersonAnswer{}
	for rows.Next() {
		var id string
		var a findings.PersonAnswer
		if err := rows.Scan(&id, &a.Answer, &a.Actor); err != nil {
			return nil, err
		}
		out[id] = a
	}
	return out, rows.Err()
}

func marshalBlackboard[T any](items []T) ([]byte, error) {
	if len(items) == 0 {
		return nil, nil
	}
	return json.Marshal(items)
}

// recordBlackboard assigns ids to what the Run reported and stores what its
// kind may report: findings from a reader, answers to findings from a
// writer, questions and answers to questions from any Run that is not a
// shadow. A shadow's findings are stored for comparison and never answered
// or briefed. It returns what it dropped, for the outcome's audit event.
func recordBlackboard(ctx context.Context, tx pgx.Tx, runID, workItemID int64, writes, shadow bool, rep harnessReport) (map[string]any, error) {
	if len(rep.FindingItems) == 0 && len(rep.FindingAnswers) == 0 && len(rep.Questions) == 0 && len(rep.QuestionAnswers) == 0 {
		return nil, nil
	}
	runs, err := findingRuns(ctx, tx, workItemID)
	if err != nil {
		return nil, err
	}
	known := findings.KnownFindingIDs(runs)
	answerable := findings.AnswerableQuestionIDs(runs)
	dropped := map[string]any{}
	var items []harness.ReviewFinding
	var answers []harness.FindingAnswer
	var questions []harness.Question
	var questionAnswers []harness.QuestionAnswer
	if writes {
		if len(rep.FindingItems) > 0 {
			dropped["finding_items_from_writer"] = len(rep.FindingItems)
		}
		for _, a := range rep.FindingAnswers {
			if known[a.FindingID] {
				answers = append(answers, a)
			}
		}
		if n := len(rep.FindingAnswers) - len(answers); n > 0 {
			dropped["finding_answers_unknown"] = n
		}
	} else {
		reraisable := known
		if shadow {
			reraisable = nil
		}
		items = findings.AssignFindingIDs(runID, rep.FindingItems, reraisable)
		if len(rep.FindingAnswers) > 0 {
			dropped["finding_answers_from_reader"] = len(rep.FindingAnswers)
		}
	}
	if shadow {
		if n := len(rep.Questions) + len(rep.QuestionAnswers); n > 0 {
			dropped["shadow_questions"] = n
		}
	} else {
		questions = findings.AssignQuestionIDs(runID, rep.Questions)
		for _, a := range rep.QuestionAnswers {
			if answerable[a.QuestionID] {
				questionAnswers = append(questionAnswers, a)
			}
		}
		if n := len(rep.QuestionAnswers) - len(questionAnswers); n > 0 {
			dropped["question_answers_unknown"] = n
		}
	}
	itemsJSON, err := marshalBlackboard(items)
	if err != nil {
		return nil, err
	}
	answersJSON, err := marshalBlackboard(answers)
	if err != nil {
		return nil, err
	}
	questionsJSON, err := marshalBlackboard(questions)
	if err != nil {
		return nil, err
	}
	questionAnswersJSON, err := marshalBlackboard(questionAnswers)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_runs SET finding_items = $2::jsonb, finding_answers = $3::jsonb,
		questions = $4::jsonb, question_answers = $5::jsonb WHERE id = $1`,
		runID, itemsJSON, answersJSON, questionsJSON, questionAnswersJSON); err != nil {
		return nil, err
	}
	detail := map[string]any{}
	if len(items) > 0 {
		detail["findings"] = len(items)
	}
	if len(answers) > 0 {
		detail["finding_answers"] = len(answers)
	}
	if len(questions) > 0 {
		detail["questions"] = len(questions)
	}
	if len(dropped) > 0 {
		detail["dropped"] = dropped
	}
	return detail, nil
}

// FindingsBrief returns the findings and questions a Run claiming round of
// shiftID is briefed with (ADR-0121).
func (s *Store) FindingsBrief(ctx context.Context, shiftID int64, round int, writes bool) ([]harness.BriefedFinding, []harness.BriefedQuestion, error) {
	var workItemID int64
	if err := s.pool.QueryRow(ctx, `SELECT work_item_id FROM shifts WHERE id = $1`, shiftID).Scan(&workItemID); err != nil {
		return nil, nil, err
	}
	runs, err := findingRuns(ctx, s.pool, workItemID)
	if err != nil {
		return nil, nil, err
	}
	if runs, err = lineageRuns(ctx, s.pool, runs, shiftID); err != nil {
		return nil, nil, err
	}
	persons, err := personAnswers(ctx, s.pool, workItemID)
	if err != nil {
		return nil, nil, err
	}
	f, q := findings.Brief(runs, shiftID, round, writes, persons)
	return f, q, nil
}

// QuestionsForPerson returns the questions to a person that the Runs of
// round of shiftID asked and nobody answered.
func (s *Store) QuestionsForPerson(ctx context.Context, workItemID, shiftID int64, round int) ([]harness.BriefedQuestion, error) {
	runs, err := findingRuns(ctx, s.pool, workItemID)
	if err != nil {
		return nil, err
	}
	persons, err := personAnswers(ctx, s.pool, workItemID)
	if err != nil {
		return nil, err
	}
	return findings.WaitingForPerson(runs, shiftID, round, persons), nil
}

// QuestionAnswerCommand is a person's answer to a question a Run addressed
// to a person.
type QuestionAnswerCommand struct {
	Consumer   string
	CommandID  string
	Actor      string
	QuestionID string
	Answer     string
}

// QuestionAnswer is a stored answer.
type QuestionAnswer struct {
	WorkItemID int64
	QuestionID string
	CreatedAt  time.Time
	Replayed   bool
}

// AnswerQuestion stores a person's answer to the question questionID on a
// Work Item in teams. A repeated (consumer, command id) with the same body
// returns the first answer with Replayed set; another body is
// ErrAnswerConflict. A question has one answer: a second, different answer is
// ErrQuestionAnswered.
func (s *Store) AnswerQuestion(ctx context.Context, workItemID int64, teams []string, cmd QuestionAnswerCommand) (QuestionAnswer, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return QuestionAnswer{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var exists bool
	err = tx.QueryRow(ctx, `SELECT true FROM work_items WHERE id = $1 AND ($2::text[] IS NULL OR team = ANY($2)) FOR UPDATE`,
		workItemID, teams).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return QuestionAnswer{}, ErrWorkItemNotFound
	}
	if err != nil {
		return QuestionAnswer{}, err
	}
	var first QuestionAnswer
	var firstWorkItem int64
	var firstQuestion, firstAnswer, firstActor string
	err = tx.QueryRow(ctx, `SELECT work_item_id, question_id, answer, actor, created_at FROM question_answers
		WHERE consumer = $1 AND command_id = $2`, cmd.Consumer, cmd.CommandID).Scan(&firstWorkItem, &firstQuestion, &firstAnswer, &firstActor, &first.CreatedAt)
	switch {
	case err == nil:
		if firstWorkItem != workItemID || firstQuestion != cmd.QuestionID || firstAnswer != cmd.Answer || firstActor != cmd.Actor {
			return QuestionAnswer{}, ErrAnswerConflict
		}
		first.WorkItemID, first.QuestionID, first.Replayed = workItemID, cmd.QuestionID, true
		return first, tx.Commit(ctx)
	case !errors.Is(err, pgx.ErrNoRows):
		return QuestionAnswer{}, err
	}
	var asked bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM agent_runs WHERE work_item_id = $1 AND NOT shadow
		AND questions @> jsonb_build_array(jsonb_build_object('id', $2::text, 'to', 'person')))`, workItemID, cmd.QuestionID).Scan(&asked); err != nil {
		return QuestionAnswer{}, err
	}
	if !asked {
		return QuestionAnswer{}, ErrQuestionNotFound
	}
	answer := QuestionAnswer{WorkItemID: workItemID, QuestionID: cmd.QuestionID}
	err = tx.QueryRow(ctx, `INSERT INTO question_answers (work_item_id, question_id, consumer, command_id, actor, answer)
		VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (work_item_id, question_id) DO NOTHING RETURNING created_at`,
		workItemID, cmd.QuestionID, cmd.Consumer, cmd.CommandID, cmd.Actor, cmd.Answer).Scan(&answer.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return QuestionAnswer{}, ErrQuestionAnswered
	}
	if err != nil {
		return QuestionAnswer{}, err
	}
	if err := audit(ctx, tx, cmd.Actor, AuditQuestionAnswered, &workItemID, map[string]any{
		"question": cmd.QuestionID, "command_id": cmd.CommandID, "characters": len([]rune(cmd.Answer))}); err != nil {
		return QuestionAnswer{}, err
	}
	return answer, tx.Commit(ctx)
}

// OperatorFinding is one finding of a Work Item on the operator API.
type OperatorFinding struct {
	ID              string                 `json:"id"`
	ShiftID         *string                `json:"shiftId"`
	RunID           string                 `json:"runId"`
	Role            string                 `json:"role"`
	Round           int                    `json:"round"`
	LastRaisedRound int                    `json:"lastRaisedRound"`
	Shadow          bool                   `json:"shadow"`
	Severity        string                 `json:"severity,omitempty"`
	Title           string                 `json:"title"`
	Body            string                 `json:"body,omitempty"`
	Path            string                 `json:"path,omitempty"`
	Line            int                    `json:"line,omitempty"`
	Answer          *OperatorFindingAnswer `json:"answer"`
	Unanswered      bool                   `json:"unanswered"`
}

// OperatorFindingAnswer is the writer's answer to a finding.
type OperatorFindingAnswer struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	RunID  string `json:"runId"`
	Role   string `json:"role"`
	Round  int    `json:"round"`
}

// OperatorQuestion is one question of a Work Item on the operator API.
type OperatorQuestion struct {
	ID         string  `json:"id"`
	ShiftID    *string `json:"shiftId"`
	RunID      string  `json:"runId"`
	Role       string  `json:"role"`
	Round      int     `json:"round"`
	To         string  `json:"to"`
	Text       string  `json:"text"`
	Answer     *string `json:"answer"`
	AnsweredBy *string `json:"answeredBy"`
	// WaitingForPerson is true for a question to a person nobody answered.
	WaitingForPerson bool `json:"waitingForPerson"`
}

// OperatorReviewDisagreement is a review Round whose shadow reviewer gave
// another verdict than the acting reviewers (ADR-0096, ADR-0121).
type OperatorReviewDisagreement struct {
	ShiftID        string `json:"shiftId"`
	Round          int    `json:"round"`
	ShadowRunID    string `json:"shadowRunId"`
	ShadowRole     string `json:"shadowRole"`
	PrimaryVerdict string `json:"primaryVerdict"`
	ShadowVerdict  string `json:"shadowVerdict"`
}

func shiftRef(id int64) *string {
	if id == 0 {
		return nil
	}
	s := fmt.Sprint(id)
	return &s
}

func operatorLedger(ctx context.Context, q querier, workItemID int64) ([]OperatorFinding, []OperatorQuestion, []OperatorReviewDisagreement, error) {
	runs, err := findingRuns(ctx, q, workItemID)
	if err != nil {
		return nil, nil, nil, err
	}
	persons, err := personAnswers(ctx, q, workItemID)
	if err != nil {
		return nil, nil, nil, err
	}
	l := findings.BuildLedger(runs, persons)
	fs := []OperatorFinding{}
	for _, f := range l.Findings {
		of := OperatorFinding{ID: f.ID, ShiftID: shiftRef(f.ShiftID), RunID: fmt.Sprint(f.RunID), Role: f.Role, Round: f.Round,
			LastRaisedRound: f.LastRaisedRound, Shadow: f.Shadow, Severity: f.Severity, Title: f.Title, Body: f.Body,
			Path: f.Path, Line: f.Line, Unanswered: f.Unanswered}
		if f.Answer != nil {
			of.Answer = &OperatorFindingAnswer{Status: f.Answer.Status, Reason: f.Answer.Reason, RunID: fmt.Sprint(f.Answer.RunID),
				Role: f.Answer.Role, Round: f.Answer.Round}
		}
		fs = append(fs, of)
	}
	qs := []OperatorQuestion{}
	for _, lq := range l.Questions {
		oq := OperatorQuestion{ID: lq.ID, ShiftID: shiftRef(lq.ShiftID), RunID: fmt.Sprint(lq.RunID), Role: lq.Role, Round: lq.Round,
			To: lq.To, Text: lq.Text, WaitingForPerson: lq.To == harness.AskPerson && lq.AnsweredBy == ""}
		if lq.AnsweredBy != "" {
			answer, by := lq.Answer, lq.AnsweredBy
			oq.Answer, oq.AnsweredBy = &answer, &by
		}
		qs = append(qs, oq)
	}
	ds := []OperatorReviewDisagreement{}
	for _, d := range l.Disagreements {
		ds = append(ds, OperatorReviewDisagreement{ShiftID: fmt.Sprint(d.ShiftID), Round: d.Round, ShadowRunID: fmt.Sprint(d.ShadowRunID),
			ShadowRole: d.ShadowRole, PrimaryVerdict: d.PrimaryVerdict, ShadowVerdict: d.ShadowVerdict})
	}
	return fs, qs, ds, nil
}

// CloseReasonQuestionForPerson is the close reason of a Shift that stopped
// on a question to a person (ADR-0121).
const CloseReasonQuestionForPerson = "question_for_person"

// ParkedQuestion is a Work Item's last Shift when it stopped on a question
// to a person and nothing has run since: the Round that asked, the Shift's
// unspent pool and the questions of that Round still waiting.
type ParkedQuestion struct {
	WorkItemID int64
	Team       string
	ShiftID    int64
	Round      int
	Remaining  float64
	Waiting    []string
	// Reason is why the Shift parked: question_for_person, or taken_over
	// when a person took its Run over (ADR-0142).
	Reason string
}

// ParkedOnQuestion returns the Work Item's parked Shift, or false when its
// last Shift is open, did not stop on a question to a person or for a person
// who took it over, or the Work Item is no longer needs_human.
func (s *Store) ParkedOnQuestion(ctx context.Context, workItemID int64) (ParkedQuestion, bool, error) {
	p := ParkedQuestion{WorkItemID: workItemID}
	var state, reason string
	var closed bool
	err := s.pool.QueryRow(ctx, `SELECT i.state, i.team, sh.id, sh.round, COALESCE(sh.close_reason, ''), sh.closed_at IS NOT NULL
		FROM work_items i JOIN LATERAL (SELECT * FROM shifts WHERE work_item_id = i.id ORDER BY id DESC LIMIT 1) sh ON true
		WHERE i.id = $1`, workItemID).Scan(&state, &p.Team, &p.ShiftID, &p.Round, &reason, &closed)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	if !closed || (reason != CloseReasonQuestionForPerson && reason != work.CloseReasonTakenOver) || state != "needs_human" {
		return p, false, nil
	}
	p.Reason = reason
	ledger, err := s.Ledger(ctx, p.ShiftID)
	if err != nil {
		return p, false, err
	}
	p.Remaining = max(ledger.Remaining(), 0)
	if reason == work.CloseReasonTakenOver {
		unspent, err := s.yieldedUnspent(ctx, p.ShiftID)
		if err != nil {
			return p, false, err
		}
		p.Remaining += unspent
	}
	waiting, err := s.QuestionsForPerson(ctx, workItemID, p.ShiftID, p.Round)
	if err != nil {
		return p, false, err
	}
	for _, q := range waiting {
		p.Waiting = append(p.Waiting, q.ID)
	}
	return p, true, nil
}

// lineageRuns maps the Runs of the Shifts a resume continued into shiftID
// (ADR-0121). A Shift a resume opened at Round F continues the parked Shift
// at the Round A that asked, so each of the parked Shift's Runs before A
// counts as an earlier Round of shiftID, renumbered by A-F; a chain of
// resumes adds up. The Runs of Round A and later, which the resume runs
// again, and Runs of other Shifts are returned unchanged.
func lineageRuns(ctx context.Context, q queryRower, runs []findings.Run, shiftID int64) ([]findings.Run, error) {
	type segment struct {
		before, shiftBy int
	}
	segments := map[int64]segment{}
	current, shiftBy := shiftID, 0
	for range 32 {
		var previous int64
		var from, resumes int
		err := q.QueryRow(ctx, `SELECT COALESCE(previous_shift_id, 0), from_round, COALESCE(resumes_round, 0)
			FROM work_item_requeues WHERE shift_id = $1`, current).Scan(&previous, &from, &resumes)
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, err
		}
		if resumes == 0 || previous == 0 {
			break
		}
		shiftBy += resumes - from
		segments[previous] = segment{before: resumes, shiftBy: shiftBy}
		current = previous
	}
	if len(segments) == 0 {
		return runs, nil
	}
	out := make([]findings.Run, 0, len(runs))
	for _, r := range runs {
		if seg, ok := segments[r.ShiftID]; ok && r.Round < seg.before {
			r.Round -= seg.shiftBy
			r.ShiftID = shiftID
		}
		out = append(out, r)
	}
	return out, nil
}

// ResumePlan is what resuming a Work Item parked on a question to a person
// would do (ADR-0121): reopen FromRound of the Team's plan, the Round that
// asked, on the parked Shift's branch, with a pool of PoolUSD. RoundCapUSD is
// the most the reopened Round's Runs can be authorized, the sum of its Roles'
// caps; PoolUSD bounds the resumed Shift as a whole, including the Rounds
// after it. Waiting names the questions still without an answer; a resume is
// refused until it is empty.
type ResumePlan struct {
	ShiftID     int64
	AskedRound  int
	FromRound   int
	RoundCapUSD float64
	PoolUSD     float64
	Waiting     []string
	// Reason is why the parked Shift stopped: question_for_person or
	// taken_over (ADR-0142).
	Reason string
}
