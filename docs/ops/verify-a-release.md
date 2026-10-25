---
type: how-to
audience: [operator, integrator, auditor]
owner: ploeg
last_verified: 2026-10-25
verified_by: "Ran every command below against v0.2.0-rc.72 from an empty directory, without a GitHub login: curl, sha256sum, git ls-remote, cosign v2.4.3, helm v4.3.0, jq, docker buildx imagetools"
---

# Verify a release

Every Ploeg release, candidate or stable, can be checked by anyone with no account and no access to the project. This page checks, in order, that:

1. the assets you downloaded are the ones the release lists
2. they come from the commit the tag names
3. every image and the chart were signed by the project's release workflow at that commit
4. the chart pins those images
5. each image carries the project's signed OpenVEX statement and CVE budget verdict
6. the SBOM and build provenance name that commit too

You need `curl`, `jq`, `sha256sum`, `git`, [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) 2.x, [Helm](https://helm.sh/docs/intro/install/) 3.8 or later, and, for the last step only, `docker buildx`. Releases are listed at <https://github.com/ploeg-hq/ploeg/releases>; every 0.2 release so far is a candidate (`-rc.N`).

## 1. Download and check the checksums

```sh
VERSION=0.2.0-rc.72
mkdir -p "ploeg-$VERSION" && cd "ploeg-$VERSION"
base="https://github.com/ploeg-hq/ploeg/releases/download/v$VERSION"
curl -fsSLO "$base/SHA256SUMS"
awk '{print $2}' SHA256SUMS | xargs -I{} curl -fsSLO "$base/{}"
sha256sum -c SHA256SUMS
```

This fetches `SHA256SUMS` and every file it lists, which is every other release asset, without the GitHub API. Every line must end in `OK`. `SHA256SUMS` covers the binary tarball, the chart, the image metadata, `signed-artifacts.txt`, `source-commit.txt`, `PROVENANCE.md`, `registry-values.yaml` and the OpenVEX and CVE budget documents.

## 2. Match the source revision to the tag

`source-commit.txt` holds the commit the release was built from. It must be the commit the tag names:

```sh
COMMIT=$(cat source-commit.txt)
TAG_COMMIT=$(git ls-remote https://github.com/ploeg-hq/ploeg "refs/tags/v$VERSION" "refs/tags/v$VERSION^{}" | tail -n1 | cut -f1)
[ "$COMMIT" = "$TAG_COMMIT" ] && echo "source-commit.txt $COMMIT is the commit tag v$VERSION names"
```

Release tags cannot be moved or deleted (repository rule `Release tags are immutable`).

## 3. Check the signatures

`signed-artifacts.txt` lists the chart and every image by digest. Each is signed keyless with cosign by `.github/workflows/release.yml` in `ploeg-hq/ploeg`, through GitHub Actions OIDC, and the signature is in the public Rekor log. No signing key exists.

```sh
ISSUER=https://token.actions.githubusercontent.com
IDENTITY='^https://github\.com/ploeg-hq/ploeg/\.github/workflows/release\.yml@refs/(heads/development|heads/main|tags/v0\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?)$'
while read -r artifact; do
  cosign verify "$artifact" \
    --certificate-oidc-issuer "$ISSUER" --certificate-identity-regexp "$IDENTITY" \
    --certificate-github-workflow-repository ploeg-hq/ploeg \
    --certificate-github-workflow-sha "$COMMIT" > /dev/null
done < signed-artifacts.txt
```

cosign prints one `Verification for <artifact>` block per line of the file and exits non-zero on the first failure. The identity is exact per release kind:

| Release | Certificate identity |
| --- | --- |
| Candidate, cut on a merge to `development` | `https://github.com/ploeg-hq/ploeg/.github/workflows/release.yml@refs/heads/development` |
| Stable, cut by a tag push | `https://github.com/ploeg-hq/ploeg/.github/workflows/release.yml@refs/tags/v0.x.y` |
| Re-published by hand ([runbook](release-runbook.md#recover-a-failed-publication)) | `…/release.yml@refs/heads/<branch it was dispatched from>` |

`--certificate-github-workflow-sha` ties each signature to the commit from step 2, so a signature from another release does not pass. A release re-published by hand was signed by the run that published it, whose commit is the head of the branch it was dispatched from; for such a release that check fails while the identity check passes. Drop `--certificate-github-workflow-sha` for it and read the `GitHub Workflow Ref` and `GitHub Workflow SHA` lines cosign prints instead.

## 4. Check the chart

The chart asset must be the signed OCI chart, and it must pin the signed images:

```sh
mkdir -p oci && helm pull oci://ghcr.io/ploeg-hq/charts/ploeg --version "$VERSION" -d oci
cmp "oci/ploeg-$VERSION.tgz" "ploeg-$VERSION.tgz" && echo "the chart asset is the signed OCI chart"
for digest in $(tar -xzOf "ploeg-$VERSION.tgz" ploeg/values.yaml | sed -n 's/^ *digest: "\(sha256:.*\)"/\1/p'); do
  grep -qF "@$digest" signed-artifacts.txt && echo "the chart pins signed $digest"
done
```

`helm pull` prints the chart's `Digest:`; it is the chart line of `signed-artifacts.txt`. Three `the chart pins signed` lines follow, for `ploegd`, `ploeg-worker` and `claude-runner`.

## 5. Check the OpenVEX and CVE budget attestations

Each image carries two signed in-toto attestations from the same workflow: its OpenVEX document (`--type openvex`) and the verdict of the CVE gate it passed before it was tagged (`--type https://github.com/ploeg-hq/ploeg/attestations/cve-budget/v1`, [ADR 0145](../adrs/0145-a-release-publishes-no-image-over-its-cve-budget-and-attaches-signed-openvex-to-every-digest.md)). The attested documents must equal the release assets, and the verdict must be `pass` under `enforce` for that exact digest:

```sh
for image in $(grep -v /charts/ signed-artifacts.txt); do
  name=$(basename "${image%@*}")
  cosign verify-attestation "$image" --type openvex \
    --certificate-oidc-issuer "$ISSUER" --certificate-identity-regexp "$IDENTITY" \
    --certificate-github-workflow-sha "$COMMIT" \
    | jq -r .payload | base64 -d | jq -S .predicate > "$name.openvex.verified.json"
  jq -S . "$name.openvex.json" | cmp - "$name.openvex.verified.json" && echo "$name: OpenVEX attestation matches $name.openvex.json"
  cosign verify-attestation "$image" --type https://github.com/ploeg-hq/ploeg/attestations/cve-budget/v1 \
    --certificate-oidc-issuer "$ISSUER" --certificate-identity-regexp "$IDENTITY" \
    --certificate-github-workflow-sha "$COMMIT" \
    | jq -r .payload | base64 -d | jq -er --arg image "$image" \
      '.predicate | select(.image == $image and .verdict == "pass" and .mode == "enforce") | "\(.imageName): cve-budget \(.verdict) (\(.mode)), critical \(.found.critical), high \(.found.high)"'
done
```

To scan an image with its statements applied, pass the verified document to your scanner: `grype "$image" --vex ploegd.openvex.verified.json` ([OpenVEX](../../ops/vex/README.md#verify-and-apply-it)). An admission controller can require the CVE budget attestation instead ([the gate's verdict](../../ops/vex/README.md#the-gates-verdict-for-admission-control)).

## 6. Read the SBOM and build provenance

BuildKit attaches an SPDX SBOM and SLSA build provenance to each image as attestation manifests inside the image index. They are not signed on their own, but the index you verified in step 3 names them by digest, so a changed SBOM or provenance changes the digest the signature covers.

```sh
image=$(grep /ploegd@ signed-artifacts.txt)
docker buildx imagetools inspect "$image" --format '{{json .Provenance}}' \
  | jq -r '.SLSA.buildDefinition.externalParameters.request.args["build-arg:IMAGE_REVISION"]'
docker buildx imagetools inspect "$image" --format '{{json .SBOM}}' \
  | jq -r '.SPDX | "\(.spdxVersion), \(.packages | length) packages"'
```

The provenance's `IMAGE_REVISION` is the commit from step 2; the image's `org.opencontainers.image.revision` label carries it too. `.SLSA.buildDefinition.resolvedDependencies` lists the base images and BuildKit frontends by digest. The SBOM lists each Go module and OS package syft found, with its version; `| jq '.SPDX.packages[] | {name, versionInfo}'` prints them. The same holds for `ploeg-worker` and `claude-runner`.

`PROVENANCE.md` is the project's statement of where the code came from and under which licence, and `THIRD-PARTY-NOTICES` inside the tarball and the images lists the licences of every bundled dependency.

## What this does not prove

- The binary tarball `ploeg_<version>_linux_amd64.tar.gz`, `SHA256SUMS` itself and the [contract bundle](../adrs/0117-every-release-publishes-a-contract-bundle-for-consumers.md) are not signed. Step 1 shows the tarball matches `SHA256SUMS`, and both come from the same GitHub release; nothing ties the tarball to the release workflow the way step 3 ties the images and the chart. Prefer the images or the chart where that matters.
- A candidate is a pre-release. Passing these checks says who built it and from what, not that it is fit for production; see [releases](release-versioning.md) and the [threat model](../security/threat-model.md).
