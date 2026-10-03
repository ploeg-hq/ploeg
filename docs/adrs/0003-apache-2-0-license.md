---
status: accepted
date: 2026-07-29
decision-makers: Ryan Grippeling
supersedes: none
review-by: none
---

# Ploeg ships under Apache-2.0

## Context and Problem Statement

Ploeg's audience is platform operators running self-hosted stacks, and its
stated exit criterion is adoption outside the originating factory. The licence
either invites or blocks the organisations most likely to adopt it.

## Decision Drivers

* **Adoption-maximising** for platform teams inside companies, who are the
  audience.
* **Explicit patent grant** — the reason a legal review at such a company clears
  Apache-2.0 faster than MIT.
* **Ecosystem convention** — the Kubernetes-adjacent projects Ploeg composes
  with (KEDA, kagent, agent-sandbox) are Apache-2.0.

## Considered Options

* Apache-2.0
* MIT
* AGPL-3.0

## Decision Outcome

Chosen option: **Apache-2.0**. It carries the patent grant MIT lacks, which is
the difference that matters to the corporate platform teams this is aimed at,
and it matches the licence of everything Ploeg sits beside in a cluster.

AGPL was rejected on audience grounds: Ploeg is infrastructure a company runs
internally, and a copyleft-over-the-network term is precisely the clause that
gets a project struck off an internal allowlist.

### Consequences

* Good, because a vendor or platform team can adopt and extend without a legal
  review cycle.
* Bad, because a commercial fork can close its additions. Accepted — the value
  is the semantics and the operational discipline, neither of which a fork can
  take away from the original operator.

### Confirmation

`LICENSE` at the repository root, `README.md`'s licence section, and the SPDX
identifier in `catalog-info.yaml` where present. Reviewed when a new dependency
enters `go.mod` — a copyleft dependency would silently invalidate this record.

## Pros and Cons of the Options

### MIT

* Good, because shortest and most permissive.
* Bad, because no patent grant, which is the specific reason corporate legal
  reviews take longer.

### AGPL-3.0

* Good, because it protects against a hosted commercial fork.
* Bad, because it blocks the adopter profile this project is aimed at.

## More Information

* Migrated from `docs/design.md` §9 on 2026-07-29. The decision predates this
  ledger; its original date is unrecorded.
* [LICENSE](../../LICENSE)
