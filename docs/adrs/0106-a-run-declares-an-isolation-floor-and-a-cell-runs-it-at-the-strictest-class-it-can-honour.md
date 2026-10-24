---
status: proposed
date: 2026-10-17
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-01
---

# A Run declares the sandbox class it wants and a floor below which it waits, and a cell honours the declared class

## Context and Problem Statement

A Run's security posture is the sum of six independent settings: the container runtime
(`executorType` and `runtimeClassName`), the pod's privileges (`harness.dind`, the
namespace's Pod Security level), credential exposure (`keyIsolation`,
`forgeTokenIsolation`), network egress (policies outside the chart), forge authority (the
per-Run token and its fences) and placement (`nodeSelector`). On the maintainers'
installation on 2026-10-17 two teams ran in Kata micro-VMs and two in plain runc pods with
the same credential model, and no file states which posture a team has. The chart still
defaults `harness.dind` to `true`, which renders a privileged sidecar and switches both
credential proxies off for that workload. A posture that lives in six places, one of them a
default nobody wrote down, cannot be audited and degrades silently. The owner's direction on
2026-10-17: support every way of isolating a Run, and default to the most stringent.

The owner refined that on 2026-10-24: gVisor and Kata are alternatives chosen by fit, not
rungs of a ladder on which a cell always runs the strictest class. They put the Run behind
different boundaries, run on different hardware, cost different things and are visible to
different monitoring. A rule that always picks one of them takes the choice away from the
team that knows its workload.

How does a team state the isolation it needs, how does an installation state the isolation
it can give, and what happens when the two disagree?

## Decision Drivers

* **Strict by default.** The default outcome of touching nothing is the strongest posture
  that needs no special hardware, not the chart author's convenience.
* **Every sandbox stays supported, chosen by fit.** gVisor and Kata solve different problems
  (see *gVisor and Kata compared* below). An installation offers what its hardware allows,
  and a team picks the one its workload fits, not the one a rule ranks higher.
* **Degradation is never silent.** A Run that cannot get its posture waits and is reported.
  It is never run below its floor. This mirrors admission before spend in
  [ADR-0012](0012-two-level-budgets-authorized-and-settled.md): refuse, do not quietly
  do less.
* **No silent change of posture.** A Run executes at the class its team declared. Better
  hardware does not move a Run to another runtime that nothing in its values names.
* **One declaration, enforceable outside Ploeg.** An estate's admission policy must be able
  to check the posture from the pod alone.
* **Portable default.** The default class must run on any conformant Kubernetes cluster,
  including cloud VMs without nested virtualization.

## Considered Options

* Named isolation classes: a Run declares the class it wants and a floor, and a cell
  honours the declared class
* Named isolation classes with a floor, executed at the cell's maximum (chosen 2026-10-17,
  replaced 2026-10-24)
* Named isolation classes as exact targets, with no floor
* One installation-wide class, the same for every team and Role
* Keep the six independent values and document their safe combination

## Decision Outcome

Chosen option: "**named isolation classes, declared class plus a floor, honoured as
declared**", because it supports every sandbox, lets the team that knows its workload pick
between gVisor and Kata, and keeps "never silently weaker" through the floor without adding
"silently different" through an automatic upgrade.

* **Classes.** Four classes bundle the axes:

  | Class | Runtime | Credentials | Egress | Forge authority |
  | --- | --- | --- | --- | --- |
  | `standard` | runc, PSA `restricted`, user namespaces (`hostUsers: false`) | placeholders, proxies that check the placeholder | DNS, ploegd, the model gateway and the forge only | per-Run token, branch-fenced, route-allowlisted |
  | `hardened` | gVisor RuntimeClass | credentials outside the Run's pod | the egress gateway is the only peer | as `standard` |
  | `isolated` | Kata RuntimeClass | as `hardened` | as `hardened` | as `standard` |
  | `confidential` | Kata on a confidential VM | as `hardened` | as `hardened` | as `standard`, plus server-side protection required on the target |

  The classes are not a ladder. `hardened` and `isolated` are two alternatives above
  `standard`: each puts a kernel boundary between the Run and the host, and neither ranks
  above the other. `confidential` extends `isolated`. For a floor the order is
  `standard` < `hardened`, and `standard` < `isolated` < `confidential`.

  A workload with `dind: true`, or with either credential proxy off, is `legacy`. The chart
  renders `legacy` only when `executor.allowLegacy` is `true`, and never by default.
* **Declared class.** A team or Role sets `isolationClass`, the class its Runs execute at.
  Unset means the installation's default class, and an installation that sets none defaults
  to `standard`. An installation with Kata nodes that wants every team in a micro-VM says so
  once, in its default class; nothing changes a Run's class behind its values.
* **Floor.** A team or Role may set `isolationFloor`, the weakest class it accepts when its
  declared class is not offered. Unset means the declared class itself, so by default a Run
  executes at its declared class or waits. The floor must lie at or below the declared class
  in the order above; `hardened` with an `isolated` floor fails to render. A Role may raise
  its team's floor and never lower it.
