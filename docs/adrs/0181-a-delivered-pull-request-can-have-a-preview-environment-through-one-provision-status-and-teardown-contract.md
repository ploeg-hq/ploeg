---
status: proposed
date: 2026-10-30
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-30
---

# A delivered pull request can have a preview environment through one provision, status and teardown contract

## Context and Problem Statement

Ploeg ends its work at a pull request that is ready for review ([ADR-0119](0119-a-pull-request-is-ready-for-review-only-when-the-forges-ci-passed-on-its-head.md)). Many reviewers cannot judge a change from its diff. They need to use it, so an operator consumer needs every pull request that Ploeg delivers to have a running copy of the application at a URL. Today Ploeg has no notion of such an environment. A consumer that wants one has to watch the forge itself, hold forge and hosting credentials of its own, and work out on its own when to tear the environment down. [ADR-0079](0079-run-cards-belong-to-the-consumer-and-ploeg-supplies-delivery-facts.md) and ADR-0176 (proposed in a separate pull request) avoided exactly that for comments and commit statuses.

The repositories Ploeg works in are often PHP applications (Laravel, Symfony, Magento, Shopware) on many kinds of hosting. Some hosts have per-pull-request environments built in. [Upsun](https://developer.upsun.com/docs/integrations/source/github) (formerly Platform.sh) builds an environment for each pull request when its GitHub integration's `build-pull-requests` option is on (the default), reports the environment's primary URL back to GitHub, and deletes the environment when the pull request is merged. [Laravel Cloud](https://laravel.com/cloud/docs/preview-environments) creates an environment from an automation for each pull request opened against a target environment's branch, posts its domain as a comment, can protect it with HTTP basic authentication (Growth plan or higher), and deletes it on merge or close when "Delete on merge or close" is on. Others have none: Laravel Forge documents no per-pull-request preview, and the tools that add one are third-party CLIs run from CI ([forge-previewer](https://github.com/ryangjchandler/forge-previewer), archived as abandoned; [Laravel Harbor](https://github.com/mehrancodes/laravel-harbor)). Ploi's public roadmap carries per-pull-request previews as a request ([item 1362](https://roadmap.ploi.io/items/1362-preview-environments)), not a shipped feature. Shared hosting has neither. All of these were read on 2026-10-30. Where no host does it, the usual recipe is the same: build an image, deploy it to a host such as `pr-N.preview.<domain>`, load a sanitized database seed, and tear it down when the pull request closes.

A preview runs code an agent wrote. It is as untrusted as a Run, reachable from the internet, and it costs compute for as long as it lives.

How does Ploeg give a delivered pull request a preview environment, on any host, without becoming a hosting vendor's integration, without carrying a delivery credential into the preview, and without leaving data behind when the preview ends?

## Decision Drivers

* **Any host.** The same Ploeg behaviour must work for a host with native previews, a host without them, and the repository's own CI. Ploeg names work sources and change destinations, never vendors ([ADR-0077](0077-ploeg-knows-work-sources-and-change-destinations-never-vendors.md)), and names none of its consumers ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)).
* **A preview is untrusted code.** It gets at least the isolation of a Run ([ADR-0106](0106-a-run-declares-an-isolation-floor-and-a-cell-runs-it-at-the-strictest-class-it-can-honour.md)) and the default-deny network of [ADR-0137](0137-the-chart-renders-a-default-deny-networkpolicy-for-every-pod-with-an-allowlist-derived-from-its-urls.md). No credential from the delivery path reaches it: no forge token, model key, Run token or ploegd secret ([ADR-0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md)).
* **No production data.** A preview holds only seed data that the operator's own sanitizer produced. Ploeg never reads production and never sanitizes anything, because it cannot know what is personal data in someone else's schema.
* **The agent cannot widen its own preview.** The pull request is written by the agent, so nothing in the pull request may loosen how its preview runs.
* **Bounded cost and no leftovers.** A preview counts toward its Team's limits, ends on close, merge or a time limit, and leaves no data or object behind.
* **One head, one environment.** A preview shows the pull request's head as the forge reports it, never a SHA an agent reported ([ADR-0059](0059-delivery-facts-come-from-the-forge-never-from-the-agents-outcome.md)).
* **Additive.** Off unless configured, with no change to an existing installation ([ADR-0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md)).

## Considered Options

* **One environment contract (provision, status, teardown) served by two kinds of implementation: an in-cluster default that Ploeg ships, and an external HTTPS hook signed with Standard Webhooks**
* Ploeg integrates each host natively (Upsun, Laravel Cloud, Forge, Ploi) through its API
* Ploeg posts nothing; the repository's CI builds previews and the consumer watches the forge
* Only the in-cluster implementation, with no external hook
* Only the external hook, with no in-cluster implementation

## Decision Outcome

Chosen option: "**one environment contract served by an in-cluster default and an external hook**", because it is the only option that covers hosts with native previews, hosts without them and the repository's own CI with one Ploeg behaviour, keeps every vendor out of Ploeg's code, and lets an installation with none of those still get a preview.

Proposed, not built. Each clause states the contract the implementation must meet.

### 1. The environment contract

An **Environment** is one preview of one pull request on one Work Target. Its id is derived from the forge, the repository and the pull request number, so the same pull request always names the same Environment. Ploeg drives it through three operations, defined in a new `docs/contracts/environment-hook.v1.schema.json` with its Go types:

* **provision** takes the Environment id, the target (forge, owner, repository, pull request number, base branch), the pull request's **head SHA** read from the forge at the time of the call, the parsed **environment template** (clause 3), the **access policy** (clause 6), the TTL and a status callback URL. Provision is idempotent per Environment id and head SHA: repeating it changes nothing, and calling it with a new head replaces the running preview with that head. A push to the pull request calls provision again with the new head.
* **status** answers one of `pending`, `ready` with an `https` URL, or `failed` with a `reason` from a closed list (`template_invalid`, `template_refused`, `image_unavailable`, `seed_unavailable`, `over_budget`, `access_policy_unsupported`, `unhealthy`, `provider_error`) and a free-text `detail` of at most 1 024 characters. Every status names the head SHA it describes. A status for an older head never replaces the status of the current one.
* **teardown** takes the Environment id. Ploeg calls it when the pull request is merged or closed, when the TTL runs out, and from the orphan sweep (clause 5). Teardown is idempotent; tearing down an Environment that does not exist succeeds.

ploegd decides when to call each operation. It provisions only for pull requests it delivered, on Work Targets whose configuration names a preview provider (`previews.provider` on the target or its Team). A Work Target without one gets no Environment and no preview comment or status. The contract carries no forge token, model key, Run token or any other credential of Ploeg's.

### 2. Two kinds of implementation

ploegd speaks the contract to a **provider**. A provider is either the in-cluster default or an external hook, and ploegd treats both the same.

**The in-cluster default.** Ploeg ships a preview controller, a separate Deployment in the release namespace with its own ServiceAccount. It serves the same contract over HTTP to ploegd, so the contract is implemented and tested once. ploegd holds no new Kubernetes authority.

* Each Environment runs in a namespace of its own, `<previews.inCluster.namespacePrefix>-<environment id>`, created by the controller and labelled with the Environment id and the Team. The namespace enforces Pod Security `restricted` and carries a ResourceQuota and a LimitRange computed from the Team's preview limits (clause 5). Deleting the namespace is the teardown.
* The controller's ServiceAccount may create and delete namespaces. A ValidatingAdmissionPolicy that the chart renders, like the worker admission policy of [ADR-0168](0168-profile-p1-turns-on-the-worker-admission-policy-and-a-sandbox-team-names-its-runtime-class-there.md), allows that ServiceAccount to create only namespaces with the prefix, the labels and `enforce: restricted`, and to create objects only in them. This is the same namespace separation as [ADR-0169](0169-worker-workloads-render-into-a-namespace-of-their-own-and-profile-p1-refuses-a-shared-one-from-chart-0-3-0.md), one level further: a preview shares no namespace with ploegd or with workers.
* Preview pods run at the Team's declared isolation class and floor under ADR-0106 and carry its `ploeg.webgrip.dev/isolation-class` label. They mount no ServiceAccount token (`automountServiceAccountToken: false`), and their ServiceAccount has no RBAC.
* Each namespace gets a default-deny NetworkPolicy for ingress and egress, written the way ADR-0137 writes them. Ingress is allowed only from the installation's ingress controller or gateway (`previews.inCluster.ingressFrom`) to the one exposed service, and between the Environment's own pods. Egress is denied, including DNS outside the namespace, unless the Team's `previews.egressAllow` names a peer, and a template may only ask for peers from that list.
* Storage is `emptyDir` with a `sizeLimit` only. No PersistentVolumeClaim is created, so a deleted namespace leaves no volume behind. A restarted preview loads its seed again.
* The host comes from `previews.inCluster.host`, a pattern that must contain `{number}` and `{repo}`, for example `pr-{number}-{repo}.preview.example.org`, under one wildcard DNS record and one wildcard certificate Secret that the installer provides (`previews.inCluster.tlsSecret`). The controller renders a Gateway API HTTPRoute when `previews.inCluster.gatewayRef` is set and an Ingress with `ingressClassName` otherwise.
* The controller does not build images. The template names an image that the repository's own CI builds and tags with the head SHA. The controller resolves it to a digest, pulls it only from `previews.inCluster.allowedRegistries`, and answers `pending` until the image exists, then `failed` with `image_unavailable` after `previews.imageWait` (default 30 minutes).

**The external hook.** `previews.hooks.<name>` names an `https` URL and a `secretRef` to a Standard Webhooks signing secret. ploegd sends provision and teardown as `POST` requests signed as the [Standard Webhooks specification](https://github.com/standard-webhooks/standard-webhooks/blob/main/spec/standard-webhooks.md) defines: headers `webhook-id`, `webhook-timestamp` and `webhook-signature`, the signature `v1,<base64 HMAC-SHA256>` over `<id>.<timestamp>.<body>`, and a `whsec_` secret of 24 to 64 random bytes. `webhook-id` stays the same on every retry, so the receiver can use it as its idempotency key. Several signatures in the header allow a secret to be rotated without downtime.

* The hook answers provision with `202` and a `pending` status, or `200` with any status. Ploeg then either polls `GET <url>/environments/{id}`, signed the same way, every `previews.hooks.<name>.pollInterval` (default 30 seconds), or receives callbacks at `POST /api/v1/environments/{id}/status`. A callback must be signed with the hook's own secret and is claimed under a lease and marked seen only after its effects commit ([ADR-0173](0173-a-webhook-delivery-is-claimed-under-a-lease-and-marked-seen-only-after-its-effects-commit.md)).
* A `ready` URL must be `https`, carry no credentials, and have a host that matches `previews.hooks.<name>.urlHosts`. Ploeg refuses any other URL as `provider_error`, so a hook cannot get an arbitrary link posted on a pull request.
* ploegd's egress to the hook URL is derived from it by the rule of ADR-0137, as a new endpoint kind `previewHook` with `peers` and `port` overrides.
* The hook covers, without Ploeg code for any of them: a host with native previews, where the receiver lets the host's own integration build the preview and reports the URL that the host's API gives for the pull request (Upsun, Laravel Cloud); a host without native previews, where the receiver runs a deploy script against the host's API (Forge, Ploi, or a script for shared hosting); and the repository's own CI, where the receiver starts a pipeline and the pipeline calls back with the status. Ploeg ships no adapter for any named host. A how-to may document recipes later.

### 3. The environment template

The template says how to run the preview. ploegd reads it from the **base branch at the base commit**, never from the pull request's head, in the same way a reviewer reads instruction files from the base branch ([ADR-0163](0163-a-reviewer-reads-the-base-branchs-instruction-files-and-a-team-can-require-a-reviewer-unlike-its-writer.md)). A change to the template in a pull request therefore takes effect only after a person merges it, and an agent cannot loosen its own preview.

* **Where.** `.ploeg/preview.yaml`, `schemaVersion: 1`. The file may set `from: compose` and name a Compose file in the repository, from which Ploeg reads the subset below. Ploeg does not read devcontainer or DDEV configuration in this version: a devcontainer describes a development container, not a served application, and turning DDEV's project types into images would make Ploeg maintain images for every stack.
* **What Ploeg reads.** Per service: `image` (with `{sha}` for the head SHA), the container `port`, literal `env` values, `command` and `entrypoint`, `healthcheck` (an HTTP path or a command), `depends_on`, `resources` requests and limits, `tmpfs` and sized scratch directories, `user`, and which one service is `expose`d. At the top level: `seed`, `ttl` (capped by the Team's `previews.maxTTL`), `access` (clause 6), the egress peers the preview asks for, `generatedSecrets` (names whose random values Ploeg creates once per Environment, for example `APP_KEY` or `APP_SECRET`), and `secrets` (names Ploeg copies from preview-only Secrets, below).
* **What Ploeg refuses.** A template is refused as `template_refused`, naming the field, when it asks for: `privileged`, any host mount or Compose bind mount (`volumes` of type `bind` or a host path), named volumes outside the Environment, `network_mode`, `pid` or `ipc` other than the default, `cap_add`, `devices`, `security_opt`, `sysctls`, `userns_mode`, `extra_hosts`, `build` without an `image`, `env_file`, Compose `secrets` or `configs` that read files, an image outside the allowed registries, an egress peer outside the Team's `previews.egressAllow`, or a `ttl` above the Team's maximum. Unknown fields are refused, not ignored.
* **Preview-only Secrets.** A name under `secrets` resolves only to a key of a Secret that the installer created in the controller's namespace and labelled `ploeg.webgrip.dev/preview-secret: "true"` for that Team. The controller refuses any other Secret. A template cannot reach a delivery credential by naming it.

A sketch (proposed, not built):

```yaml
schemaVersion: 1
services:
  web:
    image: registry.example.org/shop/app:{sha}
    port: 8080
    user: "1000"
    env: {APP_ENV: preview}
    generatedSecrets: [APP_KEY]
    healthcheck: {path: /up}
    resources: {requests: {cpu: 250m, memory: 512Mi}, limits: {memory: 1Gi}}
    expose: true
    depends_on: [db]
  db:
    image: registry.example.org/shop/mariadb:11.4-nonroot
    port: 3306
seed:
  service: db
  artifact: oci://registry.example.org/shop/preview-seed:latest
  command: ["sh", "-c", "mariadb shop < /seed/dump.sql"]
ttl: 72h
access: signed-link
```

### 4. Exposure

* **Keyed comment.** Ploeg keeps one pull request comment under the key `preview`, with the mechanism of ADR-0079, carrying the URL, the head SHA it shows, the access policy and the time it will be torn down. It is updated on every status change and removed on teardown.
* **Keyed commit status.** If ADR-0176 is accepted, Ploeg also sets the context `ploeg/preview` on the head it provisioned: `pending`, `success` with the URL as `targetUrl`, or `failure` with the reason. This status is one Ploeg wrote, so ADR-0176 clause 7 leaves it out of Ploeg's own CI gate: a failed preview holds no Shift and opens no fix round. If ADR-0176 is not accepted, only the comment is published.
* **Audit.** Each change is audited with the Environment id, Work Item, pull request, head SHA, provider and Team: `environment.provision_requested`, `environment.ready` (with the URL and access policy), `environment.failed` (with the reason), `environment.refused` (a template or budget refusal), `environment.torn_down` (with the cause: `merged`, `closed`, `ttl` or `orphan`) and `environment.teardown_failed`.
* **Operator API read.** `GET /api/v1/operator/work-items/{id}/environments` and `GET /api/v1/operator/environments?team=&state=&limit=` return each Environment's id, pull request, head SHA, provider, state, URL, reason, access policy, created, ready and torn-down times, and its measured usage, within the Teams the consumer names ([ADR-0160](0160-an-operator-consumer-reads-only-the-teams-it-names-and-run-content-only-when-granted.md)). The Work Item's facts (ADR-0079) carry the same list as `environments`. The read carries no template secrets and no seed content.

### 5. Budget and cleanup

* **Team limits.** `teams.<name>.previews` sets `maxConcurrent`, `maxTTL`, `defaultTTL` and, for the in-cluster default, a `quota` (CPU, memory, ephemeral storage and pod count per Environment). A provision that would exceed `maxConcurrent` is refused as `over_budget` and audited, not queued. An external hook counts toward `maxConcurrent` only, because Ploeg cannot measure resources it does not run.
* **Usage.** For the in-cluster default Ploeg records each Environment's lifetime and its quota as Environment-hours and requested CPU and memory, so a consumer can price it. Previews are not model spend and do not count toward the Team's spend cap ([ADR-0162](0162-a-team-spends-at-most-its-cap-per-period-and-a-claim-over-it-starts-nothing.md)). If a Team needs one money cap for both, that is a separate record.
* **Teardown triggers.** The pull request's merge or close, as Ploeg already records it from the forge, and the end of the TTL. Every new head resets the TTL's idle clock only if `previews.ttlFromLastPush` is true; by default the TTL counts from the first provision.
* **Orphan sweep.** A sweep in ploegd calls teardown for every Environment whose pull request is closed or whose TTL has passed, with backoff. After `previews.teardownAttempts` (default 5) failures it audits `environment.teardown_failed` and reports it on the Work Item. The preview controller also deletes every namespace with the preview prefix and label that has no live Environment in ploegd's answer. Namespaces without the label are never touched.
* **No data left.** In-cluster, teardown deletes the namespace, which removes its pods, Secrets, generated values and `emptyDir` storage; no volume outlives it. Ploeg keeps only the Environment row and its audit events, which hold no seed content or secret values. An external hook's teardown is the hook's responsibility. Ploeg records whether the hook confirmed it.

### 6. Security

* **No delivery credential.** No forge token, model key, Run token, transaction token, OpenBao reference or ploegd Secret is placed in a preview namespace or sent in a hook request. The preview controller's Secrets live in the release namespace and are never mounted into a preview.
* **Sanitized seed only.** The template's `seed` names an OCI artifact or an `https` URL. Ploeg resolves it to a digest at provision time, records the digest on the Environment and in the audit, pulls it with a read-only seed credential the installer configures per Team (`teams.<name>.previews.seedPullSecret`), and runs the template's seed command once in the Environment's namespace. The operator produces the seed with its own sanitizer. Ploeg never connects to a production database, never runs a sanitizer and makes no claim that a seed is sanitized. Its documentation says so.
* **Default-deny egress.** As in clause 2. A preview cannot reach ploegd, the model gateway, the forge, the cluster API or another preview unless a Team rule says so, and a Team rule can never admit ploegd, the gateway or the forge.
* **Access policy.** The contract carries `access`: `public`, `signed-link` or `sso`. The default is `signed-link`. Which one fits is the operator's choice. A Team may set a minimum, `previews.minAccess`, in the order `public` < `signed-link` < `sso`, and a template cannot ask for a weaker policy than that.
  * In-cluster, `signed-link` puts a small gate in front of the exposed service. The gate checks an HMAC token in the link, keyed per Environment and expiring with it, then sets a cookie. `sso` delegates to a forward-auth URL the installer configures (`previews.inCluster.sso.forwardAuthUrl`); Ploeg runs no identity provider. `public` adds nothing.
  * An external hook reports in its `ready` status which policy it applied. A preview whose provider cannot apply the requested policy fails with `access_policy_unsupported`. It is never published with a weaker one, in the way ADR-0106 never runs a Run below its floor.

### Support matrix

When built, the support matrix gains a **Preview environments** axis with two rows:

* **In-cluster default:** `experimental` until a dated live record shows a preview provisioned, reached and torn down on an enforcing CNI. Until then, enabling it under `supportProfile: P1` is a departure the chart refuses unless the deployment declares itself `unsupported` ([ADR-0154](0154-the-chart-refuses-values-that-leave-profile-p1-unless-they-declare-the-deployment-unsupported.md)).
* **External hook:** `tested-in-ci` once the conformance tests below run in CI against an `httptest` receiver. No live record of any named host is claimed.

### Migration

Everything is additive and off by default. No `previews` configuration means no new behaviour, no new pod and no new forge write. The change adds an append-only store migration (`environments` and their events, with `ploeg_grant_observer()` extended for the non-secret columns), the `environment-hook.v1` contract, additions to `operator-api.v1`, the preview controller Deployment and admission policy behind `previews.inCluster.enabled`, and the `previewHook` NetworkPolicy endpoint kind. An installation that already builds previews in its own CI keeps doing so, and can move to the contract by pointing an external hook at a receiver that starts that pipeline. The compatibility ledger records only additions.

### Consequences

* Good, because one Ploeg behaviour gives a delivered pull request a preview on a host with native previews, a host without them, the repository's own CI, or the cluster Ploeg already runs in.
* Good, because no vendor appears in Ploeg's code, and a new host needs a receiver, not a Ploeg release.
* Good, because the template is read from the base branch, so the agent that wrote the pull request cannot loosen its own preview.
* Good, because a preview holds no delivery credential and no unsanitized data, cannot call out by default, and leaves nothing behind in-cluster.
* Good, because the URL reaches reviewers on the pull request and in the operator API with no forge credential on the consumer's side.
* Bad, because the in-cluster default requires the repository's CI to build and push an image per head. Accepted: building arbitrary repositories inside the cluster is a large, untrusted workload that this record does not take on.
* Bad, because Pod Security `restricted` refuses images that start as root, which includes several official database and PHP images. Accepted: templates name non-root variants or set `user`. Loosening the namespace would make a preview weaker than a Run.
* Bad, because `emptyDir`-only storage reloads the seed whenever a pod restarts, and a large seed makes a restart slow.
* Bad, because the preview controller's ServiceAccount can create namespaces, a cluster-scoped permission that only the admission policy narrows. Accepted: it lives outside ploegd, and the policy is tested.
* Bad, because an external hook's isolation, data handling and teardown are only as good as its receiver. Ploeg records what the hook reports and cannot verify it.
* Neutral: a host's native integration may also post its own comment, so a pull request can show two links until the operator turns one of them off.

### Confirmation

Once built, `mise exec -- go test ./...` in `.forgejo/workflows/on_pull_request.yml` and `mise run verify` run these, with forge fakes and hook receivers built on `net/http/httptest`, and every response validated against `environment-hook.v1.schema.json` or `operator-api.v1.schema.json`:

* `go test ./pkg/preview/`: `TestProvisionIsIdempotentPerEnvironmentAndHead`, `TestANewHeadReplacesThePreview`, `TestAStatusForAnOlderHeadIsIgnored`, `TestTeardownOfAMissingEnvironmentSucceeds`, `TestTheTemplateIsReadFromTheBaseCommit` (a head that loosens `.ploeg/preview.yaml` changes nothing), `TestARefusedTemplateFieldIsNamed` (one case per refused field in clause 3, including Compose bind mounts, `privileged`, `network_mode: host`, `env_file` and an unknown field), `TestASecretWithoutThePreviewLabelIsRefused`, and `TestNoDeliveryCredentialReachesAProvisionRequest` (asserts that no configured forge, gateway, Run or transaction token value appears in any outbound body or header).
* `go test ./pkg/preview/hook/`: `TestHookRequestsAreSignedAsStandardWebhooks` (checked with an independent verifier of the specification's test vectors), `TestTheWebhookIdIsStableAcrossRetries`, `TestACallbackWithABadSignatureIsRefused`, `TestACallbackIsClaimedUnderALease`, `TestAReadyURLOutsideTheHooksHostsIsRefused`, and `TestAHookThatCannotApplyTheAccessPolicyFails`.
* `go test ./pkg/preview/controller/` with a fake Kubernetes API: `TestEachEnvironmentGetsItsOwnRestrictedNamespace`, `TestTheNamespaceCarriesTheTeamQuota`, `TestPreviewPodsMountNoServiceAccountToken`, `TestPreviewStorageIsEmptyDirOnly`, `TestTheSweepDeletesOnlyLabelledOrphanNamespaces`, and `TestASignedLinkExpiresWithTheEnvironment`.
* `go test ./cmd/ploegd/ ./pkg/store/`: `TestMergeAndCloseTearDownThePreview`, `TestTTLTearsDownThePreview`, `TestOverMaxConcurrentIsRefusedAndAudited`, `TestEachEnvironmentChangeIsAudited`, and `TestATeardownThatKeepsFailingIsReported`.
* `go test ./pkg/httpapi/`: `TestEnvironmentsReadIsTeamScoped`, `TestThePreviewCommentFollowsTheStatus`, and, if ADR-0176 is accepted, `TestThePreviewStatusIsLeftOutOfTheCIGate`.
* `scripts/test-chart-network-policies.sh` and `scripts/helm-golden.sh check` render `previews.inCluster.enabled` and require the default-deny policy per preview namespace, the admission policy that refuses the controller a namespace without the prefix, label or `restricted`, the `previewHook` egress rule for ploegd, and the P1 refusal without `supportProfile: unsupported`.
* `go test ./internal/compat/` sees only additions, and `go test ./internal/ledger/` keeps this record in the index.

## Pros and Cons of the Options

### Ploeg integrates each host natively

* Good, because an operator configures a host once and writes no receiver.
* Bad, because every host's API, plan limits and changes become Ploeg's code and release cadence, which ADR-0077 rules out.
* Bad, because hosts without previews (Forge, Ploi, shared hosting) still need a script, so a generic route is needed anyway.

### Ploeg posts nothing; the repository's CI builds previews and the consumer watches the forge

* Good, because Ploeg changes nothing.
* Bad, because the consumer needs forge credentials and its own teardown and orphan tracking, and nothing ties a preview to Ploeg's Team limits or audit.

### Only the in-cluster implementation

* Good, because one implementation is easier to secure and test.
* Bad, because an operator whose host already builds previews would run a second, different copy of the application, and production-like hosting features (the host's PHP build, its services) would be missing.

### Only the external hook

* Good, because Ploeg runs no preview workload at all.
* Bad, because an installation without a host that does previews, which is the common case for Forge, Ploi and shared hosting, gets none until someone writes and hosts a deployer.

## Re-evaluation triggers

* ADR-0176 is accepted or rejected. The exposure clause then drops its condition.
* ADR-0106 changes its classes, or the threat model adds a profile that includes previews.
* Ploeg starts building images for Runs inside the cluster, which reopens building preview images in-cluster.
* Two installations ask for devcontainer or DDEV configuration to be read as a template.
* A Team needs one money cap covering previews and model spend.
* An installation reports a preview that reached ploegd, the model gateway, the forge or another preview, or a teardown that left a namespace or volume behind.
* The Standard Webhooks specification publishes a version that changes the signed content or the header names.
* Upsun, Laravel Cloud, Forge or Ploi change how their per-pull-request environments are triggered or reported, so the recipes in clause 2 no longer hold.

## More Information

* Contract homes once built: `docs/contracts/environment-hook.v1.schema.json` (new) and `docs/contracts/operator-api.v1.schema.json` (`environment`, `environmentList`).
* Related: [ADR-0079](0079-run-cards-belong-to-the-consumer-and-ploeg-supplies-delivery-facts.md) (keyed comment), ADR-0176 (keyed commit status, proposed), [ADR-0106](0106-a-run-declares-an-isolation-floor-and-a-cell-runs-it-at-the-strictest-class-it-can-honour.md) (isolation classes), [ADR-0137](0137-the-chart-renders-a-default-deny-networkpolicy-for-every-pod-with-an-allowlist-derived-from-its-urls.md) (network policy), [ADR-0169](0169-worker-workloads-render-into-a-namespace-of-their-own-and-profile-p1-refuses-a-shared-one-from-chart-0-3-0.md) (namespaces), [ADR-0047](0047-ploeg-learns-where-a-merged-change-is-deployed-from-a-generic-deploy-endpoint.md) (deploys; a preview is not a deploy of a merged change and is not recorded as one).
* Host features read on 2026-10-30: [Upsun GitHub integration](https://developer.upsun.com/docs/integrations/source/github), [Upsun database sanitizing](https://developer.upsun.com/docs/development/sanitize-db), [Laravel Cloud preview environments](https://laravel.com/cloud/docs/preview-environments), [Ploi roadmap item 1362](https://roadmap.ploi.io/items/1362-preview-environments), [forge-previewer](https://github.com/ryangjchandler/forge-previewer), [Laravel Harbor](https://github.com/mehrancodes/laravel-harbor).
* Signing: [Standard Webhooks specification](https://github.com/standard-webhooks/standard-webhooks/blob/main/spec/standard-webhooks.md).
