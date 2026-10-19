package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/harness/adapters/claudeenv"
	"github.com/ploeg-hq/ploeg/pkg/litellm"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/taskprompt"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// clientClaimResponse is what a client needs to carry a Run with the
// person's own Claude Code (ADR-0112): the Run, what to dispatch and the Run
// credential the worker run API takes.
type clientClaimResponse struct {
	SchemaVersion string                `json:"schemaVersion"`
	Run           clientClaimRun        `json:"run"`
	Dispatch      clientClaimDispatch   `json:"dispatch"`
	Credential    clientClaimCredential `json:"credential"`
}

type clientClaimRun struct {
	ID         string `json:"id"`
	WorkItemID string `json:"workItemId"`
	ShiftID    string `json:"shiftId"`
	Team       string `json:"team"`
	Role       string `json:"role"`
	Round      int    `json:"round"`
	Writes     bool   `json:"writes"`
	ActsFor    string `json:"actsFor"`
}

type clientClaimDispatch struct {
	EnvironmentID string                `json:"environmentId"`
	Ref           string                `json:"ref"`
	Branch        string                `json:"branch"`
	BaseBranch    string                `json:"baseBranch"`
	Repository    clientClaimRepository `json:"repository"`
	Title         string                `json:"title"`
	Reference     string                `json:"reference"`
	Prompt        string                `json:"prompt"`
}

type clientClaimRepository struct {
	Forge string `json:"forge"`
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
}

type clientClaimCredential struct {
	RunToken     string    `json:"runToken"`
	ControlToken string    `json:"controlToken,omitempty"`
	WorkerID     string    `json:"workerId,omitempty"`
	Deadline     time.Time `json:"deadline"`
}

// clientWorkerPrefix starts the worker id a client-carried Run's control
// token names.
const clientWorkerPrefix = "client:"

