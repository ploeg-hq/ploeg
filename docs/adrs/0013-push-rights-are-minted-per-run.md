---
status: accepted
date: 2026-07-29
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Push rights are minted per Run and die with the Lease

## Context and Problem Statement

Every worker pod today receives the same static `AGENT_BUILDER_TOKEN` and embeds
it in the clone URL (`pkg/worker/worker.go`). The Lease says which Run may write
the branch, but nothing enforces that claim: every pod that has ever run holds
identical push rights, including a zombie whose Lease lapsed an hour ago.

[ADR-0010](0010-shift-owns-the-item-lease-owns-the-branch.md) makes this worse
rather than better. It introduces *reading* Runs — reviewer, security
specialist, CFO — which take no Lease precisely because they do not write. Under
the current credential model a reviewer pod holds push access to the very branch
it is reviewing, and the writer/reader split that all of ADR-0010's concurrency
safety rests on is a convention rather than a boundary.

So: the Lease records a right it cannot enforce. Either it earns its keep or it
should not exist.

## Decision Drivers

* **A record that cannot enforce its own claim is ceremony.** The Lease must
  correspond to a real capability or be deleted.
* **The project already decided this once.**
  [ADR-0008](0008-litellm-is-the-credential-and-metering-seam.md) made LiteLLM
  the credential seam on the reasoning that an agent handed a $2 key *cannot*
  spend $3. Money is enforced by capability; the branch is not.
* **Readers are the population that will grow.** A Shift may run one writer and
  five readers. The credential model should be strictest where the count is
  highest.
* **R2 — crash-safety must never depend on an agent behaving well at death.**
  Revocation cannot be something the agent does on its way out.
* **Blast radius.** A leaked static token is unbounded in time and scope; a
  leaked per-run token is bounded by both.

## Considered Options

* Keep the shared static token; treat the writer/reader split as convention
* Read-only credentials for readers only
* Mint a scoped token per writing Run and revoke it with the Lease
* Per-branch deploy keys
* Forge-side branch protection rules

## Decision Outcome

Chosen option: **mint a repo-scoped, write-scoped forge token for each writing
Run, and revoke it when the Run settles or its Lease expires. Readers receive a
read-only credential.** Holding the Lease and being able to push become the same
fact.

`pkg/llmbroker` gains a sibling: the same mint-scoped, hand-over, let-it-die
pattern applied to push rights instead of model spend.

Forgejo supports what this needs — tokens scoped to specific repositories,
restricted by route scope, and revocable via the API.

**Sequenced in two tiers, because their costs differ by an order of magnitude:**

1. **Readers get a read-only credential** (do first). No new secret, no
   lifecycle, no minting authority. This alone closes the hole that ADR-0010
   opens, and readers are the agents that will run most.
2. **Mint-and-revoke per writing Run** (do second). Closes the zombie-writer
   case — a partitioned pod whose Lease lapsed but which is still alive and
   still able to push. Revocation rides the sweeper already being built to
   release budget authorizations (ADR-0012), so it is one mechanism, not two.

The Lease row becomes `(shift, run, forge_token_id, expires_at, renewed_at)`:
the ledger of which credential is live, when it lapses, and what the sweeper
must revoke.

### Consequences

* Good, because the writer/reader split is enforced in two independent places —
  Ploeg not spawning two writers, and the forge not accepting a push from a
  reader. Either alone is a single point of failure.
* Good, because a leaked token is bounded to one repository and one Run.
* Good, because it is the pattern already proven in this codebase for spend, so
  it adds a familiar shape rather than a novel one.
* Good, because the Lease stops being advisory, which is the whole reason this
  record exists.
* **Bad, because ploegd gains the authority to mint forge credentials.** This is
  a real escalation: a shared static token is traded for a minting authority
  concentrated in one service, making ploegd a higher-value target. Accepted
  deliberately, on the same reasoning as `LITELLM_MASTER_KEY` (ADR-0008) and
  with the same mitigation — the secret lives only in ploegd, never in a worker
  pod (R6 keeps the worker single-purpose).
