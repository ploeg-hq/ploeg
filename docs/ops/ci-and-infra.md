# CI and infrastructure

[GitHub Actions](../../.github/workflows/ci.yml) runs the standalone Go, PostgreSQL-backed tests, Helm positive and negative fixtures, golden renders, license, brand, OpenSpec and documentation checks on public Ubuntu runners. Pull-request jobs have read-only repository permission and need no private estate credentials.

The [release workflow](../../.github/workflows/release.yml) alone publishes independent Ploeg artifacts using its repository-scoped token; the [release candidate workflow](../../.github/workflows/release-candidate.yml) cuts each candidate and calls it. [Release instructions](release-versioning.md) describe gates, version policy and recovery. Forgejo is a pull mirror, with no Ploeg release authority. Optional estate integration checks belong to a consumer or the deployment repository.

A passing standalone job does not qualify real gateways, model spend or production clusters. Cross-service tests belong to each consumer, against the Ploeg release it pins; live Kubernetes and inference qualification remain explicit opt-in work.

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

## Vulnerability and static analysis gates

- [image-security.yml](../../.github/workflows/image-security.yml) builds every image listed in [images.yaml](../../ops/security/images.yaml) and runs the [CVE gate](../../ops/vex/README.md) on it. It also runs the gate's self-test and `govulncheck ./...`. It runs on pull requests, on pushes and daily, and fails on any Critical or High finding that no reviewed OpenVEX statement covers, and on any reachable Go vulnerability.
- [codeql.yml](../../.github/workflows/codeql.yml) runs CodeQL for Go with `security-extended` and fails on a result of security severity 7.0 or more that [codeql-accepted.yaml](../../ops/security/codeql-accepted.yaml) does not accept with a written reason. Pull requests are judged on changed lines, pushes on the whole tree. [codeql-config.yml](../../.github/codeql/codeql-config.yml) leaves out `go/log-injection`, because every logger is an escaping `log/slog` handler, which a test in `internal/codeqlgate` enforces.
- [scorecard.yml](../../.github/workflows/scorecard.yml) publishes the OpenSSF Scorecard of `development`.
- The release runs the same CVE gate against each pushed digest before tagging or signing it ([ADR 0145](../adrs/0145-a-release-publishes-no-image-over-its-cve-budget-and-attaches-signed-openvex-to-every-digest.md)).

Grype and govulncheck are pinned in `mise.toml`. Grype's database is downloaded fresh on each run, verified by hash, and must be under 120 hours old. When the update check fails, the gate gives no verdict and the job fails.

## Dependency updates

