package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// TransactionTokenFacts are what ploegd signs into a Run's transaction token
// (ADR-0156), read from its own records, never from the worker that asks.
type TransactionTokenFacts struct {
	RunIdentityFacts
	Writes bool
	// Authorized is the Run's spending ceiling in USD as a decimal string.
	Authorized string
	// Forge, Owner and Repo are the Work Item's repository, empty when it
	// has none.
	Forge, Owner, Repo string
	// ActsFor is the person the Work Item's Runs act for, ActsForProof how
	// Ploeg knows them ("proven", "asserted", or empty for rows older than
	// ADR-0156) and ActsForAct the identity provider's act.sub, if any.
	ActsFor, ActsForProof, ActsForAct string
}

// TransactionTokenFacts reads the facts of the Run runToken names.
func (s *Store) TransactionTokenFacts(ctx context.Context, runToken string) (TransactionTokenFacts, error) {
	var f TransactionTokenFacts
	err := s.pool.QueryRow(ctx, `SELECT r.id, r.work_item_id, r.shift_id, r.round, r.role, r.team,
		COALESCE(r.harness, ''), COALESCE(sh.branch, ''), r.run_token, r.state,
		COALESCE(r.expires_at, l.expires_at, now()), r.carrier IS NOT DISTINCT FROM 'client',
		r.writes, r.authorized::text,
		COALESCE(w.target_forge, ''), COALESCE(w.target_owner, ''), COALESCE(w.target_repo, ''),
		COALESCE(w.acts_for, ''), COALESCE(w.acts_for_proof, ''), COALESCE(w.acts_for_act, '')
		FROM agent_runs r LEFT JOIN leases l USING (run_token) LEFT JOIN shifts sh ON sh.id = r.shift_id
		LEFT JOIN work_items w ON w.id = r.work_item_id
		WHERE r.run_token = $1`, runToken).
		Scan(&f.ID, &f.WorkItemID, &f.ShiftID, &f.Round, &f.Role, &f.Team, &f.Harness, &f.Branch,
			&f.RunToken, &f.State, &f.Deadline, &f.ClientCarried,
			&f.Writes, &f.Authorized, &f.Forge, &f.Owner, &f.Repo, &f.ActsFor, &f.ActsForProof, &f.ActsForAct)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, ErrUnknownRun
	}
	return f, err
}

// RunActor is who a request under a verified transaction token acts as
// (ADR-0156): the person the Run acts for, how Ploeg knows them, the agent
// acting for them and the Run. Every audit row written under it carries it
// under the detail key "runActor".
type RunActor struct {
	Person string `json:"person"`
	Proof  string `json:"proof"`
	Agent  string `json:"agent"`
	// Act is the identity provider's actor before the agent, if any.
	Act string `json:"act,omitempty"`
	Run string `json:"run"`
}

// AuditRunActorKey is the audit_log detail key that holds a RunActor.
const AuditRunActorKey = "runActor"

type runActorKey struct{}

// WithRunActor returns ctx carrying a.
func WithRunActor(ctx context.Context, a RunActor) context.Context {
	return context.WithValue(ctx, runActorKey{}, a)
}

// RunActorFrom returns the RunActor ctx carries.
func RunActorFrom(ctx context.Context) (RunActor, bool) {
	a, ok := ctx.Value(runActorKey{}).(RunActor)
	return a, ok && a.Person != "" && a.Agent != ""
}
