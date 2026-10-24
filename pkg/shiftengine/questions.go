package shiftengine

import (
	"context"
	"fmt"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

// A Run that asks a person instead of guessing (ADR-0121) stops its Shift
// when its Round ends: the Work Item parks at needs_human carrying the
// question, and the answer briefs the Runs of the next Shift.

const reasonQuestionForPerson = "question_for_person"

// maxQuestionMessageRunes bounds the question text the close message, and so
// the tracker comment, carries.
const maxQuestionMessageRunes = 3000

func questionMessage(waiting []harness.BriefedQuestion) string {
	var b strings.Builder
	b.WriteString("a Run asked a person before going on, so the Work Item waits for an answer. ")
	for i, q := range waiting {
		if i > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "Question %s from %s in round %d: “%s”", q.ID, q.Role, q.Round, strings.TrimSpace(q.Text))
	}
	b.WriteString(". Answer each question by its id, then resume the Work Item")
	msg := b.String()
	if r := []rune(msg); len(r) > maxQuestionMessageRunes {
		msg = string(r[:maxQuestionMessageRunes]) + "…"
	}
	return msg
}

// parkForQuestions closes the Shift when a Run of the Round that just ended
// asked a person something nobody answered. It reports whether it closed.
func (e *Engine) parkForQuestions(ctx context.Context, si store.ShiftInfo, reports []store.RunReport) (bool, error) {
	waiting, err := e.Store.QuestionsForPerson(ctx, si.WorkItemID, si.ID, si.Round)
	if err != nil {
		e.Log.Error("questions read failed; the round advances without them", "shift", si.ID, "err", err)
		return false, nil
	}
	if len(waiting) == 0 {
		return false, nil
	}
	return true, e.close(ctx, si, reasonQuestionForPerson, questionMessage(waiting), false, reports)
}

// ResumePlan returns the resume of a Work Item parked on a question to a
// person or for a person who took its Run over (ADR-0142), or false when there is nothing to resume or the Team's plan no
// longer has a Round for it.
func (e *Engine) ResumePlan(ctx context.Context, workItemID int64) (store.ResumePlan, bool, error) {
	parked, ok, err := e.Store.ParkedOnQuestion(ctx, workItemID)
	if err != nil || !ok {
		return store.ResumePlan{}, false, err
	}
	tp, planned, _ := e.planFor(parked.Team)
	if !planned || len(tp.Rounds) == 0 {
		return store.ResumePlan{}, false, nil
	}
	from := parked.Round
	if from > len(tp.Rounds) {
		from = len(tp.Rounds)
		if writer, ok := tp.WriterRound(); ok && (parked.Round-len(tp.Rounds))%2 == 1 {
			from = writer + 1
		}
	}
	if from < 1 {
		return store.ResumePlan{}, false, nil
	}
	p := store.ResumePlan{ShiftID: parked.ShiftID, AskedRound: parked.Round, FromRound: from, Waiting: parked.Waiting, Reason: parked.Reason}
	for _, role := range tp.Rounds[from-1].Roles {
		p.RoundCapUSD += role.CapUSD()
	}
	p.PoolUSD = max(parked.Remaining, p.RoundCapUSD)
	return p, true, nil
}
