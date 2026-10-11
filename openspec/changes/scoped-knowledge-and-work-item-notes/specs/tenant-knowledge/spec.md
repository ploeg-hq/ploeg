# Tenant knowledge

## ADDED Requirements

### Requirement: ploegd alone holds a Tenant's graph credentials

ploegd SHALL hold each Tenant's run actor and review actor tokens. The worker,
the harness, the claim response, the knowledge response and the TaskSpec MUST
NOT carry a graph credential (ADR-0034, ADR-0085).

#### Scenario: Nothing in the Run's pod can reach the graph

- **WHEN** a Run is claimed in a deployment with a Tenant graph configured
- **THEN** no graph token appears in the worker's environment, the claim response, the knowledge response or the TaskSpec

### Requirement: The run actor cannot write reviewed knowledge

A deployment's policy SHALL grant a Tenant's run actor read and stored-query
rights on its knowledge and notes graphs and change rights only on the notes
graph's `main`, and SHALL grant it no right on any graph outside its Tenant.

#### Scenario: Another Tenant's graph is refused

- **WHEN** the run actor of Tenant A reads Tenant B's knowledge graph, or any graph that belongs to no Tenant
- **THEN** Omnigraph answers 403

#### Scenario: A write to reviewed knowledge is refused

- **WHEN** the run actor changes the knowledge graph or creates a branch on it
- **THEN** Omnigraph answers 403

### Requirement: A Run reads only its Work Item's Knowledge Scopes

`GET /api/v1/runs/{token}/knowledge` SHALL return the Tenant's concepts scoped
`tenant`, those scoped `client` for the Work Item's Client, those scoped
`repository` for its Work Target's repository, and the Work Item Notes of its
root, each with the graph commit id it was read at. ploegd SHALL derive every
scope from its own records and MUST ignore scope values in the request.

#### Scenario: Another Client's knowledge stays out

- **GIVEN** a Tenant with concepts for Client A and Client B
- **WHEN** a Run of a Client A Work Item fetches its knowledge
- **THEN** it gets Client A's concepts and none of Client B's

#### Scenario: Another root's notes stay out

- **GIVEN** notes under two roots in the same repository
- **WHEN** a Run under the first root fetches its knowledge
- **THEN** it gets the first root's notes only

#### Scenario: A request cannot widen its scope

- **WHEN** a Run's knowledge request names another repository or root
- **THEN** the response is the same as without those parameters

#### Scenario: A Work Item without a Client

- **WHEN** a Run of a Work Item with no Client fetches its knowledge
- **THEN** it gets no concept scoped `client`

### Requirement: Graph knowledge enters the pack as evidence with provenance

The worker SHALL select from the fetched concepts and notes within the pack
budget (ADR-0065), SHALL write notes under a separate heading marked as
unreviewed, and `TaskSpec.knowledge` SHALL record each graph's commit id.

#### Scenario: Notes are labelled as unreviewed

- **WHEN** a pack holds a reviewed concept and a Work Item Note
- **THEN** the index lists the note under the unreviewed notes heading, after reviewed knowledge

### Requirement: A missing graph never stops a Run

When a graph cannot be read at claim, the Run SHALL proceed without it and
`TaskSpec.knowledge` SHALL record the source as unavailable.

#### Scenario: Omnigraph is down at claim

- **WHEN** a Run is claimed while the Tenant's graphs do not answer
- **THEN** the Run starts with its directory sources only and its TaskSpec says the graph sources were unavailable

#### Scenario: ploegd dies while serving knowledge

- **WHEN** ploegd restarts while a worker fetches knowledge
- **THEN** the worker retries within its claim deadline and otherwise proceeds without graph knowledge; the Lease is unaffected (R2)
