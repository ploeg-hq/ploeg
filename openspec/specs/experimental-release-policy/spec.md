# experimental-release-policy Specification

## Purpose

Independent Ploeg releases start at v0.1.0 under github.com/ploeg-hq/ploeg. ADR 0063 records the explicit owner approval replacing automatic shared release-candidate publication. Historical release-policy evidence remains in the archived change.

## Requirements

### Requirement: Release selection is explicit

The project MUST publish only reviewed tags in the v0.x.y or v0.x.y-rc.N format. Conventional commits MUST NOT automatically select or publish a release. Breaking changes MUST retain compatibility and migration notes.

#### Scenario: A stable zero-major release is selected

- **WHEN** a maintainer tags a reviewed main commit v0.1.0 with matching chart versions
- **THEN** validation permits publication after the standalone gates pass

### Requirement: Release provenance is checked

Publication MUST require an existing tag pointing to the checked-out commit, reachable from origin/main, with matching chart version and appVersion. Inputs MUST reach shell commands through environment variables and quoted arguments.

#### Scenario: An off-branch or mismatched tag is supplied

- **WHEN** the tag is not reachable from main or its chart versions differ
- **THEN** validation fails before artifact publication

### Requirement: Published identities are immutable

Publication MUST refuse an existing release or artifact tag and MUST stop if artifact absence cannot be confirmed. It MUST NOT publish a latest alias or modify a deployment. Older module namespaces and tags MUST remain unchanged.

#### Scenario: A version is already occupied

- **WHEN** a release or image or chart already exists at the selected version
- **THEN** publication stops and recovery requires a reviewed decision without overwriting the artifact

### Requirement: Experimental limits remain explicit

Release notes MUST identify supported architectures and qualification limits. Zero-major stable versions MUST NOT claim production readiness or CNCF acceptance.

#### Scenario: A major tag is supplied

- **WHEN** a publisher receives v1.0.0 or a malformed version
- **THEN** validation rejects it before publication
