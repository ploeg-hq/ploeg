package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/clauderoutine"
	"github.com/ploeg-hq/ploeg/pkg/claudesession"
	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/secretstore"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// ClaudeRoutines starts claude-environment Runs by firing the person's own
// routine (ADR-0197 section 7). Ploeg stores only each routine's trigger
// token, in SecretStore, and never a claude.ai login. Nil answers
// routines_not_accepted on every routine route.
type ClaudeRoutines struct {
	Client *clauderoutine.Client
	// Limits keep ploegd's hourly fires below Anthropic's 100 per account
	// and 30 per routine. Zero is DefaultRoutineFireLimits.
	Limits store.RoutineFireLimits
}

// DefaultRoutineFireLimits leave headroom under Anthropic's hourly limits for
// fires the person makes from claude.ai themselves.
var DefaultRoutineFireLimits = store.RoutineFireLimits{PerPerson: 90, PerRoutine: 25}

func (c *ClaudeRoutines) limits() store.RoutineFireLimits {
	if c.Limits == (store.RoutineFireLimits{}) {
		return DefaultRoutineFireLimits
	}
	return c.Limits
}

// routineTokenValue is the name the trigger token is stored under.
const routineTokenValue = "triggerToken"

type claudeRoutineRequest struct {
	FireURL string `json:"fireUrl"`
	Token   string `json:"token"`
}

