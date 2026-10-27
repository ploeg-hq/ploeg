# Worker boundaries under profile P1: qualification record, 2026-10-26

Status: research record, 2026-10-26. It answers the third step of [#16](https://github.com/ploeg-hq/ploeg/issues/16): exercise host mounts, metadata endpoints, unrelated services, Kubernetes API access and unapproved egress from a P1 worker, and write down what holds, what holds it, and what was not tested. The credential planes are covered by the [2026-10-25 requalification](2026-10-25-credential-proxy-requalification.md) and are not repeated here.

**Question.** On the current release, what can a P1 worker pod reach or hold beyond what its Run needs, and which of those limits were seen working on a live cluster?

**Answer.** In the one tested environment, nothing beyond the allowlist was reached, and no Kubernetes token, host path or capability was present. On 2026-10-26 [`scripts/worker-boundary-probe.sh`](../../scripts/worker-boundary-probe.sh) ran in a Kata pod with the `ploeg-worker` identity and the worker's labels, and confirmed the metadata endpoint and the Kubernetes API unreachable and the process unprivileged. It also found one gap: **seccomp is off inside the Kata guest** (mode 0), although the pod asks for `RuntimeDefault`. The kata-containers 3.32.0 extension the installer runs ships `disable_guest_seccomp=true`, so the guest never loads the pod's profile. That is finding [F-41](../security/threat-model.md#findings), and the privilege row stays not qualified until a probe passes with the guest setting fixed.

## Tested environment

Every claim below is limited to this environment.

| Item | Value |
| --- | --- |
| Cluster | The maintainer's homelab: Talos Linux, Cilium as the CNI, nodes on kernel `6.18.48-talos` |
| Release | ploegd and ploeg-worker `0.2.0-rc.72`, chart `ploeg-0.2.0-rc.72` as a subchart of the installer's own chart, Flux-reconciled. The hostile Runs cited below ran ploeg-worker `0.2.0-rc.68`; the last Run before this record ran `0.2.0-rc.71`, and no Run has used `rc.72` yet |
| Workloads read | SandboxTemplates `ploeg-worker-bronze-builder`, `-bronze-reviewer`, `-bronze-shadow-reviewer` (harness `acp/openhands`) and `ploeg-worker-copper` (harness `exec`), namespace `ploeg` |
| Executor | `sandbox` (agent-sandbox SandboxTemplate and SandboxClaim), `runtimeClassName: kata` on every template, RuntimeClass `kata` with handler `kata` |
| Deployment profile | The release as a whole sets `supportProfile: unsupported`, because other teams in it leave P1 (a `claude-environment` team and the claude-runner). The bronze and copper workloads carry every P1 worker setting the chart can render. Nothing here qualifies the other teams |
| Method | Read-only `kubectl` against the live objects, the logs of the installer's probe Jobs, and the worker logs of the hostile Runs in the cluster's log store. No object was created or changed for this record, and no model was called |

## Results per boundary

| Boundary | Enforced by | Live evidence | Status |
| --- | --- | --- | --- |
| Host mounts | The SandboxTemplates declare only `emptyDir` volumes (`worker-bin`, `harness-launcher`, `worker-tmp`, `ci-shared`) and one `image` volume (the Go toolchain); no `hostPath`. The namespace enforces Pod Security `restricted` (`pod-security.kubernetes.io/enforce=restricted`, version `v1.36`), which refuses `hostPath` volumes, `hostNetwork`, `hostPID` and `hostIPC`. Under Kata, an `emptyDir` lives behind the VM's shared-filesystem mount, not as a bind of a host directory into the agent's kernel | The hostile Run of 2026-10-25 (Work Item 279, trace `ploeg-76b41121490b`, sandbox under Kata) found no `/run/secrets`, no `/var/run/secrets` and no credential file. It did not list mount points | **Qualified** for the paths that Run looked at. The full mount table and the runtime-socket paths have not been read from inside a Kata worker |
| Cloud metadata endpoint | No egress rule allows `169.254.169.254` or any link-local address; the namespace's default-deny NetworkPolicy and the worker policies list only named peers. The homelab is bare metal and runs no metadata service, so a reach would reach nothing anyway | `worker-boundary-probe` in a Kata pod, 2026-10-26: AWS/OpenStack, GCP and Alibaba metadata addresses all unreachable | **Qualified live** in the homelab. On a cloud cluster this row stays the installer's: it holds only where the CNI enforces the policies and the provider's metadata address is not reachable through a host route |
| Kubernetes API, token | `automountServiceAccountToken: false` on every SandboxTemplate; service account `ploeg-worker` has no RoleBinding or ClusterRoleBinding in the cluster | The 2026-10-25 hostile Run found no `/var/run/secrets` | **Qualified**: the worker holds no Kubernetes credential |
| Kubernetes API, reachability | No egress rule names the API server. The chart's `networkPolicy.kubernetesApi` peers are rendered only into the launcher's policy ([network policies](../ops/network.md)) | `worker-boundary-probe` in a Kata pod, 2026-10-26: `https://kubernetes.default.svc/version` and the Service address `https://10.43.0.1:443/version` both unreachable | **Qualified live** |
| Unrelated services and unapproved egress | The chart's `ploeg-worker` NetworkPolicy (ingress none; egress to DNS, ploegd on 8080, the `ai` namespace on 4000, the `forgejo` namespace on 3000, the `observability` namespace on 4318) plus the namespace default-deny, enforced by Cilium | The installer's Job `worker-egress-probe` ran with the worker's label on 2026-10-25 at 14:26Z on worker-1 and passed: ploegd, LiteLLM and Forgejo answered; the ploeg database, Unfold, ploeg-mcp, the tracker, the knowledge graph, the gateway and forge databases, two internal MCP servers, the public forge and tracker names, both gateway addresses, two LAN addresses and `example.com` all timed out. The 2026-10-25 hostile Run under Kata found the public forge address unreachable | **Qualified** for the policy. That probe ran under runc, not Kata. Cilium enforces at the pod's host-side interface, which a Kata pod also uses (the installer's `kata-clusterip-probe` shows a Kata pod's traffic passing through Cilium's service translation), and the hostile Run saw the deny side under Kata for the forge address only |
| Privilege | SandboxTemplate security contexts: harness uid 1000, worker uid 1000 with a read-only root filesystem, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`, `seccompProfile: RuntimeDefault` on every container. Pod Security `restricted` refuses anything weaker at admission. The chart refuses a weaker `workerSecurityContext` under `supportProfile: P1` ([ADR-0154](../adrs/0154-the-chart-refuses-values-that-leave-profile-p1-unless-they-declare-the-deployment-unsupported.md)) | The installer's `kata-clusterip-probe` (2026-10-25 05:03Z) reported guest kernel `6.18.35` against the host's `6.18.48-talos`, so a pod under `kata` runs on its own kernel. `worker-boundary-probe` in a Kata pod on fringe-workstation, 2026-10-26 (guest kernel 6.18.35): uid 100; `CapEff`, `CapPrm`, `CapBnd` and `CapAmb` empty; `NoNewPrivs` set; **`Seccomp` 0** | **Not qualified: seccomp is not enforced inside the Kata guest** ([F-41](../security/threat-model.md#findings)). Everything else in the row holds live |

## What is not covered

- **The namespace is not the sandbox.** Every limit above comes from a named control: the pod template, Pod Security admission, the Kata VM, Cilium's enforcement of the NetworkPolicies, and the credential proxies. A namespace without them isolates nothing, and on a CNI that does not enforce NetworkPolicy (kindnet, Flannel) the egress row is void ([network policies](../ops/network.md)).
- **Kata is trusted, not tested.** No escape attempt was made against the Kata guest or its hypervisor. A guest-kernel or virtio bug is the residual the VM boundary carries.
- **The allowlist is per namespace and port, not per pod.** The chart derives each egress peer from its URL as a whole namespace. In the homelab, a worker may reach any pod in `ai` on 4000, any pod in `forgejo` on 3000 and any pod in `observability` on 4318. The installer's narrower `ploeg-worker-egress` policy does not narrow that, because NetworkPolicies add up. Narrowing it takes `networkPolicy.<endpoint>.peers`.
- **The opt-in worker admission policy is off.** `security.workerAdmission` is not enabled in the homelab, so the API server enforces only Pod Security `restricted`, not the chart's stricter checks on host ports, sysctls, AppArmor and Kata annotations.
- **Only one harness and two teams.** The bronze Runs used `acp/openhands` and the copper canary used `exec`. `openhands` under the KEDA executor shares the node's kernel and is outside this record.
- **`rc.72` has had no Run.** The pod templates at `rc.72` were read; the in-pod observations come from `rc.68` Runs. Between those tags the worker and chart changed lease handling and admission refusals, not the pod's mounts, token or network policy.
- **Registry egress, DinD and the claude-runner** are outside P1 and were not exercised.

## The boundary probe

[`scripts/worker-boundary-probe.sh`](../../scripts/worker-boundary-probe.sh) is a POSIX `sh` and `curl` check that runs inside a worker pod and prints one `PASS` or `FAIL` line per boundary. It checks:

- the process's uid, its effective, permitted, bounding and ambient capabilities, `no_new_privs` and its seccomp mode
- that no service-account token is present
- that no host path or runtime socket is present or mounted, and that `DOCKER_HOST` is unset
- that no metadata address answers (AWS, Azure and OpenStack link-local, the AWS IPv6 and ECS task addresses, GCP, Alibaba)
- that neither `kubernetes.default.svc` nor the address in `KUBERNETES_SERVICE_HOST` accepts a connection
- that the unapproved hosts in `PROBE_BLOCKED_URLS` are unreachable and the approved ones in `PROBE_REACHABLE_URLS` are reachable

It exits non-zero on any failure. It counts a host as reached when a TCP connection opens, so a TLS or HTTP error from the far side is still a reach.

[`scripts/test-worker-boundary-probe.sh`](../../scripts/test-worker-boundary-probe.sh), run by `scripts/verify.sh`, shows the probe working both ways. A confined fixture passes. Each of fourteen escapes fails, and the failure names it: uid 0, an effective capability, a bounding capability, privilege escalation, seccomp off, a token, the Docker socket, a kubelet mount, `DOCKER_HOST`, a reachable metadata address, a reachable API URL, a reachable API Service address, unapproved egress, and an allowed endpoint that is unreachable. Removing a check from the probe, or making every connection look blocked, turns that test red.

To qualify the three open rows, run the probe in a pod with the worker's label, `runtimeClassName: kata`, the worker service account, `automountServiceAccountToken: false` and the harness's security context, as the installer's `worker-egress-probe` Job does under runc. Set `PROBE_REACHABLE_URLS` to the ploegd, gateway and forge health URLs and `PROBE_BLOCKED_URLS` to the services the installer's probe lists. Attach the log to a dated record. A hostile Run is the other route: the task text carries the script, and the harness runs it in its own container.

## Metadata result and F-46, 2026-10-28

The metadata row above is the probe's 2026-10-26 result: `http://169.254.169.254/latest/meta-data/`, `http://metadata.google.internal/computeMetadata/v1/` and `http://100.100.100.200/latest/meta-data/` all `PASS metadata unreachable` from a Kata pod with the worker's labels. It holds for the homelab only, and for a narrower reason than the row's first column suggests: the homelab's forge, gateway and collector are `.svc` URLs, so its worker policy has no internet rule at all and nothing opened port 80 to any address.

Threat-model finding [F-46](../security/threat-model.md#findings) ([#356](https://github.com/ploeg-hq/ploeg/issues/356)) is the case that result does not cover: an `http://` public endpoint renders `0.0.0.0/0` on 80, which before the fix excepted only `privateCidrs` and so admitted `169.254.169.254`. From the fix on, every internet rule excepts link-local `169.254.0.0/16` and loopback `127.0.0.0/8` whatever `privateCidrs` holds, and `scripts/test-chart-network-policies.sh` evaluates the rendered worker policy of an `http://` public forge and finds `169.254.169.254` denied on 80 and 443 while a public address is admitted on both. The probe now also tries the AWS ECS task address `169.254.170.2` and the AWS IPv6 address `fd00:ec2::254`; the chart's internet rule is IPv4 only, so the second is denied by the default deny.

Still open: the probe has not run on a cloud node, where a metadata service exists to answer. That run, with a worker on an `http://` public forge, is the live result for F-46.

## Result for #16

- The supported profile ran a representative task (the bronze writer, reviewer and shadow-reviewer Runs of Work Item 279, the 2026-10-25 hostile task) and passed the boundary checks that were run live: egress, the forge's public address, the token and credential files.
- Settings outside P1 already fail to render with the reason, or render only under `supportProfile: unsupported` (ADR-0154 and the `leaves_p1` cases in [helm-golden.sh](../../scripts/helm-golden.sh)).
- Metadata and API reachability are qualified live. The privilege set holds except for seccomp inside the Kata guest (F-41). The issue stays open until the probe passes on a node whose Kata runtime sets `disable_guest_seccomp=false`.
