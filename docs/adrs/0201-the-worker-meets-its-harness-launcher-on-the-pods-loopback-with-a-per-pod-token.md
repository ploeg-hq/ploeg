---
status: accepted
date: 2026-11-18
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-05-18
---

# The worker meets its harness launcher on the pod's loopback with a per-pod token

## Context and Problem Statement

[ADR 0138](0138-a-harness-runs-in-its-own-container-and-no-credential-enters-it.md) put the harness in its own container and carried every process of a Run over one connection from `ploeg-worker harness-serve` to the worker. The worker listened on a Unix socket in the `harness-launcher` emptyDir that both containers mount, accepted the first connection and removed the socket. That a Unix socket created by one container is connectable from another assumes both see the same kernel object behind the volume.

Under gVisor that assumption fails. On a cluster running workers under gVisor (`runtimeClassName: gvisor`, `executor.type: cronjob`, chart `0.2.0-rc.109`) the launcher dialled for two minutes and the worker exited with `no harness launcher connected to /run/ploeg/launcher/launcher.sock: context deadline exceeded`, while the harness container ran silently and exited 0. Each gVisor container reaches a shared emptyDir through the gofer, so a socket bound in one container is a file the other container cannot connect to. gVisor shares such a volume inside the sandbox only when the pod carries its mount-hint annotations, which an admission policy may forbid and which the chart cannot assume. Every gVisor installation therefore fails every Run of a separate harness container, the default since ADR 0138.

How does the worker reach the launcher under runc, Kata and gVisor alike, without widening who can drive the launcher or pose as it?

## Decision Drivers

* **One path for every runtime.** The pod shape must not depend on the runtime class, and the chart must not need a runtime-specific annotation.
* **No wider channel than before.** With the socket, only a container that mounts `harness-launcher` could connect. Whatever replaces it must keep that set, not open the launcher to every process in the pod's network namespace.
* **The worker still accepts once.** ADR 0138's settled detail stands: the worker listens, the launcher dials, the worker takes exactly one launcher and stops listening, so nothing started later in the harness container can pose as the launcher.
* **Least machinery.** No new chart value, no new volume, no new image.

## Considered Options

* (a) Loopback TCP on an ephemeral port, with the address and a per-pod random token published in the in-memory volume both containers already mount
* (b) Loopback TCP on a fixed, chart-configurable port, with the token published the same way
* (c) Keep the Unix socket and have the chart set gVisor's mount hints for `harness-launcher`
* (d) Loopback TCP with no token

## Decision Outcome

Chosen option: "**(a) loopback TCP on an ephemeral port with a per-pod token**", because both containers of a pod share its network namespace under every runtime, and the token restores exactly the set of callers the socket had: the containers that mount `harness-launcher`.

1. **Listen.** The worker binds `127.0.0.1:0`, draws 32 random bytes from `crypto/rand` as the token and publishes `{"address","token"}` as `launcher.json` in `harness-launcher` (a Memory emptyDir, mounted read-only in the harness container), written to a temporary file and renamed so the launcher never reads half of it.
2. **Dial.** `ploeg-worker harness-serve /run/ploeg/launcher/launcher.json` waits for that file, refuses an address that is not a loopback IP, dials it and sends the token in its hello. `PLOEG_HARNESS_LAUNCHER` names the same file for the worker.
3. **Accept once.** The worker compares the token in constant time. A connection without it, or with another one, or that sends no hello, is closed and the worker keeps listening until the two-minute deadline; the first connection with the token is the launcher. The worker then closes the listener and removes `launcher.json`. A launcher with the token but another protocol fails the Run at once.
4. **Protocol.** The hello gains `token`, and the launcher protocol becomes version 2. Both ends are the same `ploeg-worker` binary from the `worker-bin` volume, so no pod mixes versions.
5. **What goes.** The Unix socket and `launcher.sock`. The volume, its mounts and the environment variable name stay; only the file they name changes.

### Consequences

