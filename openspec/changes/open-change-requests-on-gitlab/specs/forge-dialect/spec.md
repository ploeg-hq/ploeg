# Forge dialect

## ADDED Requirements

### Requirement: A Run acts against the forge its Work Item names

The forge DIALECT SHALL be a property of the Work Item, carried on
`Target.Forge` and delivered to the worker on the claim, consistent with
ADR-0014 and ADR-0023. Where a Work Item names no forge, the worker SHALL use
the deployment's configured default. An absent dialect SHALL mean `forgejo`, so
that a Task Spec or stored Target written before the field existed keeps its
meaning.

The forge URL and credential SHALL remain deployment-global: a worker pod holds
one of each.

#### Scenario: A GitLab target is worked on GitLab

- **GIVEN** a Work Item whose Target names the `gitlab` forge
- **WHEN** a writing Run claims it
- **THEN** the Run opens its change request through the GitLab API

#### Scenario: An unset dialect keeps its old meaning

- **GIVEN** a Work Item whose Target names no forge
- **AND** a deployment that configures no default dialect
- **THEN** the Run behaves exactly as it did before the field existed

#### Scenario: The engine and the worker cannot disagree

- **WHEN** ploegd publishes findings for a Shift
- **THEN** it resolves the forge from the same Work Item field the worker used
  to open the change request

### Requirement: The Task Spec carries the dialect additively

`taskspec.v1` SHALL gain an optional `repo.forge` field constrained to the
dialects the worker implements. The published schema in `docs/contracts/` and
the Go type SHALL change in the same change, additively, per
`docs/contracts/README.md`.

#### Scenario: The Go type and the published schema cannot drift

- **WHEN** a field is added to `RepoRef` without the matching schema edit
- **THEN** the contract test fails

### Requirement: An unknown dialect stops the Run loudly

A dialect the worker does not implement SHALL produce an error naming it. The
worker SHALL NOT fall back to another dialect.

A silent fallback would poll a real endpoint shape against the wrong host and
report "no change request found" indefinitely, which is indistinguishable from
an agent that never opened one — the failure this capability exists to remove.

#### Scenario: An unimplemented forge is named in the error

- **GIVEN** a Work Item whose Target names a forge the worker cannot speak
- **THEN** the Run fails with an error naming that forge

#### Scenario: Crash behaviour is unchanged

- **WHEN** a Run fails because its dialect is unknown
- **THEN** it reports a stuck Outcome carrying a reason (R4) after claiming,
  rather than exiting and stranding its Lease for the sweeper (R2)

### Requirement: The change-request poll is forge-specific and scoped to the Run

The worker SHALL find an already-open change request for its own branch through
the forge's own API, matching on head branch and — where a base branch is
configured — on base branch. It SHALL query only the Run's own target.

Where the forge supports filtering by source branch server-side, the worker
SHALL use it, so the result cannot be truncated by unrelated open requests in a
busy repository.

#### Scenario: A change request against the wrong base is not this Run's

- **GIVEN** an open change request from this Run's branch to a different base
- **THEN** it is not treated as this Run's change request (VIK-589)

#### Scenario: A busy repository does not hide the Run's own request

- **GIVEN** a repository with more open change requests than one page
- **WHEN** the forge can filter by source branch
- **THEN** the Run's own request is still found

### Requirement: The delivery contract names the forge's own vocabulary

The prompt SHALL use the noun the target forge uses for a change request, and
SHALL name a concrete endpoint for opening one. The noun and the endpoint SHALL
be derived from the same dialect, so they cannot disagree.

An agent told to open a "pull request" against GitLab searches its tools and
the repository's documentation for something that does not exist there; the
noun is not cosmetic.

#### Scenario: A GitLab writer is told to open a merge request

- **GIVEN** a writing Run against a `gitlab` target
- **THEN** the delivery contract says "merge request" and names the GitLab
  endpoint
- **AND** it names no Forgejo endpoint

#### Scenario: The Forgejo contract is unchanged

- **GIVEN** a writing Run against a target with no dialect set
- **THEN** the delivery contract is what it was before this capability existed

#### Scenario: A reading Run is told what it may not do, in the right words

- **GIVEN** a reading Run against a `gitlab` target
- **THEN** it is told not to open, update, comment on or merge a merge request
- **AND** it is told where its findings will be posted

### Requirement: Selecting a forge is visible in the rendered worker pod

The chart SHALL render one active forge, selected by configuration, and the
rendered worker pod for each supported forge SHALL be pinned by a committed
golden.

The worker pod is a security boundary: selecting a forge changes which Secret
it mounts and which API its agent is told to call. A change to either SHALL
appear in a reviewable diff.

#### Scenario: The reader/writer credential split holds on every forge

- **GIVEN** a planned Team with reading and writing Roles
- **AND** a read-only credential configured for the active forge
- **THEN** reading Roles draw the read-only credential and the writing Role
  draws the read-write one (ADR-0013 tier 1)

#### Scenario: An unused forge block need not exist

- **GIVEN** a deployment that selects one forge and empties the other's block
- **THEN** the chart renders
