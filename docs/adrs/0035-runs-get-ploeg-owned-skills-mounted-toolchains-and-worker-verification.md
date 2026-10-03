---
status: proposed
date: 2026-09-27
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2026-12-31
---

# Runs get Ploeg-owned skills, mounted toolchains and a verification the worker runs

## Context and Problem Statement

A writing Run could not run the target repository's checks. The
`agent-runner` image, maintained in `webgrip/infrastructure` and not in Unfold,
bakes the agent tooling but deliberately no language toolchain and no
container runtime (homelab-cluster ADR-0053: language gates run in CI). The
`ploeg` namespace has default-deny egress: a Run's worker pod reaches DNS, the
other pods in `ploeg` (ploegd and its Postgres), LiteLLM (`ai` namespace, port
4000), in-cluster Forgejo (`forgejo` namespace, port 3000) and the Vikunja API
(`vikunja` namespace, port 3456), and has no route to the public gateway, the
LAN or the internet, so it cannot pull an image or install a toolchain. On
2026-07-27 an agent shipped a pull request with a red `gofmt` gate twice, and
the formatting failure hid a failing test it had never run (VIK-594, Ploeg pull
request #13, CI runs 67 to 69). Three review rounds went to defects the agent
could not see (VIK-601).

The prompt also told agents to run "the verify command AGENTS.md names" with
nothing to run it with, and gave reviewers no method beyond the delivery
contract. The orchestration survey of 2026-09-26 (§10, shape 3) proposed
mounting a Ploeg-owned Agent Skills set in every Run.

How does a Run get the toolchain and the commands to verify its own work, and
how does Ploeg know whether it did?

## Decision Drivers

* The sandbox keeps its egress limits. A fix must not need registry or
  internet access from the Run.
* The agent must be able to run the checks itself, before it pushes, because
  a check it cannot see costs a whole review round.
* What Ploeg reports about a change must be Ploeg's observation, not the
  agent's claim.
* The commands that judge a change must not be editable by the change.
* Every harness must get the same method, whatever its skill discovery.

## Considered Options

* Mount toolchains as image volumes, take the checks from operator
  configuration, give every Run embedded Ploeg skills, and let the worker run
  the checks again after a writing Run
* Bake language toolchains into `agent-runner`
* Run gates in containers through DinD
* Read the check commands from the target repository

## Decision Outcome

Chosen option: "Mount toolchains as image volumes, take the checks from
operator configuration, give every Run embedded Ploeg skills, and let the
worker run the checks again after a writing Run", because it is the only option
that gives the agent a working toolchain without new egress, keeps the image
small, and produces evidence the author of the change cannot edit.

* **Toolchains.** `executor.harness.toolchains` (role, then team, then global,
  as for the other harness fields) renders one image volume per toolchain,
  mounted read-only at `/opt/ploeg/toolchains/<name>`. The kubelet pulls the
  image, so the Run needs no registry access. The worker reads
  `PLOEG_TOOLCHAINS`, puts each toolchain's directories in front of the
  harness's `PATH`, adds its `env` (for example `GOTOOLCHAIN=local`), and
  refuses to claim when a directory is missing
  ([toolchain.go](../../pkg/worker/toolchain.go)).
* **Checks.** `executor.harness.verify` is a list of shell command lines
  (`PLOEG_VERIFY_COMMANDS`). They come from the operator, not the repository,
  so a writer cannot weaken the check that judges it. The harness gets them as
  an executable script named by `PLOEG_VERIFY_SCRIPT`, and the prompt lists
  them.
* **The worker verifies.** After a writing Run whose outcome is `pr_opened` or
  `pr_updated`, and after its model key is revoked, the worker runs the same
  commands in the checkout without the forge token or the model key. It
  records the commit, whether the tree was dirty, and each check's result, and
  adds them to the Run's findings and summary
  ([verify.go](../../pkg/worker/verify.go)). Findings reach the pull request
  and the next Round's briefing through the existing blackboard (ADR-0011),
  where a reviewer weighs them. A failed verification does not change the
  outcome.
* **Skills.** `pkg/harness/skills` embeds two Agent Skills in the worker
  binary: `ploeg-verify-before-handoff` for writers and readers, and
  `ploeg-review-against-work-item` for readers. Planners get none. The worker
  writes them under the Run's own `HOME` in `.agents/skills` (named by
  `PLOEG_SKILLS_DIR`) and in the harness's user-level directory:
  `.openhands/skills/installed` for `openhands`, `.claude/skills` for
  `claude-code`. The prompt names each installed skill by path, so a harness
  that discovers none can still read it. The skills rank below the delivery
  contract and above the target repository's instructions (ADR-0030); the
  `ploeg-` prefix keeps a repository skill from shadowing one by name.

