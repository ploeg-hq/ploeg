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
	b.WriteString(". Answer each question by its id, then queue the Work Item again")
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
