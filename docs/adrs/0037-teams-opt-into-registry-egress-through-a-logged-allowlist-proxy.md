---
status: accepted
date: 2026-10-01
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2026-12-31
---

# Teams opt into registry egress through a logged allowlist proxy; airgapped stays the default

## Context and Problem Statement

[0035](0035-runs-get-ploeg-owned-skills-mounted-toolchains-and-worker-verification.md)
mounts language toolchains in a Run, but the Run cannot download
dependencies. A worker pod reaches DNS, the pods in `ploeg`, LiteLLM, in-cluster
Forgejo and the Vikunja API, and nothing else (homelab-cluster `ab75cc35`,
`92b15206`, `747890d0`; checked by
`kubernetes/apps/ploeg/ploeg/app/worker-egress-probe.job.yaml`). So `go vet`,
`go test`, `npm ci` and `tofu init` fail in the sandbox, and the worker's own
verification reports them as failed or leaves them to CI.

On 2026-09-28 the owner decided that a team can opt into a second network
profile, `registries`, which reaches package registries only, through a logged
allowlist egress proxy. The alternative, in-cluster pull-through caches, was
declined. `airgapped` stays available and stays the default.

This record answers where the profile is declared, what enforces it, how the
tools are pointed at it, what gets logged, what the allowlist is, and what a
prompt-injected agent can still do with it.

## Decision Drivers

* **Prompt injection is expected**
  ([0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md)).
  The profile must hold when the harness does what an attacker tells it to,
  including running a package's install script.
* **Default closed.** A team that says nothing gets `airgapped`, and a mistake
  in the profile machinery must fail closed.
* **Audit names the package, not only the host.** "Run 144 fetched
  `github.com/stretchr/testify@v1.10.0`" answers an incident question;
  "Run 144 talked to a Google IP" does not.
* **The registries are allowed; everything that shares their IP addresses is
  not.** npm sits behind Cloudflare, PyPI behind Fastly, and the Go proxy
  behind Google's front ends, so an allowlist of IP addresses is an allowlist
  of those CDNs' tenants.
* **Read only.** A registry profile fetches. It never publishes, logs in or
  pushes.
* **Chart and cluster stay separable.** The chart names the profile; the
  cluster that installs it owns enforcement, as for the other egress rules.

## Considered Options

* An explicit forward proxy that terminates TLS for allowlisted hosts only,
  with Cilium FQDN policy as the backstop on the proxy itself
* Cilium `toFQDNs` on the worker pods, transparent, with Hubble for audit
* An explicit forward proxy that tunnels `CONNECT` without terminating TLS
* In-cluster pull-through caches (declined by the owner on 2026-09-28)

## Decision Outcome

Chosen option: "An explicit forward proxy that terminates TLS for allowlisted
hosts only, with Cilium FQDN policy as the backstop on the proxy itself",
because it is the only option that enforces the allowlist per request rather
than per IP address, restricts methods and paths, logs the package that was
fetched, and lets a registries worker run with no external DNS at all.

### Where the profile lives

* **Chart (Unfold).** `executor.network.profile` is the global default,
  `executor.teams[].network.profile` overrides it, and
  `plan[].roles[].network.profile` overrides the team, in the same role, team,
  global chain as `harness:`. The schema accepts `airgapped` and `registries`
  only, and the chart default is `airgapped`.
* The chart renders the label `ploeg.webgrip.dev/network-profile` on **every**
  worker pod, `airgapped` included, so a cluster policy can select either
  profile explicitly and a probe can impersonate either.
* `executor.network.registries` carries `proxyUrl` and `caBundle`
  (`configMap`, `key`). Rendering fails when any team or Role resolves to
  `registries` and `proxyUrl` is empty.
* **Cluster (homelab-cluster).** The proxy, its allowlist, its CA and the
  policies that select the label live where the rest of the ploeg egress
  policy lives. The chart renders no NetworkPolicy for the `keda` and `cronjob`
  executors. Under the `sandbox` executor with
  `executor.sandbox.networkPolicy: {}` the same cluster policies select the
  same label.

### Enforcement

* **Worker pods get no new route to the internet.** A cluster policy lets a pod
  labelled `registries` reach the proxy's port and nothing else new. A pod
  labelled `airgapped`, or with no label, matches nothing new. Default deny
  does the rest.
* **The proxy** is Squid in its own namespace. It terminates TLS for the
  allowlisted hosts with a CA whose X.509 name constraints permit only those
  hosts, so a leaked CA key cannot mint a certificate for any other name. It
  allows `GET` and `HEAD` only, applies the per-host path rules below, strips
  `Authorization` and `Cookie` from every request, refuses every other host
  and every `CONNECT` it does not terminate, caches nothing, and denies its
  cache manager.
