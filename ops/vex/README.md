# OpenVEX statements

Each image Ploeg publishes has a budget of zero Critical and zero High findings in `enforce` mode ([cve-budgets.yaml](../security/cve-budgets.yaml)). The image appears in [images.yaml](../security/images.yaml) and has its own OpenVEX document here, `statements/<image>.openvex.json`. A finding the CVE gate should not count needs a reviewed statement in that document. Nothing else suppresses a finding: the Harbor allowlist stays empty and there is no ignore file. The decision is [ADR 0145](../../docs/adrs/0145-a-release-publishes-no-image-over-its-cve-budget-and-attaches-signed-openvex-to-every-digest.md).

## The gate

`go run ./internal/cvegate/cmd/cve-gate` has the same contract as the estate's `cve-gate`, which the homelab's daily re-scan runs against these files.

| Variable | Default | Meaning |
| --- | --- | --- |
| `IMAGE_REF` | required | A registry reference pinned by digest (`ghcr.io/ploeg-hq/ploegd@sha256:…`), or a local `docker:`, `docker-archive:`, `oci-archive:` or `oci-dir:` source |
| `IMAGE_NAME` | required | The image's name in `images.yaml` |
| `BUDGETS` | `ops/security/cve-budgets.yaml` | Budgets per image, with `defaults` |
| `VEX_DIR` | `ops/vex/statements` | Directory of `<image>.openvex.json` |
| `OUT_DIR` | `.` | Where `grype.json` and `cve-gate.md` are written |
| `IMAGES` | `ops/security/images.yaml` | The image catalogue |
| `GRYPE` | `grype` | The scanner (pinned in `mise.toml`) |

It exits 0 when the image is within budget (or over budget in `warn` mode), 1 when it is over an enforced budget, and 2 when there is no verdict. No verdict covers a scan that failed, a vulnerability database that is unreachable or more than 120 hours old, a missing or invalid VEX document, an unpinned registry reference, and an image that is not catalogued. Grype runs with `GRYPE_DB_AUTO_UPDATE`, `GRYPE_DB_VALIDATE_BY_HASH_ON_START`, `GRYPE_DB_VALIDATE_AGE`, `GRYPE_DB_MAX_ALLOWED_BUILT_AGE=120h` and `GRYPE_DB_REQUIRE_UPDATE_CHECK` set by the gate itself.

To run it on a local build:

```sh
docker build --file ops/docker/ploegd/Dockerfile --tag ghcr.io/ploeg-hq/ploegd:local .
IMAGE_REF=docker:ghcr.io/ploeg-hq/ploegd:local IMAGE_NAME=ploegd mise exec -- go run ./internal/cvegate/cmd/cve-gate
```

Tag the local build under the image's real repository. Grype matches a statement's product to the image's tags.

The gate runs on every pull request and push, and daily, in [image-security.yml](../../.github/workflows/image-security.yml). During a release, [release.sh](../../scripts/release.sh) runs it against each image's pushed digest before anything is tagged or signed.

## Writing a statement

Add a statement only when a rebuild on a patched base cannot remove the finding, and you can say why the vulnerable code cannot be reached or exploited in this image. Every statement names this image as the product and the affected package as a subcomponent. The gate refuses a statement without both, so nothing can suppress a finding image-wide or across images.

```json
{
  "vulnerability": { "name": "CVE-2024-45492" },
  "products": [
    {
      "@id": "pkg:oci/ploeg-worker?repository_url=ghcr.io%2Fploeg-hq%2Fploeg-worker",
      "subcomponents": [ { "@id": "pkg:apk/wolfi/expat@2.6.2-r0" } ]
    }
  ],
  "status": "not_affected",
  "justification": "vulnerable_code_not_in_execute_path",
  "impact_statement": "git never parses XML input in ploeg-worker; the library is linked for an unused WebDAV transport."
}
```

* `status` is `not_affected`, `affected`, `fixed` or `under_investigation`. Only `not_affected` and `fixed` remove a finding.
* `not_affected` requires a `justification` from the OpenVEX vocabulary (`component_not_present`, `vulnerable_code_not_present`, `vulnerable_code_not_in_execute_path`, `vulnerable_code_cannot_be_controlled_by_adversary`, `inline_mitigations_already_exist`) and an `impact_statement`.
* `affected` requires an `action_statement`.
* Take the subcomponent purl from the finding in `grype.json` (`.matches[].artifact.purl`). Qualifiers can be dropped, because a statement without them matches every architecture and distribution qualifier.
* Raise `version` and `timestamp` with each change, and list every statement in the pull request for review.

## What a release publishes

Every release renders each image's document with the product narrowed to the published digest. It attaches the result to that digest as a keyless cosign attestation of type `openvex`, signed by the release workflow, and uploads it as the `<image>.openvex.json` release asset, listed in `SHA256SUMS`. A document with no statements is published too: the attestation's subject is the image digest, and the empty list says that no finding was suppressed.

### Verify and apply it

```sh
image=ghcr.io/ploeg-hq/ploegd@sha256:<digest>
cosign verify-attestation --type openvex \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github\.com/ploeg-hq/ploeg/\.github/workflows/release\.yml@' \
  "$image" | jq -r '.payload' | base64 -d | jq '.predicate' > ploegd.openvex.json

grype "$image" --vex ploegd.openvex.json
trivy image --vex ploegd.openvex.json "$image"
```

The release asset is the same document; check it against `SHA256SUMS` before use. Dependency-Track imports CycloneDX VEX only, and the release ships SPDX SBOMs, so these documents do not load there yet ([ADR 0145](../../docs/adrs/0145-a-release-publishes-no-image-over-its-cve-budget-and-attaches-signed-openvex-to-every-digest.md), point 8).

### The gate's verdict, for admission control

The gate also writes `cve-budget.json`: the image reference, `verdict` (`pass` or `fail`), `mode`, the budget, the Critical and High counts left after OpenVEX, how many findings OpenVEX suppressed, and the scanner. A release attaches it to each digest as a keyless cosign attestation of predicate type `https://github.com/ploeg-hq/ploeg/attestations/cve-budget/v1` and uploads it as the `<image>.cve-budget.json` release asset. The release refuses to attest a verdict that is not `pass` under `enforce` for that exact digest.

An admission controller can require it instead of re-deriving the budget. A Kyverno `verifyImages` rule matches `ghcr.io/ploeg-hq/*`, uses a keyless attestor with the issuer and identity above, sets `predicateType` to the URI above, and checks `{{ verdict }}` equals `pass` and `{{ mode }}` equals `enforce`. An image with no such attestation, which includes every release before this attestation existed, does not satisfy that rule.

```sh
cosign verify-attestation --type https://github.com/ploeg-hq/ploeg/attestations/cve-budget/v1 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github\.com/ploeg-hq/ploeg/\.github/workflows/release\.yml@' \
  "$image" | jq -r '.payload' | base64 -d | jq '.predicate'
```

## Current statements

None. On 2026-10-24 each image built from `development` scanned at zero findings of any severity with Grype 0.121.0 ([evidence](../../docs/research/2026-10-24-hardened-image-bases.md)).
