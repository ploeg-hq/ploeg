---
status: proposed
date: 2026-09-26
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# The harness gets placeholders; the worker keeps the credentials

## Context and Problem Statement

The harness is the part of a Run most exposed to untrusted input: the Work Item
text, the repository's own files and every tool output reach the model that
drives it. Until 2026-09-26 it also held every credential of the Run. It
received the per-Run model key and, for a writer, the forge token in its
environment, and because it ran as the worker's user in the worker's container
it could read the worker's own environment through `/proc`, including the
long-lived worker bootstrap token and builder forge token.

Per-Run keys, budgets and revocation
([0008](0008-litellm-is-the-credential-and-metering-seam.md),
[0012](0012-two-level-budgets-authorized-and-settled.md)) bound what a stolen
per-Run key can spend. They do not stop a key or token from being written into
a commit, a pull request body or a prompt log, where it outlives the Run.

The question is where a Run's credentials live.

## Decision Drivers

* **Spend authority is the product** ([0032](0032-keep-the-dispatch-plane-and-compete-on-authorized-spend.md)).
  A credential the agent can read is a credential the agent can leak.
* **Prompt injection is expected, not exceptional.** The boundary must hold
  when the harness does what an attacker tells it to.
* **Thin glue.** No new container, sidecar or dependency if the worker can do
  it.
* **Evidence from the market.** kagent/Agent Substrate, Omnigent and NVIDIA
  OpenShell all converge on the same shape: a placeholder in the sandbox and
  the real credential attached at an egress point the sandbox cannot read.

## Considered Options

* Keep handing credentials to the harness, bounded by per-Run scope and TTL
* An egress sidecar in the Run pod that swaps placeholders for credentials
* The worker itself holds every credential, is non-dumpable, and proxies the
  harness's authenticated traffic over loopback

## Decision Outcome

Chosen option: **the worker holds every credential, is non-dumpable, and
proxies the harness's authenticated traffic over loopback.**

* `ploeg-worker` marks itself non-dumpable before it does anything else, so a
  same-user harness cannot read its environment or memory
  ([conceal_linux.go](../../pkg/worker/conceal_linux.go)).
* The per-Run model key stays in the worker. The harness receives a random
  placeholder and a loopback base URL; a reverse proxy in the worker attaches
  the real key on each request to LiteLLM
  ([llmproxy.go](../../pkg/worker/llmproxy.go)). This is opt-in
  (`PLOEG_LLM_KEY_ISOLATION=proxy`) until each harness is qualified, because a
  harness that calls the model from inside a DinD container cannot reach the
  worker's loopback.
* A writer's forge token follows the same pattern: git (redirected with
  `url.<proxy>/.insteadOf`) and the forge API reach only the Run's own
  repository through a loopback proxy that adds the token
  ([forgeproxy.go](../../pkg/worker/forgeproxy.go)); everything else gets 403.
  Opt-in as `PLOEG_FORGE_TOKEN_ISOLATION=proxy` for the same reason.

### Consequences

* Good, because a prompt-injected harness can still spend within its budget but
  can no longer exfiltrate a key or token that outlives the Run.
* Good, because it needs no sidecar and no new dependency; the worker already
  owns the credentials' lifecycle.
* Bad, because it only holds while the harness lacks `CAP_SYS_PTRACE` and runs
  in the worker's container; a pod spec that grants the capability undoes it.
* Bad, because both proxies are opt-in until each harness is qualified, so a
  default deployment still hands credentials over.

### Confirmation

* `go test ./pkg/worker/` runs `TestHarnessCannotReadConcealedWorkerEnvironment`
  (Linux), `TestIsolatedRunNeverHandsTheKeyToTheHarness`,
  `TestForgeProxyRefusesEverythingOutsideTheRunsRepository` and
  `TestGitPushesThroughTheForgeProxy`; CI runs them in `go test ./...`.
* The chart golden renders show no worker container with added capabilities; a
  reviewer rejects any `CAP_SYS_PTRACE` or `shareProcessNamespace` on the
  worker pod against this record.
* `go test ./internal/ledger/` validates this record.

## Pros and Cons of the Options

### Keep handing credentials to the harness

* Good, because nothing changes and every harness works.
* Bad, because a leaked key in a pull request is readable by anyone with
  repository access until the Run ends, and a leaked bootstrap token for much
  longer.

### An egress sidecar

* Good, because it also covers traffic from containers the harness starts, if
  routed through it.
* Bad, because it adds a container per Run and a second place credentials
  live, for a benefit the worker can provide for in-container harnesses.

## Re-evaluation triggers

Any one of these reopens this record:

* A qualified harness needs model or forge access from inside a DinD container
  (the loopback proxy cannot serve it; revisit the sidecar option).
* agent-sandbox, OpenShell or Agent Substrate ship per-sandbox credential
  injection that can bind a different credential per Run.
* A Run's pod spec gains `CAP_SYS_PTRACE` or `shareProcessNamespace` for any
  reason.

## More Information

* Evidence:
  [research/2026-09-26-agent-orchestration-landscape.md](../research/2026-09-26-agent-orchestration-landscape.md)
  §5 and §6.
* Related: [0013](0013-push-rights-are-minted-per-run.md) (forge tokens are
  minted per Run), [0030](0030-target-repository-instructions-rank-below-the-delivery-contract.md)
  (repository instructions rank below the delivery contract).
