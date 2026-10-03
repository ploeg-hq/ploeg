## Context

Current source confirms De Vloer independently starts work, while Ploeg only exposes queue depths to it. Worker images inherit broker management credentials. The change implements the owner's requested unified baseline while preserving ADRs 0005, 0008, 0010 and 0012 and rules R1, R2, R4 and R6. ADR 0024 records the operator boundary before implementation.

## Goals / Non-Goals

Goals are authenticated visibility, common execution admission, explicit intervention, durable command identity and worker authority isolation. Existing adapters remain reusable. This change does not add a Ploeg board, a second scheduler, automatic publication or unqualified throughput claims.

## Decisions

The operator read API is versioned, uses string identifiers/cursors for bigint safety, allowlists response fields and never emits credential-bearing raw audit records. Named static consumer tokens resolve from environment references; team scope is enforced by Ploeg. De Vloer independently maps logged-in users to allowed teams and defaults unmapped non-administrators to no access. Unknown cost and unsupported pause state remain explicit null/unknown values.

Existing audit sequence allocation does not establish commit order. Read pagination is labelled snapshot consistency; continuous execution events require per-execution revision serialization and durable replay. No reconnect guarantee is inferred from sequence IDs alone.

The governed interactive path admits an explicitly manual-origin Work Item under Ploeg authority. The existing workbench executor operates under a scoped, expiring grant. Commands identify actor and request and use expected revision where state-sensitive. Detach changes no authority. Replacement execution requires confirmed stop and reconciled side effects; unavailable authority blocks further paid turns. Native sessions remain opaque and paid submission ambiguity never retries automatically.

Ploeg's control process owns broker minting/metering/blocking. Worker environment construction uses an explicit allowlist. Run accounting records remain reserved after process expiry while external charges are unresolved. Migrations append new numbered files. Changes to pending/claim predicates must also change the matching KEDA query and its golden tests.

## Risks / Trade-offs

External Git writers remain a compatibility boundary until trusted publication fencing exists. New governed execution must not claim safe takeover of a legacy writer with unrevoked direct Git access. Static consumer trust delegates user identity from the authenticated workbench and therefore requires separately scoped read and execution permissions. Loss of coordinator connectivity favors a visible interruption over duplicate paid work.

## Migration Plan

Ship authenticated reads first. Deploy server-side broker support before worker images remove management credentials. Enable governed execution explicitly for configured teams after its integration tests pass. Deploy standalone and shared profiles deliberately. A profile with shared execution requires Ploeg for every new launch; existing already-bound sessions never fall back to local authority. Tracker imports into that shared profile remain refused until canonical work identity binding exists. Rollback disables new admission while retaining durable records and reconciles already-issued capabilities.

## Open Questions

The former tracker-only front-door constraint is extended for manually originated work by the owner's explicit unification request. This is recorded as a proposed decision rather than rewriting any accepted historical ADR. Exact live-provider and cluster qualification depends on existing configured credentials and deployment access; local doubles are labelled and cannot substitute for paid/cluster evidence.
