---
status: proposed
date: 2026-10-06
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-01
---

# Ploeg knows Work Sources and Change Destinations, never vendors

## Context and Problem Statement

About 7,500 production lines of Ploeg are vendor-aware. About 5,000 sit in `pkg/provider`. The other 2,500 sit in core:

* `cmd/ploegd/main.go:25-28,97-180` reads vendor environment variables.
* `cmd/ploegd/webhooks.go` holds a Vikunja-only webhook check, surfaced in `/readyz`.
* `cmd/ploegd/forgecreds.go` and `pkg/forgebroker/forgejo.go` mint Forgejo tokens.
* `pkg/httpapi/server.go:307-310` reads `X-Forgejo-Delivery`.
* `pkg/worker/forge.go` and `pkg/worker/forgeproxy.go:219-255` speak forge REST dialects.
* `pkg/worker/task.go:228-246` briefs the agent with vendor REST calls.
* `pkg/harness/contract.go:143-149` puts forge dialects in `taskspec.v1`.
* `pkg/work/branch.go:13` has a Vikunja branch-naming case.
* `pkg/config/config.go` has vendor sections.

Adding a tracker or a forge is therefore a core change. That is the connector matrix [ADR-0005](0005-build-a-dedicated-dispatch-plane.md) said Ploeg would not maintain.

The owner's direction is that Ploeg should have zero knowledge of these integrations, and that they should be plug and play.

The case that raised it: Forgejo sends no check webhooks, so repairing failed CI was proposed as forge polling inside Ploeg.

What does Ploeg core know about the systems work comes from and changes go to?

## Decision Drivers

* **No connector matrix in core** (ADR-0005), and no names of the outside world in core ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)).
* **Per-Run push rights stay** ([ADR-0013](0013-push-rights-are-minted-per-run.md), [ADR-0016](0016-forge-registry-and-per-run-repo-scoped-credentials.md)): minted for one Run, revoked at settlement, swept when orphaned.
* **Delivery facts come from the destination, never from the agent** ([ADR-0059](0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md)).
* **Inbound events are deduplicated and outbound effects happen once** ([ADR-0060](0060-authenticated-webhooks-go-through-a-durable-inbox-and-required-publications-through-an-outbox.md)).
* **ploegd holds no vendor admin credentials** ([ADR-0025](0025-management-authority-stays-in-the-control-plane.md)).

## Considered Options

* Out-of-process adapters behind contracts Ploeg publishes, with events at the edges
* Vendor code moved into separately versioned Go modules, still compiled into ploegd
* WebAssembly adapter plugins
* Keep the provider SPI as it is

## Decision Outcome

Chosen option: "**Out-of-process adapters behind contracts Ploeg publishes, with events at the edges**". It is the only option in which adding a tracker or a forge changes no Ploeg code, and in which ploegd holds no vendor credential.

### What core knows

Core knows a git remote (smart-HTTP is a protocol, not a vendor) and two abstractions, each identified by a registered id.

* **A Work Source.**
  * It emits `work.assigned`, `work.updated`, `work.unassigned`, `work.closed` and `work.status_moved`, carrying an opaque scope, revision and labels ([ADR-0015](0015-routing-is-core-policy-over-provider-opaque-scopes.md), [ADR-0038](0038-a-repo-label-selects-among-registered-targets-and-the-board-default-is-the-fallback.md)).
  * It answers an item read, a comment upsert by marker, and a state set.
* **A Change Destination.**
  * It mints, revokes and lists a per-Run git credential: `{id, gitURL, username, token, expiresAt}`.
  * It checks that a repository is ready.
  * It ensures and observes a change request for a branch.
  * It upserts a note by marker.
  * It emits `change.opened`, `change.updated`, `change.merged`, `change.closed`, `change.review_submitted`, `change.checks_completed` and `change.conflicted`.

Ploeg emits `delivery.observed` and `run.settled`, so adapters can watch heads and revoke credentials.

### Where adapters run

* Each adapter runs as a sidecar or a separate service, holds its vendor's credentials, verifies its vendor's webhooks, and speaks the contract.
* Inbound events arrive as CloudEvents into ADR-0060's inbox. CloudEvents `source` plus `id` is the deduplication key, and `subject` gives ordering.
* Outbound effects go through the outbox to the adapter.
* Registering an adapter means configuring its id, URL and bearer token. No Ploeg build changes.

### Checks and failed-CI repair

