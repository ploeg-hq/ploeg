---
status: proposed
date: 2026-10-17
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-01
---

# A Run declares an isolation floor, and a cell runs it at the strictest class it can honour

## Context and Problem Statement

A Run's security posture is the sum of six independent settings: the container runtime
(`executorType` and `runtimeClassName`), the pod's privileges (`harness.dind`, the
namespace's Pod Security level), credential exposure (`keyIsolation`,
`forgeTokenIsolation`), network egress (policies outside the chart), forge authority (the
per-Run token and its fences) and placement (`nodeSelector`). On the webgrip homelab on
2026-10-17 two teams ran in Kata micro-VMs and two in plain runc pods with the same
credential model, and no file states which posture a team has. The chart still defaults
`harness.dind` to `true`, which renders a privileged sidecar and switches both credential
proxies off for that workload. A posture that lives in six places, one of them a default
nobody wrote down, cannot be audited and degrades silently. The owner's direction on
2026-10-17: support every way of isolating a Run, and default to the most stringent.

How does a team state the isolation it needs, how does an installation state the isolation
it can give, and what happens when the two disagree?

## Decision Drivers

* **Strict by default.** The default outcome of touching nothing is the strongest posture
  the installation can provide, not the chart author's convenience.
* **Every rung stays supported.** gVisor on VMs, Kata on bare metal, user namespaces
  anywhere: an installation chooses by hardware, not by fork.
* **Degradation is never silent.** A Run that cannot get its posture waits and is reported.
  It is never downgraded. This mirrors admission before spend in
  [ADR-0012](0012-two-level-budgets-authorized-and-settled.md): refuse, do not quietly
  do less.
* **One declaration, enforceable outside Ploeg.** An estate's admission policy must be able
  to check the posture from the pod alone.
* **Portable floor.** The default class must run on any conformant Kubernetes cluster,
  including cloud VMs without nested virtualization.

## Considered Options

* Named isolation classes: a Run declares a floor, a cell advertises its maximum, every
  Run executes at the cell's maximum
* Named isolation classes as exact targets: a Run executes at the class it names
* One installation-wide class, the same for every team and Role
* Keep the six independent values and document their safe combination

## Decision Outcome

Chosen option: "**named isolation classes with a floor, executed at the cell's maximum**",
because it is the only option where both "support many ways" and "default to the most
stringent" hold at the same time: the floor keeps a Run off capacity that is too weak, and
the maximum rule means nobody has to decide per team to get the strongest available.

* **Classes.** Four ordered classes bundle the axes:

  | Class | Runtime | Credentials | Egress | Forge authority |
  | --- | --- | --- | --- | --- |
  | `standard` | runc, PSA `restricted`, user namespaces (`hostUsers: false`) | placeholders, proxies that check the placeholder | DNS, ploegd, the model gateway and the forge only | per-Run token, branch-fenced, route-allowlisted |
  | `hardened` | gVisor RuntimeClass | credentials outside the Run's pod | the egress gateway is the only peer | as `standard` |
  | `isolated` | Kata RuntimeClass | as `hardened` | as `hardened` | as `standard` |
  | `confidential` | Kata on a confidential VM | as `hardened` | as `hardened` | as `standard`, plus server-side protection required on the target |

  A workload with `dind: true`, or with either credential proxy off, is `legacy`. The chart
  renders `legacy` only when `executor.allowLegacy` is `true`, and never by default.
* **Floor.** A team or Role sets `isolationClass`. It is a floor. Unset means `standard`. A
  Role may raise its floor above its team's and never lower it.
* **Maximum.** An executor, today the one cluster the chart installs into and later each
  cell of [ADR-0107](0107-execution-runs-in-cells-that-pull-from-one-hub.md), advertises
  the highest class it can honour. Every Run it accepts executes at that class. A Kata
  installation runs `standard` work under Kata.
* **Mismatch.** ploegd offers a Run only to an executor whose maximum is at or above the
  Run's floor. A Run no executor can honour stays `pending`, counts as a capacity wait in
  the demand signal of
  [ADR-0072](0072-keda-stays-and-ploegd-reserves-a-run-for-each-launcher.md), and is
  reported on the Work Item. It is never downgraded.
* **Written exceptions.** A team may set `maxIsolationClass` with a `reason`, for example
  gVisor's file I/O cost on a clone-heavy repository. The reason renders into the pod's
  annotations so the exception is visible where the Run runs.
* **Validation and label.** The chart refuses to render a class whose prerequisites are
  missing: `isolated` without a `runtimeClassName`, `standard` with a harness image the
  values declare as root. Every Run pod carries `ploeg.webgrip.dev/isolation-class` with the
  class it executes at, so an estate's admission policy can enforce the label's meaning.
* **Axis defaults.** On the axes that need no hardware, credentials, egress and forge
  authority, the chart defaults to the highest rung it can render.

### Consequences

* Good, because the posture of any Run is one label, readable from the pod, and the
  posture of any team is one value.
* Good, because a new installation is strict without configuration, and a weaker shape
  needs a value that names itself `legacy`.
* Good, because an installation with better hardware improves every Run on it without a
  values change.
* Bad, because `standard` cannot be honoured today: the worker container renders no
  `securityContext` and the `agent-runner` image runs as root. Both are prerequisites, and
  until they land the chart must refuse `standard` rather than pretend.
* Bad, because the Kata overhead, 160Mi and 100m per pod, is paid by every Run on a Kata
  installation, including work that did not ask for it. The owner accepts this: the
  overhead is small and the alternative is a per-team decision.
* Bad, because a floor above every executor's maximum is a stall, not an error. The Work
  Item report and the demand signal must make that wait visible, or it looks like a hang.

### Confirmation

* `scripts/helm-golden.sh check` carries one golden per class and the refusals: `isolated`
  without a `runtimeClassName` fails to render; `legacy` without `allowLegacy` fails to
  render; every rendered Run pod carries the class label.
* `go test ./pkg/isolation/` pins the class order, the floor and maximum comparison, and
  that a Role cannot lower its team's floor.
* `go test ./pkg/httpapi/` pins that a reservation for an executor whose maximum is below
  a Run's floor answers `204`, and that the Run stays `pending`.
* `ploeg-worker` logs the class it runs at on boot, and refuses to start when the pod's
  label and its environment disagree.
* The homelab's admission policy on the label is confirmed estate-side and is not part of
  this record.

## Pros and Cons of the Options

### Named isolation classes as exact targets

* Good, because the class a team names is the class it gets, which is simpler to reason
  about per team.
* Bad, because a Kata installation would run `standard` work in runc when nothing in the
  team's values asked for Kata, so the default is not the most stringent available.
* Bad, because every team needs a decision before it benefits from better hardware.

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
  advertises a maximum).
* Backlog items absorbed: #74 (RuntimeClass per team).
