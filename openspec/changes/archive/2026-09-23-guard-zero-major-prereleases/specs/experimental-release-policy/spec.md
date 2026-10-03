## ADDED Requirements

### Requirement: Breaking changes remain in the experimental series

The release configuration MUST map every breaking-change rule to a minor increase, preserve compatibility notices in release notes, and retain the `v${version}` tag format.

#### Scenario: Historical breaking commit after the last valid release candidate

- **WHEN** the installed analyzer examines the managed-authentication breaking commit after `v0.3.0-rc.4`, with the withdrawn 1.x tag absent
- **THEN** it returns a minor release type and the installed version calculator selects the next 0.x release candidate

### Requirement: Publication requires experimental release policy

Automatic release MUST be limited to `development` and a `0.x.y-rc.N` version. An early lifecycle guard MUST stop other branches before existing-release promotion. `verifyRelease` MUST reject stable versions, major versions above zero, missing versions, and mismatched tags before preparation, tagging, or publishing. Changing this restriction MUST require an explicit repository policy change rather than a commit footer or environment switch.

#### Scenario: A breaking commit or stale tag proposes a major release

- **WHEN** the computed next version is `1.0.0-rc.1`
- **THEN** release verification fails before preparation or publication

#### Scenario: Main is selected for promotion

- **WHEN** semantic-release selects `main`
- **THEN** condition verification fails before any existing release can acquire the default channel

#### Scenario: A stable zero-major version is proposed

- **WHEN** the computed version is `0.4.0`
- **THEN** release verification fails even though its major version is zero

### Requirement: CI proves the effective release behavior

The existing release workflow MUST run tests against its installed shared configuration, analyzer and semantic-release version calculator before invoking the release composite. Tests MUST demonstrate the previous major result and the corrected minor result, rather than only matching configuration text.

#### Scenario: Shared release configuration changes

- **WHEN** the installed configuration no longer contains the expected analyzer or adds an unsupported major rule
- **THEN** the policy or its tests fail visibly before publication

### Requirement: Artifact publication enforces the same version boundary

The artifact-publishing workflow MUST reject stable, major and malformed tags for both release events and manual dispatch before emitting a version to downstream jobs. It MUST pass the tag through an environment variable rather than interpolating untrusted input into shell source, and MUST never emit a latest image alias for a release candidate.

#### Scenario: A manually created release bypasses the release analyzer

- **WHEN** artifact publication receives `v1.0.0-rc.1` or `v0.4.0`
- **THEN** its initial parsing job fails and downstream publishers receive no version
