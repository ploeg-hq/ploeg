---
type: explanation
audience: [owner, contributor, reviewer]
owner: ploeg
last_verified: 2026-10-25
verified_by: "Owner decisions of 2026-10-25 on issue #5; MAINTAINERS.md read at development"
---

# Capacity and work in progress

This page records how much time goes into Ploeg, how much work may be open at once, and who approves paid qualification spend. It answers [issue #5](https://github.com/ploeg-hq/ploeg/issues/5).

## Weekly capacity

| Maintainer | Hours per week | Workstreams |
| --- | --- | --- |
| Ryan Grippeling | about 20 | All: runtime, control plane, releases and community |

There is one maintainer ([MAINTAINERS.md](../../MAINTAINERS.md)), so every workstream shares those hours and none has a separate allocation. A forecast that needs more than about 20 hours a week is not a plan this project can keep.

## Work in progress

At most **two open tickets per maintainer** are in progress at a time.

Tickets an agent works count toward that cap: a Ploeg Run and a Claude session each hold one of the maintainer's two slots while the ticket is open, because the maintainer reviews their output. Starting a third ticket, by hand or by an agent, waits until one of the two closes.

## Paid qualification spend

Paid qualification covers the model calls of test Runs and live accounting checks.

* The maintainer approves this spend.
* An agent may spend up to **$5 per run** without asking again.
* Anything above $5 for one run needs the maintainer's approval before it starts.

## Decision history

* 2026-10-25: about 20 hours a week; work in progress capped at two open tickets per maintainer, agent-run tickets included; the maintainer approves paid qualification spend and an agent may spend up to $5 per run without asking again. Decided by the maintainer.
