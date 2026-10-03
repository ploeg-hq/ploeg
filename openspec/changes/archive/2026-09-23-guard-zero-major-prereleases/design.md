## Context

Ploeg uses the shared `makeConfig` Helm release pipeline. Its analyzer contains an explicit breaking-to-major rule; additional lower-priority rules cannot downgrade it because the analyzer selects the highest matching release type. The release engine also handles existing-release channel promotion before `verifyRelease`.

## Goals / Non-Goals

Keep automatic releases experimental and retain honest compatibility notes. External withdrawal is a separate authorized operation. Runtime domain rules and accepted ADRs remain unchanged, including [ADR 0004](../../../docs/adrs/0004-forgejo-leading-home-github-mirror-module-path.md) and [ADR 0020](../../../docs/adrs/0020-published-artifacts-name-the-mirror-as-source.md).

## Decisions

Build the existing shared configuration, locate its single analyzer, and replace its breaking rule's major result with minor. Reject an unexpected analyzer shape or any remaining major release rule. Add a local plugin with `verifyConditions` restricting the active branch to the development rc branch and `verifyRelease` validating the exact 0.x rc version and matching standard tag. Keep these restrictions in source with no environment bypass.

Run dependency-free policy checks and behavioral analyzer/version-calculator tests in the existing pinned release image before the composite action. Local validation may resolve the same installed package family through `NODE_PATH`; CI always tests its own actual packages. Record exact tool versions with test output.

The artifact-publishing parse job independently accepts only the same zero-major candidate tag shape. Reusable publisher calls receive a guarded `enabled` input based on successful parse and a nonempty version; registry mirrors also require the Harbor build to succeed. This preserves the version barrier when Forgejo flattens a reusable workflow and discards its caller's outer conditions.

## Risks / Trade-offs

Keeping a mistaken semantic-version tag in discovery can still produce a major next version. The guard deliberately stops that result; it does not silently hide release history. Withdrawal preserves the original commit under a non-semver archive reference and removes only the invalid semantic-version tag. An empty valid history also computes semantic-release's default 1.0 first release and is blocked.

An upgrade changing private version-calculation module paths fails the executable test and requires review. A human wanting stable or 1.x releases must update this policy, its tests and the decision record explicitly.

## Validation

Behavioral tests cover original and corrected historical analysis, subsequent version calculation, retained breaking notes, malformed input and branch/version/tag rejection. Use the installed plugin pipeline to verify failures, and assert CI invokes the suite before release. Run the repository ledger, OpenSpec, Go and Helm gates before delivery.
