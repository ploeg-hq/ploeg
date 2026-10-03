package work

import "testing"

func TestStateForOutcome(t *testing.T) {
	cases := map[Outcome]State{
		OutcomeStuck:           StateNeedsHuman,
		OutcomeFailed:          StateQueued,
		OutcomePROpened:        StateAwaitingReview,
		OutcomePRUpdated:       StateAwaitingReview,
		OutcomeIssueUpdated:    StateDone,
		OutcomeFollowUpCreated: StateDone,
		OutcomeNoChangeNeeded:  StateDone,
	}
	for o, want := range cases {
		if got := StateForOutcome(o); got != want {
			t.Errorf("StateForOutcome(%s) = %s, want %s", o, got, want)
		}
	}
}
