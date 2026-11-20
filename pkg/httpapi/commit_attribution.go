package httpapi

import (
	"context"
	"strconv"

	"github.com/ploeg-hq/ploeg/pkg/work"
)

// claimAttribution is who a claimed Run's commits name (ADR-0206): the
// account its forge credential pushes as, read from the forge, and the
// person it acts for with the Run's id, read from ploegd's own records.
// What ploegd cannot read is left out and logged.
func (s *Server) claimAttribution(ctx context.Context, team, runToken string, forge ClaimForge, item work.WorkItem) *work.Attribution {
	a := &work.Attribution{}
	if forge.Committer != nil {
		owner, repo := "", ""
		if item.Target != nil {
			owner, repo = item.Target.Owner, item.Target.Repo
		}
		if id, err := forge.Committer(ctx, owner, repo); err != nil {
			s.Log.Warn("could not read the identity the Run's forge credential commits as; the worker commits as its fallback",
				"team", team, "work_item", item.ID, "forge", forge.Dialect, "err", err)
		} else {
			a.Committer = &work.GitIdentity{Name: id.Name, Email: id.Email}
		}
	}
	if f, err := s.Store.TransactionTokenFacts(ctx, runToken); err != nil {
		s.Log.Warn("could not read who the Run acts for; its commits name no person", "team", team, "work_item", item.ID, "err", err)
	} else {
		a.Run = strconv.FormatInt(f.ID, 10)
		a.Person, a.PersonProof, _ = s.runPerson(f)
	}
	if a.Committer == nil && a.Person == "" && a.Run == "" {
		return nil
	}
	return a
}
