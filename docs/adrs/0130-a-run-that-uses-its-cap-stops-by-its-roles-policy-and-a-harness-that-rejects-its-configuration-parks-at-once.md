---
status: accepted
date: 2026-10-20
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A Run that uses its cap stops by its Role's policy, and a harness that rejects its configuration parks at once

## Context and Problem Statement

Two kinds of failed Run were classified `agent_error` and retried as if the ticket had beaten the agent.

On Work Item 242 (vikunja:2240, Team bronze) builder Runs 381, 385 and 390 each worked 30 to 40 minutes until the model gateway refused the Run's own key: `litellm.BadRequestError: Litellm_proxyException - Budget has been exceeded! Key=… Current cost: 2.059854912, Max budget: 2.0`. Run 306 on Work Item 190 ended the same way through `litellm.RateLimitError`. The ACP adapter read the gateway's refusal only from stderr and only at `session/new`; mid-prompt the agent reported it as its own message, so the Round was reopened twice at the same cap, model and prompt. $6.12 bought no branch, and the Shift closed `writing_run_failed_repeatedly`.

On Work Item 188, Runs 292 to 295 failed in under 30 seconds each with `Error: 6 validation errors for MCPConfig mcpServers.litellm…`, and on Work Item 232 Runs 353, 355 and 359 with `observe() got an unexpected keyword argument 'rollout_entrypoint'`. Neither called the model. Both are properties of the agent image or of the configuration the Run was given, so every retry fails the same way, and charging them to the ticket parks good work: Shift 163 used its three attempts within two minutes.

What does Ploeg do after a writing Run used its whole cap, how does it recognise a harness that never got to work, and how do consumers tell a person which of the two, the ticket or the system, is at fault?

## Decision Drivers

* A retry must change something that could change the result; the same cap, model and prompt on the same ticket does not.
* The classification must come from what the worker observed, not from matching the wording of two incidents.
* A deterministic system fault must not cost the ticket its attempts, and must say plainly that it is not the ticket.
* The owner decides per Role whether more money is worth trying, so the policy is configuration, refused at boot when malformed.
* A consumer (the Unfold UI) groups failures without knowing every enum value.

## Considered Options

* A per-Role `onCapReached` policy with `stop` as the default, a `harness_config` reason that parks at its first occurrence, and a fixed explanation table on every failure read
* Keep retrying a spent cap and let MaxRunAttempts bound it
* Always escalate a spent cap until the pool is spent
* Park a harness configuration failure only after a small number of occurrences

## Decision Outcome

Chosen option: "A per-Role `onCapReached` policy with `stop` as the default, a `harness_config` reason that parks at its first occurrence, and a fixed explanation table on every failure read", because both failures are deterministic for the work in hand, and the owner, not a retry loop, is the one who can change the cap or the image.

**A Run that used its own cap fails `budget`.** The worker classifies it from its own evidence after the harness ends, whatever phase the harness was in: the gateway reported the key's spend at or past what the Run was authorized, or the worker's loopback model proxy saw the gateway refuse the Run's own key for its budget after the Run had spent something. The ACP adapter also recognises the refusal mid-prompt on stderr or the transport error once the protocol shows the model at work. A fresh key refused before any spend stays `infra_llm`, as ADR-0111 records, because that is a gateway misconfiguration, not the Run's spend. ACP's `max_tokens` stop reason, which `budget` used to mean, is now `token_ceiling` and retried as before.

**The writing Role's `onCapReached` decides what follows.** It is set per Role in the Team plan, next to `cap`:

* `stop`, the default when unset: no retry. The Shift closes `run_cap_reached` and the Work Item goes to `needs_human` with the Run's own numbers: "the builder used its whole $2.00 cap ($2.06 spent) in round 2 before it finished … Raise the cap for this role or split the ticket".
* `retry`: the Round reopens at the same cap and the attempt counts against MaxRunAttempts, which was the behaviour for every spent cap before.
* `escalate`: the Round reopens with the next attempt authorized at the failed Run's authorization times `capEscalation` (default 2), stored on the pending Run as `agent_runs.escalated_cap` (migration `0068_escalated_run_caps.sql`). The claim bounds it by what the Shift's pool has left, as for every Run, so it never exceeds what the pool can fund. It counts against MaxRunAttempts.

