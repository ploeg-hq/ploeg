package httpapi

import (
	"fmt"
	"slices"
	"sort"

	"github.com/ploeg-hq/ploeg/pkg/plan"
)

// CheckIndependentReview refuses a Team plan whose independentReview the
// configured Roles break (ADR-0163). With Model set, no model a reviewing
// Role's inference policy admits may be one its writing Role's policy admits;
// with Harness set, the same holds for the Harnesses each Role's admission may
// choose. A Role with no policy or no Harness configuration is refused, since
// nothing then proves the difference. llm is nil when ploegd runs without
// managed inference.
func CheckIndependentReview(plans plan.Plans, llm *LLMControl, harnesses TeamHarnesses) error {
	teams := make([]string, 0, len(plans))
	for team := range plans {
		teams = append(teams, team)
	}
	sort.Strings(teams)
	for _, team := range teams {
		rule := plans[team].IndependentReview
		if rule == nil {
			continue
		}
		for _, pair := range plans[team].ReviewPairs() {
			if rule.Model {
				if err := distinctModels(team, pair, llm); err != nil {
					return err
				}
			}
			if rule.Harness {
				if err := distinctHarnesses(team, pair, harnesses); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func distinctModels(team string, pair plan.ReviewPair, llm *LLMControl) error {
	models := func(role string) ([]string, error) {
		if llm == nil {
			return nil, fmt.Errorf("team %q requires its reviewing Role %q to run a different model from its writing Role %q, "+
				"but ploegd runs without managed inference policies, so nothing proves the models differ (ADR-0163)", team, pair.Reviewer, pair.Writer)
		}
		p, ok := llm.Policy(team, role)
		if !ok {
			return nil, fmt.Errorf("team %q requires its reviewing Role %q to run a different model from its writing Role %q, "+
				"but Role %q has no managed inference policy, so nothing proves the models differ (ADR-0163)", team, pair.Reviewer, pair.Writer, role)
		}
		return p.Models, nil
	}
	reviewer, err := models(pair.Reviewer)
	if err != nil {
		return err
	}
	writer, err := models(pair.Writer)
	if err != nil {
		return err
	}
	for _, m := range reviewer {
		if slices.Contains(writer, m) {
			return fmt.Errorf("team %q requires its reviewers to run a different model from its writers, "+
				"but reviewing Role %q and writing Role %q may both run model %q (ADR-0163)", team, pair.Reviewer, pair.Writer, m)
		}
	}
	return nil
}

func distinctHarnesses(team string, pair plan.ReviewPair, harnesses TeamHarnesses) error {
	admitted := func(role string) ([]string, error) {
		h, ok := harnesses[team][role]
		if !ok {
			return nil, fmt.Errorf("team %q requires its reviewing Role %q to run a different harness from its writing Role %q, "+
				"but Role %q has no harness configuration, so nothing proves the harnesses differ (ADR-0163)", team, pair.Reviewer, pair.Writer, role)
		}
		var keys []string
		for _, a := range h.Allowlist() {
			if key := harnessKeyOf(a.OperatorRoleHarness); h.Admits(key) {
				keys = append(keys, key)
			}
		}
		return keys, nil
	}
	reviewer, err := admitted(pair.Reviewer)
	if err != nil {
		return err
	}
	writer, err := admitted(pair.Writer)
	if err != nil {
		return err
	}
	for _, k := range reviewer {
		if slices.Contains(writer, k) {
			return fmt.Errorf("team %q requires its reviewers to run a different harness from its writers, "+
				"but reviewing Role %q and writing Role %q may both run harness %q (ADR-0163)", team, pair.Reviewer, pair.Writer, k)
		}
	}
	return nil
}
