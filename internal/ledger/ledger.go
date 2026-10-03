// Package ledger holds the consistency gate for the ADR corpus in docs/adrs/
// (ADR 0001). The records and their Records index are two views of the same
// facts, and nothing but a gate keeps them agreeing.
//
// It lives in Go rather than as a script so it runs inside the existing
// `go test ./...` CI step: the runner is guaranteed a Go toolchain and is not
// guaranteed python3, and .forgejo/workflows/on_pull_request.yml already
// carries two comments about network-fragile setup actions. One implementation,
// no new CI dependency.
package ledger

// ADRDir is the corpus location, relative to the repository root.
const ADRDir = "docs/adrs"
