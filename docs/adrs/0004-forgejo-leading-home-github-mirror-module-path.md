---
status: accepted
date: 2026-07-29
decision-makers: Ryan Grippeling
supersedes: none
review-by: none
---

# Ploeg lives on Forgejo, mirrors to GitHub, and takes its module path from the mirror

## Context and Problem Statement

Ploeg exists to serve self-hosted stacks and is itself developed on a
self-hosted Forgejo. Go's module resolution, however, fetches over HTTPS from a
publicly reachable host: a module path pointing at `forgejo.webgrip.dev` is
unresolvable for anyone outside the LAN, so `go get` would fail for exactly the
external adopters the project is trying to earn.

The project name had to survive the same test. "Ploeg" (Dutch: a work crew or
shift) was checked for collisions: the GitHub user `ploeg` exists — which blocks
a future bare organisation but not `webgrip/ploeg` — the npm name is free, and
there is no product collision in the niche.

## Decision Drivers

* **Dogfooding** — a dispatch plane for self-hosted forges developed on a SaaS
  forge would be an unforced credibility problem.
* **`go get` must work for outsiders**, or the Apache-2.0 adoption bet in
  [0003](0003-apache-2-0-license.md) is void.
* **One writable origin.** Two writable remotes is a merge-conflict generator.

## Considered Options

* Forgejo-leading with a GitHub push-mirror; module path uses the mirror
* GitHub-leading with a Forgejo mirror
* Forgejo only

## Decision Outcome

Chosen option: **Forgejo-leading (`webgrip/ploeg`) with a GitHub push-mirror,
and a module path that names the GitHub mirror.**

Forgejo is the single writable origin and the source of truth for issues, PRs,
CI and releases. GitHub is a read-only mirror that exists for one reason: to be
the publicly resolvable host in the module path so `go get` works.

The asymmetry is deliberate and worth stating plainly, because it looks like an
inconsistency: *development* is self-hosted, *distribution* is not, because Go's
module proxy gave no third option.

### Consequences

* Good, because outsiders can `go get` and read the code without a Forgejo
  account.
* Good, because CI, releases and the agent fleet all target one writable forge —
  the same forge Ploeg's own `ForgeProvider` is written against.
* Bad, because the module path names a host that is not the development home,
  which reads as inconsistent until explained. Accepted; explained here.
* Bad, because PRs opened on the GitHub mirror have nowhere to go. Mitigation:
  the mirror's README points at Forgejo.

### Confirmation

The `module` line in `go.mod` names the GitHub mirror path; `git remote -v`
shows exactly one writable `origin` on `forgejo-ssh.webgrip.dev`. CI runs on
Forgejo Actions (`.forgejo/workflows/`), so a move would be immediately visible.

## Pros and Cons of the Options

### GitHub-leading with a Forgejo mirror

* Good, because module path and home agree, and discovery is better.
* Bad, because a project whose thesis is "self-hosted stacks deserve first-class
  tooling" would be developed somewhere that contradicts it.

### Forgejo only

* Good, because maximally consistent.
* Bad, because `go get` fails for everyone outside the LAN, which is
  disqualifying for a library-shaped Go project.

## More Information

* Migrated from `docs/design.md` §9 on 2026-07-29. The decision predates this
  ledger; its original date is unrecorded.
* Delivery discipline for this repo — trunk is `development`, `main` is a
  release-promotion stub — is recorded in [AGENTS.md](../../AGENTS.md); the
  estate-wide rule that a repo declares one server-enforced delivery contract is
  `webgrip/homelab-cluster` ADR-0050.
