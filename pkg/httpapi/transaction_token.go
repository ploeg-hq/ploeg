package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/txntoken"
)

// TransactionTokenKeysPath is where ploegd publishes the keys that verify a
// Run's transaction token (ADR-0156).
const TransactionTokenKeysPath = "/.well-known/jwks.json"

func (s *Server) registerTransactionTokens(mux *http.ServeMux) {
	mux.HandleFunc("GET "+TransactionTokenKeysPath, s.handleTransactionTokenKeys)
}

func (s *Server) handleTransactionTokenKeys(w http.ResponseWriter, _ *http.Request) {
	if s.TransactionTokens == nil {
		http.Error(w, "transaction tokens are not issued by this ploegd", http.StatusNotFound)
		return
	}
	body, err := json.Marshal(s.TransactionTokens.KeySet())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/jwk-set+json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(body)
}

// issueTransactionToken signs the transaction token of the Run runToken
// names for worker, the workload that claimed it, from ploegd's own records.
// The person is the one who handed the Work Item over, else the Team's
// sponsor; a Run with neither gets none.
func (s *Server) issueTransactionToken(ctx context.Context, runToken, worker string) (string, time.Time, error) {
	f, err := s.Store.TransactionTokenFacts(ctx, runToken)
	if err != nil {
		return "", time.Time{}, err
	}
	run := txntoken.Run{
		ID: strconv.FormatInt(f.ID, 10), Team: f.Team, Role: f.Role, Writes: f.Writes, Worker: worker, Deadline: f.Deadline,
		Context: txntoken.Context{WorkItem: store.IDString(f.WorkItemID), Shift: store.IDString(f.ShiftID), Branch: f.Branch},
	}
	if f.Repo != "" {
		run.Context.Repository = f.Forge + "/" + f.Owner + "/" + f.Repo
	}
	if a, err := strconv.ParseFloat(f.Authorized, 64); err == nil && a > 0 {
		run.Context.Allowance = f.Authorized
	}
	run.Person, run.Proof, run.IdentityActor = s.runPerson(f)
	return s.TransactionTokens.Sign(run)
}

// runPerson is who a Run acts for and how Ploeg knows them: the person who
// handed the Work Item over, else the Team's sponsor (ADR-0156). act is the
// identity provider's actor of a proven person.
func (s *Server) runPerson(f store.TransactionTokenFacts) (person, proof, act string) {
	switch {
	case f.ActsFor != "" && f.ActsForProof == txntoken.ProofProven:
		return f.ActsFor, txntoken.ProofProven, f.ActsForAct
	case f.ActsFor != "":
		return f.ActsFor, txntoken.ProofAsserted, ""
	case s.teamSponsor(f.Team) != "":
		return s.teamSponsor(f.Team), txntoken.ProofSponsor, ""
	}
	return "", "", ""
}

// verifyTransactionToken checks a worker's transaction token against
// ploegd's own key and the Run the path names: its id, Team and Role, and the
// worker it was issued to. It returns who the request acts as.
func (s *Server) verifyTransactionToken(token, worker string, run store.RunControlState) (store.RunActor, bool) {
	signer := s.TransactionTokens
	if signer == nil {
		return store.RunActor{}, false
	}
	c, err := txntoken.Verify(token, signer.Keys(), txntoken.Expect{Issuer: signer.Issuer(), Audience: signer.Audience(), Leeway: 5 * time.Second})
	if err != nil || c.Txn != strconv.FormatInt(run.ID, 10) || c.ReqWL != worker || c.TCtx.Team != run.Team || c.TCtx.Role != run.Role {
		return store.RunActor{}, false
	}
	actor := store.RunActor{Person: c.Subject, Proof: c.TCtx.SubjectProof, Agent: c.Act.Sub, Run: c.Txn}
	if c.Act.Act != nil {
		actor.Act = c.Act.Act.Sub
	}
	return actor, true
}

// refuseUnsponsoredTeam answers 403 for a Team without a sponsor while
// ploegd signs transaction tokens (ADR-0156): such a Team runs nothing.
func (s *Server) refuseUnsponsoredTeam(w http.ResponseWriter, team string) bool {
	if s.TransactionTokens == nil || s.teamSponsor(team) != "" {
		return false
	}
	s.Log.Warn("claim refused: the Team has no sponsor", "team", team)
	http.Error(w, unsponsoredTeam(team), http.StatusForbidden)
	return true
}

func unsponsoredTeam(team string) string {
	return fmt.Sprintf("Team %q has no sponsor, so it runs nothing: name the person its Runs act for in teams.%s.sponsor (ADR-0156).", team, team)
}

type runWorkerKey struct{}

func withRunWorker(ctx context.Context, worker string) context.Context {
	return context.WithValue(ctx, runWorkerKey{}, worker)
}

// runWorkerFrom is the worker id the middleware verified for a Run route.
func runWorkerFrom(ctx context.Context) string {
	w, _ := ctx.Value(runWorkerKey{}).(string)
	return w
}