`pkg/plan` refuses at boot an unknown value, `onCapReached` on a reading Role, `capEscalation` without `escalate`, a factor of 1 or less, `escalate` on a Role without a cap, and a Role that sets different policies in different Rounds. The chart's values schema mirrors the enum. A synthesized plan (a plan-less Team under uniform dispatch) has no Roles to configure and keeps requeueing a failed Run as before.

**A harness that rejected its configuration fails `harness_config` and parks at its first occurrence.** It is detected structurally in two places. The ACP adapter classifies a JSON-RPC error the agent itself wrote on the wire in answer to `initialize` or `session/new`, and a protocol version this client cannot speak; a timeout or a closed transport, which the SDK also reports as a request error, stays `infra_node`. The worker classifies any harness that drives a model and failed (`agent_error`, `infra_llm` or no classification) when its loopback proxy forwarded not one model-driving request (a path ending in `/chat/completions`, `/completions`, `/responses`, `/messages`, `:generateContent` or `:streamGenerateContent`) to the model gateway, provided the proxy holds the Run's key or the gateway reported no spend for it. Metadata reads do not count: OpenHands reads `/v1/model/info` through the proxy during `session/new`, and both incidents then failed at the first prompt, answering `session/prompt` with JSON-RPC `-32603` "Failed to process prompt", which the ACP adapter alone cannot tell from an agent that worked and failed. The Run's summary quotes the harness's error line. `harness_config` is a reason that parks for a person (`ParksForHuman`): the Shift closes `harness_config`, the Work Item goes to `needs_human` with "This is a system configuration problem, not the ticket … The harness said: …", and no attempt is spent, so the ticket keeps all three when a person restarts it.

**Every failure read says whether the ticket or the system caused it.** A fixed table in `pkg/work` explains each failure reason and each failure close reason with a `cause` of `ticket` or `system`, a one-sentence `title` and an `action`. The operator API carries it as `failure` on a Run and on the Run list, and as `closeFailure` on a Shift; `ploeg-mcp`'s `ploeg_get_work` and `ploeg_recent_runs` carry the same, and the Work Item summary names the cause. The mapping is published in [the contracts README](../contracts/README.md#failure-explanations).

### Consequences

* Good, because a spent cap costs one cap, not three, unless the owner chose to pay more, and the person reading the Work Item sees the cap, the spend and what to change.
* Good, because an image or configuration fault parks after one Run of a few seconds instead of three, and the message sends a person to the system rather than the ticket.
* Good, because a consumer groups failures by `cause` and shows Ploeg's own sentence without mapping enum values itself.
* Bad, because `budget` changed meaning: rows written before this record with `budget` were ACP token ceilings. Dashboards that group by reason see the split from this release on.
* Bad, because a transient fault before the first model call, such as an image pull race that lets the harness start but not reach the proxy, parks the Work Item; a person restarts it. A deterministic fault was judged far more likely from the incidents, and a person-visible park is cheaper than three wasted Runs.
* Bad, because `stop` as the default parks tickets that a retry at the same cap would have finished. The owner chose it; a Team that prefers the old behaviour sets `retry`.

### Confirmation

