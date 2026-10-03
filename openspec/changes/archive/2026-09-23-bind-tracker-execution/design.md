## Context

`work_items` has a unique `(provider, external_id)` identity. Current operator admission creates a manual Work Item; current ClaimRole locks Run, Shift, then Work Item. OpenShift previously checked only the live-Shift unique index. The existing Shift engine can therefore create pending Runs before a person finishes selecting a task. ADR 0024 supplies operator authority; ADR 0010 and R1 require adoption to arbitrate against these same rows.

## Goals / Non-Goals

**Goals:** bind queued pristine tracker work, preserve R2 and R13 under retries, and independently compare tracker freshness, repository policy and Store state.

**Non-Goals:** transferring an active Harness session, adopting historical patches or paid Runs, implicit handback, publishing changes, or introducing a multi-instance tracker identity model.

## Decisions

The durable choice is recorded in ADR 0026. Lookup accepts provider, native external ID, Scope and API root; the API root is only compared with configured provider policy. An optional provider read SPI returns a normalized Work Item and explicit open status. Lookup and new admission compare the current configured target resolver and container Team pin with the stored expectations; a missing or changed rule fails closed even before another webhook refreshes the row. A Forge locator resolves the registered endpoint and target tuple into the approved repository URL. Credentials and caller URLs are never used to select a new outbound endpoint.

Admission adds an optional source expectation with native revision, exact Work Target and row timestamp. The session advisory lock and original fingerprint settle replay first. New binding locks the Work Item, then attempts existing Shift and Run locks with NOWAIT. This avoids waiting against ClaimRole's inverse lock order; a competing claim yields an explicit conflict. Only pristine pending Runs are retired, retaining their rows as finished with no invented Outcome, before their Shift closes and the operator Shift is inserted.

Unattended Shift creation locks and rechecks its Work Item. Live Shift queries and Run expiry exclude operator membership rather than rewriting tracker origin. Bound items remain held even after completion; tracker updates refresh content but do not clear dispatch ownership. Adoption removes all pending Runs and marks the Work Item leased atomically, and a row-local `operator_owned` fence survives concurrent upserts. Legacy claims and KEDA queue counts exclude that flag; pending role counts remain accurate because adoption retires their entire unstarted roster.

## Risks / Trade-offs

- A provider read and database commit cannot form one distributed transaction → fresh reads precede locked comparison; the next tracker edit is preserved as content but cannot silently change dispatch authority.
- Native revisions may be missing → fail closed for binding; ordinary webhook mirroring remains available.
- Busy row locks can cause a retryable conflict → no paid effect occurs before admission and the client retains the same payload for replay.
- Existing bindings have lifetime uniqueness → no automatic relinquishment; subsequent work requires an explicit future policy.

## Migration Plan

Add migration 0014 for source provenance on operator executions and a row-local Work Item ownership fence, backfilled for existing operator bindings. Configure matching singleton sources and exact repository targets on both services before enabling imports. Disable the client binding option to roll back admission while keeping existing bindings fenced. Do not downgrade Ploeg while active bindings exist.

## Open Questions

Multi-instance provider identity and explicit post-review relinquishment are deferred. No live takeover or publication policy is inferred.
