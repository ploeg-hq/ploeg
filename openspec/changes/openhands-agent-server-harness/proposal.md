## Why

The OpenHands CLI behind the `openhands` and `acp/openhands` Harnesses is unmaintained, and [ADR-0118](../../../docs/adrs/0118-the-unmaintained-openhands-cli-is-retired-as-a-harness.md) keeps OpenHands only through the interface OpenHands supports for orchestrators: the Agent Server API ([OpenHands#18000](https://github.com/OpenHands/OpenHands/issues/18000), [OpenHands#18046](https://github.com/OpenHands/OpenHands/pull/18046)). Until a Harness drives that server, Teams that want OpenHands run a CLI frozen at 1.16.0 and SDK 1.21.0 while the SDK moves on (1.55.1).

## What Changes

- A new Harness `openhands-agent-server`: the worker starts `openhands-agent-server` inside the Run's workload on loopback, opens one conversation with the Run's prompt, model and gateway key placeholder, follows its events until the conversation finishes or a limit stops it, and stops the server.
- Server events become transcript entries ([ADR-0090](../../../docs/adrs/0090-a-runs-agent-session-is-kept-as-a-bounded-redacted-transcript.md)) and touch the Run's activity clock, so the idle watchdog and the prompt limit ([ADR-0114](../../../docs/adrs/0114-each-run-records-the-limits-that-stop-its-prompt.md)) apply as they do to ACP.
- The Run's permission mode sets the conversation's confirmation policy; a confirmation request is answered from that mode or relayed through ploegd for an attended Run ([ADR-0100](../../../docs/adrs/0100-an-attended-run-relays-its-harnesss-requests-through-ploegd-and-falls-back-to-its-teams-policy.md)).
- The outcome comes from the outcome drop-box ([ADR-0018](../../../docs/adrs/0018-the-outcome-drop-box-is-every-harnesss-return-path.md)) as for every Harness; the conversation's end state decides `stuck` or `failed` when the drop-box is empty.
- The worker records the server's own version as the Harness version ([ADR-0097](../../../docs/adrs/0097-a-run-records-its-harness-version-and-the-teams-read-shows-each-roles-harness-and-model.md)) and passes the Run's trace context through the server's observability headers instead of OTLP environment variables ([ADR-0113](../../../docs/adrs/0113-a-runs-harness-exports-opentelemetry-to-a-collector-set-per-team-and-role.md)).
- The chart accepts `openhands-agent-server` as a Harness name; it is not added to `ploeg.qualifiedHarnesses`, so a Team uses it only as a Role default until it qualifies.

## Capabilities

### New Capabilities

- `openhands-agent-server-harness`: how a Run executes through the OpenHands Agent Server: server lifecycle inside the Run, one conversation per Run, event and outcome mapping, limits, permission handling and credentials.

### Modified Capabilities

None. The Harness seam's contract (`docs/contracts/taskspec.v1.schema.json`, `outcomereport.v1.schema.json`) is unchanged; the new Harness is selected through the existing Harness configuration ([ADR-0098](../../../docs/adrs/0098-an-operator-admission-may-choose-each-runs-harness-from-its-teams-allowlist.md), [ADR-0099](../../../docs/adrs/0099-each-allowed-harness-runs-in-its-own-workload-and-the-claim-selects-on-it.md)).

## Non-goals

- Removing the `openhands` or `acp/openhands` Harnesses; ADR-0118 removes them only after no Team defaults to them for a month.
- Running the Agent Server outside the Run's workload, sharing one server between Runs, or using the server's Docker runtime: the Run's own sandbox is the isolation boundary, as for every Harness.
- The server's git, file, bash, goal, profile and canvas routes: Ploeg's worker already owns the clone, the branch and the push.
- Building or installing the agent image; Ploeg names what the image needs, as for every profile ([ACP profiles](../../../docs/contracts/acp-profiles.md), "What an image needs").
- Qualification Runs; they follow when an operator chooses to spend on them.

## Impact

- Seam: **Harness** only (`pkg/harness/adapters/agentserver`, wiring in `pkg/worker/adapters.go`, Helm values for the Harness name).
- Rules: R2 (a dead pod ends the server with it; the Lease, not the server, is the crash-safety mechanism) and R4 (a stuck Outcome carries the conversation's end reason).
- New runtime dependency inside the agent image: `openhands-agent-server`, pinned with its lockfile.
- Docs: `docs/contracts/acp-profiles.md` gains a pointer, and a new `docs/contracts/agent-server-harness.md` records the upstream version and API surface the adapter uses.