* **The backstop** is a CiliumNetworkPolicy on the proxy pod: ingress only
  from `ploeg` pods labelled `registries`, egress only to DNS and `toFQDNs` for
  the allowlisted names on port 443. If Squid is misconfigured it still cannot
  reach the LAN, the cluster or the public gateway.

### Pointing the tools at it

The chart sets, for a `registries` worker:

| Variable | Value |
| --- | --- |
| `HTTPS_PROXY`, `HTTP_PROXY` | `executor.network.registries.proxyUrl` |
| `NO_PROXY` | `localhost,127.0.0.1,::1,.svc,.cluster.local` plus the hosts of the Ploeg API, LiteLLM and the forge URLs |
| `GOPROXY` | `https://proxy.golang.org` (no `direct`) |
| `GOSUMDB` | `sum.golang.org` |
| `npm_config_ignore_scripts` | `true` |
| `PIP_ONLY_BINARY` | `:all:` (wheels only; no source build runs `setup.py`) |
| `SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`, `REQUESTS_CA_BUNDLE`, `PIP_CERT` | the mounted `caBundle` (public roots plus the proxy CA) |

`NO_PROXY` is load-bearing: `ploeg-worker` uses Go's `ProxyFromEnvironment`,
so without it the worker's own calls to ploegd, LiteLLM and Forgejo would go to
the proxy and be refused. The loopback proxies of 0034 are never proxied.

For an `airgapped` worker the chart sets `GOPROXY=off`,
`npm_config_offline=true` and `PIP_NO_INDEX=1`, so a tool fails at once with a
clear message instead of waiting out a connect timeout.

The worker passes these variables to the harness and to its own
post-Run verification, so a `registries` team's verification can run
`go test ./...`.

### Logging and audit

* Squid writes one JSON line per request to stdout: time, client pod IP,
  method, full URL, status, bytes and the Proxy-Authorization user. The
  cluster's log pipeline ships it to VictoriaLogs.
* `ploeg-worker` logs its Run id and pod IP when it starts, and sets the
  userinfo of `HTTPS_PROXY` to `run-<id>`. The pod IP is the authoritative
  join key, because Cilium does not let a pod send from another pod's address;
  the user name is a convenience the harness could forge.
* A denied request from a `registries` pod raises an alert. A legitimate build
  has no reason to leave the allowlist, so a denial is either a missing entry
  or an attempt.

### Initial allowlist

All hosts: port 443, `GET` and `HEAD` only, except `POST` to
`git-upload-pack` for the git clone allowlist below.

| Ecosystem | Host | Paths |
| --- | --- | --- |
| Go | `proxy.golang.org` | module paths under the module-host allowlist below |
| Go | `sum.golang.org` | `/lookup/` under the module-host allowlist, `/tile/`, `/latest` |
| npm | `registry.npmjs.org` | any path except those starting with `/-/` (search, login, tokens, whoami) |
| PyPI | `pypi.org` | `/simple/`, `/pypi/` |
| PyPI | `files.pythonhosted.org` | `/packages/` |
| OpenTofu | `registry.opentofu.org` | `/.well-known/terraform.json`, `/v1/providers/` |
| Terraform | `registry.terraform.io` | `/.well-known/terraform.json`, `/v1/providers/` |
| Terraform | `releases.hashicorp.com` | `/terraform-provider-` |
| GitHub releases | `github.com` | `/<owner>/<repo>/releases/download/` only |
| GitHub releases | `release-assets.githubusercontent.com`, `objects.githubusercontent.com` | any (signed, read-only redirect targets) |
| git clone | `github.com` | for each `<org>/<repo>` on the clone allowlist: `GET /<org>/<repo>[.git]/info/refs?service=git-upload-pack` and `POST /<org>/<repo>[.git]/git-upload-pack` |

With `GOPROXY=https://proxy.golang.org` and no `direct`, every module and
checksum fetch goes to `proxy.golang.org` and `sum.golang.org`, which fetch
from the module's origin themselves. Those two are therefore the only Go
**egress hosts**, whatever vanity domains the modules use. A vanity host would
become an egress host only for a `GOPRIVATE` or `direct` fetch, which this
profile does not allow.

The **module-host allowlist** is a separate thing: a **path** filter on
requests to those two hosts. It admits a module whose path starts with a host
the target repositories' `go.sum` files already use (for Unfold's
`apps/ploeg/go.sum` today: `github.com`, `golang.org`, `gopkg.in` and
`go.uber.org`), and it lives in the cluster's proxy configuration. A public
vanity path such as `go.uber.org/...` still needs an entry here, because it is
the origin host `proxy.golang.org` would contact on the Run's behalf. The
reason is under *Secrets exposure* below.

