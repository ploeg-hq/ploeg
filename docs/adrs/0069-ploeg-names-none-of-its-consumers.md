---
status: accepted
date: 2026-10-04
decision-makers: Ryan Grippeling
supersedes: none
review-by: none
---

# Ploeg names none of its consumers

## Context and Problem Statement

Ploeg was built next to one front end, in one repository, and it kept that front end's names after it became a standalone project ([ADR 0062](0062-github-is-the-independent-project-home.md)). Its code linked usage reports to that front end's Work Item route and to dashboards named after it, defaulted every card to that front end's skin, branded the card image it posts with that product's name, named operator branches after it and mapped its skins to palettes. Its domain model described Ploeg as that product's engine and imported that product's own terms. A deployment with another front end, or none, inherited all of it.

The owner decided on 2026-10-04 that Ploeg has zero knowledge of what consumes it. What does Ploeg know about a consumer, and what does it leave to configuration?

## Decision Drivers

* Ploeg is an independent project that any front end, tracker or forge can use through its contracts.
* A consumer changes its name, routes and look without a Ploeg release.
* Deployments configure what Ploeg links to; Ploeg does not assume it.
* The rule is checked by a test, so it holds after this change.

## Considered Options

* Ploeg knows consumers only as Operator Consumers through its contracts, and every consumer-specific value is configuration
* Keep the names and add a configurable product name

## Decision Outcome

Chosen option: "Ploeg knows consumers only as Operator Consumers through its contracts", because a configurable name would still leave routes, skins and dashboards that only one consumer has.

* The usage report's links are four URL templates a deployment sets: `PLOEG_REPORT_WORK_ITEM_URL` (`{id}`), `PLOEG_REPORT_TEAM_DASHBOARD_URL` (`{team}`), `PLOEG_REPORT_RUN_DASHBOARD_URL` (`{run}`) and `PLOEG_REPORT_SPEND_DASHBOARD_URL`. Ploeg fills and escapes the placeholders and leaves out a link whose value is missing. `PLOEG_REPORT_GRAFANA_URL` and `PLOEG_REPORT_VLOER_URL` are gone.
* A card's skin and theme are opaque names Ploeg passes through. A Work Target without a `cardStyle` gets the skin `default`, which means the consumer's own default.
* The card image Ploeg posts on a pull request is Ploeg's own: one palette, branded PLOEG, marked `<!-- ploeg:run-card -->`, stored as `run-card-<id>.svg`. Ploeg still finds a card comment that an earlier release marked with another `<name>:run-card` prefix, so no pull request gets a second card.
* An Operator Execution's Shift branch is `operator/<session>`.
* The domain model describes Ploeg on its own and imports no consumer's terms; the generator names no product.
* `internal/boundary` fails when a consumer's name appears outside dated records: changelogs, `PROVENANCE.md`, ADRs, research, OpenSpec changes and migrations, which keep the words of their time.

### Consequences

* Good, because any consumer can use Ploeg without inheriting another's routes, look or vocabulary.
* Good, because a deployment decides where report links point.
* Bad, because deployments that set `PLOEG_REPORT_GRAFANA_URL` or `PLOEG_REPORT_VLOER_URL` must set the new templates; until they do, reports carry no links.
* Bad, because a consumer that matched an operator branch by its old prefix must use `operator/<session>`.

### Confirmation

`go test ./internal/boundary/` scans every file outside the dated records for a consumer's name, and `pkg/shiftengine` and `pkg/cardimage` pin the link templates, the neutral skin, the marker and the image.

## More Information

* 2026-10-04 — Decided by the owner ("Ploeg needs to have zero knowledge about downstream stuff").
