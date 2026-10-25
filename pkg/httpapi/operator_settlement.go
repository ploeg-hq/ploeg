package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

type settleFromEvidenceBody struct {
	CommandID *string  `json:"commandId"`
	AmountUSD *float64 `json:"amountUsd"`
	Evidence  *string  `json:"evidence"`
}

func (s *Server) handleOperatorSettleFromEvidence(w http.ResponseWriter, r *http.Request) {
	p, actor, ok := executionActor(w, r)
	if !ok {
		return
	}
	id, ok := operatorID(w, r)
	if !ok {
		return
	}
	var body settleFromEvidenceBody
	if !decodeExecution(w, r, &body) {
		return
	}
	if body.CommandID == nil || !operatorName.MatchString(*body.CommandID) {
		operatorError(w, 400, "command_required", "A settlement needs a commandId of 1 to 128 identifier characters.")
		return
	}
	if body.AmountUSD == nil || body.Evidence == nil {
		operatorError(w, 400, "invalid_settlement", "A settlement needs amountUsd and evidence.")
		return
	}
	settled, err := s.Store.SettleLLMAccountFromEvidence(r.Context(), id, p.Teams, store.OperatorSettlementCommand{
		Consumer: p.Name, CommandID: *body.CommandID, Actor: operatorAuditActor(r, p, actor),
		AmountUSD: *body.AmountUSD, Evidence: *body.Evidence,
	})
	switch {
	case errors.Is(err, store.ErrInvalidSettlement):
		operatorError(w, 400, "invalid_settlement", fmt.Sprintf("amountUsd is a number from 0 below 100000000 and evidence is 1 to %d characters.", store.MaxSettlementEvidenceRunes))
		return
	case errors.Is(err, store.ErrUnknownRun):
		operatorError(w, 404, "not_found", "The resource was not found in the consumer's scope.")
		return
	case errors.Is(err, store.ErrSettlementCommandConflict):
		operatorError(w, 409, "command_conflict", "This commandId already settled another Run, amount or evidence.")
		return
	case errors.Is(err, store.ErrAccountAlreadySettled):
		operatorError(w, 409, "already_settled", "This Run's account is already settled.")
		return
	case errors.Is(err, store.ErrAccountNotUnsettled):
		operatorError(w, 409, "not_unsettled", "This Run's account is not listed as unsettled; the sweeps still settle it.")
		return
	case errors.Is(err, store.ErrBelowObservation):
		operatorError(w, 409, "below_observation", "amountUsd is below the spend Ploeg observed for this Run; settle at or above it.")
		return
	case err != nil:
		operatorError(w, 503, "unavailable", "Ploeg could not confirm the settlement.")
		return
	}
	status := 201
	if settled.Replayed {
		status = 200
	}
	var workItemID *string
	if settled.WorkItemID != nil {
		value := strconv.FormatInt(*settled.WorkItemID, 10)
		workItemID = &value
	}
	operatorJSON(w, status, map[string]any{"schemaVersion": "1.0", "settlement": map[string]any{
		"runId":         strconv.FormatInt(settled.RunID, 10),
		"workItemId":    workItemID,
		"team":          settled.Team,
		"commandId":     settled.CommandID,
		"previousState": settled.PreviousState,
		"settledUsd":    settled.SettledUSD,
		"observedUsd":   settled.ObservedUSD,
		"releasedUsd":   settled.ReleasedUSD,
		"settledAt":     settled.SettledAt.Format(time.RFC3339Nano),
		"replayed":      settled.Replayed,
	}})
}
