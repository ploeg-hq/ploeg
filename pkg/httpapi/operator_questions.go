package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

type questionAnswerBody struct {
	CommandID *string `json:"commandId"`
	Answer    *string `json:"answer"`
}

// handleOperatorQuestionAnswer stores a person's answer to a question a Run
// addressed to a person (ADR-0121). The answer briefs every later Run of the
// Work Item; it does not queue the Work Item again.
func (s *Server) handleOperatorQuestionAnswer(w http.ResponseWriter, r *http.Request) {
	p, actor, ok := executionActor(w, r)
	if !ok {
		return
	}
	id, ok := operatorID(w, r)
	if !ok {
		return
	}
	questionID := r.PathValue("question")
	if !harness.BlackboardIDPattern.MatchString(questionID) || questionID[0] != 'q' {
		operatorError(w, 400, "invalid_request", "Invalid question identifier.")
		return
	}
	var body questionAnswerBody
	if !decodeExecution(w, r, &body) {
		return
	}
	if body.CommandID == nil || !operatorName.MatchString(*body.CommandID) {
		operatorError(w, 400, "command_required", "An answer needs a commandId of 1 to 128 identifier characters.")
		return
	}
	answer := ""
	if body.Answer != nil {
		answer = strings.TrimSpace(*body.Answer)
	}
	if answer == "" || utf8.RuneCountInString(answer) > store.MaxQuestionAnswerRunes {
		operatorError(w, 400, "invalid_answer", fmt.Sprintf("An answer is 1 to %d characters.", store.MaxQuestionAnswerRunes))
		return
	}
	stored, err := s.Store.AnswerQuestion(r.Context(), id, p.Teams, store.QuestionAnswerCommand{Consumer: p.Name, CommandID: *body.CommandID,
		Actor: operatorAuditActor(r, p, actor), QuestionID: questionID, Answer: answer})
	switch {
	case errors.Is(err, store.ErrWorkItemNotFound), errors.Is(err, store.ErrQuestionNotFound):
		operatorError(w, 404, "not_found", "The resource was not found in the consumer's scope.")
		return
	case errors.Is(err, store.ErrQuestionAnswered):
		operatorError(w, 409, "already_answered", "This question already has another answer.")
		return
	case errors.Is(err, store.ErrAnswerConflict):
		operatorError(w, 409, "command_conflict", "This commandId was already used for a different answer.")
		return
	case err != nil:
		operatorError(w, 503, "unavailable", "Ploeg could not confirm the answer.")
		return
	}
	status := 201
	if stored.Replayed {
		status = 200
	}
	operatorJSON(w, status, map[string]any{"schemaVersion": "1.0", "answer": map[string]any{
		"workItemId": strconv.FormatInt(stored.WorkItemID, 10),
		"questionId": stored.QuestionID,
		"commandId":  *body.CommandID,
		"createdAt":  stored.CreatedAt.UTC().Format(time.RFC3339Nano),
		"replayed":   stored.Replayed,
	}})
}

// ResumePlanner is implemented by the shift engine: what resuming a Work
// Item parked on a question to a person would do (ADR-0121).
type ResumePlanner interface {
	ResumePlan(ctx context.Context, workItemID int64) (store.ResumePlan, bool, error)
}

func (s *Server) resumeOffer(ctx context.Context, workItemID int64) *store.OperatorResume {
	planner, ok := s.Engine.(ResumePlanner)
	if !ok {
		return nil
	}
	plan, ok, err := planner.ResumePlan(ctx, workItemID)
	if err != nil {
		s.logger().Error("resume plan read failed; detail shows none", "work_item", workItemID, "err", err)
		return nil
	}
	if !ok {
		return nil
	}
	waiting := plan.Waiting
	if waiting == nil {
		waiting = []string{}
	}
	return &store.OperatorResume{ShiftID: strconv.FormatInt(plan.ShiftID, 10), AskedRound: plan.AskedRound, FromRound: plan.FromRound,
		RoundCapUSD: plan.RoundCapUSD, PoolUSD: plan.PoolUSD, Ready: len(plan.Waiting) == 0, WaitingQuestionIDs: waiting}
}
