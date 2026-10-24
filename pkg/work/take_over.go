package work

// CloseReasonTakenOver closes a Shift whose running Run a person took over
// (ADR-0142). The Work Item waits at needs_human until the person hands it
// back.
const CloseReasonTakenOver = "taken_over"

// Handback tells the first writing Run of a Shift a person handed back where
// to continue (ADR-0142): Branch as the person left it, and YieldedCommit,
// the branch head when the taken-over Run yielded, so the Run can name the
// commits the person added since. YieldedCommit is empty when the yielded
// Run pushed nothing.
type Handback struct {
	Branch        string `json:"branch"`
	YieldedCommit string `json:"yieldedCommit,omitempty"`
}
