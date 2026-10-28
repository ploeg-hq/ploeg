---
status: proposed
date: 2026-10-28
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# An operator change needs a proven person, and the chart refuses asserted attribution from 0.3.0

## Context and Problem Statement

[ADR-0150](0150-an-operator-consumer-proves-the-person-it-acts-for-with-a-token-exchanged-for-ploegs-audience.md) lets an Operator Consumer prove the person it acts for with a person token exchanged at the identity provider, and records each operator change's attribution as `proven` or `asserted`. It kept header-only attribution working "for one release" and left the refusal to a later decision under [ADR-0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md). Until then anyone holding a consumer's static token can still make a change in the name of any person inside that consumer's Teams ([threat model](../security/threat-model.md) F-07, T-PD-3, T-PD-5). How does a deployment refuse asserted attribution on changes, and when does that become the default?

## Decision Drivers

* Every operator change links to a person the identity provider proved, not to the consumer's word.
* Reads stay open to asserted attribution: a read changes nothing, and a dashboard or reporting consumer has no person to prove.
* A refusal is visible after the fact: the audit log names the consumer and why it was refused.
* ADR-0134: a default changes only after a stable minor announced it, with a warning while the old default holds.
* The precedent of [ADR-0154](0154-the-chart-refuses-values-that-leave-profile-p1-unless-they-declare-the-deployment-unsupported.md) clause 8 and ADR-0150's `requireClaims`: the chart turns a default at 0.3.0 with `semverCompare`, and warns in its install notes before then.

## Considered Options

* **A ploegd mode, `PLOEG_OPERATOR_ATTRIBUTION`, that the chart sets to `proven` from chart 0.3.0**
* Refuse asserted attribution in every deployment now
* Refuse it per consumer, with a consumer flag

## Decision Outcome

Chosen option: "**a ploegd mode that the chart sets to `proven` from chart 0.3.0**", because it lets a deployment close F-07 today, gives every consumer the rest of the 0.2 line to adopt person tokens, and turns the default in the release ADR-0150's disposition names.