Renovate proposes updates from [`renovate.json5`](../../renovate.json5). It runs in homelab-cluster as the RenovateJob `ploeg-hq`, authenticated as a GitHub App of its own (`ploeg-hq-renovate`), separate from the App that manages rulesets and settings. The shared [Webgrip presets](https://forgejo.webgrip.dev/webgrip/renovate-config) set the policy:

- Security fixes, from OSV and from GitHub's Dependabot alerts when they are on, open at once and merge themselves when `verify` is green.
- Minor, patch, digest and pin updates share the branch `renovate/all-non-major` and merge themselves when `verify` is green, after a soak of three days for a minor and one day for a patch. Indirect Go modules are included.
- Majors, Go toolchain minors, the DinD image in `values.yaml` and the semantic-release configuration wait for a tick on the Dependency Dashboard issue. A `values.yaml` image changes the golden renders, so run `scripts/helm-golden.sh update` on its branch.
- Ploeg's own images and the chart version belong to the release train and are never proposed. Paired version and checksum pins (`CLAUDE_CODE_VERSION`, `GOOSE_VERSION`) are not managed: Renovate cannot update the checksum with them.

The `toolchain` line in `go.mod` decides which Go builds every shipped binary. The official `golang` images set `GOTOOLCHAIN=local`, which ignores that line, so on 2026-10-24 `ploegd:0.2.0-rc.50` shipped go1.27.1 while `go.mod` named go1.27.3, with three Critical and 24 High standard-library findings. The image builders now set `GOTOOLCHAIN=auto`: a builder older than `go.mod` downloads the named toolchain, checksum-verified against the Go checksum database. CI runs [`check-go-toolchain.sh`](../../scripts/check-go-toolchain.sh) on each built image, and it fails when any Go binary in it is older than `go.mod`'s toolchain. Bump `go.mod`'s toolchain, the builder tag and `mise.toml` together.

Dependency bumps are releasable commits and cut a candidate. Actions and the release configuration land as `ci(...)`, which do not. Setup and the App's permissions: homelab-cluster `docs/techdocs/docs/runbooks/github-app-ploeg-hq.md`.

## Forge webhooks

`POST /webhooks/forge/forgejo` and `POST /webhooks/forge/gitlab` act only when two things are in place:

1. **A webhook secret.** Set `executor.forgejo.webhookSecret` (`PLOEG_FORGEJO_SECRET`) or `executor.gitlab.webhookSecret` (`PLOEG_GITLAB_SECRET`) and give the forge's webhook the same value. Without it ploegd rejects every delivery and logs a warning at start ([configuration](../reference/configuration.md)).
2. **A network path from the forge to ploegd on port 8080.** Both the forge's egress policy and ploegd's ingress policy must allow it.

Until both hold, merges still reach Ploeg through the periodic pull request reconcile, but a person's review and a failed check do not.

On 2026-09-27, homelab-cluster's `kubernetes/apps/forgejo/networkpolicy.yaml` allowed Forgejo egress to the `ploeg` namespace on 8080, but `kubernetes/apps/ploeg/ploeg/app/networkpolicy.yaml` admitted no traffic from the `forgejo` namespace, and the Ploeg HelmRelease set no webhook secret. Forgejo webhooks therefore did not reach Ploeg in that deployment. Wiring them is a change to homelab-cluster: an ingress rule, a secret and the webhook registration.

## Credentials and signing

GitHub publication uses the workflow's short-lived repository token. The one long-lived credential is `RELEASE_CONSUMER_TOKEN_FORGEJO`, which opens consumer pull requests after a candidate ([releases](release-versioning.md#consumer-pull-requests)). Pull requests receive no publishing secrets.

Every release signs the `ploegd`, `claude-runner` and `ploeg-worker` images and the OCI chart by digest with keyless cosign: the release job requests a GitHub Actions OIDC token (`id-token: write`), Fulcio issues a short-lived certificate for the workflow identity, and the signature is recorded in the public Rekor log. No signing key exists to store or rotate. `scripts/release.sh` verifies each signature before it publishes the release and lists the signed digests in `signed-artifacts.txt`. Verify a release with:

```sh
cosign verify ghcr.io/ploeg-hq/ploegd:<version> \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github\.com/ploeg-hq/ploeg/\.github/workflows/release\.yml@refs/(heads/development|heads/main|tags/v0\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?)$'
```

The certificate names `release.yml` at the ref that started the run: `refs/heads/development` for a candidate cut by the release-candidate workflow, `refs/tags/v0.x.y` for a stable tag push, and the dispatching branch for a manual re-publication. BuildKit SBOM and provenance attestations remain unsigned in-index attestations; the cosign signature is the project signature.

Historical WebGrip releases used estate OpenBao credentials and signing infrastructure. Their runbooks remain relevant to those deployments, not authority for the new independent publisher. A local check cannot prove live registry visibility or cluster signature enforcement.

## Historical incidents

The [July infrastructure notes](https://forgejo.webgrip.dev/webgrip/ploeg/src/commit/94c7c8e2dc07037ee2fd38a69343426e40bd680d/docs/ops/ci-and-infra.md) preserve the DNS/TLS investigation, package-linking observations and signing rollout context. They contain deployment-specific values and superseded credential advice.

A resolved incident is a diagnostic lead. Its prior cause is neither confirmed nor ruled out when the same symptom returns; verify the current runner, DNS path, action version and relevant service response.
