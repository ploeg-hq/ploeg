---
type: reference
audience: [owner, integrator, contributor, agent]
owner: ploeg
generated_by: "mise run domain"
---

# Ploeg — Domain Overview

Ploeg schedules and runs work handed to agents, tracks progress and cost, and supports cooperation between agents. It is Unfold's only execution engine and the Authority for every Run (Unfold ADR-0002). De Vloer is its front end, where people work on tickets, give work to agents and review the results. Vloer Start requests Ploeg admission; a manual-origin request need not have a tracker ticket. Until Vloer's own engine is retired, Vloer still performs delegated operator Runs. Trackers keep their work content and priorities. This vocabulary includes intended domain rules; it is not a database or API field inventory. The architecture and published contracts identify current implementation limits. Vloer uses these execution terms.

*Model version 0.6.0. Generated from `model.yaml` — do not edit by hand.*

## Bounded contexts

- **Dispatch** — Core semantics owned by ploegd and Postgres: work items, leases, queues, checkpoints, outcomes, audit. Provider- and runtime-agnostic by rule.
- **Integration** — The provider SPI boundary: tracker and forge adapters, webhook parsing, normalized events, write-backs. Everything vendor-specific lives here.
- **Execution** — How admitted work executes, including Kubernetes workers and delegated workbench runs. KEDA is one implementation, not part of the language.
- **Harness** — The contract between Ploeg and an agent container: Task Spec in, Outcome Report out. Isolates the fast-churning agent-tool boundary.

## Context map

Arrows point from the context that holds the reference to the context it references.

```mermaid
flowchart LR
    Dispatch["Dispatch"]
    Integration["Integration"]
    Execution["Execution"]
    Harness["Harness"]
    Dispatch -->|has_many, has_one, references| Execution
    Dispatch -->|references| Integration
    Execution -->|belongs_to, references| Dispatch
    Harness -->|references| Dispatch
```

## ⚠ Open ambiguities

These terms are contested or vague. Resolve them before writing specs that depend on them.

- **who decides that delegated work is finished** — Successful checks, another agent's review and a person's approval are different possible conditions for finishing work; the product has no agreed rule for choosing between them.
  - Options: Choose the condition for each task, Always require a person's approval, Let the agent finish when its checks pass
  - Recommendation: Choose the condition for each task so research, coding and other work can use appropriate checks and approval.
- **the "leased" Work Item state** — The Work Item state enum calls the working state `leased`, named for the Lease it used to imply. After ADR-0010 a Work Item in that state has a Shift, and may have no Lease at all — a Round of readers takes none. The state name now describes the wrong thing.
  - Options: Keep `leased` and accept the vocabulary drift, Rename to `active`, Rename to `in_shift`
  - Recommendation: Decide with the implementing change, not before. The rename touches the state enum, an applied migration, both contract schemas and the KEDA scaler query, so it is a real cost to weigh against a name that is merely imprecise. `active` reads best if it goes ahead.
- **when a Shift closes** — ADR-0010 introduces the Shift but leaves its terminal rule to the implementation. A Shift plainly closes on a terminal Outcome; less plainly when an item goes needs_human and a human re-queues it — does the old Shift resume with its remaining budget and Round counter, or does a fresh one open?
  - Options: Re-queue always opens a new Shift (budget resets, rounds restart), Re-queue resumes the existing Shift (budget and rounds carry over), Human chooses per re-queue
  - Recommendation: Resume the existing Shift. A re-queue after needs_human is usually a human unblocking work already done, and restarting the budget silently doubles what the item may cost. Confirm against the first real needs_human Shift.
- **follow-up mirroring** — Follow-Ups enter at queued with no Tracker Item, which tensions with "the tracker is the source of truth for what to do" — work now exists that the board cannot see.
  - Options: Keep Follow-Ups tracker-invisible (current), Asynchronously write a Tracker Item back for every Follow-Up, Write back only Follow-Ups that survive longer than one Run
  - Recommendation: Revisit in roadmap phase 2 (PR-feedback ingestion); async write-back is the likely answer so the board regains full visibility without blocking dispatch.
- **groomer run** — The design says Ploeg "can schedule a groomer run" but grooming semantics belong to the operator — it is unclear whether Groomer is Ploeg vocabulary at all, and what distinguishes a groomer run from a normal Run.
  - Options: Keep Groomer out of the core language (operator concern), Define it as a Team with a single grooming Role, First-class GroomerRun concept
  - Recommendation: Keep it out of the core language for now; if it lands in phase 2, model it as an ordinary Team whose single Role grooms — no new concepts.

## Entity relationships

```mermaid
erDiagram
    Operator_Execution {}
    Work_Item {}
    Work_Target {}
    Shift {}
    Lease {}
    Run {}
    Checkpoint {}
    Team {}
    Routing_Rule {}
    Forge {}
    Task_Spec {}
    Outcome_Report {}
    Operator_Execution ||--|| Work_Item : has_one
    Operator_Execution ||--|| Shift : has_one
    Operator_Execution ||--|| Run : has_one
    Work_Item ||--|| Shift : has_one
    Work_Item ||--|| Work_Target : has_one
    Work_Item ||--o{ Run : has_many
    Work_Item ||--o{ Checkpoint : has_many
    Work_Target }o..o{ Forge : references
    Shift }o--|| Work_Item : belongs_to
    Shift ||--|| Lease : has_one
    Shift ||--o{ Run : has_many
    Lease }o--|| Shift : belongs_to
    Lease }o..o{ Run : references
    Run }o--|| Work_Item : belongs_to
    Run }o..o{ Team : references
    Checkpoint }o--|| Work_Item : belongs_to
    Team ||--o{ Lease : has_many
    Routing_Rule }o..o{ Work_Target : references
    Routing_Rule }o..o{ Team : references
    Task_Spec }o..o{ Work_Item : references
    Task_Spec }o..o{ Work_Target : references
    Task_Spec }o..o{ Checkpoint : references
    Outcome_Report }o..o{ Checkpoint : references
```

## Contents

- [Glossary](glossary.md)
- [Entities](entities.md)
- [Business rules](rules.md)
- [Domain events](events.md)
