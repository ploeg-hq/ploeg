---
status: proposed
date: 2026-10-05
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-01
---

# Ploeg stays in Go, and admits Rust only as a separately deployed component

## Context and Problem Statement

[ADR-0002](0002-go-as-the-implementation-language.md) chose Go over Rust and TypeScript in July 2026. The owner asked whether ploegd, the launcher or ploeg-worker should now move to Rust, wholly or in part.

This record re-examines ADR-0002 against the code as it stands. It does not supersede it. It adds the rule for where Rust may enter.

## Decision Drivers

* **Where the time goes.** Wall-clock time per Run is spent on model calls, scheduling and Kata boot, not in Ploeg's own code ([research](../research/2026-10-05-substrate-language-and-run-bottlenecks.md) §1).
* **Load.** ploegd is low-traffic and Postgres-bound, sized at 100m CPU and 256Mi (`ops/helm/ploeg/values.yaml:113-119`).
* **The claim transaction.** Launch and claim must stay in one PostgreSQL transaction with the capacity lock ([ADR-0072](0072-ploegd-launches-each-runs-sandbox-and-keda-leaves-the-sandbox-path.md)).
* **Agents write most of this repository.** They must be able to build and test it inside the worker sandbox, which cannot reach package registries.
* **The codebase changes weekly:** 70 ADRs since 2026-07-29.

## Considered Options

* Keep Go, and admit Rust only for a new, separately deployed component with a CPU or untrusted-input need
* Rewrite ploegd and ploeg-worker in Rust
* Strangle ploegd behind its HTTP contracts, starting with the operator API
* Write the push launcher or ploeg-worker in Rust

## Decision Outcome

Chosen option: "**Keep Go, and admit Rust only for a new, separately deployed component with a CPU or untrusted-input need**". The Rust gains that matter here can mostly be had in Go at a fraction of the cost.

The rewrite was measured:
* **Size.** ploegd's import closure is 34,7 k non-test and 36,0 k test lines; the worker is 12,9 k and 12,3 k.
* **Effort.** About 60–65 person-weeks unassisted, or 30–40 with heavy agent help, plus 15–20 for the worker.
* **Its real gains:**
  * exhaustive matching on state enums;
  * compile-time checked SQL for about 440 static query sites; the 42 dynamic builders would lose the check;
  * a typestate that protects only the in-process path between load and update.

  The authoritative transitions are guarded SQL updates and CHECK constraints, and those hold in either language.

The Go equivalents adopted instead:

* the `exhaustive` linter on the state string types (`pkg/work/types.go` and the store's states);
* a store test that prepares every static query against the migrated schema, or `sqlc` for new query code;
* a transition-table test per state machine, matched to its CHECK constraint;
* for the worker's sandbox hardening, which is the one place Rust's `pre_exec` is a real advantage: a `ploeg-worker harness-exec` re-exec shim that sets no-new-privileges, a Landlock ruleset and a seccomp filter before `execve` on the harness.

Rust may enter for a component that meets all of these:

* it is deployed and released on its own, behind a published contract in `docs/contracts/`;
* it holds no claim, Lease or budget state of its own;
* it has a measured CPU, latency or untrusted-input parsing need.

That component needs its own ADR, which cites this one.

### Consequences

* Good, because effort goes to the bottlenecks that cost time (research §8) rather than a port.
* Good, because one toolchain stays buildable inside the worker sandbox and by agents in one pass.
* Good, because the state-machine and SQL safety Rust would give is pursued directly, with tests that also run on the current code.
* Bad, because Rust's stronger compile-time guarantees on the money path are not gained. The linters and tests cover the cases seen so far, not every case.
* Bad, because a Rust-first contributor has a higher bar to contribute to Ploeg itself.

### Confirmation

* **Lint.** `mise exec -- go vet ./...` and the repository's lint step include the `exhaustive` analyser on state types once it is added. A missing case fails CI.
* **Schema.** A `pkg/store` test prepares every static query against the migrated schema in embedded Postgres, and fails on an unknown column or table.
* **Review.**
  * A pull request adding a `Cargo.toml` under this repository must cite an ADR that meets the three conditions above. Without one, it is rejected under this record.
  * A pull request that moves claim, Lease or budget logic out of Go must cite a record that supersedes this one.

## Pros and Cons of the Options

### Rewrite ploegd and ploeg-worker in Rust

* Good, because of exhaustive enums, checked SQL and no nil.
* Bad, because of 30–65 person-weeks with no value until cutover, against a codebase that changes weekly.
* Bad, because the test suite would not carry over: about 10 k lines of `httpapi` tests call the Go handler in-process and seed data through Go store calls.
* Bad, because agents inside the worker sandbox could not build it without vendored crates or a pre-baked image.

### Strangle ploegd behind its HTTP contracts

* Good, because it is incremental. Unfold's black-box qualification scripts could verify a Rust operator API.
* Bad, because there would be two writers on one schema for the length of the migration. The advisory-lock key strings, such as `ploeg.team-running:<team>`, must be reproduced exactly, or the caps break silently.
* Bad, because the run API, webhooks and the sweep have thin black-box coverage.

### Write the push launcher or ploeg-worker in Rust

* Good, because kube-rs 4.x and kopium are mature. A musl-static binary meets ADR-0002's static-binary driver.
* Bad, because the launcher must share the dispatch transaction in `pkg/store`. A Rust launcher would copy the predicate again or need a new API.
* Bad, because the worker's start time is milliseconds in either language against minutes of scheduling. Its hardening is reachable through the Go re-exec shim.

## Re-evaluation triggers

Any two of these reopen the language question for a superseding record:

* An incident on the money path is caused by a nil dereference, a data race or an unhandled state that the linters and transition tests did not catch.
* A component is proposed that meets the three conditions above. That one is built in Rust without reopening this record.
* Rust-first contributors join, or agent Rust builds inside the worker sandbox reach parity with Go: green in one pass, cold build under two minutes.
* ADR churn drops below two records a month for a quarter, so a strangler would not chase a moving target.
* [ADR-0072](0072-ploegd-launches-each-runs-sandbox-and-keda-leaves-the-sandbox-path.md) is superseded by a design whose scheduler runs outside ploegd.

## More Information

* Evidence: [Substrate, language and Run bottlenecks](../research/2026-10-05-substrate-language-and-run-bottlenecks.md), §4.
* Related: [ADR-0002](0002-go-as-the-implementation-language.md) (still in force), [ADR-0072](0072-ploegd-launches-each-runs-sandbox-and-keda-leaves-the-sandbox-path.md).
* 2026-10-05: proposed after a research spike sized the port and compared crate maturity.
