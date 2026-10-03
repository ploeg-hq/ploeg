package work

// Terminal reports whether nothing further happens to the item without a new
// human mandate. It is the tracker write-back's gate: queued is the only
// settle result that is NOT terminal, because a failed run under the retry
// threshold comes back round and telling the board "Ploeg finished" mid-retry
// would be a lie.
func Terminal(s State) bool {
	return s == StateDone || s == StateNeedsHuman || s == StateAwaitingReview || s == StateStale || s == StateWithdrawn
}

// StateForOutcome maps a terminal Outcome to the Work Item state it produces:
// stuck routes to a human queue (R4), failed releases the lease for retry
// (R5), a pull request opened or updated awaits human review, and everything
// else completes the item.
func StateForOutcome(o Outcome) State {
	switch o {
	case OutcomeStuck:
		return StateNeedsHuman
	case OutcomeFailed:
		return StateQueued
	case OutcomePROpened, OutcomePRUpdated:
		return StateAwaitingReview
	default:
		return StateDone
	}
}