type claudeRoutineView struct {
	Team          string    `json:"team"`
	FireURL       string    `json:"fireUrl"`
	CreatorPinned bool      `json:"creatorPinned"`
	RegisteredAt  time.Time `json:"registeredAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type claudeRoutineResponse struct {
	SchemaVersion string            `json:"schemaVersion"`
	Routine       claudeRoutineView `json:"routine"`
}

type claudeRoutinesResponse struct {
	SchemaVersion string              `json:"schemaVersion"`
	Routines      []claudeRoutineView `json:"routines"`
}

type routineFireResponse struct {
	SchemaVersion string             `json:"schemaVersion"`
	Run           clientClaimRun     `json:"run"`
	EnvironmentID string             `json:"environmentId"`
	Session       routineFireSession `json:"session"`
}

type routineFireSession struct {
	ID  string `json:"id"`
	URL string `json:"url,omitempty"`
}

func routineView(r store.ClaudeRoutine) claudeRoutineView {
	return claudeRoutineView{Team: r.Team, FireURL: r.FireURL, CreatorPinned: r.Creator != "",
		RegisteredAt: r.RegisteredAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()}
}

func (s *Server) registerClaudeRoutines(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/operator/claude-routines", s.handleListClaudeRoutines)
	mux.HandleFunc("PUT /api/v1/operator/claude-routines/{team}", s.handlePutClaudeRoutine)
	mux.HandleFunc("DELETE /api/v1/operator/claude-routines/{team}", s.handleDeleteClaudeRoutine)
	mux.HandleFunc("POST /api/v1/operator/work-items/{id}/fire-routine", s.handleFireRoutine)
}

// routinePerson answers the request itself and returns false unless routines
// are accepted, the consumer may control executions and a proven person
// calls. Every routine route acts only for that person.
func (s *Server) routinePerson(w http.ResponseWriter, r *http.Request) (OperatorPrincipal, ProvenPerson, bool) {
	p, ok := OperatorPrincipalFromContext(r.Context())
	if !ok || !p.CanExecute {
		operatorError(w, 403, "execution_forbidden", "This consumer cannot control executions.")
		return p, ProvenPerson{}, false
	}
	if s.ClaudeRoutines == nil {
		operatorError(w, 422, "routines_not_accepted", "This installation does not start claude-environment Runs through routines (PLOEG_CLAUDE_ROUTINES).")
		return p, ProvenPerson{}, false
	}
	person, ok := ProvenPersonFromContext(r.Context())
	if !ok {
		operatorError(w, 403, reasonPersonTokenRequired, "A routine is registered and fired only by a proven person, in every attribution mode: send the person's exchanged token. Nothing changed.")
		return p, person, false
	}
	return p, person, true
}

func (s *Server) routineTeam(w http.ResponseWriter, p OperatorPrincipal, team string) bool {
	if !p.AllowsTeam(team) {
		operatorError(w, 404, "not_found", "The resource was not found in the consumer's scope.")
		return false
	}
	if !s.routineCarriesTeam(team) {
		operatorError(w, 409, "no_routine_team", "No Role of this Team starts its Runs through a routine (carrier: routine).")
		return false
	}
	return true
}

// routineCarriesTeam reports whether any Role of the team starts its Runs by
// firing the person's routine.
func (s *Server) routineCarriesTeam(team string) bool {
	for _, h := range s.OperatorConfig.TeamHarnesses[team] {
		if h.RoutineCarried() {
			return true
		}
	}
	return false
}

// routineSecretPath is where a person's trigger token for a Team lives. The
// person id is hashed so the path carries no identity and always fits the
// store's segment rules.
func routineSecretPath(person, team string) string {
	sum := sha256.Sum256([]byte(person))
	return "claude-routines/" + hex.EncodeToString(sum[:16]) + "/" + team
}

func (s *Server) handlePutClaudeRoutine(w http.ResponseWriter, r *http.Request) {
	p, person, ok := s.routinePerson(w, r)
	if !ok {
		return
	}
	team := r.PathValue("team")
	if !s.routineTeam(w, p, team) {
		return
	}
	var req claudeRoutineRequest
	if !decodeExecution(w, r, &req) {
		return
	}
	if err := clauderoutine.CheckFireURL(s.ClaudeRoutines.Client.BaseURL, req.FireURL); err != nil {
		operatorError(w, 400, "invalid_fire_url", err.Error()+".")
		return
	}
	if err := clauderoutine.CheckToken(req.Token); err != nil {
		operatorError(w, 400, "invalid_token", err.Error()+".")
		return
	}
	path := routineSecretPath(person.ID, team)
	if !secretstore.Configured(s.SecretStore) || secretstore.CheckPath(path) != nil {
		operatorError(w, 503, "secret_store_unavailable", "This installation has no secret store to keep the routine's trigger token in (ADR-0184). Nothing changed.")
		return
	}
	ref, err := s.SecretStore.Put(r.Context(), path, map[string]string{routineTokenValue: req.Token})
	if err != nil {
		s.Log.Error("storing a routine trigger token failed", "team", team, "err", err)
		operatorError(w, 503, "secret_store_unavailable", "Ploeg could not write the routine's trigger token to its secret store. Nothing changed.")
		return
	}
	routine, err := s.Store.PutClaudeRoutine(r.Context(), store.ClaudeRoutine{Person: person.ID, Team: team, FireURL: req.FireURL,
		SecretStore: ref.Store, SecretPath: ref.Path, SecretVersion: ref.Version, RegisteredBy: operatorAuditActor(r, p, person.ID)})
	if err != nil {
		s.Log.Error("recording a routine failed", "team", team, "err", err)
		operatorError(w, 503, "unavailable", "Ploeg could not record the routine.")
		return
	}
	s.Log.Info("routine registered", "team", team, "consumer", p.Name, "secret_version", ref.Version)
	w.Header().Set("Cache-Control", "no-store")
	operatorJSON(w, http.StatusOK, claudeRoutineResponse{SchemaVersion: "1.0", Routine: routineView(routine)})
}

func (s *Server) handleListClaudeRoutines(w http.ResponseWriter, r *http.Request) {
	p, person, ok := s.routinePerson(w, r)
	if !ok {
		return
	}
	routines, err := s.Store.ClaudeRoutinesOf(r.Context(), person.ID)
	if err != nil {
		operatorError(w, 503, "unavailable", "Ploeg could not read the routines.")
		return
	}
	out := claudeRoutinesResponse{SchemaVersion: "1.0", Routines: []claudeRoutineView{}}
	for _, routine := range routines {
		if p.AllowsTeam(routine.Team) {
			out.Routines = append(out.Routines, routineView(routine))
		}
	}
	operatorJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteClaudeRoutine(w http.ResponseWriter, r *http.Request) {
	p, person, ok := s.routinePerson(w, r)
	if !ok {
		return
	}
	team := r.PathValue("team")
	if !p.AllowsTeam(team) {
		operatorError(w, 404, "not_found", "The resource was not found in the consumer's scope.")
		return
	}
	routine, err := s.Store.DeleteClaudeRoutine(r.Context(), person.ID, team)
	switch {
	case errors.Is(err, store.ErrNoClaudeRoutine):
		operatorError(w, 404, "routine_not_registered", "You registered no routine for this Team.")
		return
	case err != nil:
		operatorError(w, 503, "unavailable", "Ploeg could not remove the routine.")
		return
	}
	if err := s.SecretStore.Delete(r.Context(), routine.SecretPath); err != nil {
		s.Log.Error("deleting a removed routine's trigger token failed; delete it from the claude.ai routine too", "team", team, "err", err)
	}
	s.Log.Info("routine removed", "team", team, "consumer", p.Name)
	operatorJSON(w, http.StatusOK, claudeRoutineResponse{SchemaVersion: "1.0", Routine: routineView(routine)})
}

// handleFireRoutine starts a Work Item's waiting claude-environment Run by
// firing the routine of the proven person it acts for (ADR-0197 section 7).
// Every refusal before the claim leaves the Run waiting; a fire that fails
// after the claim ends the Run stuck with its reason. Nothing falls back to
// a claude.ai login.
func (s *Server) handleFireRoutine(w http.ResponseWriter, r *http.Request) {
	p, person, ok := s.routinePerson(w, r)
	if !ok {
		return
	}
	_, actor, ok := executionActor(w, r)
	if !ok {
		return
	}
	id, ok := operatorID(w, r)
	if !ok {
		return
	}
	item, sessionForge, ok := s.clientClaimable(w, r, p, id)
	if !ok {
		return
	}
	if !s.routineTeam(w, p, item.Team) {
		return
	}
	routine, err := s.Store.ClaudeRoutineFor(r.Context(), person.ID, item.Team)
	switch {
	case errors.Is(err, store.ErrNoClaudeRoutine):
		operatorError(w, 409, "routine_not_registered", "You registered no routine for this Team: register its fire URL and token first. The Run keeps waiting.")
		return
	case err != nil:
		operatorError(w, 503, "unavailable", "Ploeg could not read your routine.")
		return
	}
	token, ok := s.routineToken(w, r, routine)
	if !ok {
		return
	}
	reservation, err := s.Store.ReserveRoutineFire(r.Context(), person.ID, item.Team, s.ClaudeRoutines.limits())
	switch {
	case errors.Is(err, store.ErrRoutineRateLimited):
		operatorError(w, 429, "routine_rate_limited", "Your routine was fired as often this hour as Ploeg allows below Anthropic's limits. The Run keeps waiting.")
		return
	case err != nil:
		operatorError(w, 503, "unavailable", "Ploeg could not count your routine's fires.")
		return
	}
	claim, ok := s.claimClientRun(w, r, p, actor, id, person.ID, store.CarrierRoutine)
	if !ok {
		s.dropRoutineFire(r.Context(), reservation)
		return
	}
	body, ok := s.clientClaimBody(w, r, claim, person.ID, sessionForge)
	if !ok {
		s.dropRoutineFire(r.Context(), reservation)
		return
	}
	text := clauderoutine.Text(body.Dispatch.Reference, body.Run.ID, body.Dispatch.Branch, body.Dispatch.BaseBranch, body.Dispatch.Prompt)
	session, err := s.ClaudeRoutines.Client.Fire(r.Context(), routine.FireURL, token, text)
	if err != nil {
		s.routineFireFailed(w, r, reservation, claim, err)
		return
	}
	sessionID := claudesession.NormalizeSessionID(session.ID)
	if err := s.Store.FinishRoutineFire(r.Context(), reservation, claim.RunID, "fired", sessionID); err != nil {
		s.Log.Error("recording a routine fire failed", "run", prefix12(claim.RunToken), "err", err)
	}
	err = s.Store.RecordClaudeSession(r.Context(), claim.RunToken, store.ClaudeSession{EnvironmentID: body.Dispatch.EnvironmentID,
		SessionID: sessionID, BaseBranch: body.Dispatch.BaseBranch, Title: body.Dispatch.Title, Reference: body.Dispatch.Reference})
	if err == nil && s.ClaudeSessions != nil {
		_, err = s.Store.HoldClientCarriedRun(r.Context(), claim.RunToken, time.Now().Add(s.ClaudeSessions.sessionTimeout()))
	}
	if err != nil {
		s.Log.Error("recording a fired routine's session failed; the run ends at its deadline", "run", prefix12(claim.RunToken), "err", err)
	}
	s.Log.Info("routine fired for a run", "team", claim.Team, "role", claim.Role, "work_item", claim.Item.ID,
		"run", prefix12(claim.RunToken), "consumer", p.Name)
	w.Header().Set("Cache-Control", "no-store")
	operatorJSON(w, http.StatusOK, routineFireResponse{SchemaVersion: "1.0", Run: body.Run, EnvironmentID: body.Dispatch.EnvironmentID,
		Session: routineFireSession{ID: session.ID, URL: session.URL}})
}

// routineToken reads a routine's trigger token from the secret store, or
// answers the request itself.
func (s *Server) routineToken(w http.ResponseWriter, r *http.Request, routine store.ClaudeRoutine) (string, bool) {
	if !secretstore.Configured(s.SecretStore) {
		operatorError(w, 503, "secret_store_unavailable", "This installation has no secret store, so Ploeg cannot read your routine's trigger token. The Run keeps waiting.")
		return "", false
	}
	values, err := s.SecretStore.Get(r.Context(), secretstore.Ref{Store: routine.SecretStore, Path: routine.SecretPath, Version: routine.SecretVersion})
	if err != nil || values[routineTokenValue] == "" {
		s.Log.Error("reading a routine trigger token failed", "team", routine.Team, "err", err)
		operatorError(w, 503, "secret_store_unavailable", "Ploeg could not read your routine's trigger token from its secret store. The Run keeps waiting.")
		return "", false
	}
	return values[routineTokenValue], true
}

func (s *Server) dropRoutineFire(ctx context.Context, reservation int64) {
	if err := s.Store.DropRoutineFire(context.WithoutCancel(ctx), reservation); err != nil {
		s.Log.Error("dropping an unused routine fire reservation failed; it counts until the hour passes", "err", err)
	}
}

// routineFireFailed ends a claimed Run whose routine could not be fired as
// stuck with the named reason, and answers it.
func (s *Server) routineFireFailed(w http.ResponseWriter, r *http.Request, reservation int64, claim store.ClientClaim, fireErr error) {
	ctx := context.WithoutCancel(r.Context())
	reason := clauderoutine.Reason(fireErr)
	if err := s.Store.FinishRoutineFire(ctx, reservation, claim.RunID, reason, ""); err != nil {
		s.Log.Error("recording a failed routine fire failed", "run", prefix12(claim.RunToken), "err", err)
	}
	s.Log.Warn("routine fire refused; the run ends stuck", "team", claim.Team, "role", claim.Role,
		"run", prefix12(claim.RunToken), "reason", reason, "err", fireErr)
	report := harness.OutcomeReport{Outcome: work.OutcomeStuck, Summary: "the person's routine could not be fired",
		StuckReason: reason + ": " + routineReasonText(reason)}
	if err := s.applyOutcome(ctx, claim.RunToken, report); err != nil {
		s.Log.Error("ending a run whose routine could not be fired failed; it ends at its deadline", "run", prefix12(claim.RunToken), "err", err)
	}
	status := map[string]int{"routine_token_refused": 409, "routine_not_found": 409, "routine_rate_limited": 429}[reason]
	if status == 0 {
		status = 503
	}
	operatorError(w, status, reason, routineReasonText(reason)+" The Run ended stuck; Ploeg never falls back to a Claude login.")
}

func routineReasonText(reason string) string {
	switch reason {
	case "routine_token_refused":
		return "Anthropic refused the routine's trigger token: it was rotated or deleted, so register the routine's current token."
	case "routine_not_found":
		return "Anthropic has no routine or API trigger at the registered fire URL."
	case "routine_rate_limited":
		return "Anthropic's hourly limit for your routine or account is reached."
	default:
		return "Anthropic's routines API could not start the session."
	}
}

// routineSessionCreator answers the request itself and returns false when
// the session was started by a fired routine and its creator is not the
// Anthropic account first seen for that routine (ADR-0197 section 7).
func (s *Server) routineSessionCreator(w http.ResponseWriter, r *http.Request, claims claudesession.Claims) bool {
	_, err := s.Store.PinRoutineCreator(r.Context(), claims.SessionID, claims.Creator)
	switch {
	case errors.Is(err, store.ErrRoutineCreatorMismatch):
		s.Log.Warn("claude session refused: its creator is not the routine's", "environment", claims.EnvironmentID)
		http.Error(w, "session_creator_mismatch: the session was not created by the routine's account", http.StatusForbidden)
		return false
	case err != nil:
		runError(w, err)
		return false
	}
	return true
}