* Good, because the worker pod runs under gVisor, Kata and runc with one shape and no runtime-specific annotation.
* Good, because who can drive the launcher is unchanged: only a process that can read `harness-launcher`, which only the worker and harness containers mount, learns the token. An injected sidecar, an ephemeral debug container or a process in another container of the pod reaches the port but not the token.
* Good, because nothing the agent starts can pose as the launcher or reach the worker through this channel: every agent process is started over the already-accepted connection, after the worker stopped listening and removed the token. The token file is readable by the harness container for those few seconds only, before any agent process exists.
* Good, because the ephemeral port cannot collide with the model or forge proxy, with a port the agent binds later, or with an installer's sidecar.
* Good, because nothing can pose as the worker to the launcher: the launcher dials only the address in `launcher.json`, which only the worker container can write (the harness container mounts the volume read-only), and the kernel picks that port from the ones no other process holds, so there is no address to squat in advance.
* Bad, because the token travels in the clear on the loopback. Reading another process's loopback traffic needs `CAP_NET_RAW` or `CAP_NET_ADMIN` in the pod's network namespace, which `restricted` drops ([ADR 0120](0120-a-worker-container-meets-the-restricted-pod-security-standard-by-default.md)), and the token is worthless once the worker stops listening.
* Neutral: a NetworkPolicy does not apply to traffic on the pod's own loopback, so the chart's policies are unchanged.

### Confirmation

* `go test ./pkg/harness/launch/` (Linux, in CI's `go test ./...`): `TestWorkerListensOnTheLoopbackOnly`, `TestEveryAcceptDrawsItsOwnToken`, `TestAConnectionWithoutTheTokenIsRefusedAndTheLauncherStillConnects` (no token, a wrong token and a silent connection are refused, then the launcher connects), `TestLauncherRendezvousTakesOneConnectionOnly` (the file is gone and the address refuses a second connection), `TestLauncherRefusesARendezvousOffTheLoopback`, `TestLauncherWaitsForTheWorkersRendezvous` and `TestLauncherWithAnotherProtocolIsRefused`.
* `go test ./pkg/worker/`: `TestHarnessContainerSeesNoCredential` runs the launcher in its own user, PID and mount namespace, sharing only the network namespace as a pod does, and connects over this channel.
* `scripts/helm-golden.sh check`: every golden renders `harness-serve /run/ploeg/launcher/launcher.json`, and [check-harness-container.py](../../scripts/check-harness-container.py) fails a separate-container pod whose launcher argument is not the path the worker gets in `PLOEG_HARNESS_LAUNCHER`.
* A Run under gVisor reaches its harness: the worker logs `harness runs in its own container behind the launcher`. No CI job runs a gVisor pod; the first installation that does confirms it, and until then this item is open.
* `go test ./internal/ledger/` validates this record.

## Pros and Cons of the Options

### (b) A fixed, chart-configurable port

* Good, because the port is visible in the pod spec.
* Bad, because a fixed port can collide with a port an installer's sidecar already uses, and a process that binds it first is where the launcher dials.
* Bad, because it adds a chart value, its schema and its documentation for no property (a) lacks.

### (c) gVisor mount hints

* Good, because the socket and its file-permission boundary stay.
* Bad, because the annotations are gVisor-specific, an admission policy may forbid them, and the next sandboxed runtime may have its own rule for sockets on shared volumes.

### (d) Loopback TCP without a token

* Good, because it is the smallest change.
* Bad, because any process in the pod's network namespace could connect first and receive the Run's argv and environment, then answer verification with an exit status of its choice; the socket limited that to the two containers that mount the volume.

## Re-evaluation triggers

* Kubernetes gives containers of one pod separate network namespaces, or a supported runtime stops sharing loopback between them.
* gVisor shares emptyDir sockets between containers without annotations, and a reviewer prefers the socket's file boundary back.
* A Run fails because the launcher never connected under any runtime, or a supported runtime cannot read a file one container renamed into a shared Memory emptyDir.
* A reviewer finds a process in the pod that can read `harness-launcher` other than the worker and the harness container's launcher.

## More Information

* Amends item 2 of [ADR 0138](0138-a-harness-runs-in-its-own-container-and-no-credential-enters-it.md): the transport and the rendezvous change; the pod shape, the one connection, the PID 1 check and every credential rule stay.
* Code: [rendezvous.go](../../pkg/harness/launch/rendezvous.go), [client.go](../../pkg/harness/launch/client.go) `Accept`, [server.go](../../pkg/harness/launch/server.go) `Serve`; chart [_helpers.tpl](../../ops/helm/ploeg/templates/_helpers.tpl).
* Threat model: TB3 and the level 1 diagram in [threat-model.md](../security/threat-model.md).
* gVisor shares a volume inside the sandbox only with its `dev.gvisor.spec.mount.<volume>.*` pod annotations; option (c) would have set them.
* 2026-11-18: accepted. The maintainer chose loopback TCP over the gVisor mount hints after the failure above; the ephemeral port, the token and the rendezvous file were settled in the pull request that implements it.