The **git clone allowlist** is an explicit list of `<org>/<repo>` (or
`<org>/*`) entries in the cluster's proxy configuration, empty at first. An
entry is added when a target repository needs a `git::` module source or a
Go module that must be cloned.

### What stays blocked

* `api.github.com`, `codeload.github.com`, `raw.githubusercontent.com`,
  `gist.github.com`, `uploads.github.com`, and every `github.com` path outside
  release downloads and allowlisted `git-upload-pack` clones.
* `git-receive-pack` on every host and repository, always: no push.
* `upload.pypi.org`; every npm write: `PUT`, `POST` and `DELETE` on any path,
  and `/-/v1/login`, `/-/npm/v1/tokens`, `/-/whoami`.
* Git clones of any repository not on the clone allowlist, and Go `direct`.
* Any other host, the LAN, cluster services not already allowed, and the
  public and internal gateways, both through the proxy and directly.
* Any direct connection from a worker pod to the internet, in either profile.

### Secrets exposure

What a prompt-injected Run holds: the target repository's working tree, the
Work Item and the placeholders of 0034. The model key and the forge token stay
in `ploeg-worker`. The repository itself is the asset worth stealing.

| Channel | Profile | Mitigation | Residual |
| --- | --- | --- | --- |
| Publish to a registry with an attacker's token (`npm publish`, `twine upload`) | registries | `GET`/`HEAD` only; `Authorization` stripped; `upload.pypi.org` not listed | none known |
| Push to an attacker's GitHub repository | registries | `git-receive-pack` always refused; clones only from allowlisted repositories; no `api.github.com` | none known |
| Go proxy origin fetch: `proxy.golang.org` fetches an unknown module from its origin host, so `attacker.example/<data>` hands the data to the attacker's web server | registries | module-host allowlist; a module path on an unlisted host is refused | a module on a listed host whose owner can read request logs (none of the listed hosts expose them) |
| Public download counters (npm, PyPI, GitHub release assets) | registries | full-URL audit log, denial alert | a few bits per day; accepted |
| Domain fronting: allowed SNI, different `Host` | registries | the proxy terminates TLS and routes by its own allowlist, so it never forwards a foreign `Host` | none known |
| Install scripts (`postinstall`, `setup.py`) run attacker code in the pod | registries | `npm_config_ignore_scripts=true`; the code is still bound by the proxy | `PIP_ONLY_BINARY=:all:` stops `setup.py` builds; a package with no wheel fails to install and shows as a failed check |
| DNS tunnelling through CoreDNS to an attacker's name server | **both, today** | a DNS rule on worker pods that resolves cluster names only; a `registries` pod needs no external name because the proxy resolves | none once the rule lands |

The last row is a hole in `airgapped` as deployed, not a new one: the
namespace's generated `allow-dns` lets any worker pod resolve any public name,
and CoreDNS forwards it upstream. Closing it is part of this decision's
implementation.

### Consequences

* Good, because a `registries` team's Runs and the worker's own verification
  can run `go vet`, `go test`, `npm ci`, `pip install` and `tofu init`, which
  resolves the first Bad consequence of 0035.
* Good, because every fetch is attributable to a Run and a package, and every
  refusal is visible.
* Good, because `airgapped` teams gain nothing and lose the DNS channel.
* Bad, because a TLS-terminating proxy is one more component on the Run's
  path: it holds a CA key, and every toolchain must trust its bundle. The name
  constraints bound the key; the chart owns the trust variables.
* Bad, because the Go module-host allowlist needs an entry for every new vanity
  host a target repository adopts. It fails closed, with a denial in the log
  that names the host.
* Bad, because a single proxy replica is a single point of failure for
  `registries` Runs. It fails closed, and `airgapped` Runs do not depend on it.
* Neutral, because terminating TLS for every allowlisted host is heavier than
  terminating only the writable ones (npm, GitHub). Doing it everywhere buys
  one uniform rule set and a full-URL log for every ecosystem.

### Confirmation

* Chart golden files in `apps/ploeg/ops/helm/ploeg/ci/golden/` pin, for a
  default install, the `network-profile: airgapped` label, the fail-fast
  variables and no proxy variables; and for a `registries` team with an
  `airgapped` Role, both labels, the proxy variables, `NO_PROXY` and the CA
  mount. A `reject-*` values file proves that `registries` without `proxyUrl`
  fails to render, and the schema rejects any other profile name.
* A `pkg/worker` test proves that the harness and the worker's verification
  receive the proxy variables and that the worker's calls to ploegd, LiteLLM
  and the forge bypass the proxy.
