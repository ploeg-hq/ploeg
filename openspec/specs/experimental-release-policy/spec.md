# experimental-release-policy Specification

## Purpose

Independent Ploeg releases start at v0.1.0 under github.com/ploeg-hq/ploeg. ADR 0063 set the independent version line, and ADR 0115 calculates candidates from conventional commits while stable releases stay reviewed tags. Historical release-policy evidence remains in the archived change.

## Requirements

### Requirement: Release selection is explicit

The project MUST publish only tags in the v0.x.y or v0.x.y-rc.N format. A release candidate MUST be calculated by semantic-release from the conventional commits merged to `development` since the previous tag, and MUST be refused before any commit, tag or release is written unless it is a new `0.x.y-rc.N`. A breaking change MUST bump the minor version and MUST keep its notice in the release notes. A stable v0.x.y MUST NOT be calculated from commits; it MUST be a reviewed tag on `main`.

#### Scenario: A stable zero-major release is selected

- **WHEN** a maintainer tags a reviewed main commit v0.1.0 with matching chart versions
- **THEN** validation permits publication after the standalone gates pass

#### Scenario: A releasable commit is merged to development

- **WHEN** a `fix` commit lands on `development` after `v0.2.0-rc.26`
- **THEN** semantic-release cuts `v0.2.0-rc.27` with matching chart versions and the release workflow publishes it

#### Scenario: A breaking change is merged to development

- **WHEN** a commit with a breaking-change notice lands on `development` after `v0.2.0-rc.27`
- **THEN** the next candidate is `v0.2.0-rc.28`, not a 1.x version

#### Scenario: The calculated candidate is not new

- **WHEN** semantic-release calculates a candidate whose tag already exists, or a stable or 1.x version
- **THEN** `scripts/release.sh check` fails before the chart, changelog or tag are written

### Requirement: Release provenance is checked

Publication MUST require an existing tag pointing to the checked-out commit, reachable from origin/main, with matching chart version and appVersion. Inputs MUST reach shell commands through environment variables and quoted arguments.

#### Scenario: An off-branch or mismatched tag is supplied

- **WHEN** the tag is not reachable from main or its chart versions differ
- **THEN** validation fails before artifact publication

### Requirement: Published identities are immutable

Publication MUST refuse an existing published release or artifact tag, MUST refuse a draft release that already carries assets, and MUST stop if artifact absence cannot be confirmed. It MUST NOT publish a latest alias or modify a deployment. Older module namespaces and tags MUST remain unchanged.

#### Scenario: A version is already occupied

- **WHEN** a release or image or chart already exists at the selected version
- **THEN** publication stops and recovery requires a reviewed decision without overwriting the artifact

#### Scenario: A candidate's draft release is published

- **WHEN** the release candidate workflow has opened a draft release with no assets for its tag
- **THEN** publication uploads the assets to that draft and publishes it as a prerelease

### Requirement: Experimental limits remain explicit

Release notes MUST identify supported architectures and qualification limits. Zero-major stable versions MUST NOT claim production readiness or CNCF acceptance.

#### Scenario: A major tag is supplied

- **WHEN** a publisher receives v1.0.0 or a malformed version
- **THEN** validation rejects it before publication
