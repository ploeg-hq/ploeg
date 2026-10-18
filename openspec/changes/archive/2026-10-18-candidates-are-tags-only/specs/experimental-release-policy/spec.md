## MODIFIED Requirements

### Requirement: Release selection is explicit

The project MUST publish only tags in the v0.x.y or v0.x.y-rc.N format. A release candidate MUST be calculated by semantic-release from the conventional commits merged to `development` since the previous tag, MUST be refused before its tag is written unless it is a new `0.x.y-rc.N`, and MUST be a tag on the merged commit with nothing written back to `development`. Its chart MUST be packaged with the tag's version as `version` and `appVersion`. A breaking change MUST bump the minor version and MUST keep its notice in the release notes. A stable v0.x.y MUST NOT be calculated from commits; it MUST be a reviewed tag on `main` whose `Chart.yaml` matches it.

#### Scenario: A stable zero-major release is selected

- **WHEN** a maintainer tags a reviewed main commit v0.1.0 with matching chart versions
- **THEN** validation permits publication after the standalone gates pass

#### Scenario: A releasable commit is merged to development

- **WHEN** a `fix` commit lands on `development` after `v0.2.0-rc.26`
- **THEN** semantic-release tags that commit `v0.2.0-rc.27`, `development` gains no commit, and the release workflow publishes a chart packaged at `0.2.0-rc.27`

#### Scenario: A breaking change is merged to development

- **WHEN** a commit with a breaking-change notice lands on `development` after `v0.2.0-rc.27`
- **THEN** the next candidate is `v0.2.0-rc.28`, not a 1.x version

#### Scenario: The calculated candidate is not new

- **WHEN** semantic-release calculates a candidate whose tag already exists, or a stable or 1.x version
- **THEN** `scripts/release.sh check` fails before the tag is written