1. **The mode.** `PLOEG_OPERATOR_ATTRIBUTION` is `asserted` or `proven`. Unset is `asserted`. Any other value refuses to start.
2. **What `proven` refuses.** Every operator request whose method is not `GET` or `HEAD` needs a verified person token (ADR-0150): as the bearer, or in `X-Ploeg-Person-Token` next to the consumer's credential. Without one ploegd answers **403 `person_token_required`** and nothing changes, whatever `X-Ploeg-Actor` says. The check runs in the operator middleware, before any route, so a route added later is covered.
3. **What it does not refuse.** `GET` and `HEAD` keep accepting a consumer's credential alone and an asserted `X-Ploeg-Actor`. Every other ADR-0150 refusal (`invalid_token`, `actor_conflict`, `person_claim_required`) still answers first.
4. **The audit row.** Each refusal writes `operator.attribution_refused` with actor `operator:<consumer>` and `detail` holding `consumer`, `reason` (`person_token_required`), `method`, `path`, and `assertedActor` when the request named one. A refused request is not an attributed change, so the row carries no `attribution`.
5. **`asserted` warns.** ploegd logs a warning at start when consumers are configured under `asserted`, and each change it accepts with asserted attribution answers with `Warning: 299 ploegd "..."` ([RFC 9111 §5.5](https://www.rfc-editor.org/rfc/rfc9111#section-5.5), the header Kubernetes uses for deprecation warnings), so a consumer sees the coming refusal in its own logs.
6. **Start checks.** `proven` with consumers configured and no person token issuer (`PLOEG_OPERATOR_PERSON_ISSUER`) refuses to start: such a deployment could accept no change.
7. **The chart.** `operator.attribution` is `""`, `asserted` or `proven`. `""` resolves to `asserted` before chart 0.3.0 and to `proven` from chart 0.3.0, prereleases included. The chart renders `PLOEG_OPERATOR_ATTRIBUTION` whenever the resolved value is `proven` or the value is set, refuses `operator.attribution` together with `env.PLOEG_OPERATOR_ATTRIBUTION`, and refuses to render a resolved `proven` with consumers and no `operator.personTokens`. Before 0.3.0 the install notes warn when consumers are configured and the value resolves to `asserted`. From 0.3.0 an explicit `asserted` is a profile P1 departure.

### Consequences

* Good, because a deployment that sets `proven` closes F-07: a stolen consumer token alone can read but can no longer change anything in a person's name.
* Good, because every refusal is an audit row naming the consumer, so a consumer that has not adopted person tokens shows up in the audit log before it shows up as an outage.
* Good, because the default flips with a release, not with a surprise: 0.2 warns in ploegd's log, in each response and in the install notes.
* Bad, because a machine consumer that changes work (a pipeline reporting publication status, a delivery bot) needs a person token too, or a deployment that keeps `asserted` and leaves P1. Ploeg has no separate attribution for a non-person caller; a trigger below covers that need.
* Bad, because a deployment that installs ploegd without the chart keeps `asserted` until it sets the variable; the binary default stays `asserted` so that an upgrade of the binary alone never starts refusing changes.

### Confirmation

`go test ./pkg/httpapi/ -run 'Attribution|ProvenMode|PersonTokenRequired'` verifies against an in-test issuer that under `proven` a change with only `X-Ploeg-Actor` answers 403 `person_token_required` and never reaches its route, the same change with an exchanged person token as the bearer or in `X-Ploeg-Person-Token` reaches it and is recorded `proven`, a `GET` with only the header reaches its route, and under `asserted` the header-only change reaches its route with the `Warning` header. `TestARefusedChangeWritesTheConsumerAndReasonToTheAudit` checks the stored `operator.attribution_refused` row against PostgreSQL. `go test ./cmd/ploegd/ -run Attribution` checks the mode's parsing and start refusals. The `ATTRIBUTION` cases in `scripts/helm-golden.sh` render the chart at 0.2 without the variable and with the warning, at a copy of the chart at 0.3.0-rc.1 and 0.3.0 with `proven`, refuse `proven` without `operator.personTokens`, refuse an explicit `asserted` under P1 and render it under `unsupported`.

## Pros and Cons of the Options

### Refuse asserted attribution in every deployment now

* Good, because F-07 closes everywhere at once.
* Bad, because it breaks every consumer that sends headers inside 0.2, which ADR-0134 forbids for a default.

### Refuse it per consumer, with a consumer flag

* Good, because a deployment can move one consumer at a time.
* Bad, because a consumer left off keeps F-07 open with no deployment-wide signal, and P1 would have to judge each consumer instead of one value. A deployment that needs to move gradually can watch the `Warning` headers and the `asserted` audit rows, then set the mode once.

## Re-evaluation triggers

* A consumer that changes work has no person behind it, such as a pipeline or a delivery bot, and cannot obtain a person token: decide how a machine caller is attributed.
* Every known consumer sends person tokens: decide whether the binary default becomes `proven` as well.
* A deployment needs a change route that asserted attribution may reach under `proven`.
* The audit rows of refused requests grow fast enough to matter, which would mean a consumer is retrying a refused change in a loop.

## More Information

* Completes [ADR-0150](0150-an-operator-consumer-proves-the-person-it-acts-for-with-a-token-exchanged-for-ploegs-audience.md)'s "Headers stay asserted for one release"; [ADR-0156](0156-each-run-carries-a-transaction-token-that-names-its-person-and-a-team-without-a-sponsor-runs-nothing.md)'s Run tokens then carry only proven persons from operator admissions under `proven`.
* Closes [threat model](../security/threat-model.md) F-07 for a deployment under `proven`, and so for every chart install from 0.3.0 that stays in P1; [#296](https://github.com/ploeg-hq/ploeg/issues/296).
* The chart default change is recorded in [breaking-changes.yaml](../contracts/breaking-changes.yaml).
