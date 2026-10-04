# Ploeg knowledge

An Open Knowledge Format (OKF v0.1) bundle about this repository. Ploeg's worker
briefs Runs from it (proposed; proof of concept).

- [The harness contract](concepts/harness-contract.md)
- [Commits and staging](conventions/commits.md)
- [Contract schemas and Go types change together](conventions/contracts-change-together.md)
- [Fake external services with httptest](conventions/fake-external-services-with-httptest.md)
- [Store migrations are append-only](conventions/migrations-append-only.md)
- [The harness gets placeholders; the worker keeps the credentials](decisions/harness-gets-placeholders.md)
- [The pull request is the blackboard](decisions/pr-is-the-blackboard.md)
- [Where Run logic lives](facts/where-run-logic-lives.md)
- [Do not run the adr-writer validator here](pitfalls/do-not-run-the-adr-writer-validator.md)
- [Migration numbers collide across open branches](pitfalls/migration-numbers-collide-across-open-branches.md)
- [A new OutcomeReport field must be carried through every copy](pitfalls/new-outcome-fields-must-be-carried-through.md)
- [A new PLOEG_ setting needs the configuration reference regenerated](pitfalls/new-settings-regenerate-the-configuration-reference.md)
- [Registry pulls time out in the worker sandbox](pitfalls/registry-pulls-time-out-in-the-sandbox.md)
