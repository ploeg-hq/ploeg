// Package llmbroker is the credential seam between Ploeg and an LLM
// gateway: mint a budgeted per-run credential before the harness starts,
// revoke it on every return path, and reconcile leaks from ploegd's sweeps.
// Callers speak run tokens; gateway-specific identity (LiteLLM's hashed
// tokens, the alias format) stays inside the implementation.
package llmbroker

import (
	"context"
	"time"
)

// Broker mints and revokes per-run LLM credentials (worker side).
type Broker interface {
	Mint(ctx context.Context, req MintRequest) (Credential, error)
	// Revoke is idempotent and best-effort: the gateway TTL is the backstop,
	// never the mechanism.
	Revoke(ctx context.Context, cred Credential) error
}

// Metered is an optional Broker capability: what a credential has actually
// spent, according to the GATEWAY. This is deliberately not part of Broker —
// a broker that cannot meter is still a valid broker, and the caller degrades
// to "cost unknown" rather than failing the run.
//
// It exists because the alternative is asking the agent how much it spent.
// Every adapter that could answer that (acp, claudecode) self-reports, and
// the two adapters actually deployed (openhands, exec) do not answer at all,
// which is why agent_runs.usage was NULL on every run ever recorded and
// shifts.spent stayed 0.0000 — silently, because the settlement SQL
// COALESCEs a missing cost to zero.
type Metered interface {
	// Spend returns the credential's spend so far. Only meaningful before
	// the credential is revoked.
	Spend(ctx context.Context, cred Credential) (float64, error)
}

// Settler is an optional capability: the gateway's durable record of what a
// run's credentials spent, read from a source that survives key revocation
// and deletion. Only a Settler can settle an account whose mint began.
type Settler interface {
	SettledSpendForRun(ctx context.Context, runToken string, keyIDs []string) (SettledSpend, error)
}

// KeyProber is an optional capability: whether the gateway holds no key for
// a run at all, neither under its alias nor under any recorded key identity.
// It answers true only on the gateway's own positive report of absence; an
// unreachable or failing gateway is an error, never an absence.
type KeyProber interface {
	RunKeysGone(ctx context.Context, runToken string, keyIDs []string) (bool, error)
}

// SettledSpend is a run's gateway spend, the size of the record behind it,
// and the token usage and models that record names. ByModel splits spend and
// tokens per model, sorted like Models.
type SettledSpend struct {
	USD  float64
	Keys int
	// ByAlias is true when no key identity was known and the entries were
	// found by the Run's key alias instead.
	ByAlias      bool
	Entries      int
	InputTokens  int64
	OutputTokens int64
	Models       []string
	ByModel      []ModelSpend
}

// ModelSpend is the part of a run's settled spend that one model accounts for.
type ModelSpend struct {
	Model        string
	USD          float64
	InputTokens  int64
	OutputTokens int64
}

// Sweeper is ploegd's reconciliation view: crash cleanup by run token and
// the periodic orphan sweep.
type Sweeper interface {
	// RevokeForRun revokes whatever credentials exist for a run token
	// (lease-expiry sweep).
	RevokeForRun(ctx context.Context, runToken string) error
	// SweepOrphans revokes every ploeg credential that does not belong to a
	// live run, returning how many were revoked (boot sweep).
	SweepOrphans(ctx context.Context, aliveRunTokens []string) (int, error)
}

// MintRequest describes the credential one run needs.
type MintRequest struct {
	RunToken  string
	BudgetUSD float64
	Models    []string // model scope; empty = unrestricted
	TTL       time.Duration
	// TeamID is the gateway team the credential is minted in; empty = none.
	TeamID string
	// MCPAccessGroups are the gateway MCP access groups the credential may
	// call; empty = no MCP tools. A broker refuses groups without a TeamID,
	// because the team is what bounds them (ADR-0078).
	MCPAccessGroups []string
}

// Credential is a minted per-run credential. Alias is the audit/trace id
// (exported to the harness as LLM_TRACE_ID); an empty APIKey means the
// harness image authenticates itself.
//
// MCPAccessGroups names the gateway MCP access groups the credential was
// minted with. A worker gives the harness the gateway's MCP endpoint only
// when it is non-empty (ADR-0078).
type Credential struct {
	APIKey          string   `json:"apiKey"`
	Alias           string   `json:"alias"`
	MCPAccessGroups []string `json:"mcpAccessGroups,omitempty"`
	RunToken        string   `json:"-"`
}