// handleClientClaim lets the person a Work Item's client-carried Run acts for
// claim it through their own client (ADR-0112). The Run starts as a worker's
// claim starts it; the client dispatches it with the person's own Claude
// login and then speaks the worker run API with the returned credential.
func (s *Server) handleClientClaim(w http.ResponseWriter, r *http.Request) {
	p, actor, ok := executionActor(w, r)
	if !ok {
		return
	}
	id, ok := operatorID(w, r)
	if !ok {
		return
	}
	item, err := s.Store.WorkItem(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrWorkItemNotFound) || (err == nil && !p.AllowsTeam(item.Team)):
		operatorError(w, 404, "not_found", "The resource was not found in the consumer's scope.")
		return
	case err != nil:
		operatorError(w, 503, "unavailable", "Ploeg could not read the Work Item.")
		return
	}
	if !s.clientCarriesTeam(item.Team) {
		operatorError(w, 409, "no_client_run", "No Run of this Work Item waits for a client: its Team carries no Run from a client.")
		return
	}
	if item.Target == nil || !item.Target.Resolved() {
		operatorError(w, 409, "no_target", "The Work Item has no resolved repository, so a client cannot dispatch it.")
		return
	}
	person := store.ActingPerson(r.Context(), actor)
	claim, err := s.Store.ClaimClientCarriedRun(r.Context(), id, person, operatorAuditActor(r, p, actor), s.LeaseTTL, s.claimLimits)
	switch {
	case errors.Is(err, store.ErrWorkItemNotFound):
		operatorError(w, 404, "not_found", "The resource was not found in the consumer's scope.")
		return
	case errors.Is(err, store.ErrNoClientRun):
		operatorError(w, 409, "no_client_run", "No Run of this Work Item waits for a client.")
		return
	case errors.Is(err, store.ErrClientRunClaimed):
		operatorError(w, 409, "already_claimed", "The Run waiting for a client was already claimed.")
		return
	case errors.Is(err, store.ErrRunPersonUnknown):
		operatorError(w, 409, "person_unknown", "Ploeg does not know which person this Run acts for, so nobody can claim it.")
		return
	case errors.Is(err, store.ErrNotRunPerson):
		operatorError(w, 403, "not_your_run", "Only the person this Run acts for can claim it.")
		return
	case errors.Is(err, store.ErrNoWork):
		operatorError(w, 409, "team_at_capacity", "The Team already runs as many Runs as its maxRunning allows.")
		return
	case errors.Is(err, store.ErrBudgetExhausted):
		operatorError(w, 409, "budget_exhausted", "The Shift's pool cannot fund another Run.")
		return
	case err != nil:
		s.Log.Error("client claim failed", "work_item", id, "err", err)
		operatorError(w, 503, "unavailable", "Ploeg could not claim the Run.")
		return
	}
	s.recordRunConfig(r.Context(), claim.RunToken, claim.Team, claim.Role, true, runHarness{Name: claudeenv.Name, Model: claim.Model})
	s.recordBoardClaim(r.Context(), claim.RunToken)
	briefing, findings, questions := s.runBriefing(r.Context(), &claim.ClaimedRun)
	if claim.Role != "ask" {
		briefing = append(briefing, s.consumeOperatorNotes(r.Context(), claim.Item.ID, claim.RunToken, claim.Round)...)
	}
	target := *claim.Item.Target
	spec := harness.TaskSpec{
		WorkItem: claim.Item, Role: claim.Role, Branch: claim.Branch, TraceID: litellm.Alias(claim.RunToken),
		Repo:     harness.RepoRef{Forge: target.Forge, Owner: target.Owner, Name: target.Repo, BaseBranch: target.BaseBranch},
		Briefing: briefing, ReviewFindings: findings, Questions: questions,
		ActiveWork: s.activeWork(r.Context(), claim.Item.ID),
	}
	if s.ClaudeSessions != nil {
		spec.Repo.ForgeURL = s.ClaudeSessions.ForgeURL
	}
	base := target.BaseBranch
	if base == "" {
		base = "main"
	}
	branchExists := claim.BranchExists || claim.Item.Origin == work.OriginPullRequest
	ref := base
	if branchExists && claim.Branch != "" {
		ref = claim.Branch
	}
	planner := false
	if planners, ok := s.RoleCaps.(PlannerRoles); ok && !claim.Writes {
		planner = planners.IsPlanner(claim.Team, claim.Role)
	}
	resp := clientClaimResponse{
		SchemaVersion: "1.0",
		Run: clientClaimRun{ID: strconv.FormatInt(claim.RunID, 10), WorkItemID: claim.Item.ID, ShiftID: strconv.FormatInt(claim.ShiftID, 10),
			Team: claim.Team, Role: claim.Role, Round: claim.Round, Writes: claim.Writes, ActsFor: claim.ActsFor},
		Dispatch: clientClaimDispatch{
			EnvironmentID: s.OperatorConfig.TeamHarnesses[claim.Team][claim.Role].EnvironmentID,
			Ref:           ref, Branch: claim.Branch, BaseBranch: base,
			Repository: clientClaimRepository{Forge: target.Forge, Owner: target.Owner, Repo: target.Repo},
			Title:      claim.Item.Title, Reference: work.Reference(claim.Item),
			Prompt: taskprompt.ForRun(spec, planner, claim.Writes, taskprompt.PullRequest{}, !claim.Writes && branchExists) +
				claudeenv.SessionContract(spec, base),
		},
		Credential: clientClaimCredential{RunToken: claim.RunToken, Deadline: claim.Deadline},
	}
	if a := s.WorkerSecurity; a != nil && !(a.AllowLegacy && s.LLMControl == nil) {
		resp.Credential.WorkerID = clientWorkerPrefix + person
		resp.Credential.ControlToken = a.sign(workerCapability{Audience: "ploeg-worker-run", RunToken: claim.RunToken, WorkerID: resp.Credential.WorkerID,
			Team: claim.Team, Role: claim.Role, Expires: time.Now().Add(24 * time.Hour).Unix()})
	}
	s.Log.Info("client-carried run claimed", "team", claim.Team, "role", claim.Role, "work_item", claim.Item.ID,
		"shift", claim.ShiftID, "round", claim.Round, "consumer", p.Name, "briefing", len(briefing))
	w.Header().Set("Cache-Control", "no-store")
	operatorJSON(w, http.StatusOK, resp)
}

// clientCarriesTeam reports whether any Role of the team has its Runs carried
// by the person's own client (ADR-0112).
func (s *Server) clientCarriesTeam(team string) bool {
	for _, h := range s.OperatorConfig.TeamHarnesses[team] {
		if h.ClientCarried() {
			return true
		}
	}
	return false
}

func (s *Server) claimLimits(team, role string) (float64, int) {
	cap := 0.0
	if s.RoleCaps != nil {
		cap = s.RoleCaps.RoleCap(team, role)
	}
	return cap, s.maxRunning(team)
}