* **Offer.** An executor, today the one cluster the chart installs into and later each
  cell of [ADR-0107](0107-execution-runs-in-cells-that-pull-from-one-hub.md), advertises
  the set of classes it can honour, not a maximum. A cell with both gVisor and Kata
  advertises both and runs each Run in the one it declared.
* **Mismatch.** ploegd offers a Run to an executor that advertises its declared class. Only
  when no executor does, it offers the Run to one that advertises its floor, and the Run
  executes at the floor class. That substitution is recorded on the pod and on the Work
  Item, never silent. A Run that no executor can honour at its declared class or its floor
  stays `pending`, counts as a capacity wait in the demand signal of
  [ADR-0072](0072-keda-stays-and-ploegd-reserves-a-run-for-each-launcher.md), and is
  reported on the Work Item. It is never run below its floor and never moved to a class it
  did not name.
* **Validation and label.** The chart refuses to render a class whose prerequisites are
  missing: `hardened` or `isolated` without a `runtimeClassName`, `standard` with a harness
  image the values declare as root. Every Run pod carries `ploeg.webgrip.dev/isolation-class`
  with the class it executes at, and `ploeg.webgrip.dev/isolation-class-declared` when that
  differs, so an estate's admission policy can enforce the labels' meaning.
* **Kata pod annotations are refused.** An `isolated` or `confidential` executor must refuse,
  at admission, any pod carrying an `io.katacontainers.*` annotation. Kata Containers up to
  4.2.0 installed by kata-deploy configured containerd to forward every
  `io.katacontainers.*` pod annotation to the runtime, which lets a pod author override the
  Kata configuration and reach root on every node that schedules Kata pods
  ([GHSA-37rf-pj8c-g5g3](https://github.com/kata-containers/kata-containers/security/advisories/GHSA-37rf-pj8c-g5g3),
  published 2026-10-21, fixed in 4.3.0 by allowing only known-safe annotations). The chart
  never renders such an annotation. A Kata executor qualifies on 4.3.0 or later, or with
  containerd's `pod_annotations` narrowed by hand, and with the admission refusal in place
  either way.
* **Axis defaults.** On the axes that need no hardware, credentials, egress and forge
  authority, the chart defaults to the highest rung it can render.

### gVisor and Kata compared

Read on 2026-10-24 from public sources; this is the fit a team chooses on.

| | `hardened` (gVisor) | `isolated` (Kata) |
| --- | --- | --- |
| Boundary | a user-space kernel that intercepts the Run's system calls | the Run's own guest kernel in a micro-VM |
| Hardware | runs on any node, including cloud VMs without nested virtualization | needs hardware virtualization: bare metal, or nested virtualization, which for example [Hetzner Cloud's server FAQ](https://docs.hetzner.com/cloud/servers/faq/) says its cloud servers do not offer |
| Overhead per pod | tens of MiB | about 130 to 160 MiB of memory and 100 to 250m of CPU |
| Workload fit | slower file I/O: a measured run took about 1.4 times runc's time for a test suite and about 3.1 times for writing 20,000 small files; a network-bound install took about the same | builds and Docker inside the VM work as on a node |
| Host monitoring | visible to Falco through [gVisor Runtime Monitoring](https://gvisor.dev/docs/user_guide/runtime_monitoring/) | invisible to host eBPF tools such as Tetragon: the Run's processes live in the guest kernel |

Firecracker is not a practical Kata hypervisor on Talos today. It has no shared
filesystem, so each node needs the devmapper snapshotter and a thin pool; Talos ships no
extension for it; and Kata 4's Rust runtime does not support it. `isolated` therefore means
Kata with its default hypervisor.

### Consequences

* Good, because the posture of any Run is one label, readable from the pod, and the
  posture of any team is one value.
* Good, because a team picks the sandbox its workload fits: gVisor where nodes lack
  hardware virtualization or host monitoring must see the Run, Kata where builds need a
  real kernel and file I/O must stay fast.
* Good, because a new installation is strict without configuration, and a weaker shape
  needs a value that names itself `legacy`.
* Good, because a Run's runtime never changes without a values change: what a team tested
  is what it gets.
* Bad, because better hardware no longer improves a Run by itself. An installation that adds
  Kata nodes changes its default class, or teams change theirs, to use them.
* Bad, because `standard` cannot be honoured today: the worker container renders no
  `securityContext` and the `agent-runner` image runs as root. Both are prerequisites, and
  until they land the chart must refuse `standard` rather than pretend.
* Bad, because two sandbox classes mean two qualifications per harness, one under each
  RuntimeClass, before a team can declare either.
* Bad, because a floor no executor offers is a stall, not an error. The Work Item report and
  the demand signal must make that wait visible, or it looks like a hang.

### Confirmation

* `scripts/helm-golden.sh check` carries one golden per class and the refusals: `hardened`
  or `isolated` without a `runtimeClassName` fails to render; a floor above its declared
  class fails to render; `legacy` without `allowLegacy` fails to render; every rendered Run
  pod carries the class label; no rendered pod carries an `io.katacontainers.` annotation.
* `go test ./pkg/isolation/` pins the class order (`hardened` and `isolated` incomparable),
  the floor check, and that a Role cannot lower its team's floor.
* `go test ./pkg/httpapi/` pins that a reservation for an executor that advertises neither
  a Run's declared class nor its floor answers `204` and the Run stays `pending`; that an
  executor advertising only the floor gets the Run only while no executor advertises the
  declared class; and that an executor advertising both `hardened` and `isolated` gets the
  declared one.
* `ploeg-worker` logs the class it runs at on boot, and refuses to start when the pod's
  label and its environment disagree.
* An estate's admission policy on the labels and its refusal of `io.katacontainers.*`
  annotations are confirmed estate-side and are not part of this record.

## Pros and Cons of the Options

### Named isolation classes with a floor, executed at the cell's maximum

* Good, because nobody has to decide per team to get the strongest class a cell offers.
* Bad, because "strongest" has no answer between gVisor and Kata. On a node with both
  runtimes the rule ranks Kata higher and never picks gVisor, so a team that needs gVisor's
  host monitoring or runs on VMs without nested virtualization cannot get it there.
* Bad, because every Run on a Kata installation pays the micro-VM's memory and CPU, and a
  Run's runtime changes when the installation's hardware changes, without a values change.

### Named isolation classes as exact targets, with no floor

* Good, because the class a team names is the class it gets.
* Bad, because a Run whose class is offered nowhere can only wait, and a team that would
  accept a weaker class meanwhile has no way to say so.

### One installation-wide class

* Good, because there is exactly one knob.
* Bad, because one team's clone-heavy repository that cannot afford gVisor forces every
  team down, or is excluded.

### Keep the six independent values

* Good, because nothing changes in the chart.
* Bad, because this is the shape that produced the silent `dind: true` inheritance and
  the two-teams-two-postures state the record opens with.

## Re-evaluation triggers

* kubernetes-sigs/agent-sandbox ships per-sandbox credential injection or its own
  isolation levels: map the classes onto them and shrink the chart's share.
* Kubernetes turns user namespaces on by default for all pods: `standard` loses a
  prerequisite and the refusal logic simplifies.
* An estate asks for a class between two rungs, or for a fifth axis such as data
  residency: reopen the table rather than add a flag.
* Talos ships the `kata-containers-snp` extension on hardware an estate owns: define
  `confidential` for real.
* Talos ships a Firecracker extension, or Kata's Rust runtime supports Firecracker with a
  shared filesystem: decide whether `isolated` names a hypervisor.
* gVisor's file I/O on the small-files measurement comes within 1.5 times runc's, or host
  eBPF tools gain visibility into Kata guests: the fit table above changes, and with it the
  advice to teams.

## More Information

* Evidence trail:
  [docs/research/2026-10-17-isolation-ladder-and-execution-cells.md](../research/2026-10-17-isolation-ladder-and-execution-cells.md),
  sections 1 to 4.
* Related: [0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md)
  (credentials stay in the worker; this record's `hardened` and above move them out of
  the pod), [0037](0037-teams-opt-into-registry-egress-through-a-logged-allowlist-proxy.md)
  (egress profiles become part of a class),
  [0072](0072-keda-stays-and-ploegd-reserves-a-run-for-each-launcher.md) (the demand and
  reservation routes that carry the class),
  [0107](0107-execution-runs-in-cells-that-pull-from-one-hub.md) (the executor that
  advertises the classes it can honour).
* Backlog items absorbed: #74 (RuntimeClass per team).
* 2026-10-24: [ADR 0138](0138-a-harness-runs-in-its-own-container-and-no-credential-enters-it.md) adds a rung to the `standard` class's credential column: the placeholders and proxies live in a worker container separate from the harness's, so no credential enters the harness's container. `hardened` and above still move the credentials out of the pod. The table above is unchanged.
* 2026-10-24 — Amended by the owner: gVisor (`hardened`) and Kata (`isolated`) are
  alternatives chosen by fit, not rungs where a cell runs the strictest class it can honour.
  A team or Role declares the class it wants (`isolationClass`) and a floor below which it
  waits (`isolationFloor`); an executor advertises the classes it can honour instead of a
  maximum, and runs a Run at its declared class, or at its floor only when no executor
  offers the declared class, recorded on the pod and the Work Item. `maxIsolationClass` and
  its written exception are dropped, because nothing upgrades a Run any more. `hardened` now
  also needs a `runtimeClassName` to render. Added the gVisor and Kata comparison, the
  Firecracker note and the refusal of `io.katacontainers.*` pod annotations
  (GHSA-37rf-pj8c-g5g3). The file name keeps the 2026-10-17 title, because files are never
  renamed. Status stays proposed.