* `go test ./pkg/worker/` replays both incidents through the ACP adapter and the key proxy (initialize, `session/new` with a `model/info` read, a prompt answered with `-32603` carrying the real error) and expects `harness_config`, while the same failure after a completion call stays `agent_error`; it also proves a Run whose observed spend reached its cap, or whose gateway refused its own key mid-run, fails `budget` with the cap and spend in its summary; that a fresh key refused before any spend does not; that a model-driving harness that failed without one proxied model request fails `harness_config` quoting its error line; that one that called the model keeps `agent_error`; and that a Run without a proxy, an exec harness and a timed-out Run are left alone.
* `go test ./pkg/harness/adapters/acp/` proves the mid-prompt own-key refusal is `budget`, `max_tokens` is `token_ceiling`, a JSON-RPC error answer to `initialize` or `session/new` and an unspeakable protocol version are `harness_config`, and an SDK-made transport error or timeout stays `infra_node`, including against a fake agent process.
* `go test ./pkg/plan/` refuses each malformed `onCapReached` and `capEscalation`; `go test ./pkg/store/` proves the escalated cap is what the next claim authorizes and never more than the pool has left; `go test ./pkg/shiftengine/` proves `stop` parks at the first spent cap with the message, `retry` keeps the cap and the attempt limit, `escalate` doubles the cap within the pool, and `harness_config` parks at its first occurrence quoting the harness.
* `go test ./pkg/work/` proves every failure reason and failure close reason has an explanation and that every infrastructure reason, `shared_budget_spent` and `harness_config` are `system`; `go test ./pkg/httpapi/` and `go test ./cmd/ploeg-mcp/` validate the reads against `operator-api.v1.schema.json` and the MCP output.
* `scripts/helm-golden.sh check`, run by `mise run verify`, renders the plan with the new fields and refuses an unknown `onCapReached`.
* CI runs all of them with `mise exec -- go test ./...`.

## Pros and Cons of the Options

### Keep retrying a spent cap and let MaxRunAttempts bound it

* Good, because it needs no configuration.
* Bad, because it is what spent $6.12 on Work Item 242 for nothing: the same cap, model and prompt fail the same way.

### Always escalate a spent cap until the pool is spent

* Good, because a ticket that needs $3 finishes without a person.
* Bad, because it spends the whole pool on tickets that a person would have split, and the owner wants that choice per Role.

### Park a harness configuration failure only after a small number of occurrences

* Good, because a one-off startup fault would be retried.
* Bad, because both incidents were deterministic, so the extra attempts only delay the person and cost Runs; Runs 292 to 294 were three attempts within 60 seconds.

## Re-evaluation triggers

* A Team sets `onCapReached: retry` or `escalate` and more than half of its retried attempts after a spent cap fail again with `budget` over a month.
* `harness_config` parks a Work Item whose restart then succeeds without any image or configuration change, more than twice in a month.
* A harness Ploeg runs reaches the model by a path the worker's proxy does not see.
* LiteLLM changes how it reports a key's spend or its budget refusal.
* A consumer needs a cause other than `ticket` and `system`.

## More Information

* Evidence: Work Items 242 (Runs 381, 385, 390), 190 (Run 306), 188 (Runs 292 to 295, Shift 163) and 232 (Runs 353, 355, 359, Shift 213).
* The MCPConfig rejection came from OpenHands' ACP server turning the `litellm` HTTP MCP server Ploeg sends in `session/new` (ADR-0078) into its own `MCPConfig`, which rejected it; homelab-cluster `1abf5a05` removed bronze's `mcpAccessGroups` on 2026-10-17 so its Runs get no MCP server. The lmnr `observe()` error came from the OpenHands CLI's openhands-sdk 1.21.0 under `OTEL_*`; `edecdf2` withheld the variables from OpenHands and `2da422f` lets a deployment opt it back in on agent-runner 1.4.0 ([ADR-0113](0113-a-runs-harness-exports-opentelemetry-to-a-collector-set-per-team-and-role.md)).
* Related: [ADR-0019](0019-a-failed-writing-run-reopens-its-round.md), [ADR-0021](0021-infra-failures-and-agent-failures-get-separate-retry-budgets.md), [ADR-0105](0105-a-credential-leak-parks-the-work-item-and-closes-its-pull-request.md), [ADR-0111](0111-a-spent-litellm-budget-wider-than-the-runs-key-parks-the-work-item.md), [ADR-0012](0012-two-level-budgets-authorized-and-settled.md).