### Consequences

* Good, because an agent can run the repository's formatter, linter and tests
  before it pushes, with no new egress.
* Good, because the pull request carries Ploeg's own record of which checks
  passed on which commit, and a reviewer Round sees it in its briefing.
* Good, because the checks and the skills live outside the branch under
  change.
* Bad, because a toolchain that downloads dependencies still needs a source
  for them. Go modules come from `proxy.golang.org`, which the namespace's
  egress policy blocks. Until an in-cluster module proxy is reachable from
  worker pods, or the toolchain image carries the module cache, only
  dependency-free checks such as `gofmt` run in the sandbox; the rest are
  reported as failed or left to CI. That fix lives in `webgrip/homelab-cluster`,
  not in Unfold.
* Bad, because image volumes need Kubernetes 1.35 or later and a container
  runtime that supports them.
* Bad, because the checks are per team or Role, not per repository. A team
  that serves repositories with different checks needs a Role per repository
  until checks travel on the target.
* Neutral, because a failed verification is evidence, not a gate. Reopening
  the writing Round on a failed verification is proposed below, not
  implemented.

### Confirmation

* `pkg/harness/skills/skills_test.go` validates each embedded `SKILL.md`
  against the Agent Skills name and description rules, the set per kind of
  Run, and installation under `HOME` only.
* `pkg/worker/verify_test.go` runs a writing Run end to end against a fixture
  forge: the skills are installed, a fake toolchain is first on the harness's
  `PATH`, the verify script passes inside the harness, and the worker's own
  verification after the agent's change fails and is named in the summary and
  findings. It also asserts that the worker's verification sees neither the
  forge token nor the model key, and that readers and planners are not
  verified by the worker.
* `pkg/worker/toolchain_test.go` pins `PATH` order, reserved environment names
  and the refusal of a missing mount.
* The Helm golden `ci/golden/executor.yaml` pins the image volume, its
  read-only mount and the escaped `PLOEG_VERIFY_COMMANDS` value.
* Not yet confirmed: that OpenHands 1.16 and opencode load a skill from the
  directories above in a real Run, and that the cluster's container runtime
  mounts image volumes. Both need a qualification Run.

## Pros and Cons of the Options

### Bake language toolchains into `agent-runner`

* Good, because it needs no chart change.
* Bad, because every language multiplies the image. The Rust toolchain alone
  added about 1.4 GB, which is why ADR-0053 removed toolchains.
* Bad, because the image is maintained outside Unfold and serves every
  repository with one set of versions.

### Run gates in containers through DinD

* Good, because gates would run in CI's own images.
* Bad, because the namespace no longer admits privileged pods (ADR-0053), and
  the Run cannot pull the images anyway.

### Read the check commands from the target repository

* Good, because each repository would declare its own checks.
* Bad, because a writer could edit the checks that judge its change. Reading
  them from the base branch would close that, and is a candidate follow-up.

## Re-evaluation triggers

* Worker pods can reach a Go module proxy or another dependency source, which
  makes `go vet` and `go test` runnable in the sandbox.
* A writing Run's pull request fails a check in CI that the worker's
  verification reported as passed.
* The owner decides a failed verification should reopen the writing Round
  (proposed: treat it like a `request_changes` verdict within the ADR-0017
  cap).
* Check commands move onto the target (per repository) instead of the team.

## More Information

* 2026-09-27 — Proposed with the implementation. Evidence: VIK-601 and
  VIK-594; `webgrip/infrastructure` `ops/docker/agent-runner/Dockerfile` (no
  language toolchains by design); the `ploeg` namespace's Kyverno-generated
  default-deny egress in `webgrip/homelab-cluster`; the
  [orchestration landscape survey](../research/2026-09-26-agent-orchestration-landscape.md)
  §8 and §10.
* 2026-09-28 — Network reach corrected. Worker pods lost the public gateway and
  the LAN in homelab-cluster `ab75cc35`, `92b15206` and `747890d0`;
  `kubernetes/apps/ploeg/ploeg/app/worker-egress-probe.job.yaml` checks it.
* 2026-09-28 — Amended by
  [0037](0037-teams-opt-into-registry-egress-through-a-logged-allowlist-proxy.md)
  (accepted 2026-10-01): a team can opt into a `registries` network profile that reaches
  package registries through a logged allowlist proxy, which answers the
  first Bad consequence above for that team. `airgapped` stays the default.
* Related: [0011](0011-the-pull-request-is-the-blackboard.md),
  [0017](0017-the-review-loop-is-verdict-driven-and-capped.md),
  [0030](0030-target-repository-instructions-rank-below-the-delivery-contract.md),
  [0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md).
