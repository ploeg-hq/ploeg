# Operator knowledge

## ADDED Requirements

### Requirement: Knowledge requests name the acting person

Every operator knowledge request SHALL name the acting person, their role in
the consumer's Tenant (`viewer`, `member` or `admin`) and, for a client user,
their Client. ploegd SHALL refuse a request without them and SHALL audit every
write with the consumer and the person (ADR-0089).

#### Scenario: A request without a person

- **WHEN** a consumer reads knowledge without naming the person
- **THEN** ploegd refuses it

### Requirement: A person reads only their Knowledge Scopes

`GET /api/v1/operator/knowledge` SHALL return, for a Tenant member or admin,
the Tenant's concepts and notes; for a client user, concepts scoped to their
Client and their Client's repositories and notes of their Client's Work Items,
and no concept scoped `tenant`. An object outside the person's scope SHALL
return 404.

#### Scenario: A client user asks for another Client's concept

- **WHEN** a client user of Client A reads a concept scoped to Client B
- **THEN** ploegd answers 404

#### Scenario: A client user gets no Tenant-wide concept

- **WHEN** a client user searches knowledge
- **THEN** no concept scoped `tenant` is in the result

### Requirement: A person writes Work Item Notes and requests Promotions

`POST /api/v1/operator/work-items/{id}/notes` SHALL let a Tenant member or
admin who can see the Work Item write a Work Item Note attributed to them.
`POST /api/v1/operator/promotions` SHALL create a pending Promotion; a Tenant
admin's request SHALL be accepted at once and recorded as their acceptance. A
client user MAY request a Promotion about their Client's repositories and
SHALL NOT write notes. Both routes SHALL be idempotent per consumer and
request key.

#### Scenario: A repeated note request

- **WHEN** the same note request with the same key arrives twice
- **THEN** one note exists and the second response names it

#### Scenario: A member's wider note waits

- **WHEN** a Tenant member requests a Promotion to repository scope
- **THEN** the Promotion is pending and no Run outside the note's root reads it

### Requirement: Only a Tenant admin decides a Promotion

`GET /api/v1/operator/promotions` SHALL list pending Promotions within the
person's scope, and `POST /api/v1/operator/promotions/{id}/accept` and
`/reject` SHALL succeed only for a Tenant admin.

#### Scenario: A member tries to accept

- **WHEN** a Tenant member accepts a Promotion
- **THEN** ploegd refuses it and the Promotion stays pending

#### Scenario: A lost accept response

- **WHEN** an admin repeats an accept after a lost response
- **THEN** the Promotion is accepted once and one concept exists
