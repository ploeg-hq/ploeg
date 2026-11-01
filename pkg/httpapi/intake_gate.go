package httpapi

import (
	"context"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/spendcap"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/target"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// PlanWriters is implemented by shiftengine.Engine. TeamWrites reports
// whether the team's plan, configured or synthesized, has a Role that
// writes; planned is false for a team that gets no Shift. A ShiftEngine
// without it leaves the writer check to the Shift.
type PlanWriters interface {
	TeamWrites(team string) (writes, planned bool)
}

// openHandsHarness reports whether a Role's default Harness is one of the
// OpenHands harnesses, which refuse a repository that ships
// provider.RepositoryHooksPath (ADR-0148).
func openHandsHarness(h RoleHarnesses) bool {
	if h.Name == "openhands" {
		return true
	}
	return h.Name == "acp" && h.Profile != nil && *h.Profile == "openhands"
}

// fitGate is ADR-0187's fit check at intake, run after routing resolved the
// repository and before anything is queued: the checks Ploeg can make from
// what it holds, each refusing with a code from pkg/target and one sentence
// a person reads on the card. headroom is the Team's spend in its period
// when it has a cap, for the first activity's budget line.
func (s *Server) fitGate(ctx context.Context, item work.WorkItem) (refusal *target.Refusal, headroom *store.TeamPeriodSpend, err error) {
	if writers, ok := s.Engine.(PlanWriters); ok {
		if writes, planned := writers.TeamWrites(item.Team); planned && !writes {
			return &target.Refusal{Code: target.RefusalTeamCannotWrite, Allowed: []string{},
				Reason: fmt.Sprintf("Team %s has no Role that writes code", item.Team)}, nil, nil
		}
	}
	spend, capped, err := s.Store.TeamPeriodSpend(ctx, item.Team)
	if err != nil {
		return nil, nil, err
	}
	if capped {
		headroom = &spend
		if spend.Exhausted() {
			return &target.Refusal{Code: target.RefusalBudgetExhausted, Allowed: []string{},
				Reason: fmt.Sprintf("Team %s has spent %s of its %s %s; it can take this card again from %s",
					item.Team, usd(spend.SettledUSD+spend.HeldUSD), usd(spend.Cap.USD), periodWords(spend.Cap.Period), spend.PeriodEnd.Format("Monday 2 January 2006"))}, headroom, nil
		}
	}
	if role, ok := s.harnessRefusesRepository(ctx, item); ok {
		return &target.Refusal{Code: target.RefusalHarnessRefusesRepository, Allowed: []string{},
			Reason: fmt.Sprintf("Team %s runs OpenHands for its %s Role, and OpenHands refuses a repository that ships %s, which %s/%s does",
				item.Team, role, provider.RepositoryHooksPath, item.Target.Owner, item.Target.Repo)}, headroom, nil
	}
	return nil, headroom, nil
}

// harnessRefusesRepository names the first Role of the Team whose default
// Harness is OpenHands when the resolved repository ships the hooks file,
// as the readiness gate's last inspection of it reported (ADR-0148). It
// knows nothing when the target is unresolved or unregistered, or when
// PLOEG_TEAM_HARNESSES does not name the Team's Harnesses; the worker's own
// refusal at Prepare then stays the only check.
func (s *Server) harnessRefusesRepository(ctx context.Context, item work.WorkItem) (string, bool) {
	roles := s.OperatorConfig.TeamHarnesses[item.Team]
	if len(roles) == 0 || item.Target == nil || s.Readiness == nil {
		return "", false
	}
	key, ok := s.Readiness.KeyOf(*item.Target)
	if !ok {
		return "", false
	}
	if v := s.Readiness.Admit(ctx, key); v.Unknown || !v.Repository.HooksFile {
		return "", false
	}
	names := make([]string, 0, len(roles))
	for role := range roles {
		names = append(names, role)
	}
	sort.Strings(names)
	for _, role := range names {
		if openHandsHarness(roles[role]) {
			return role, true
		}
	}
	return "", false
}

// fittingTeams lists the known Teams other than exclude that could take the
// item: their routing lands it in the same repository (or in one at all
// when the item's own routing resolved none), and the fit gate passes them.
func (s *Server) fittingTeams(ctx context.Context, item work.WorkItem, labelsRead bool, exclude string) []string {
	fitting := []string{}
	for _, team := range s.knownTeams() {
		if team == exclude {
			continue
		}
		candidate := item
		candidate.Team = team
		candidate.Target = nil
		if s.Targets != nil {
			route, ok, err := s.Targets.Route(target.Request{Scope: item.ExternalScope, Team: team, Labels: item.Labels, LabelsRead: labelsRead})
			if err != nil {
				continue
			}
			if ok {
				candidate.Target = &route.Target
			}
			if item.Target != nil && (candidate.Target == nil || *candidate.Target != *item.Target) {
				continue
			}
		}
		refusal, _, err := s.fitGate(ctx, candidate)
		if err != nil {
			s.Log.Warn("fitting Teams unknown; the card names none", "external_id", item.ExternalID, "team", team, "err", err)
			return []string{}
		}
		if refusal == nil {
			fitting = append(fitting, team)
		}
	}
	return fitting
}

// refuseAtGate records a fit gate refusal under the Team the work would have
// run as, so GET /operator/route-refusals lists it, and posts it as the
// card's first activity (ADR-0187).
func (s *Server) refuseAtGate(ctx context.Context, tp provider.TrackerProvider, item work.WorkItem, labelsRead bool, refusal *target.Refusal) error {
	s.Log.Warn("tracker item refused by the fit gate; nothing queued", "provider", item.Provider,
		"external_id", item.ExternalID, "scope", item.ExternalScope, "team", item.Team, "code", refusal.Code, "reason", refusal.Reason)
	if err := s.Store.RefuseRoute(ctx, item, refusal.Code, refusal.Reason, refusal.Allowed); err != nil {
		return err
	}
	s.postActivity(ctx, tp, item.ExternalID, provider.SessionActivity{Kind: provider.SessionError,
		Body: refusal.Reason + ".\n" + teamsThatCanTakeIt(s.fittingTeams(ctx, item, labelsRead, item.Team))})
	return nil
}

// askWhichTeam is the first activity for an item that names no Team and has
// no pin, assignee mapping or default to fall back on: one question, with
// the field to set and the Teams that could take the card (ADR-0187).
func (s *Server) askWhichTeam(ctx context.Context, tp provider.TrackerProvider, item work.WorkItem, labelsRead bool) error {
	field := "Team"
	if named, ok := tp.(provider.DelegateField); ok && strings.TrimSpace(named.DelegateFieldName()) != "" {
		field = strings.TrimSpace(named.DelegateFieldName())
	}
	reason := fmt.Sprintf("no Team is named for this card: set the %s field", field)
	s.Log.Warn("tracker item names no Team; asking", "provider", item.Provider, "external_id", item.ExternalID, "scope", item.ExternalScope)
	if err := s.Store.RefuseRoute(ctx, item, target.RefusalTeamMissing, reason, []string{}); err != nil {
		return err
	}
	fitting := s.fittingTeams(ctx, item, labelsRead, "")
	body := fmt.Sprintf("Which Team should take this card? Set the %s field.", field)
	if len(fitting) > 0 {
		body += " Teams that can take this repository: " + strings.Join(fitting, ", ") + "."
	} else {
		body += " " + teamsThatCanTakeIt(fitting)
	}
	s.postActivity(ctx, tp, item.ExternalID, provider.SessionActivity{Kind: provider.SessionElicitation, Body: body})
	return nil
}

// startingActivity is the first activity of a card a Team takes: who takes
// it, where the work lands, the headroom left when the Team has a cap, and
// what lands on the card next.
func startingActivity(item work.WorkItem, headroom *store.TeamPeriodSpend) provider.SessionActivity {
	lines := []string{fmt.Sprintf("Team %s takes this card.", item.Team)}
	if item.Target != nil && item.Target.Resolved() {
		where := fmt.Sprintf("Repository %s/%s", item.Target.Owner, item.Target.Repo)
		if item.Target.BaseBranch != "" {
			where += ", base branch " + item.Target.BaseBranch
		}
		lines = append(lines, where+".")
	}
	if headroom != nil {
		lines = append(lines, fmt.Sprintf("Budget left %s: %s of %s.", periodWords(headroom.Cap.Period), usd(headroom.RemainingUSD()), usd(headroom.Cap.USD)))
	}
	lines = append(lines, "The pull request link lands here when the work is ready for review.")
	return provider.SessionActivity{Kind: provider.SessionThought, Body: strings.Join(lines, "\n")}
}

// postActivity reports an activity on the tracker item: through the
// tracker's AgentIdentity when it declares one, else as a comment, one
// paragraph per line, in the HTML every tracker comment is written in. A
// failure is logged; the intake went through.
func (s *Server) postActivity(ctx context.Context, tp provider.TrackerProvider, externalID string, activity provider.SessionActivity) {
	var err error
	if identity, ok := tp.(provider.AgentIdentity); ok {
		err = identity.PostActivity(ctx, externalID, activity)
	} else {
		var b strings.Builder
		for _, line := range strings.Split(activity.Body, "\n") {
			b.WriteString("<p>" + html.EscapeString(line) + "</p>")
		}
		err = tp.Comment(ctx, externalID, b.String())
	}
	if err != nil {
		s.Log.Error("first activity not posted", "provider", tp.Name(), "external_id", externalID, "kind", string(activity.Kind), "err", err)
	}
}

func teamsThatCanTakeIt(teams []string) string {
	if len(teams) == 0 {
		return "No configured Team can take it."
	}
	return "Teams that can take it: " + strings.Join(teams, ", ") + "."
}

// usd formats an amount as a person reads it on the card: two decimals
// after a US$ sign.
func usd(v float64) string {
	return "US$ " + strconv.FormatFloat(v, 'f', 2, 64)
}

func periodWords(p spendcap.Period) string {
	switch p {
	case spendcap.Day:
		return "today"
	case spendcap.Month:
		return "this month"
	}
	return "this week"
}
