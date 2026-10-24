# Self-hosting security contract

This page describes this source revision, not an installation's running state.
Each release packages it as `SECURITY.md` together with
`security-contract.json`. Keep both with the chart digest and values.
Contract revision 1 starts with this change and does not retroactively qualify
older releases. Development docs are not evidence about an older chart.

## Ownership and chart settings

Ploeg owns work admission, Run/Shift authority, budgets, scoped credentials,
worker outcomes and recovery. The chart owns worker pod shapes and optional
admission/network controls. The estate owns cluster components; the consumer
owns human login and object authorization.

```yaml
security:
  workerAdmission:
    enabled: true
    runtimeClassNames: [kata]
networkPolicy:
  enabled: true
```

Merge this fragment into a fully configured installation. Native admission is
opt-in; the chart already enables its baseline NetworkPolicies and strict
credential-isolation profile by default. This fragment is not a complete
deployment or proof of enforcement.

Admission covers every Pod labelled `ploeg-worker` in the release namespace,
including sandbox worker Pods and init/ephemeral containers. Dedicate the
namespace to one Ploeg installation; admission is not separated by release label.
It refuses host namespaces, shared process namespaces, hostPath, automatic API
tokens, root/privileged containers, escalation, added capabilities and unconfined
seccomp. It does not make the root filesystem read-only or restrict arbitrary
shell commands. Privileged DinD is incompatible and fails Helm rendering when
admission is enabled. A nonempty RuntimeClass allowlist requires a selected name
but does not install its handler. Apply it only with executor/runtime settings
that actually produce that RuntimeClass.

The policy does not cover ploegd, sandbox launchers, the separate Claude runner
or client-carried sessions. Secure these independently. A launcher needs limited
SandboxClaim API authority; an agent worker must not inherit its token.

The existing `networkPolicy` settings cover the controller, workers, sandbox
launcher, MCP server and Claude runner. Configure the actual database, forge,
inference, API and ingress peers through those settings; see the
[network guide](network.md) for endpoint derivation, private-CIDR exclusions,
public-HTTPS exceptions and service/target-port limitations. Extra policies add
allows; they cannot subtract the baseline's allowances. Review all policies
that select a pod. Admission is separately namespace-scoped by the worker label.

Standard NetworkPolicy cannot filter DNS names or HTTPS paths. Use a tested
CNI-specific rule or proxy for those controls. Reaching the forge host does not
authorize every repository: the credential broker must narrow that separately.
Managed credentials and proxies also depend on the selected harness; confirm
the isolation mode from the rendered workload and worker's startup record.

## Identity is an application boundary

**End-to-end delegated user access tokens and automatic personal forge ACL
inheritance are not implemented.** Ploeg authenticates the consumer and trusts its
asserted person. A recorded person is not independent proof of that person's
authority. Worker capabilities identify a Run, team, role and worker; they are
not human OIDC credentials.

The required next integration is verified user-and-actor delegation, audience
checking, and a grant bound to the Run, repository/branch, permitted actions and
expiry. Effective permission must fit the person, team policy and role. Re-check
authorization at renewal and consequential actions, and test stale/revoked
grants. Keep browser ID tokens, refresh tokens and broad upstream credentials out
of the sandbox. Authentik documents OBO from 2026.8.0; a chart cannot add it to an
older IdP or teach an unchanged application to validate it. Forge APIs still need
provider-accepted credentials. Check OAuth scope support at the actual forge
version and mediate narrow repository/action grants at a trusted gateway when
the upstream token cannot express them.

This is an application/provider implementation requirement, not an impossible
deployment task or an available Helm toggle. An installation requiring this
guarantee must not enable execution until the integration and refusal tests
exist. Tracker-triggered autonomous work needs an explicit organizational grant
and a recorded source/initiator; the ticket author alone is not authorization.

## Cluster-owned dependencies and version limits

| Component | What the chart can do | What the operator must establish |
| --- | --- | --- |
| Kubernetes/CEL | Optional native policy, Fail behavior and Deny binding; refuse Kubernetes <1.30 | API-server type checking, binding activation, cluster-scoped installer RBAC and live refusal tests |
| Kyverno | Coexist with estate policy; no Kyverno CRDs or exceptions installed | Exact policy API/version, Enforce/Deny rather than Audit, exceptions reviewed for every agent shape |
| CNI/network | Render standard NetworkPolicy | Actual enforcement, DNS restrictions, overlapping allows, node/metadata and guest-network behavior |
| Kata/sandbox runtime | Select or constrain a RuntimeClass name | Installed handler, compatible nodes, CRDs/controllers and workload/tenant isolation |
| Tetragon | No node sensor or tracing policy installed | Kernel/hook support, capacity, event delivery, tested enforcement and alerts |
| Availability | Durable application state | Database HA, controller recovery and consumer availability qualified separately |

Host-kernel Tetragon events do not establish visibility into Kata guest processes.
Sigkill alone does not prove the triggering operation was blocked. Override
depends on kernel and hook support. A privileged node sensor or CNI has an
independent release and trust boundary; it must not be silently installed by an
application dependency.

The controller, provisioner, policy administrators and credential broker remain
trusted. Agents must not edit policies, namespaces, admission selectors, Secrets
or RoleBindings. A workload label is not an identity proof. Native admission
does not retroactively evict existing Pods. Restrict creation and relabeling of
agent Pods to trusted provisioners.

## Qualification before execution and after upgrades

1. Retain the chart/app versions, OCI and image digests, rendered values and
   security contracts. Record Kubernetes, CNI, runtime handler, kernel,
   Kyverno/Tetragon, IdP and forge versions.
2. Have the cluster owner review and install baseline policies before agents
   start. Server-dry-run the exact policies and Pods and inspect the admission
   policy's `status.typeChecking.expressionWarnings` and Deny binding. Helm
   rendering or a file in Git is not proof of enforcement.
3. Prove a normal agent Pod passes and hostile variants fail: root, privileged
   init/ephemeral containers, SYS_PTRACE, shared process/host namespaces,
   hostPath/socket, API token and an unapproved RuntimeClass.
4. From a disposable agent identity, prove necessary services work and database,
   admin/IdP, other tenants, LAN, metadata and arbitrary internet access fail.
   Test DNS exfiltration and every overlapping allow policy. NetworkPolicies
   are additive; a permissive policy can widen the chart's boundary.
5. Try another repository/branch, another person's target, a stale Run and
   revoked credentials. Verify refusal, human/actor/Run audit attribution,
   cancellation, cleanup and alerts. Record missing delegation as a blocker
   even if network checks pass.
6. Repeat affected checks after chart, image, CNI, runtime, kernel, IdP, policy
   or exception changes. Keep both positive and refused cases with the
   installation revision and a rollback plan.

The CI check `scripts/check-security-profile.py` renders the real policy and
evaluates its CEL against a real agent Pod plus hostile mutations. An independent
CEL evaluator is not the Kubernetes API server; server type-checking and live
CNI/runtime qualification remain required. The guide packaged with the exact
release takes precedence over development documentation for that installation.

References: [OAuth token exchange](https://www.rfc-editor.org/rfc/rfc8693.html),
[Authentik](https://docs.goauthentik.io/add-secure-apps/providers/oauth2/token_exchange/),
[Kubernetes admission](https://kubernetes.io/docs/reference/access-authn-authz/validating-admission-policy/),
[NetworkPolicy](https://kubernetes.io/docs/concepts/services-networking/network-policies/),
[Tetragon enforcement](https://tetragon.io/docs/concepts/enforcement/).