* In homelab-cluster, the worker egress probe runs once per profile as a
  Flux-reconciled Job. The `airgapped` probe must fail to reach the proxy, a
  registry directly and an external DNS name. The `registries` probe must
  succeed on one fetch per ecosystem through the proxy, and must be refused on
  `api.github.com`, a `github.com` non-release path, an npm `PUT`,
  `upload.pypi.org`, a Go module on an unlisted host, `example.com`, the
  gateways and the LAN, and on a registry reached directly. The pass cases
  prove the probe can tell a working gate from a broken one.

### Owner decisions

2026-09-28, on review of this record. The owner accepted the record with these
decisions on 2026-10-01.

1. TLS is terminated for **every** allowlisted host, not only the writable
   ones, for one read-only rule set and a full-URL log in every ecosystem.
2. The first team is silver: its builder Role gets `registries`, and its
   reviewer Role stays `airgapped`.
3. `pip` installs wheels only (`PIP_ONLY_BINARY=:all:`).
4. Git is allowed as read-only clones (`git-upload-pack`) from an explicit
   allowlist of organisations or repositories; `git-receive-pack` is always
   refused.
5. The Go module-host allowlist stays. The Go egress hosts are only
   `proxy.golang.org` and `sum.golang.org` (see *Initial allowlist*); vanity
   domains would be egress hosts only under `GOPRIVATE` or `direct`. They stay
   in the module-host path filter, because a public vanity path is still an
   origin that `proxy.golang.org` contacts for the Run.

## Pros and Cons of the Options

### Cilium `toFQDNs` on the worker pods

* Good, because it is transparent: no proxy variables, no CA, and the
  cluster already uses it (`tei-embeddings-model-fetch` in namespace `ai`).
* Bad, because it allows the IP addresses a name resolved to, and those
  addresses are shared CDN front ends. A pod that resolves
  `registry.npmjs.org` can open TLS to the same Cloudflare address with any
  other tenant's name.
* Bad, because it cannot see inside TLS, so it cannot refuse `npm publish` or
  `git push` to an allowed host. Cilium's TLS interception would, but it is
  beta and this cluster runs Cilium 1.19.8 with the standalone Envoy disabled.
* Bad, because the audit trail is DNS names and flows, and Hubble here is
  metrics only: no relay and no flow export.
* Bad, because the worker pods need external DNS, which keeps the DNS
  tunnelling channel open.

### An explicit forward proxy that tunnels `CONNECT` without terminating TLS

* Good, because it needs no CA and is the simplest proxy to run.
* Good, because the proxy dials the allowlisted name itself, which closes the
  CDN address problem, and a peek at the TLS SNI can refuse a mismatch.
* Bad, because methods and paths are invisible, so `npm publish` and
  `git push` to `github.com` with an attacker's token pass, and the Go origin
  fetch channel cannot be narrowed.
* Bad, because the log names hosts, not packages.

### In-cluster pull-through caches

* Good, because fetches are served locally and survive an upstream outage.
* Bad, because one cache per ecosystem is several stateful services to run,
  and they share state across teams. The owner declined it on 2026-09-28.

## Re-evaluation triggers

* The first `registries` Run's verification passes `go test ./...` in the
  sandbox, or a denial shows a missing allowlist entry that a build needed.
* A denial alert fires for a request no build explains.
* Cilium's TLS interception reaches general availability in the deployed
  Cilium version.
* A target repository needs a private module source (a Forgejo-hosted Go
  module or npm package).
* The owner reopens pull-through caches, for example because upstream outages
  fail Runs.

## More Information

* 2026-09-28 — Proposed after the owner's decision of the same day. Evidence:
  homelab-cluster `kubernetes/apps/ploeg/ploeg/app/networkpolicy.yaml` and
  `worker-egress-probe.job.yaml` at `8e9e58ba`; Cilium 1.19.8 with
  `envoy.enabled: false` and metrics-only Hubble in
  `kubernetes/apps/kube-system/cilium/app/helmrelease.yaml`; the existing
  `toFQDNs` precedent in `kubernetes/apps/ai/tei-embeddings/app/networkpolicy.yaml`;
  cert-manager and trust-manager are installed.
* 2026-10-01 — Accepted by the owner with the decisions under *Owner decisions*.
* Implementation: VIK-1332 (chart profile) and VIK-1333 (Run attribution) in
  Unfold; VIK-1334 (proxy and policies), VIK-1336 (DNS lockdown), VIK-1335
  (per-profile probes) and VIK-1337 (first team) in homelab-cluster; all under
  epic VIK-1276.
* Related: [0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md),
  [0035](0035-runs-get-ploeg-owned-skills-mounted-toolchains-and-worker-verification.md).
