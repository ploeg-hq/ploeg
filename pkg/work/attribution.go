package work

// Attribution is who a Run's commits name (ADR-0206): the account its forge
// credential pushes as, which authors and commits them, and the person the
// Run acts for (ADR-0156), named in a trailer.
type Attribution struct {
	// Committer is the forge account's own identity. Nil when ploegd could
	// not read it; the worker then commits as its neutral fallback.
	Committer *GitIdentity `json:"committer,omitempty"`
	// Person is Ploeg's id of the person the Run acts for, and PersonProof
	// how Ploeg knows them: proven, asserted or sponsor.
	Person      string `json:"person,omitempty"`
	PersonProof string `json:"personProof,omitempty"`
	// Run is the Run's id, the transaction token's txn.
	Run string `json:"run,omitempty"`
}

// GitIdentity is a git author or committer.
type GitIdentity struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}
