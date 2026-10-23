---
status: proposed
date: 2026-10-23
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-01
---

# A harness runs in its own container, and no credential enters it

## Context and Problem Statement

[ADR 0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md) put every credential of a Run in `ploeg-worker` and gave the harness placeholders. The two still share one container, one uid and one filesystem: the chart renders the harness image as the pod's only container and starts the worker binary in it ([_helpers.tpl](../../ops/helm/ploeg/templates/_helpers.tpl) lines 457 to 462; [golden executor-isolation.yaml](../../ops/helm/ploeg/ci/golden/executor-isolation.yaml) lines 306 to 309), and the worker starts the harness as a child process ([adapter.go](../../pkg/harness/adapter.go) lines 225 to 228). Every writer container therefore carries the shared builder forge token and every worker its team's bootstrap token as `secretKeyRef` environment ([_helpers.tpl](../../ops/helm/ploeg/templates/_helpers.tpl) lines 634 to 639 and 669 to 678; golden lines 335 to 348), long-lived and unscoped, while a per-Run key and token are minted on top. What keeps them from the harness is `PR_SET_DUMPABLE=0` on the worker process ([conceal_linux.go](../../pkg/worker/conceal_linux.go) line 15) and the refusal to run isolated while `CAP_SYS_PTRACE` is held ([capabilities_linux.go](../../pkg/worker/capabilities_linux.go); [main.go](../../cmd/ploeg-worker/main.go) lines 137 to 141). Threat-model finding [F-03](../security/threat-model.md#findings) rates this High and release-blocking ([#224](https://github.com/ploeg-hq/ploeg/issues/224)): one process-level control, in the attacker's own container, guards credentials that outlive every Run.

Checking the control shows it is narrower than the records say. The dumpable flag covers the worker process alone. Linux sets it back to dumpable on `execve`, so every `git` the worker spawns is a same-uid process whose `/proc/<pid>/environ` the harness can read, and the worker puts the forge token in exactly that environment ([git.go](../../pkg/worker/git.go) lines 51 to 57 and 77). The board watch spawns such a git every two minutes while the harness runs ([board.go](../../pkg/worker/board.go) line 177, wired at [worker.go](../../pkg/worker/worker.go) line 513); for a reader it carries the shared read-only token, because a reader receives no per-Run token ([server.go](../../pkg/httpapi/server.go) lines 709 to 712). The experiment in the evidence note reproduces it: a non-dumpable parent's environment is unreadable, its child's is readable. Nothing in the container stops a planted `git` earlier on the worker's `PATH` either: the root filesystem is writable (`workerSecurityContext` sets no `readOnlyRootFilesystem`, [values.yaml](../../ops/helm/ploeg/values.yaml) lines 713 to 721), and which `PATH` directories uid 1000 may write is a property of the harness image, which the chart does not control.

Where does a Run's credential live so that the harness cannot reach it by any same-container means, and how does an existing installation get there?

## Decision Drivers

* **A container boundary, not a process boundary.** TB3 of the threat model must become what TB2 already is: a different PID namespace, mount namespace and root filesystem. A `/proc` trick, a planted binary, a core dump and a readable child then get one answer: there is nothing in the harness's container to find.
* **Close the class, not the file.** The worker spawns git, verification commands and the OpenSpec CLI. Any shape that keeps a credentialed helper process in the harness's container keeps the channel above.
* **Thin glue, same harness contract.** Adapters already describe a harness as data, `harness.Invocation` with argv, environment and an outcome file ([adapter.go](../../pkg/harness/adapter.go) lines 178 to 186); the ACP adapter needs only a stdin and stdout pair. Nothing a harness reads or writes may change ([contracts](../contracts/README.md)).
* **Strict by default, degrade never silently** ([ADR 0106](0106-a-run-declares-an-isolation-floor-and-a-cell-runs-it-at-the-strictest-class-it-can-honour.md)). The default render has to be the one the threat model assumes.
* **One release at a time** ([ADR 0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md)): an existing installation upgrades without reading the diff, and the old shape keeps working for a deprecated stable minor.
* **The egress gateway stays the next rung.** `hardened` and above in ADR 0106 move credentials out of the pod. What lands now must be a step towards that, not a second design to retire.

## Considered Options

* (a) The harness runs in its own container; `ploeg-worker` keeps every credential and both proxies in another container of the same pod
* (b) An init container exchanges the bootstrap token for the per-Run material; the bootstrap and shared tokens never enter the harness's container
* (c) Keep one container, drop the shared token, move the bootstrap token out through a short-lived exchange
* (d) Harden in place: seccomp, AppArmor, a second uid, `hidepid`

## Decision Outcome

Chosen option: "**(a) the harness runs in its own container**", because it is the only option that gives the harness nothing to read rather than one more rule about how to read it. It closes the worker process, every child it spawns and its `PATH` in one move, keeps every harness contract unchanged, and moves both proxies into the place the egress gateway will later take over from.

1. **Pod shape.** A Run pod has two containers. `worker` runs `ploeg-worker` from the Ploeg worker image and is the Job's main container; it holds the bootstrap token, the forge token, the per-Run key and both loopback proxies, as today. `harness` runs the harness image as a native sidecar (`initContainers[].restartPolicy: Always`, the shape the DinD sidecar already uses at [_helpers.tpl](../../ops/helm/ploeg/templates/_helpers.tpl) lines 479 to 481, so no new Kubernetes requirement) under `workerSecurityContext` ([ADR 0120](0120-a-worker-container-meets-the-restricted-pod-security-standard-by-default.md)) with the toolchain image volumes. Its pod-spec environment is `PATH` alone. The pod ends when `worker` exits, which stops `harness` with it.
2. **Launcher.** `harness` runs `ploeg-worker harness-serve` from the `worker-bin` volume, listening on a Unix socket in the shared scratch directory. The worker sends it the `harness.Invocation` an adapter prepared (argv, the allowlisted environment of [environment.go](../../pkg/worker/environment.go) with placeholders, cwd, the outcome file) and gets stdin, stdout, stderr, the exit status and process-group kill back over the socket. The `exec`, `openhands` and `claude-code` adapters pass through unchanged; `acp` relays the two pipes it opens today ([launcher.go](../../pkg/harness/adapters/acp/launcher.go) lines 52 to 61). The launcher is PID 1 of its container and kills every process left in it before verification runs.
3. **Workspace.** The `ci-shared` emptyDir stays shared and the clone stays where it is. The worker clones, fetches, scans and reads pull requests from its own container; verification commands and the OpenSpec gate run through the launcher, because the toolchains and the `openspec` binary live in the harness image. Verification keeps its fresh clone of the pushed head ([verify.go](../../pkg/worker/verify.go) lines 166 to 207), made by the worker.
4. **Images.** The worker needs git, which the distroless ploegd image does not carry ([Dockerfile](../../ops/docker/ploegd/Dockerfile)). A `ploeg-worker` image (distroless base plus git and CA certificates) ships, signed and digest-pinned like the others, and `executor.workerImage` defaults to it. The `worker-bin` init container stays, because the launcher binary has to reach the harness image.
5. **Values and default.** `executor.harnessContainer: separate` is the default for every workload with both proxies on. `shared` renders today's single container, is marked `DEPRECATED` in `values.yaml` and is removed in the minor after 0.2. A workload with `dind: true` or either proxy off is `legacy` already (ADR 0106) and keeps `shared`. `claude-environment` keeps `shared`: its local `claude` CLI is a dispatcher holding a person's login ([claudeenv.go](../../pkg/harness/adapters/claudeenv/claudeenv.go) lines 153 to 163) and no agent runs in that pod; P1 excludes it.
6. **What stays in the worker container.** The bootstrap token, until a later record gives it an expiry; the shared builder token, until the fallback at [worker.go](../../pkg/worker/worker.go) line 273 is removed with per-Run minting made mandatory (option (c), a follow-up). Neither is reachable from the harness's container any more, which is what F-03 asks.
7. **Profile.** P1 of the threat model requires `harnessContainer: separate`; TB3 becomes a container boundary; F-03 closes when the gates under Confirmation exist.

### Consequences

* Good, because the harness's container has no secret-bearing environment, no credentialed process, no readable child, no writable `PATH` of the worker's and no PID of the worker's to target; a kernel or procfs bypass that defeats `PR_SET_DUMPABLE` finds nothing.
* Good, because every adapter and every harness contract is unchanged; the seam that moves is the one call that execs the harness.
* Good, because the proxies now live where the egress gateway of ADR 0106's `hardened` class will replace them, so that rung removes code instead of redesigning it.
* Good, because the launcher can end every process in the harness's container between the harness and verification, which narrows F-25's `setsid` survivor.
* Bad, because the Run pod gains a container, an image and a socket protocol. Accepted: a Job pod is disposable, and the protocol carries a struct the adapters already produce.
* Bad, because the pod's requests grow by the worker container's share, and an installer's quota may need adjusting. The worker is one Go process; 50m and 128Mi are the expected order.
* Bad, because the network namespace stays shared: a listener the harness runs on the pod's loopback is still reachable by the worker's git if the clone's `.git/config` sends it there ([F-24](../security/threat-model.md#findings)). The proxies already answer 403 without the Run's placeholder; the F-24 fix is still required and is independent of this record.
* Bad, because the bootstrap and shared tokens remain in the pod. They are out of the harness's container, which this record decides; leaving the pod is option (c) and the gateway rung.
* Neutral: a Kata or gVisor installation puts both containers in one sandbox; the container boundary holds inside it, and the pod-level posture is unchanged.

### Confirmation

* `go test ./pkg/worker/` runs `TestHarnessContainerSeesNoCredential`: the launcher runs in a child process with its own environment, a hostile `exec` harness dumps its environment, every `/proc/*/environ` and `/proc/*/cmdline` it can open and every file under the shared mounts, and the test asserts that none of the Run's bootstrap token, builder token, per-Run key or per-Run forge token appear while the proxies' placeholders do. The sibling `TestLauncherRelaysStdioAndKillsTheGroup` proves the relay. CI runs both in `go test ./...`.
* `scripts/helm-golden.sh check`: in every executor golden with `harnessContainer: separate`, only the `worker` container carries a `valueFrom.secretKeyRef`, the `harness` container's env is `PATH` alone and it has `restartPolicy: Always`; a `shared` fixture still renders the old shape with its deprecation note; `separate` with `dind: true` fails to render.
* The attack drill of [ADR 0126](0126-a-recurring-attack-drill-runs-every-harness-against-a-hostile-repository.md) gains the same probe as a hostile-repository step, so the live check runs weekly against a real pod and reports what the harness could read.
* The F-03 row says fixed only when all three run green; until then it stays open.
* `go test ./internal/ledger/` validates this record.

### Implementation tasks

Sized S (a day), M (a few days), L (a week or more); one pull request each unless noted.

- [ ] **M** `ploeg-worker harness-serve`: Unix socket in the scratch directory, spawn requests carrying `harness.Invocation`, cwd and environment, stdio relay, process-group kill, kill-all before verification, exit status. `pkg/harness` gains a spawner seam with the in-process spawner as the default so existing tests keep running; `RunCommand` and the `acp` launcher take the seam.
- [ ] **M** Worker image `ops/docker/ploeg-worker/Dockerfile`: distroless base, git, CA certificates; built, signed and digest-pinned by the release workflow like ploegd and claude-runner; `executor.workerImage` defaults to it.
- [ ] **M** Chart: `executor.harnessContainer` (`separate` default, `shared` deprecated); the `harness` native sidecar from the harness image with toolchain mounts, `workerSecurityContext`, workspace mounts and `PATH` only; the `worker` container from the worker image with every `secretKeyRef`; per-container resources; refusals (`separate` with `dind: true`); `claude-environment` pinned to `shared`; goldens for every executor fixture and the SandboxTemplate.
- [ ] **S** Worker: `CheckHarnessBinary` becomes a launcher probe before the claim; verification and the OpenSpec gate run through the launcher; the harness's `HOME` and scratch stay on the shared volume.
- [ ] **S** Tests and drill: `TestHarnessContainerSeesNoCredential`, `TestLauncherRelaysStdioAndKillsTheGroup`, the golden assertions above, the ADR 0126 drill step.
- [ ] **S** Documentation: threat model (F-03 row, TB3, T-WK-4, P1 table, level-1 diagram), [worker-control](../contracts/worker-control.md#workspace-boundary) Workspace boundary, [managed workers](../ops/managed-workers.md#operating-limits) Operating limits, ADR 0034's More Information, the `standard` row of ADR 0106.
- [ ] **S** Release: `feat!` with a `BREAKING CHANGE:` footer naming the new default pod shape and the `shared` escape hatch, the `DEPRECATED` line in `values.yaml`, release notes; removal of `shared` in the minor after 0.2 as its own `feat!`.
- [ ] Follow-ups, separate records: the shared builder token leaves the pod (option (c): mandatory minting, per-Run read-only tokens for readers, an answer for GitLab writers); a bootstrap credential with an expiry; the egress gateway rung of ADR 0106.

## Pros and Cons of the Options

### (b) An init container exchanges the bootstrap token

An init container holding `PLOEG_WORKER_BOOTSTRAP_TOKEN` claims the Run and writes the claim, the control token and the per-Run material to an emptyDir that the worker reads and unlinks before it starts the harness.

* Good, because the bootstrap token leaves the harness's container with a small change: `install` already runs as an init step.
* Bad, because the shared builder token cannot be exchanged: a plan-less claim or an untargeted Work Item gets no per-Run token ([server.go](../../pkg/httpapi/server.go) line 712) and the worker falls back to the shared one ([worker.go](../../pkg/worker/worker.go) lines 273 to 275), so it still enters the main container unless (c) removes the fallback.
* Bad, because the per-Run material and the control token still live in the harness's container behind the same single control, and the worker's children still carry the forge token in a readable environment.
* Bad, because the claim moves ahead of `CheckHarnessBinary` ([main.go](../../cmd/ploeg-worker/main.go) lines 189 to 195), which today fails a misconfigured pod before it leases anything; an init claim needs the `FailUnstartedRun` path for a harness that then turns out missing.

### (c) Drop the shared token and move the bootstrap token out

* Good, because a credential that does not exist cannot be stolen, and P1 already excludes the static shared writer token.
* Bad, because per-Run minting becomes mandatory: Forgejo 15 or later with a bot password, or a GitHub App. GitLab writers have no broker ([F-31](../security/threat-model.md#findings)); readers receive no per-Run token ([server.go](../../pkg/httpapi/server.go) lines 709 to 712), so either read-only per-Run tokens are added or a shared read-only token stays, which is the finding again at lower impact.
* Bad, because a projected ServiceAccount token as the bootstrap credential is a file on a volume the harness reads for the whole Run, worse than an environment variable behind `PR_SET_DUMPABLE`, and TB2 rests on worker pods mounting no such token ([_helpers.tpl](../../ops/helm/ploeg/templates/_helpers.tpl) line 446). A short-lived exchange still needs a place the harness cannot read, which is (b)'s init container or (a)'s container.
* Bad, because everything per-Run stays behind the one control, children included.
* Right as a follow-up: once (a) holds, removing the fallback at [worker.go](../../pkg/worker/worker.go) line 273 is a deprecation of `tokenSecret` for writers over one stable minor.

### (d) Harden in place

* Good, because it costs no new container or image.
* Bad, because a second uid for the worker needs `CAP_SETUID`, and `restricted` with `capabilities.drop: [ALL]` ([values.yaml](../../ops/helm/ploeg/values.yaml) lines 718 to 719) allows adding only `NET_BIND_SERVICE`; `hidepid` is a procfs mount option no pod field sets, and remounting needs `CAP_SYS_ADMIN`; a seccomp profile cannot tell a read of `/proc/<pid>/environ` from any other file read; an AppArmor profile must exist on every node, which the chart cannot ship.
* Bad, because what remains is more of the same single layer, and F-03 stays open.

## Re-evaluation triggers

* kubernetes-sigs/agent-sandbox or the egress gateway of ADR 0106's `hardened` class ships per-Run credential injection outside the pod: the proxies leave the worker container and this record's boundary becomes defence in depth.
* A qualified harness cannot run behind the launcher (it needs a TTY, a PID namespace of its own or the worker's process), or ten Runs in a month fail with a launcher error.
* An installation still sets `shared` when the minor after 0.2 is prepared.
* The F-24 fix lands in a shape that assumes a shared container, or Kubernetes gives containers in one pod separate network namespaces: revisit the shared-loopback consequence.
* A reviewer finds a same-pod channel from the harness's container to the worker's that this record does not list.

## More Information

* Evidence trail: [docs/research/2026-10-23-credentials-out-of-the-harness-container.md](../research/2026-10-23-credentials-out-of-the-harness-container.md): the file and line evidence per claim, the dumpable-child experiment and its output, the documentation and code disagreements, and what each option leaves reachable.
* Tracking: [#224](https://github.com/ploeg-hq/ploeg/issues/224); threat model F-03, TB3 and T-WK-4. F-24 (fix in progress) and F-25 interact as described under Consequences.
* Related: [ADR 0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md) (placeholders and proxies stay; the worker that holds them moves into its own container), [ADR 0106](0106-a-run-declares-an-isolation-floor-and-a-cell-runs-it-at-the-strictest-class-it-can-honour.md) (`standard` gains "credentials outside the harness's container"; `hardened` moves them out of the pod), [ADR 0120](0120-a-worker-container-meets-the-restricted-pod-security-standard-by-default.md) (the harness container keeps the restricted context), [ADR 0126](0126-a-recurring-attack-drill-runs-every-harness-against-a-hostile-repository.md) (the drill carries the live check), [ADR 0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md) (the deprecation of `shared`).
* 2026-10-23: proposed. Nothing built.
