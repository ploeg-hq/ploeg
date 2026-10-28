package plan

import "slices"

// IndependentReview is a Team's demand that the Roles judging a writer's work
// run on something other than the writer (ADR-0163). Model requires that no
// model a reviewing Role may call is one its writing Role may call; Harness
// asks the same of the Harnesses the two Roles may run.
type IndependentReview struct {
	Model   bool `json:"model" yaml:"model"`
	Harness bool `json:"harness,omitempty" yaml:"harness"`
}

// ReviewPair is a reviewing Role and a writing Role whose work it judges.
type ReviewPair struct {
	Reviewer string
	Writer   string
}

// ReviewPairs pairs every reviewing Role with every writing Role of the plan,
// each pair once, in plan order. A reviewing Role reads in a Round after the
// first writing Round and acts on its verdict, so a shadow or a planner does
// not review (ADR-0096, ADR-0031).
func (p TeamPlan) ReviewPairs() []ReviewPair {
	var writers []string
	for _, round := range p.Rounds {
		for _, r := range round.Roles {
			if r.Writes && !slices.Contains(writers, r.Name) {
				writers = append(writers, r.Name)
			}
		}
	}
	var pairs []ReviewPair
	afterWriter := false
	for _, round := range p.Rounds {
		afterWriter = afterWriter || slices.ContainsFunc(round.Roles, func(r Role) bool { return r.Writes })
		if !afterWriter {
			continue
		}
		for _, r := range round.Roles {
			if r.Writes || r.Shadow || r.Planner {
				continue
			}
			for _, w := range writers {
				pair := ReviewPair{Reviewer: r.Name, Writer: w}
				if !slices.Contains(pairs, pair) {
					pairs = append(pairs, pair)
				}
			}
		}
	}
	return pairs
}
