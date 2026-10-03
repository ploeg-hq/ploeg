# Close the review loop

## Why

`run-multi-agent-shifts` puts several agents on one item and gets a reviewer's
findings onto the pull request. It cannot act on them. A Team's plan is a
fixed list of Rounds, so a reviewer that finds a real defect has nowhere to
send it: the next planned Round opens regardless of what the reviewer
concluded, and if the reviewer was last, the Shift closes with the defect
recorded and unfixed.

Feedback from a HUMAN has the same problem by a different route. Ploeg has no
forge webhook endpoint, so a person requesting changes on the pull request
reaches nothing (architecture.md §9.1, backlog #1). The only way back in is
re-assigning the ticket by hand.

Both halves are the same missing capability: work that has been reviewed
should be able to continue.

## What Changes

**Seams touched:** run API (an additive contract field), store (one migration),
and tracker/forge ingest. The engine is the only orchestration surface that
changes.

- **A verdict on the OutcomeReport.** A reading Run may return
  `verdict: approve | request_changes`. Additive to `outcomereport.v1`,
  persisted on `agent_runs` (migration 0010). Honoured only from a reading
  Role — a writer approving its own work would be the loop grading itself.
- **Verdict-driven fix rounds (ADR-0017).** When a plan's final Round
  completes and any reader asked for changes, the engine re-opens the plan's
  last writing Round with the findings attached, then the review Round after
  it. Bounded, in order, by: the Shift pool, `maxFixRounds` (default 2), and
  the verdict itself.
- **`maxFixRounds` on the Team plan.** Config, validated at boot: a plan that
  configures fix rounds with no writing Round to re-run is refused.
- **A forge webhook route.** `POST /webhooks/forge/{provider}`, HMAC-verified
  on the raw body, fast-acking inside Forgejo's 5 s `DELIVER_TIMEOUT`, deduped
  on `X-Forgejo-Delivery`. It gives `ForgeProvider.ParseWebhook` — shipped and
  callerless — its first caller.
- **Forge feedback is untrusted input.** A human's review body reaches a
  higher-trust context, so it is framed as evidence to weigh rather than
  instructions to follow, exactly as agent findings already are (backlog #9).

## Capabilities

**New Capabilities**

- `verdict-advancement` — what makes a fix Round open, and the three bounds
  that stop the loop.
- `forge-ingest` — the forge webhook endpoint: verification, dedup, and what
  a review event does when it lands.

**Modified Capabilities**

- `blackboard` — findings gain a verdict alongside them.

## Non-goals

- **Follow-up Work Items from forge events.** This change lands the endpoint
  and normalizes what arrives; routing a `review_submitted` into a re-mandate
  is the next change, and needs the branch→Work Item reverse lookup backlog
  #107 owes it.
- **Classifying feedback as vague or security-sensitive** (R9's classifier,
  backlog #9). Everything arriving from a forge is treated as untrusted here;
  grading it is later work.
- **Reconciling the two request-changes paths.** An agent's verdict and a
  human's review both mean "keep going", and unifying them is deliberately
  deferred until the forge route has carried real traffic — ADR-0017 names
  that as a re-evaluation trigger.
- **Check-failure and merge-conflict handling.** Parsed, audited, dropped.
- **A live network path from Forgejo to ploegd.** Both directions are blocked
  today (documented in the runbook); wiring it is ops work, not a code change.

## Impact

**Code.** `pkg/shiftengine` (the loop) · `pkg/httpapi` (the route) ·
`pkg/harness` + `docs/contracts/outcomereport.v1.schema.json` (the verdict
field, additively) · `pkg/plan` (`maxFixRounds` and its validation) ·
`pkg/store` (migration 0010, verdict on the round-roster read).

**Contracts.** `outcomereport.v1` gains one optional enum field. No shape
changes.

**Money.** A Shift's maximum spend is no longer readable from its plan alone —
it is `plan × (1 + maxFixRounds)`, still bounded by the pool, which is checked
before every Round.

**Security.** The forge endpoint is a new unauthenticated-by-default surface.
It verifies HMAC on the raw body before parsing, and it is the first inbound
path carrying text written by someone outside the factory.
