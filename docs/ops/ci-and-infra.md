# CI and infrastructure

[GitHub Actions](../../.github/workflows/ci.yml) runs the standalone Go, PostgreSQL-backed tests, Helm positive and negative fixtures, golden renders, license, brand, OpenSpec and documentation checks on public Ubuntu runners. Pull-request jobs have read-only repository permission and need no private estate credentials.

The [release workflow](../../.github/workflows/release.yml) alone publishes independent Ploeg artifacts using its repository-scoped token. [Release instructions](release-versioning.md) describe gates, version policy and recovery. Forgejo is a pull mirror, with no Ploeg release authority. Optional estate integration checks belong to Unfold or the deployment repository.

A passing standalone job does not qualify real gateways, model spend or production clusters. Cross-service tests remain in Unfold against its pinned Ploeg source; live Kubernetes and inference qualification remain explicit opt-in work.

## Forgejo pull mirror

1. Sign in to Forgejo and choose **New Migration**, then the Git source migration form.
2. Source: `https://github.com/ploeg-hq/ploeg.git`.
3. Choose the intended Forgejo owner and an unused destination name. If `webgrip/ploeg` exists, preserve/archive it under a historical name before reusing that location. Do not overwrite an uninspected repository.
4. Enable **This repository will be a mirror** and choose a periodic sync interval. Public source reads need no GitHub write token.
5. Start migration and compare the `main` and release-tag SHAs on both forges. Repeat after the next upstream change.
6. Point contributor links to GitHub. Do not accept development pushes or run another publisher on the mirror.

A pull mirror copies Git refs and objects, not the complete issues, reviews, release assets or package registry. Back those up separately. [Forgejo mirror documentation](https://forgejo.org/docs/latest/user/repo-mirror/) explains the migration-only setup.

## Deployment infrastructure

Existing WebGrip desired state remains in [homelab-cluster](https://forgejo.webgrip.dev/webgrip/homelab-cluster). This repository cut does not change production pins, network policies or secrets.

## Forge webhooks

`POST /webhooks/forge/forgejo` and `POST /webhooks/forge/gitlab` act only when two things are in place:

1. **A webhook secret.** Set `executor.forgejo.webhookSecret` (`PLOEG_FORGEJO_SECRET`) or `executor.gitlab.webhookSecret` (`PLOEG_GITLAB_SECRET`) and give the forge's webhook the same value. Without it ploegd rejects every delivery and logs a warning at start ([configuration](../reference/configuration.md)).
2. **A network path from the forge to ploegd on port 8080.** Both the forge's egress policy and ploegd's ingress policy must allow it.

Until both hold, merges still reach Ploeg through the periodic pull request reconcile, but a person's review and a failed check do not.

On 2026-09-27, homelab-cluster's `kubernetes/apps/forgejo/networkpolicy.yaml` allowed Forgejo egress to the `ploeg` namespace on 8080, but `kubernetes/apps/ploeg/ploeg/app/networkpolicy.yaml` admitted no traffic from the `forgejo` namespace, and the Ploeg HelmRelease set no webhook secret. Forgejo webhooks therefore did not reach Ploeg in that deployment. Wiring them is a change to homelab-cluster: an ingress rule, a secret and the webhook registration.

## Credentials and signing

GitHub publication uses the workflow's short-lived repository token. Pull requests receive no publishing secrets. Independent release signing is not configured yet; do not describe BuildKit SBOM/provenance as a cryptographic project signature.

Historical WebGrip releases used estate OpenBao credentials and signing infrastructure. Their runbooks remain relevant to those deployments, not authority for the new independent publisher. A local check cannot prove live registry visibility or cluster signature enforcement.

## Historical incidents

The [July infrastructure notes](https://forgejo.webgrip.dev/webgrip/ploeg/src/commit/94c7c8e2dc07037ee2fd38a69343426e40bd680d/docs/ops/ci-and-infra.md) preserve the DNS/TLS investigation, package-linking observations and signing rollout context. They contain deployment-specific values and superseded credential advice.

A resolved incident is a diagnostic lead. Its prior cause is neither confirmed nor ruled out when the same symptom returns; verify the current runner, DNS path, action version and relevant service response.
