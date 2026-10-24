# Run oversight in layers, and execution backends that scale

Date: 2026-10-24 · Ticket: none yet · Decision records: none yet; section 7 proposes them. This continues [2026-10-17-isolation-ladder-and-execution-cells.md](2026-10-17-isolation-ladder-and-execution-cells.md) ([ADR-0106](../adrs/0106-a-run-declares-an-isolation-floor-and-a-cell-runs-it-at-the-strictest-class-it-can-honour.md), [ADR-0107](../adrs/0107-execution-runs-in-cells-that-pull-from-one-hub.md)) and the [threat model](../security/threat-model.md).

## Why this came up

The owner set two requirements for the production cell.

1. **Layered oversight.** Isolation stops a Run from doing some things. It does not tell anyone what a Run tried. The owner wants judge models to watch each agent action right after it happens, so a flag is raised within seconds, and an asynchronous audit afterwards. These come on top of the deterministic guards Ploeg already has.
2. **Execution backends that scale.** Kata nodes must scale, and Ploeg should show and support other ways to run an isolated Run: gVisor nodes, a real VM per Run, and others.

The facts in sections 2 and 5 were read from primary sources on 2026-10-24. Each is cited where it is used. Anything that could not be confirmed is marked unverified. Sections 3, 4, 6 and 7 are a proposal.

## 1. What Ploeg already has

| Layer | Where | What it does |
| --- | --- | --- |
| Isolation classes | ADR-0106 | Runtime, credential, egress and forge rungs; a Run executes at its cell's maximum |
| No credential in the harness | ADR-0034, ADR-0138 | The harness container holds placeholders; the worker container keeps the tokens and both proxies |
| Default-deny network | ADR-0137 | Each pod's allowlist comes from its URLs |
| Credential leak guard | ADR-0105 | An exact match of a Run's credential parks the Work Item and closes its pull request |
| Live transcript | ADR-0090 | Typed, redacted entries shipped every 2 seconds |
| Harness telemetry | ADR-0113 | OpenTelemetry per Team and Role, joined on the Run alias |
| Untrusted text is quoted | ADR-0122 | Notes carry provenance; agent text never reads as an instruction |
| Shadow readers and judges | ADR-0096, ADR-0135 | A model records a verdict and never acts |
| Review gate | ADR-0017, ADR-0119 | Verdict-driven review; a pull request is ready only after the forge's CI passed |
| Attack drill | ADR-0126 | Every harness runs weekly against a hostile repository |

Two things are missing. Nothing looks at an action between the moment the agent takes it and the review of the finished pull request. And nothing compares what the agent says it did with what the kernel and the network saw.

## 2. What the research says

### 2.1 Monitoring agents with other models

