---
status: proposed
date: 2026-10-08
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Runs reach read-only MCP tools only through the gateway, bounded by a LiteLLM team

## Context and Problem Statement

[ADR-0030](0030-target-repository-instructions-rank-below-the-delivery-contract.md)
runs `claude-code` with `--strict-mcp-config` and no `--mcp-config`, so a Run
loads no MCP server at all. That closed the path where a target repository's
`.mcp.json` connects servers without a trust dialog. It also means an agent
cannot read anything outside the clone and the forge, even when an executor team
wants it to read the metrics and logs of the system it is changing.

The LiteLLM gateway now serves MCP tools behind the same per-Run virtual key the
Run already holds. As verified on the live gateway on 2026-10-08:

* With `require_key_mcp_access_defined: true`, a key gets MCP tools only when
  it is minted with `object_permission.mcp_access_groups`. Team membership
  alone grants nothing.
* A key minted with `team_id` of team X and an access group team X does not
  allow is refused with HTTP 403 ("Key requests MCP access groups not allowed
  by team"). The pair (team, access groups) is the boundary.
* `key_type: llm_api` keys work on `/mcp` when granted.
* `/key/generate` takes the team's `team_id` and never its alias: a mint
  naming the alias is refused ("Unable to find team object"). LiteLLM assigns
  a random id unless `/team/new` is given one, so operators should create the
  team with a fixed `team_id` (the alias itself works well), and Ploeg takes
  that id.

Can a Run use MCP tools, and if so which ones and through what?

## Decision Drivers

* Least privilege: a Run's tools must be bounded by something other than the
  master key Ploeg mints with, which can grant every access group.
* ADR-0030 stays intact for everything it protects: no MCP server from the
  target repository, ever.
* The real key never reaches the harness under key isolation; MCP traffic must
  go through the same worker proxy as model traffic.
* Opt-in per executor team, with today's behaviour as the default.

## Considered Options

* MCP only through the LiteLLM gateway, read-only access groups bound by a
  LiteLLM team, opt-in per executor team
* Keep ADR-0030's "no MCP" rule for every Run
* Let a team name MCP servers for the Run directly (URLs and credentials in the
  chart)

## Decision Outcome

Chosen option: "MCP only through the LiteLLM gateway, read-only access groups
bound by a LiteLLM team, opt-in per executor team", because the gateway already
holds the per-Run key, its spend and its revocation, and the LiteLLM team gives
an enforcement point Ploeg's master key cannot exceed.

This partly supersedes ADR-0030: its "no MCP servers under `claude-code`" rule
now reads "no MCP server except the gateway's, and only for a team that opts
in". Everything else in ADR-0030 stands, including `--strict-mcp-config`.

* **Policy.** An LLM policy gains optional `litellmTeamId` and
  `mcpAccessGroups` ([llm_control.go](../../pkg/httpapi/llm_control.go)). The
  chart renders them from `executor.teams[].litellmTeamId` and
  `executor.teams[].mcpAccessGroups`. Both empty is exactly the behaviour
  before this record. Groups without a team id are refused by the chart, by
  ploegd at boot, by the store and by the broker.
* **Minting.** ploegd mints the Run's key, for worker and operator Runs alike,
  with `team_id` and `object_permission: {"mcp_access_groups": [...]}`
  ([litellm.go](../../pkg/llmbroker/litellm.go)). Both are fixed on the Run's
  LLM account at reservation (migration `0042`), like the budget and the model
  scope. The `static-compatibility` credential mode mints nothing and never
  wires MCP.
* **Agent wiring.** When the issued credential carries access groups, the
  worker sets the gateway's MCP endpoint (`<gateway root>/mcp`, through its
  loopback proxy). `claude-code` gets `--mcp-config` with one HTTP server named
  `litellm` and the header `x-litellm-api-key: Bearer <key the harness holds>`,
  and keeps `--strict-mcp-config`, so nothing else loads. The worker's key proxy
  accepts the placeholder in `x-litellm-api-key` and swaps the real key in, as
  it does for `Authorization` and `X-Api-Key`. `acp` passes the same server in
  `session/new` only when the agent advertises MCP over HTTP; `openhands` and
  `exec` get none.
* **Read-only only.** A team names only access groups whose servers are
  read-only. Granting a write-capable access group to any Run needs a further
  ADR.

### Consequences

* Good, because a team can let its agents read observability data through the
  gateway's audit and spend ledger, with no new credential in the chart.
* Good, because the bound is enforced twice: Ploeg refuses groups without a
  team, and LiteLLM refuses groups the team does not allow.
* Good, because the target repository still loads no MCP server.
* Bad, because "read-only" is a property of the gateway's server configuration,
  not something Ploeg can check. A LiteLLM admin who adds a write tool to an
  allowed group widens every opted-in team's Runs without a Ploeg change.
* Bad, because the chart carries a LiteLLM-assigned team id. A team recreated
  in LiteLLM gets a new id and every mint for that executor team fails until the
  value changes; it fails closed, as an unresolved issuance.
* Bad, because the tool results enter the agent's context, so the data the
  access group exposes becomes prompt-injection surface for the Run.
* Neutral, because the MCP traffic is billed and logged on the Run's key and
  counts as harness activity for the idle timeout.

### Confirmation

* `pkg/litellm/client_test.go` and `pkg/llmbroker/litellm_test.go` pin the
  `/key/generate` body with and without a team, and the refusal of groups
  without a team.
* `pkg/httpapi/llm_mcp_test.go` pins that a policy's team and groups reach the
  mint and the issued credential, and that a policy with groups but no team
  stops ploegd at boot.
* `pkg/harness/adapters/claudecode/claudecode_test.go` asserts
  `--strict-mcp-config` in every case, no `--mcp-config` without a grant, and
  exactly one `litellm` HTTP server with the placeholder header with one.
* `pkg/worker/llmproxy_test.go` asserts the proxy accepts the placeholder in
  `x-litellm-api-key` and forwards the real key there, never the placeholder.
* `scripts/helm-golden.sh check` (run by `mise run verify`) pins the rendered
  policy JSON and refuses groups without a team id.
* Not yet confirmed: a real `claude-code` Run in the worker image listing the
  gateway's tools through the proxy. That needs one opted-in team on a cluster.

## Pros and Cons of the Options

### Keep ADR-0030's "no MCP" rule for every Run

* Good, because nothing changes and the attack surface stays smallest.
* Bad, because a team that wants its agents to read production signals has to
  paste them into the Work Item by hand.

### Let a team name MCP servers for the Run directly

* Good, because any MCP server would work, not only those the gateway serves.
* Bad, because every server brings its own credential into the chart and the
  Run, outside the per-Run key, its budget and its revocation.
* Bad, because nothing bounds the tools except the server's own configuration.

## Re-evaluation triggers

* A team asks for a write-capable access group.
* LiteLLM drops `require_key_mcp_access_defined`, or stops refusing a key's
  access groups that its team does not allow.
* `/key/generate` accepts a team alias, which would let the chart name teams
  stably.
* An opted-in Run's transcript shows an instruction taken from a tool result.

## More Information

* 2026-10-08 — Proposed with the policy, mint, worker and chart change. The
  gateway facts above were verified against the live LiteLLM that day.
* Related: [0008](0008-litellm-is-the-credential-and-metering-seam.md),
  [0030](0030-target-repository-instructions-rank-below-the-delivery-contract.md).