* **Ploeg core polls nothing.**
* A destination reports `change.checks_completed {destination, repo, branch, headSha, state, checks[]}`. How it learns of checks is the adapter's business: a webhook, polling only the heads Ploeg announced in `delivery.observed`, or the repository's own pipeline posting checks the way it posts deploys.
* Ploeg acts only when `headSha` equals the stored delivery head. A failure on the current head drives the repair Follow-Up that `ForgeCheckFailed` drives today.
* Ploeg keeps one timer, on its own state: checks not reported within a route's deadline send the item to a configured state. The timer reads Ploeg's rows, not a forge.
* [ADR-0070](0070-a-pull-request-is-ready-for-review-only-when-its-checks-passed-on-the-pushed-commit.md)'s worker verification stays the review gate. Reported checks become a second source of evidence, recorded in a record of their own.

### Migration

1. **Contracts.** Write `docs/contracts/work-source.v1`, `change-destination.v1`, `events.v1` and `checks-api.v1`. Make the tracker revision an opaque token.
2. **Ports.** Core depends only on `WorkSource` and `ChangeDestination` ports. The existing four providers become in-process adapters behind them. Remove the delivery-header reads, the Vikunja branch case and the vendor configuration sections from core.
3. **Change requests.**
   * Ploeg opens the change request: the agent pushes, and the worker asks the destination to ensure the change request.
   * The forge REST allowlists in the worker proxy and the REST briefing are removed, which shrinks what an agent can reach.
   * `taskspec.v1` carries a destination id instead of a dialect, superseding [ADR-0023](0023-the-forge-dialect-travels-on-the-work-item.md) in its own record.
4. **Intake.** Add the CloudEvents intake and the checks intake, and move failed-check repair onto `change.checks_completed`.
5. **Extraction.** Move the adapters into their own modules and images, running as sidecars by default with a single-bundle option.
6. **Removal.** Delete vendor code from core, and extend the `internal/boundary` test to vendor names.

The card-only provider capabilities are deleted, not ported ([ADR-0074](0074-ploeg-exposes-its-execution-facts-and-consumers-collect-everything-else.md)).

### Consequences

* Good, because a new tracker or forge is an adapter, written and released without touching Ploeg.
* Good, because ploegd holds no vendor credential, and an agent no longer reaches forge REST at all.
* Good, because failed-check repair works on forges that send no check webhooks, without forge code in core.
* Bad, because per-Run credential minting depends on a network call to an adapter on the claim path. As a sidecar this costs about a millisecond. An adapter outage fails the claim as `infra_forge`, inside the infra retry budget.
* Bad, because adapters are trusted to state delivery facts. Each adapter's token is scoped to its registered ids, and the ADR-0059 binding check stays in core.
* Bad, because the migration is 8–10 weeks across six phases, and the contracts may need changes while the in-process shim exists. They ship as `v1beta` until it is removed.

### Confirmation

* **Boundary test.** `internal/boundary` fails on vendor names (`forgejo`, `gitea`, `gitlab`, `github`, `vikunja`, `clickup`) outside `adapters/` and records. It is shown to fail on one injected name and to pass on a clean tree.
* **Conformance suite.** It runs against every adapter, including two `httptest` reference adapters:
  * duplicate event ids are dropped;
  * out-of-order events per subject are ordered;
  * a credential mint, revoke and orphan sweep work;
  * a stale-head `checks_completed` decides nothing;
  * one well-formed sequence passes.
* **Polling check.** A test fails if any package outside `adapters/` constructs an HTTP client for a configured forge or tracker base URL.
* **CI.** `mise exec -- go test ./...` runs all of them.

## Pros and Cons of the Options

### Vendor code in separately versioned Go modules, compiled into ploegd

* Good, because it is about two weeks of work, and there is no network hop.
* Bad, because adding a vendor still needs a Ploeg build, so the connector matrix survives in the build. ploegd still holds vendor credentials.

### WebAssembly adapter plugins

* Good, because there is no network hop, and the plugins are sandboxed.
* Bad, because wazero has no Component Model, and adapters need HTTP, secrets, clocks and HMAC from the host: a large ABI to design and secure.

### Keep the provider SPI as it is

* Good, because nothing changes.
* Bad, because every integration stays a core change, and core keeps vendor knowledge.

## Re-evaluation triggers

* Claim latency from adapter minting passes 500 ms at p95, or adapter outages cause more than five `infra_forge` failures a week.
* A third party writes an adapter. Its experience revises the contract before it leaves `v1beta`.
* A Change Destination appears that is not git-backed. The git-remote assumption is revisited.

## More Information

* Evidence: [KEDA, plug-and-play integrations, and card collection](../research/2026-10-06-keda-integrations-and-card-collection.md), §2.
* Related: ADR-0005, ADR-0013, ADR-0015, ADR-0016, ADR-0023, ADR-0059, ADR-0060, ADR-0069, ADR-0070, ADR-0074.
* 2026-10-06: proposed after the owner asked that Ploeg have zero knowledge of its integrations.