* Bad, because minting and revoking add two forge API calls per writing Run, and
  a failed revocation leaks a token until the sweeper retries. Mitigated by
  making revocation idempotent and retried, and by setting native token expiry
  as a backstop if available.
* Bad, because tier 2 puts a forge credential broker on the critical path before
  the zombie-writer case has ever been observed. This is why the tiers are
  sequenced rather than shipped together.

### Confirmation

* **No push credential reaches a reading Run.** `pkg/worker` gains
  `TestReaderCloneURLHasNoPushCredential`; the reader's clone URL and env carry
  a read-only token or none. A regression here is a test failure, not a review
  miss.
* `TestSweeperRevokesForgeToken` — expire a writer's Lease and assert the token
  is revoked in the same sweep that releases its budget authorization. This is
  the R2 proof for push rights.
* `TestMintedTokenIsRepoScoped` — the minted token names exactly one repository.
* Grep-checkable: `AGENT_BUILDER_TOKEN` no longer appears in a worker pod's
  environment for reading Roles; the minting secret appears only in ploegd's
  Deployment, never in `ploeg.workerPodTemplate`.
* Gate: `go test ./...` and `helm template` in
  `.forgejo/workflows/on_pull_request.yml`.

## Pros and Cons of the Options

### Keep the shared static token

* Good, because it is what ships today and costs nothing.
* Bad, because it makes ADR-0010's writer/reader split unenforceable, and the
  Lease a note rather than a capability.
* Bad, because the blast radius of a leak is every repository, forever.

### Read-only credentials for readers only

* Good, because it closes the highest-volume hole for almost no work and needs
  no new authority in ploegd.
* Bad, because it leaves the zombie writer able to push. Adopted as tier 1
  precisely because it is most of the value for a fraction of the cost.

### Mint per writing Run and revoke with the Lease

* Good, because it makes Lease-holding and push-capability the same fact.
* Bad, because it requires ploegd to hold a minting credential.

### Per-branch deploy keys

* Good, because a deploy key is naturally scoped to one repository.
* Bad, because deploy keys are repository-level, not branch-level, so they do
  not actually narrow what a Run may write — and key rotation per Run is
  heavier than token revocation.

### Forge-side branch protection rules

* Good, because enforcement lives entirely in the forge, with nothing for Ploeg
  to revoke.
* Bad, because protection rules are configured per repository and per branch
  pattern, so Ploeg would be mutating repository settings on every Shift — far
  more invasive than minting a token, and it mutates state Ploeg does not own.

## Re-evaluation triggers

* **Forgejo ships native API-token expiry** ([forgejo#8837]) — currently
  unverified whether this has landed. If it has, tokens gain a backstop that
  survives a failed revocation, and the sweeper becomes defence-in-depth rather
  than the only guard.
* **Forgejo gains per-branch push scoping** — that would let a token be narrowed
  from "this repository" to "this Shift's branch", which is the scope actually
  wanted.
* **A zombie writer is observed in production** — the trigger to promote tier 2
  from planned to urgent.
* **ploegd's blast radius is reviewed** — if concentrating minting authority
  proves uncomfortable, the alternative is a separate minting service, at the
  cost of one more moving part.
* **The forge provider gains a second implementation** — GitHub's token model
  differs, and the broker interface must not encode Forgejo specifics (R7).

## More Information

* The Lease this makes real: [ADR-0010](0010-shift-owns-the-item-lease-owns-the-branch.md).
* The pattern being reused: [ADR-0008](0008-litellm-is-the-credential-and-metering-seam.md).
* Shared sweeper with budget release: [ADR-0012](0012-two-level-budgets-authorized-and-settled.md).
* Domain rules cited: R2 (crash-safety), R6 (worker stays single-purpose), R7
  (no vendor specifics in core semantics).
* Forgejo capabilities: token scope documentation, and repository-scoped tokens
  (forgejo#2332, forgejo#4992). Native expiry tracked at forgejo#8837 —
  **verify before relying on it**.

[forgejo#8837]: https://codeberg.org/forgejo/forgejo/issues/8837
