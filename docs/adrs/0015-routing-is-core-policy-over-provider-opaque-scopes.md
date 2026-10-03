---
status: proposed
date: 2026-07-29
decision-makers: Ryan Grippeling
supersedes: none
review-by: none
---

# Route work in the core over provider-opaque Scopes

## Context and Problem Statement

Routing — deciding which Team and which Work Target an incoming tracker event
belongs to — currently happens *inside the provider*. `provider.TrackerEvent`
carries a `Team` field (`pkg/provider/provider.go:27`), and the Vikunja adapter
fills it by looking the assignee up in `PLOEG_TEAM_MAP`. That is a core concept
in the provider SPI and a Ploeg-domain decision taken by vendor code, which is
precisely what R7 forbids: "core semantics must never encode a provider-specific
workaround; everything vendor-specific lives behind the SPI."

At the same time the one signal that *is* a native routing scope is thrown away:
`pkg/provider/vikunja/vikunja.go:38-51` unmarshals only `event_name`,
`data.task.{id,title,description,priority}` and `data.assignee.username`.
Vikunja does send `project_id`; `encoding/json` discards it silently (zero
`project_id` references repo-wide). Per `docs/ops/board.md` ("Dispatch topology
— the trap") every team's webhook is registered on **Ploeg Test (project 11)**,
so all teams ingest one project and the repository is chosen only by which
persona happens to be assigned. With the Work Target now an attribute of the
Work Item (ADR-0014), something has to resolve it — this ADR decides what, and
where. Scope: `pkg/provider`, the ingest path in `pkg/httpapi`, and the routing
configuration rendered into ploegd.

## Decision Drivers

* R7: no vendor concept in core semantics, and no core concept in the SPI.
* A repository must never be nameable by untrusted tracker text (#9).
* Routing must be inspectable and testable without a provider in the loop.
* The SPI is the project's compatibility promise (#34), so a break must be
  deliberate and documented, not incidental.

## Considered Options

* **Providers emit an opaque Scope id; the core maps `(provider, scope, actor,
  hint)` → `(team, target)`**
* Keep `TrackerEvent.Team` and add `TrackerEvent.Target` beside it
* Per-provider routing config owned by each provider adapter
* Let the ticket carry the repository directly

## Decision Outcome

Chosen option: "**Providers emit an opaque Scope id; the core maps `(provider,
scope, actor, hint)` → `(team, target)`**", because it is the only option under
which no vendor concept enters the core *and* no core concept stays in the SPI —
both halves of R7, which the current shape violates in both directions.

A provider emits a **Scope**: an opaque, provider-scoped identifier for whatever
container the event arrived in (Vikunja: the `project_id` currently dropped at
`vikunja.go:38-51`; a forge provider later: the repository id). The core holds
an **ordered, first-match-wins rule table** mapping `(provider, scope, actor,
hint)` → `(team, target)`, evaluated once at ingest, with the resolved target
pinned on the row per ADR-0014.

`provider.TrackerEvent.Team` is **removed**. This is an SPI-breaking change and
is treated as one: it owes backlog #34 a compatibility-window entry naming the
removal, and the conformance fixtures (#33) are re-recorded with the Scope field
in place of the Team field.

The rule that keeps this R7-clean, and the test to apply to every future field:
**does the core interpret the value, or only compare it for equality?** The core
compares a Scope for equality against rule keys and never parses, splits, or
range-checks it. Equality-only ⇒ the value is opaque ⇒ no vendor semantics
leaked into the core. Any pressure to pattern-match a scope (prefixes,
wildcards, numeric ranges) is the signal that a vendor concept is being
smuggled in, and is rejected.

**Closed target set.** A `hint` — a label, a ticket field, a magic string in the
body — may only *select among pre-registered routes*. It can never supply a raw
owner/repo/branch. Tracker text is untrusted input (#9): a hint that constructs
a target is an arbitrary-repository write primitive available to anyone who can
comment on the board. An unmatched hint is a routing failure, not a fallback.

The rule table is **rendered**, not hand-maintained: it is derived from the
`org.yaml` roster manifest in `webgrip/homelab-cluster`, the same source that
provisions bot users
([research/2026-07-28-agent-roster-ssot.md](../research/2026-07-28-agent-roster-ssot.md)).
An event whose scope matches no rule is refused with an audit row and no work
item when `PLOEG_TARGET_STRICT` is set (#108) — the default once this lands, and
the twin of the `PLOEG_DEFAULT_TEAM` footgun recorded in #103.

### Consequences

* Good, because R7 is satisfied in both directions: the SPI stops carrying
  `Team`, and the core stops carrying `vik`-shaped knowledge.
* Good, because routing becomes testable as a pure function — a table of
  `(provider, scope, actor, hint)` tuples in, `(team, target)` out, with no
  webhook, no HTTP and no vendor fixture required.
* Good, because the answer to "why did this ticket go to that repo?" is one
  ordered table plus one audit row, instead of an env map on one deployment and
  a webhook registration on one Vikunja project.
* Good, because it removes the coupling `docs/ops/board.md` calls the trap: many
  teams may keep ingesting from one project while routing to different targets.
* Bad, because it breaks the provider SPI. Every out-of-tree provider (there are
  none today, which is exactly why now is the moment) must be updated, and #34
  must gain the deprecation entry before the removal ships.
* Bad, because it adds a configuration artifact that can be wrong in a new way —
  a rule table that is out of step with `org.yaml` routes work correctly and
  invisibly to the wrong place. Rendering it rather than hand-editing it is the
  mitigation, not a guarantee.
* Bad, because strict mode will drop events that today would run: an unmapped
  scope becomes a refusal. That is the intended trade (a silent default is how a
  stranger's ticket reaches a real repository), but it will read as an outage
  the first time it fires.

### Confirmation

* `grep -rn "TrackerEvent" pkg/provider` shows no `Team` field, and
  `grep -rn "PLOEG_TEAM_MAP" .` returns nothing outside this ADR and the
  backlog.
* A table-driven unit test for the resolver covering first-match-wins ordering,
  unmatched scope under `PLOEG_TARGET_STRICT` (refusal + audit row, no work
  item), and a hint naming an unregistered target (refusal, not construction).
* The provider conformance kit (#33) fixtures assert that the Vikunja adapter
  emits `project_id` as an opaque scope and that the core never string-inspects
  it — the equality-only rule as an executable check.

## Pros and Cons of the Options

### Providers emit an opaque Scope id; the core maps to (team, target)

* Good, because it satisfies both halves of R7 and gives the routing decision
  one owner.
* Good, because a second tracker provider needs no new routing code, only a
  scope.
* Bad, because it is an SPI break with a migration cost for anyone downstream.

### Keep `TrackerEvent.Team` and add `TrackerEvent.Target`

* Good, because nothing breaks; it is the smallest diff.
* Bad, because it doubles down on the R7 violation — now two core concepts sit
  in the SPI and vendor code decides both.
* Bad, because every provider must then implement routing, so routing policy
  diverges per vendor and cannot be tested in one place.

### Per-provider routing config owned by each adapter

* Good, because each adapter can express vendor-native selectors (Vikunja
  filters, forge topics).
* Bad, because routing policy fragments across adapters and the "why did this
  land here" question needs N answers.
* Bad, because provider-native selectors are exactly the interpretation of a
  value that the equality-only test rejects.

### Let the ticket carry the repository directly

* Good, because it is zero configuration.
* Bad, because it hands repository selection to untrusted text (#9) — the
  security clause above exists to forbid precisely this.

## More Information

* Technical story: architecture.md §9.13 and §9.14 · backlog #105 (with #34,
  #36, #103, #108)
* 2026-07-29 — decision approved by the repo owner. Status stays `proposed`
  until it lands end to end; on `development` at this date `TrackerEvent.Team`
  is still present at `pkg/provider/provider.go:27`, `PLOEG_TEAM_MAP` is still
  the live mechanism, and no rule table exists.
* Refines [ADR-0014](0014-work-target-is-a-work-item-attribute.md) — it
  supplies the resolution ADR-0014 pins on the row.
