package httpapi

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

const maxViaLength = 128

var (
	viaLabel          = regexp.MustCompile(`^[\x21-\x7e](?:[\x20-\x7e]*[\x21-\x7e])?$`)
	confirmationValue = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}:[0-9a-f]{64}$`)
)

type attributionError struct {
	code, message string
}

// operatorAttribution reads who makes an operator change and how (ADR-0108):
// X-Ploeg-Actor, X-Ploeg-Acting-User, X-Ploeg-Via and X-Ploeg-Confirmation.
// Each header is optional here and may appear at most once; a present one
// must be valid. Routes that change work require the actor themselves. When a
// verified person token named the person, the actor is that person and the
// attribution is proven (ADR-0150); otherwise a named actor is asserted.
func operatorAttribution(r *http.Request, consumer string) (store.Attribution, *attributionError) {
	a := store.Attribution{Consumer: consumer}
	actor := r.Header.Values("X-Ploeg-Actor")
	if len(actor) > 1 || (len(actor) == 1 && !operatorName.MatchString(actor[0])) {
		return a, &attributionError{"actor_required", "An authenticated actor identity is required."}
	}
	acting := r.Header.Values("X-Ploeg-Acting-User")
	if len(acting) > 1 || (len(acting) == 1 && !operatorName.MatchString(acting[0])) {
		return a, &attributionError{"invalid_acting_user", "Supply one valid authenticated acting identity."}
	}
	via := r.Header.Values("X-Ploeg-Via")
	if len(via) > 1 || (len(via) == 1 && (len(via[0]) > maxViaLength || !viaLabel.MatchString(via[0]))) {
		return a, &attributionError{"invalid_via", "X-Ploeg-Via is one channel label of 1 to 128 printable ASCII characters."}
	}
	confirmation := r.Header.Values("X-Ploeg-Confirmation")
	if len(confirmation) > 1 || (len(confirmation) == 1 && !confirmationValue.MatchString(confirmation[0])) {
		return a, &attributionError{"invalid_confirmation", "X-Ploeg-Confirmation is <confirmation id>:<lowercase SHA-256 hex of the text the person agreed to>."}
	}
	if len(actor) == 1 {
		a.Actor = actor[0]
	}
	if len(acting) == 1 {
		a.ActingUser = acting[0]
	}
	if len(via) == 1 {
		a.Via = via[0]
	}
	if len(confirmation) == 1 {
		a.Confirmation = confirmation[0]
	}
	if a.Actor == "" && (a.ActingUser != "" || a.Via != "" || a.Confirmation != "") {
		return a, &attributionError{"actor_required", "X-Ploeg-Acting-User, X-Ploeg-Via and X-Ploeg-Confirmation need an X-Ploeg-Actor."}
	}
	if proven, ok := ProvenPersonFromContext(r.Context()); ok {
		if (a.Actor != "" && a.Actor != proven.ID) || (a.ActingUser != "" && a.ActingUser != proven.ID) {
			return a, &attributionError{"actor_conflict", "X-Ploeg-Actor and X-Ploeg-Acting-User must name the person the person token proves."}
		}
		a.Actor, a.ActingUser, a.Proof, a.Act = proven.ID, "", ProofProven, proven.Act
	} else if a.Actor != "" {
		a.Proof = ProofAsserted
	}
	return a, nil
}

// AttributionMode says whether an operator change may rest on attribution the
// consumer only asserted (ADR-0171).
type AttributionMode string

const (
	// AttributionAsserted accepts a change whose person only X-Ploeg-Actor
	// names, records it asserted and answers with a Warning header.
	AttributionAsserted AttributionMode = "asserted"
	// AttributionProven refuses every operator change without a verified
	// person token.
	AttributionProven AttributionMode = "proven"
)

// ParseAttributionMode reads PLOEG_OPERATOR_ATTRIBUTION; empty is
// AttributionAsserted.
func ParseAttributionMode(raw string) (AttributionMode, error) {
	switch mode := AttributionMode(strings.TrimSpace(raw)); mode {
	case "":
		return AttributionAsserted, nil
	case AttributionAsserted, AttributionProven:
		return mode, nil
	default:
		return "", fmt.Errorf("PLOEG_OPERATOR_ATTRIBUTION must be %s or %s, not %q", AttributionAsserted, AttributionProven, raw)
	}
}

const reasonPersonTokenRequired = "person_token_required"

const assertedAttributionWarning = `299 ploegd "This change names its person only in X-Ploeg-Actor (asserted attribution). PLOEG_OPERATOR_ATTRIBUTION=proven refuses it, and the chart defaults to proven from 0.3.0: send the person's exchanged token (ADR-0150, ADR-0171)."`

func (s *Server) refuseUnprovenChange(w http.ResponseWriter, r *http.Request, consumer, assertedActor string) {
	if s.Store != nil {
		refusal := store.AttributionRefusal{Consumer: consumer, Reason: reasonPersonTokenRequired, Method: r.Method, Path: r.URL.Path, AssertedActor: assertedActor}
		if err := s.Store.RecordAttributionRefusal(r.Context(), refusal); err != nil && s.Log != nil {
			s.Log.Error("attribution refusal audit failed", "consumer", consumer, "err", err)
		}
	}
	operatorError(w, http.StatusForbidden, reasonPersonTokenRequired, "This deployment accepts an operator change only from a proven person: send the person's exchanged token as the bearer or in "+PersonTokenHeader+". Nothing changed.")
}