// DefaultClaudeSessionTimeout bounds how long ploegd holds a client-carried
// Run once its runner session is recorded, as PLOEG_HARNESS_TIMEOUT bounds a
// worker's harness.
const DefaultClaudeSessionTimeout = 100 * time.Minute

func (c *ClaudeSessions) sessionTimeout() time.Duration {
	if c.SessionTimeout > 0 {
		return c.SessionTimeout
	}
	return DefaultClaudeSessionTimeout
}

// settleFromRunner finishes the client-carried Run a runner reported its
// session for, as a worker finishes a claude-environment Run from the same
// report, so the Run no longer needs its client (ADR-0112). A session of a
// Run a worker carries is left to that worker.
func (s *Server) settleFromRunner(ctx context.Context, environmentID, sessionID string) {
	run, found, err := s.Store.ClientCarriedSessionRun(ctx, environmentID, sessionID)
	if err != nil {
		s.Log.Error("reading the client-carried run of a session failed; the run ends at its deadline", "err", err)
		return
	}
	if !found {
		return
	}
	state, err := s.Store.ClaudeSessionFor(ctx, run.RunToken)
	if err != nil {
		s.Log.Error("reading the session report failed; the run ends at its deadline", "run", prefix12(run.RunToken), "err", err)
		return
	}
	report := runnerOutcome(run, harness.RemoteSessionState{Reported: state.ReportedAt != nil, ExitReason: state.ExitReason,
		PushedCommit: state.PushedCommit, PullRequest: state.PullRequest, FinalMessage: state.FinalMessage})
	if err := validateOutcomeReport(report); err != nil {
		report = harness.OutcomeReport{Outcome: work.OutcomeStuck, Summary: "the session's final message carried an outcome Ploeg refuses",
			StuckReason: err.Error(), Links: report.Links}
	}
	if err := s.applyOutcome(ctx, run.RunToken, report); err != nil {
		s.Log.Error("settling a client-carried run from its runner failed; the run ends at its deadline", "run", prefix12(run.RunToken), "err", err)
		return
	}
	s.Log.Info("client-carried run settled from its runner's report", "run", prefix12(run.RunToken), "outcome", report.Outcome)
}

// runnerOutcome is the Outcome Report of a client-carried Run from what its
// runner reported, following the worker's resolution of a claude-environment
// Run: the pull request the runner names decides delivery, Claude's final
// message carries the rest, and a session that ended mid-turn failed.
func runnerOutcome(run store.ClientSessionRun, state harness.RemoteSessionState) harness.OutcomeReport {
	box, _ := claudeenv.FinalMessageReport(state.FinalMessage)
	resolved := func(r harness.OutcomeReport) harness.OutcomeReport {
		return harness.MergeDropBox(r, box)
	}
	pr := state.PullRequest
	prExisted := pr != "" && slices.Contains(run.PriorLinks, pr)
	var links []string
	if pr != "" {
		links = []string{pr}
	}
	switch {
	case run.Writes && pr != "" && !prExisted:
		return resolved(harness.OutcomeReport{Outcome: work.OutcomePROpened, Summary: claudeenv.Name + " run opened a PR for " + run.Title,
			Links: links, Checkpoint: &work.Checkpoint{Phase: "pr_opened", Branch: run.Branch, PRURL: pr}})
	case !claudeenv.Finished(state):
		return resolved(harness.OutcomeReport{Outcome: work.OutcomeFailed, Links: links, FailureReason: string(work.FailureAgentError),
			Summary: fmt.Sprintf("%s run failed: the session ended %s without a final message", claudeenv.Name, state.ExitReason)})
	case box.Outcome.Valid():
		box.Links = links
		if box.Outcome == work.OutcomeStuck && box.StuckReason == "" {
			box.StuckReason = "the session reported stuck without a reason"
		}
		return box
	case !run.Writes && pr != "":
		return resolved(harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: claudeenv.Name + " review finished for " + run.Title,
			Links: links, Checkpoint: &work.Checkpoint{Phase: "reviewed", Branch: run.Branch, PRURL: pr}})
	case pr != "":
		return resolved(harness.OutcomeReport{Outcome: work.OutcomePRUpdated, Summary: claudeenv.Name + " run updated the open PR for " + run.Title,
			Links: links, Checkpoint: &work.Checkpoint{Phase: "pr_updated", Branch: run.Branch, PRURL: pr}})
	default:
		return resolved(harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: claudeenv.Name + " run finished without opening a PR"})
	}
}
