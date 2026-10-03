---
type: explanation
audience: [owner, contributor, integrator]
owner: ploeg
last_verified: 2026-10-03
verified_by: "Owner-approved separation scope, source boundary and public approval issue; CNCF acceptance remains pending"
---

# Ploeg scope and stewardship

Ploeg is a reusable self-hosted control plane for authorizing, budgeting, coordinating and recording agent work. Its scope includes the controller, workers, execution and operator contracts, PostgreSQL migrations, tracker/forge integration interfaces, Helm deployment chart and their documentation.

The independent project lives at `ploeg-hq/ploeg`. Unfold and Vloer are consumers, not part of the proposed Ploeg CNCF application. WebGrip production infrastructure, hosted-product business logic, customer data, private credentials and deployment repositories are excluded.

Ryan Grippeling approved independent development and CNCF preparation on 2026-10-03. [The public parent decision](https://github.com/webgrip/unfold/issues/1) still needs the full Unfold maintainer roster and any additional votes before it can establish consensus. The [maintainer roster](../../MAINTAINERS.md) identifies current Ploeg stewardship.

GitHub Issues owns project work and public pull requests own source changes. Maintainers review changes within the execution authority, credential, accounting and compatibility boundaries documented in the contracts. Architectural decisions use the append-only ADR ledger. Contributors may propose maintainership through a public issue; nominees must explicitly consent and demonstrate sustained review and operational work. Until more maintainers join, decisions concentrate in one person; broader review, conflict handling and independent conduct appeals remain readiness work.

Preparing for CNCF does not authorize signing a contribution agreement or transferring trademarks, domains, accounts or repositories. Those actions require a concrete later owner decision and CNCF's process. No CNCF membership or acceptance is claimed.
