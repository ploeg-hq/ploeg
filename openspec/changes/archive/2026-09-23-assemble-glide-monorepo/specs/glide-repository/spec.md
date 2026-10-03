## ADDED Requirements

### Requirement: Preserve source histories and reviewed content

Glide SHALL retain both source histories, the audited working-tree changes, and separate application directories. Imported tags MUST have collision-free application namespaces.

#### Scenario: Verify an imported application

- **WHEN** the migration completes
- **THEN** each recorded source HEAD is an ancestor of Glide and the import manifest accounts for every input path

### Requirement: Preserve independent application operation

Applications SHALL retain their existing package, image, chart and module identities. Root commands and CI SHALL validate both applications from their new paths, including the cross-application contract.

#### Scenario: Validate the new checkout

- **WHEN** the root verification command runs
- **THEN** application tests, document checks and cross-application qualification execute with explicit results

### Requirement: Maintain one shared documentation source

Shared product language and workflows SHALL live in root documentation. Service contracts and decision records SHALL remain owned by their applications.

#### Scenario: Follow a moved guide

- **WHEN** a reader uses an old local documentation path
- **THEN** the guide remains available through a redirect or points to its canonical destination
