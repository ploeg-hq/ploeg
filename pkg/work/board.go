package work

import "time"

// PhaseBriefed is the checkpoint phase a worker writes once it has composed
// the Run's briefing, carrying what the briefing was read at and held
// (ADR-0124).
const PhaseBriefed = "briefed"

// BoardVersions is the version vector of the four owned sources a Run's
// briefing was read at (ADR-0124): the forge commit the Run started from,
// Ploeg's audit sequence, the notes and knowledge graph commits, and the
// tracker revision of the Work Item. An empty value is unknown.
type BoardVersions struct {
	ForgeCommit     string `json:"forgeCommit,omitempty"`
	PloegSequence   int64  `json:"ploegSequence"`
	NotesCommit     string `json:"notesCommit,omitempty"`
	ConceptsCommit  string `json:"conceptsCommit,omitempty"`
	TrackerRevision string `json:"trackerRevision,omitempty"`
}

// BoardBriefing is what a worker reports in its briefed checkpoint: the
// versions only it knows, and what the briefing held.
type BoardBriefing struct {
	ForgeCommit    string `json:"forgeCommit,omitempty"`
	NotesCommit    string `json:"notesCommit,omitempty"`
	ConceptsCommit string `json:"conceptsCommit,omitempty"`
	// Notes are the keys of the Work Item Notes the briefing showed.
	Notes []string `json:"notes,omitempty"`
	// Concepts are the "<source>/<path>" of the concepts the briefing showed.
	Concepts []string          `json:"concepts,omitempty"`
	Sections []BriefingSection `json:"sections,omitempty"`
}

// The names of a briefing's sections, each with a fixed budget (ADR-0124).
const (
	SectionFindings   = "findings"
	SectionWorkItem   = "work_item"
	SectionRepository = "repository"
	SectionClient     = "client"
	SectionTenant     = "tenant"
	// SectionReviewFindings and SectionQuestions are the answered review
	// findings and the questions and answers of ADR-0121, each a section of
	// the board view with a budget of its own.
	SectionReviewFindings = "review_findings"
	SectionQuestions      = "questions"
	// SectionActiveWork names the other live Shifts on the same repository
	// (ADR-0123), with a budget of its own.
	SectionActiveWork = "active_work"
)

// BriefingSection is one section of a briefing: its budget in bytes, what it
// used, how many items it showed and left out, and a digest of its text.
type BriefingSection struct {
	Name    string `json:"name"`
	Budget  int    `json:"budget"`
	Bytes   int    `json:"bytes"`
	Items   int    `json:"items"`
	Omitted int    `json:"omitted,omitempty"`
	Digest  string `json:"digest,omitempty"`
}

// The checkpoints at which a Run catches up with the board (ADR-0124). A
// refresh keeps the board file current for the agent to read before it
// pushes or gives its verdict; before_finish is the last look when the
// agent's turn ends.
const (
	CatchUpRefresh      = "refresh"
	CatchUpBeforeFinish = "before_finish"
)

// How a catch-up reaches the agent: the board file it reads, a further
// prompt turn, or the briefing of the next Round.
const (
	DeliveryFile      = "file"
	DeliveryPrompt    = "prompt"
	DeliveryNextRound = "next_round"
)

// BoardChangesRequest asks ploegd what changed on the board since the Run's
// briefing. ForgeHead is the head of the Run's branch as the worker read it
// now; ForgeMovedByOthers says that head holds a commit the Run did not make.
type BoardChangesRequest struct {
	Checkpoint         string `json:"checkpoint"`
	Delivery           string `json:"delivery"`
	ForgeHead          string `json:"forgeHead,omitempty"`
	ForgeMovedByOthers bool   `json:"forgeMovedByOthers,omitempty"`
}

// The sources of a board change.
const (
	SourceForge   = "forge"
	SourcePloeg   = "ploeg"
	SourceGraph   = "graph"
	SourceTracker = "tracker"
)

// BoardChange is one thing that changed since a Run's briefing. Summary is
// Ploeg's own words; Untrusted is text a tracker, an agent or a person
// wrote, rendered to the agent only as quoted data. Cite names the record
// the change is read from.
type BoardChange struct {
	Source    string    `json:"source"`
	Kind      string    `json:"kind"`
	At        time.Time `json:"at,omitzero"`
	Cite      string    `json:"cite"`
	Summary   string    `json:"summary"`
	Untrusted string    `json:"untrusted,omitempty"`
}

// BoardChangesResponse is what changed since the briefing: the versions the
// briefing was read at, the versions now, and at most MaxBoardChanges
// changes, newest first. Omitted counts the changes left out.
type BoardChangesResponse struct {
	Since   BoardVersions `json:"since"`
	Current BoardVersions `json:"current"`
	Changes []BoardChange `json:"changes"`
	Omitted int           `json:"omitted,omitempty"`
}

// MaxBoardChanges bounds one catch-up: it is a short difference, not a
// second briefing.
const MaxBoardChanges = 12