- **Per-action monitoring catches most attacks, if the flagged action is examined further.** In Ctrl-Z ([arXiv 2504.10374](https://arxiv.org/abs/2504.10374)), a monitor that flags a suspicious action plus a protocol that samples that action again cut attack success from 58% to 7%, at a 5% cost to benign work.
- **Monitors miss a lot at practical false-positive rates.**
  - In SHADE-Arena ([arXiv 2506.15740](https://arxiv.org/abs/2506.15740)), the best monitor scored AUC 0.87 (a 0-to-1 measure of how well it separates sabotage from honest runs). At useful catch rates, more than one honest run in five was still flagged.
  - In SLEIGHT-Bench ([arXiv 2605.16626](https://arxiv.org/abs/2605.16626)), a frontier monitor at 1% false positives caught 32% of covert attacks. Half the attacks were never caught in ten trials.
  - A monitor built as a coding agent, able to inspect the repository itself, beat prompted monitors.
- **Agents can attack the monitor.** Adaptive Attacks on Trusted Monitors ([arXiv 2510.09462](https://arxiv.org/abs/2510.09462)) shows agents putting prompt injections into their own output and consistently evading monitors. Sampling the flagged action again makes this worse: every new sample is another attempt at the injection.
- **The monitor's model family matters.**
  - A single-author 2026 paper ([arXiv 2607.06596](https://arxiv.org/abs/2607.06596)) found that a monitor's accuracy measured on one model family does not carry over to agents from another. Treat it as provisional.
  - METR ([2026-01-19](https://metr.org/blog/2026-01-19-early-work-on-monitorability-evaluations/)) found monitors that read the agent's reasoning catch far more, because agents state their evasion plans there.
- **Asynchronous review works with cheaper models, but only for harm that can be undone.**
  - UK AISI's Async Control ([arXiv 2512.13526](https://arxiv.org/abs/2512.13526)): an ensemble of after-the-fact monitors missed 6% of attacks at 1% false positives.
  - DeepMind and AISI ([arXiv 2512.22154](https://arxiv.org/abs/2512.22154)): monitoring after the fact is only acceptable where the harm can be reversed.
- **A production design to learn from: Claude Code's auto mode** ([Anthropic, 2026-03](https://www.anthropic.com/engineering/claude-code-auto-mode)).
  - A cheap first-stage classifier that errs toward blocking: 8.5% false positives.
  - A reasoning second stage that looks only at what the first stage flagged: 0.4% false positives. It still missed 17% of real overeager actions.
  - The classifier sees user messages and tool calls, not the assistant's prose. That keeps the agent from talking it into anything.

**Conclusion for Ploeg:**

- Use a cascade: a cheap classifier on every action, and a strong model only on what it flags.
- The judge must come from a different model family than the agent. Measure each agent-and-judge pair that actually runs.
- The judge sees structured tool calls with agent text quoted. It also sees what the kernel and the network recorded, not only what the agent reports.
- A flag leads to a hold and a person, never to automatic resampling.
- No judge is good enough to be the only control. The deterministic layers stay first.

### 2.2 Guard models that can run on our own hardware

None of these is trained to recognise sabotage by a coding agent. They recognise prompt injection, jailbreaks and content harm, and two of them judge against a policy you write.

| Model | Released | What it judges | Size | Licence |
| --- | --- | --- | --- | --- |
| Llama Prompt Guard 2 | Apr 2025 | Injection and jailbreak in a 512-token window | 22M (about 19 ms), 86M | Llama 4 Community |
| Granite Guardian 4.1 | Apr 2026 | Harm, jailbreak, function-call hallucination, criteria you write | 8B | Apache-2.0 ([model card](https://huggingface.co/ibm-granite/granite-guardian-4.1-8b)) |
| gpt-oss-safeguard | Oct 2025 | Any policy given at inference; returns a label and its reasoning | 20B (3.6B active), 120B | Apache-2.0 ([report](https://openai.com/index/gpt-oss-safeguard-technical-report)) |
| Qwen3Guard Gen and Stream | Sep 2025 | Nine harm categories; the Stream variant scores token by token | 0.6B to 8B | Apache-2.0 |
| Llama Guard 4 | Apr 2025 | Fourteen hazard categories | 12B | Llama 4 Community |

Protect AI's LLM Guard was archived on 2026-07-09 ([repository](https://github.com/protectai/llm-guard)), so it is not an option.

### 2.3 Inline enforcement points

- **Harness hooks can refuse a tool call before it runs.**
  - Claude Code: a `PreToolUse` hook can be an HTTP call. With `onFailure: block`, a timeout blocks the call ([docs](https://code.claude.com/docs/en/hooks)).
  - Codex: a hook error or timeout lets the call through ([docs](https://learn.chatgpt.com/docs/hooks)), so a wrapping command must fail closed itself.
  - OpenHands: uses the Claude Code hook format ([docs](https://docs.openhands.dev/sdk/guides/hooks)).
- **The model gateway.**
  - LiteLLM's guardrails run before the call, alongside it, after it, or only log ([docs](https://docs.litellm.ai/docs/proxy/guardrails/quick_start)). A guardrail after a streamed response can only audit.
  - Its Tool Permission guardrail allows or denies tool names and argument paths by regular expression ([docs](https://docs.litellm.ai/docs/proxy/guardrails/tool_permission)).
  - MCP guardrails run before and after each tool call ([docs](https://docs.litellm.ai/docs/mcp_guardrail)).
  - Assigning a guardrail per key or per team is an Enterprise feature. In the open-source edition, the Run's key metadata has to be read by a custom guardrail instead.
- **MCP.** The 2026-07-28 specification puts `Mcp-Method` and `Mcp-Name` in request headers ([changelog](https://modelcontextprotocol.io/specification/2026-07-28/changelog)). A proxy can then allow or deny a tool call without parsing the body.
- **The kernel.** Tetragon 1.7 enforces synchronously ([docs](https://tetragon.io/docs/concepts/enforcement/)). A kill alone can let an in-flight write land, so it is paired with Override, which fails the system call. The Talos kernel lacks `BPF_KPROBE_OVERRIDE` but has `BPF_LSM`, so on Talos, enforcement uses LSM hooks.

### 2.4 Holding a Run quickly

Kubernetes has no pod pause, and no enhancement proposal for one was found. These options exist, fastest first:

1. **Block the Run's model key.** The agent stalls at its next model call. This works for every harness and keeps the pod and session for a person.
2. **Cut its egress.** Label the pod for a Cilium policy that denies all egress. Connections already open may live until their connection-tracking entry expires.
3. **Kill in the kernel.** A Tetragon policy kills it. That stops the Run outright; it does not pause it.

Kata's Go runtime can freeze the guest's processes. Whether the Rust runtime, the default in Kata 4, can do the same is unverified.

### 2.5 Event transport

| Option | Fit |
| --- | --- |
| NATS JetStream 2.15 ([releases](https://github.com/nats-io/nats-server/releases)) | **Chosen.** CNCF incubating, one Go binary. Gives a subject per Run, deduplication by message id, and durable pull consumers |
| VictoriaLogs live tail | No acknowledgement and no replay cursor; it holds results back about five seconds. Fine for dashboards, not as a judge's queue |
| Kafka | Too heavy for a cell |
| Redpanda | Business Source License |

The OpenTelemetry GenAI conventions (`invoke_agent`, `execute_tool`) are all still at Development status, and tool arguments are opt-in ([repository](https://github.com/open-telemetry/semantic-conventions-genai)). Ploeg's own transcript entries (ADR-0090) are the stable event to judge. OpenTelemetry is a second source.

## 3. Proposal: five layers of oversight

Each layer has its own failure mode, so no layer relies on another having worked.

```
          milliseconds              seconds                    minutes to hours        days
 Run ─► [1 Guard: refuse] ─► [2 Watch: flag and hold] ─► [3 Audit: review] ─► [4 Person] ─► thresholds
          deterministic       two-stage judge models       strong cross-family     decides      tuned
          fails closed        on every action              model, sampled
```

### Layer 0: prevention

This layer already exists or is decided:

- the isolation class (ADR-0106)
- no credential in the harness (ADR-0138)
- default-deny egress (ADR-0137)
- per-Run forge tokens (ADR-0013, ADR-0016)
- budgets (ADR-0012)

It decides what a Run *can* do. The layers below decide what to do about what it *tries*.

### Layer 1: Guard (inline, deterministic, fails closed)

| Point | Rule | On violation |
| --- | --- | --- |
| Harness `PreToolUse` | A Ploeg policy endpoint on the worker evaluates every tool call. It runs CEL over the tool name and arguments, set per Team and Role. It blocks force-pushes, edits to CI and workflow files when the Work Item does not name them, reads of credential paths, and network tools pointed at hosts outside the allowlist | The tool call is refused with a reason the agent sees; a `guard.refused` event is recorded |
| Model gateway | Tool Permission rules; Prompt Guard 2 (22M) scores tool results and fetched pages for injection before they reach the model; outbound secret patterns | The request is refused or the tool result is masked; recorded |
| Kernel (Tetragon, Run pods only) | Kill and Override on reads of service-account tokens and SSH keys; `setns` and `unshare`; module and BPF loads; execution from `/tmp`; connections outside the allowlist | The process dies and the system call fails; recorded with pod labels |
| Network (Cilium) | FQDN allowlist per Run | The packet is dropped; Hubble records it |

A harness that cannot call a hook is not refused. It runs with the kernel and network guards only, and its Run records `guard: partial`, so its posture is visible.

### Layer 2: Watch (seconds, two judge stages, can hold a Run)

**Events.** Every source publishes to NATS subject `ploeg.run.<run>.<kind>`, deduplicated on `<run>:<source>:<seq>`. A Run cannot forge or suppress the server-side and kernel sources:

| Source | What it publishes |
| --- | --- |
| Worker | Transcript entries and guard results |
| Model gateway | Request metadata, tool calls, spend |
| Tetragon | Exec, file, connect and kill events for the Run's pod |
| Hubble | Flows, allowed and dropped |
| Forge webhooks | Pushes, branches, workflow runs |

**Stage 1, every action.** An open-weight policy model scores each action against a written Ploeg policy: gpt-oss-safeguard-20b, or Granite Guardian 4.1 in its fast mode. Deterministic correlation rules run beside it and need no model. For example:

- the agent says it ran the tests, but Tetragon saw no test binary run
- a connection went out to a host no tool call named
- a file was written that the agent's plan never mentioned

**Stage 2, flagged actions only.** A frontier model from a different family than the Run's agent reviews the flagged action. It sees the action, the steps before it, and the kernel and network evidence, in a fixed structure with agent text quoted (ADR-0122). It has no tools. It answers a fixed schema: `severity`, `category`, `evidence[]`, `confidence`.

**Actions.**

| Severity | What happens |
| --- | --- |
| `info` | A note on the Run |
| `flag` | The note appears in Unfold's review queue |
| `hold` | The Run's model key is blocked and the pod is labelled for egress quarantine. The Work Item moves to `needs_human` with close reason `held_by_watch`. The pod and session are kept for the pause bound (ADR-0101) for a person to inspect |
| `kill` | Reserved for Tetragon's in-kernel rules, never for a model |

A hold is never followed by an automatic retry or by sampling the action again.

**Rollout.** The Watch starts in shadow mode (ADR-0096). It records what it would have done and holds nothing. It may hold Runs only after its false-positive rate on a month of honest Runs is known and accepted by a person.

**Its own failure mode.** If the Watch is down or behind, Runs continue. The lag is a metric and an alert. The guards still enforce, and the Audit covers what the Watch missed. A Team may set `watch: required`; its Runs then wait until the Watch is healthy.

**Cost.** Stage 1 runs on our own hardware or a cheap gateway model. Stage 2 runs only on flags. Both are paid from the Run's reservation as a reading cost (ADR-0012), so a Team sees what oversight costs.

### Layer 3: Audit (minutes to hours, sampled, strongest model)

Runs selected:

- every Run the Watch flagged or held
- every Run that touched a sensitive path: CI, infrastructure, authentication, dependency manifests
- a random 5% to 10% of the rest
- a higher rate for a new harness, model or Team

The auditor is a reading Run of a strong model from a different family. It runs as a coding agent with read-only access to the repository, which beat prompted monitors in SLEIGHT-Bench. It reviews:

- the whole trajectory
- the diff against the Work Item's acceptance conditions
- new dependencies
- network destinations against the Team's baseline

Per-Team baselines are VictoriaMetrics recording rules: binaries run, destinations, files touched, tokens per Run. A finding is a note with provenance on the Work Item. If it comes before merge, it is a review finding (ADR-0121).

**Measuring the Audit.** A red-team Team plants sabotage in test Work Items on a schedule, as the attack drill does for injection (ADR-0126). The share caught by Guard, Watch and Audit is the published catch rate. Without it, nobody knows whether the layers work.

### Layer 4: a person

Every hold and every Audit finding reaches Unfold with its evidence. The person's verdict (real, benign, unclear) is stored and tunes the Watch thresholds. Ploeg never closes a hold by itself.

## 4. What it costs a cell

| Component | Where | Notes |
| --- | --- | --- |
| NATS JetStream | Hub | Three replicas when the hub is HA; one before that |
| Watch consumers | Hub | Stateless. The cursor is stored with the verdict, keyed by `(run, seq)` |
| Stage 1 model | Hub, via the model gateway | Open weights on our own GPU later; a hosted open-weight model until then |
| Tetragon | Every node that runs Runs | Policies scoped to Run pods; enforcement through LSM hooks on Talos |
| Hubble export | Every cell | Allowed and dropped flows of Run pods |

## 5. Execution backends

### 5.1 What exists in October 2026

| Backend | Boundary | Needs `/dev/kvm` | Start | Per-Run overhead | Scales on | State |
| --- | --- | --- | --- | --- | --- | --- |
| runc and user namespaces | Shared kernel | No | Under 1 s | None | Any cloud VM | GA |
| gVisor, systrap ([platforms](https://gvisor.dev/docs/architecture_guide/platforms/)) | User-space kernel | No | About 1 s (unverified) | Tens of MiB (unverified) | Any cloud VM | GA; Talos core extension 20261005.0 |
| Kata with Cloud Hypervisor | Micro-VM | Yes | 1 to 2 s | About 130 MiB plus the guest | Bare metal, or VMs with nested virtualization | Kata 4.3.1 upstream; Talos extension at 3.32 |
| Kata with QEMU (SEV-SNP, TDX) | Confidential micro-VM | Yes, plus SNP or TDX hardware | 2 s or more | About 160 MiB plus the guest | Own EPYC hardware, GCP, Azure | Confidential Containers v0.23; vendors ship it as a technology preview |
| KubeVirt | Full VM | Yes | Tens of seconds to minutes | A full VM | As Kata | GA |
| A cloud VM per Run | The provider's hypervisor | No | 20 to 30 s | A full VM; Hetzner bills whole hours | Any cloud API | Build it ourselves |
| Peer pods ([providers](https://github.com/confidential-containers/cloud-api-adaptor/tree/main/src/cloud-providers)) | A cloud VM behind a pod | No | Tens of seconds | A full VM | AWS, Azure, GCP, IBM, Alibaba, libvirt; not Hetzner | Beta |
| Edera ([architecture](https://docs.edera.dev/reference/technical-overviews/architecture)) | Xen guest | No | Unverified | Unverified | Any VM | Commercial core |
| Firecracker platforms (E2B runtime, microsandbox) | Micro-VM | Yes | About 0.3 s (vendor claim) | Small | Nomad or a single host | Not Kubernetes-native, or pre-1.0 |

Constraints that shape the design:

- **Hetzner Cloud offers no nested virtualization** ([FAQ](https://docs.hetzner.com/cloud/servers/faq/)). Kata there needs dedicated servers.
- **Hetzner dedicated servers are billed monthly with a setup fee.** The Robot API can order them, but nothing autoscales them.
- **Hourly bare metal exists in the EU.** Scaleway Elastic Metal bills from €0.077 an hour, is delivered in minutes, and has an API and Terraform ([product page](https://www.scaleway.com/en/elastic-metal/)).
- **cluster-autoscaler's Hetzner provider scales from zero per server type**, with the Talos worker config passed as cloud-init ([README](https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/cloudprovider/hetzner/README.md)).
- **A Karpenter provider for Hetzner Cloud exists**, validated against Talos, but it is small and young.
- **Sidero no longer develops its Cluster API providers for Talos** ([repository](https://github.com/siderolabs/cluster-api-bootstrap-provider-talos)), so Cluster API is not a path for Talos cells.
- **kubernetes-sigs/agent-sandbox v1.0.8** has warm pools (`SandboxWarmPool`) and runs any RuntimeClass, tested daily with gVisor ([releases](https://github.com/kubernetes-sigs/agent-sandbox/releases)).
- **Docker inside gVisor works** with flags that let sandboxed code craft raw packets ([guide](https://github.com/google/gvisor/blob/master/g3doc/user_guide/tutorials/docker-in-gvisor.md)). `kata-qemu` exposes virtualization to its guest.

### 5.2 Proposal: one class, several backends, each a kind of cell

ADR-0106 classes say how strong the boundary is. A backend says how it is built. A cell advertises its class *and* its backend. A Team may name acceptable backends for its floor, and by default accepts any backend at or above it.

| Class | Backends Ploeg supports | How it scales |
| --- | --- | --- |
| `standard` | runc and user namespaces | Cloud VMs through cluster-autoscaler, from zero |
| `hardened` | gVisor | Cloud VMs through cluster-autoscaler, from zero, because gVisor needs no `/dev/kvm`. This is the elastic class on any cloud |
| `isolated` | Kata with Cloud Hypervisor on bare metal | A base pool sized by a person from the demand signal (ADR-0072), plus burst cells of hourly bare metal created and destroyed by OpenTofu |
| `isolated` | A cloud VM per Run (`vm` backend) | No nodes to scale: the cell's launcher creates a VM per claimed Run through the provider API, runs the worker there, and deletes the VM when the Run ends. Suited to long Runs; whole-hour billing makes short Runs expensive |
| `isolated` | Kata with Cloud Hypervisor on cloud VMs with nested virtualization | Only on providers that officially expose `/dev/kvm` |
| `confidential` | Kata with QEMU and SEV-SNP or TDX | Own EPYC hardware, or a confidential-VM cloud as its own cell. Not available on Hetzner |

What the cell layer adds:

1. **Warm pools.** A cell keeps a configured number of pre-started Run pods per backend, so a Kata or gVisor Run starts warm. agent-sandbox's `SandboxWarmPool` is the candidate mechanism; a spike decides whether it replaces the launcher path of ADR-0072 or sits under it.
2. **Demand per backend.** The demand signal of ADR-0072 gains a backend label. Cloud pools scale from it. Bare-metal pools alert a person when `isolated` Runs have waited longer than a set time for a week.
3. **Burst cells.** An hourly bare-metal cell joins the hub as any cell does (ADR-0107). Nothing about it is special to Ploeg.
4. **An inner sandbox.** Inside any backend, the harness may run its own bubblewrap or Landlock sandbox: Claude Code's `sandbox-runtime` ([docs](https://code.claude.com/docs/en/sandboxing)), or Codex's. It is a further layer, never a replacement for the class.
5. **Rejected as backends.** WASM, because it cannot host git, compilers or package managers. Firecracker platforms that are not Kubernetes-native, because they fit beside Ploeg rather than inside a cell; revisit when one runs under a RuntimeClass.

### 5.3 Docker builds inside a Run

`dind: true` stays `legacy` (ADR-0136). A Run that must build images does so under:

- `isolated`, whose `kata-qemu` guest can run its own daemon inside the micro-VM; or
- `hardened`, with gVisor's Docker support, whose raw-packet flag is recorded as a written exception (ADR-0106's `maxIsolationClass` reason).

Rootless BuildKit as a sidecar is unverified in either runtime.

## 6. Open questions

1. Is the Kata extension's lag on Talos (3.32 against upstream 4.3.1) acceptable for production, or should a cell carry its own Kata build?
2. Does the Watch's stage 1 run on our own GPU, or on a hosted open-weight model through the gateway until there is a GPU?
3. Should the `vm` backend be built at all before a Team asks for it, given Kata on bare metal covers `isolated`?
4. Which harnesses Ploeg supports cannot call a pre-tool hook today, and what replaces it for them?

## 7. Decision records this proposes

| Proposed ADR | Decides |
| --- | --- |
| A tool call passes a Team's guard policy before it runs, and a harness without a hook is recorded as partially guarded | Layer 1 |
| Every source of a Run's actions publishes to one event stream per Run that the Run cannot write to | The event model and NATS |
| A Watch judges every action in two stages from a different model family, and a hold parks the Work Item for a person | Layer 2, shadow first |
| A sample of Runs and every flagged Run is audited by a strong cross-family model, and a red-team Team measures the catch rate | Layer 3 |
| A cell advertises its backend beside its class, and a Team may name the backends it accepts | Section 5.2 |
| A cell keeps warm Run pods per backend | Warm pools |
| A `vm` backend runs each Run in its own cloud VM | Only if question 3 says yes |
