---
status: accepted
date: 2026-10-10
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-30
---

# Ploeg serves its operator reads as MCP tools through a separate ploeg-mcp command

## Context and Problem Statement

People work in AI clients that speak the Model Context Protocol. From there they could reach the tracker but not Ploeg: which Work Items wait for a person and why, what a Shift spent, what a Run's verdict was. The operator API already serves all of it, but only to a consumer that writes its own HTTP client. The owner accepted an MCP server on the operator API on 2026-09-30, in the consumer's decision record, and asked on 2026-10-10 for it to be built. Where does that server live, and what may it do?

## Decision Drivers

* A person asks "what is waiting for me and why" from their own client, without a browser.
* Ploeg names none of its consumers ([ADR 0069](0069-ploeg-names-none-of-its-consumers.md)); the server is one more Operator Consumer.
* ploegd's attack surface and its authority over budgets do not grow.
* Runs get no Ploeg MCP server; they reach tools only through the gateway ([ADR 0078](0078-runs-reach-read-only-mcp-tools-only-through-the-gateway-bounded-by-a-litellm-team.md)).

## Considered Options

* A separate `ploeg-mcp` command that reads the operator API with its own consumer token
* MCP routes inside ploegd
* No server: document raw operator API calls

## Decision Outcome

Chosen option: "A separate `ploeg-mcp` command", because it adds no route, no listener and no authority to ploegd, and a deployment that does not want it never runs it.

* `cmd/ploeg-mcp` serves MCP over stdio with `github.com/modelcontextprotocol/go-sdk`. It reads `PLOEG_URL` and `PLOEG_MCP_TOKEN` and refuses to start without them.
* It calls the operator API through `pkg/operatorclient`, which sends the bearer token, refuses redirects, bounds each call to 5 seconds and 16 MiB, and never puts the token in an error.
* It has five read-only tools: `ploeg_overview`, `ploeg_find_work`, `ploeg_get_work`, `ploeg_recent_runs` and `ploeg_changes_since`. Each has a title, `readOnlyHint`, an output schema, and returns its answer as structured content and text.
* Text from trackers, forges and agents sits under an `untrusted` key; the server's instructions tell the client to treat it as data. Amounts are rounded to cents.
* An operator answer of 401, 403, 404 or a timeout becomes a tool error without the token, and the server keeps running.
* The consumer that `ploeg-mcp` uses should have `execute: false`. Write tools (propose, approve, cancel) and a remote transport are not part of this decision.
* The Ploeg image ships the binary at `/usr/local/bin/ploeg-mcp`; the chart is unchanged.

### Consequences

* Good, because any MCP client reads Ploeg's state with one local command and one token.
* Good, because ploegd is unchanged and the reader is an ordinary, revocable Operator Consumer.
* Bad, because the module gains the MCP Go SDK and its dependencies; only `cmd/ploeg-mcp` imports them.
* Bad, because a laptop needs a route to ploegd, a port-forward until a remote transport is decided.

### Confirmation

`go test ./cmd/ploeg-mcp/` drives every tool through in-memory MCP transports against the real operator handler and PostgreSQL, including the 401, 404 and timeout paths and the token never appearing in a result. `go test ./pkg/operatorclient/` tests the client against the same handler. `go test ./internal/boundary/` keeps consumer names out of both.

## Pros and Cons of the Options

### MCP routes inside ploegd

* Good, because no extra process.
* Bad, because ploegd would carry a protocol, session handling and a new public surface next to budget authority, for every deployment.

### No server

* Good, because nothing to build.
* Bad, because every client and every person writes their own operator calls and token handling.

## Re-evaluation triggers

* A person asks to propose, approve or cancel work from an AI client.
* Someone needs Ploeg's tools from a machine without a route to ploegd.
* The MCP Go SDK publishes a major version or the stdio transport is deprecated.

## More Information

* How-to: [Ask Ploeg from an AI client over MCP](../how-to/ask-ploeg-from-an-ai-client.md).
* The consumer's record of 2026-09-30 named the tools after the consumer; this record names them after Ploeg because of ADR 0069.
