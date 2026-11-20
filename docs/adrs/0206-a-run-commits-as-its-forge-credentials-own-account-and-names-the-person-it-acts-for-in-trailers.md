---
status: accepted
date: 2026-11-20
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-05-20
---

# A Run commits as its forge credential's own account and names the person it acts for in trailers

## Context and Problem Statement

The worker wrote every commit, in the agent's checkout, in the base-sync rebase and in the yield commit, as one hard-coded name and e-mail, the same on every forge and every installation. [ADR-0016](0016-forge-registry-and-per-run-repo-scoped-credentials.md) put the git identity on the forge registry entry as a configured value. Configuring it is a second source of truth: the forge already knows which account a credential belongs to, and a configured value can name an account the Run does not push as. Since [ADR-0156](0156-each-run-carries-a-transaction-token-that-names-its-person-and-a-team-without-a-sponsor-runs-nothing.md) every Run also has a person it acts for, and no commit says who that is.

Who authors and commits a Run's commits, and how does a commit name the person and the Run?

## Decision Drivers

* A commit is attributed to the account that pushed it, so the forge links it to that account and nothing has to be kept in step by hand.
* Every commit names the person the Run acts for and the Run, as far as Ploeg can prove them ([ADR-0180](0180-a-call-without-a-live-person-is-attributed-to-its-run-or-a-proven-workload-and-an-administrator-overrides-as-themselves.md)), and never invents what it cannot prove.
* No operator setting for something Ploeg can read from the forge.
* Ploeg names no installation ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)).

## Considered Options

* **ploegd reads the credential's own account from the forge, the claim carries it with the person and the Run, and the worker commits as that account with attribution trailers**
* A configured commit name and e-mail per forge
* Commit as the person the Run acts for

## Decision Outcome

Chosen option: "**ploegd reads the credential's own account from the forge**", because the forge is the one source that knows which account pushes, and the person stays named without being impersonated.

1. **The committer is the forge account.** ploegd reads the account the Run's forge credential belongs to and caches it; a failed read is retried after five minutes.
   * A GitHub App: `GET /app` with the App's JWT gives the slug, `GET /users/<slug>[bot]` the bot user's id. The identity is `<slug>[bot] <<id>+<slug>[bot]@users.noreply.github.com>`.
   * A static GitHub token: `GET /user`, and the account's noreply address `<id>+<login>@users.noreply.github.com`.
   * Forgejo: `GET /api/v1/user` with the bot's token, its full name or login and its e-mail.
   * GitLab: `GET /api/v4/user`, its name and its commit e-mail, else its e-mail.
   * A runtime forge connection ([ADR-0193](0193-a-proven-operator-registers-a-work-target-at-runtime-only-for-a-repository-ploegs-forge-connection-was-granted.md)): the same per dialect, with the credential of the connection the repository's runtime target was granted through, cached per repository.
2. **The claim carries it.** The claim's `attribution` holds `committer` (name and e-mail), `person` and `personProof` (the transaction token's `sub` and `subjectProof`), and `run` (its `txn`), as [ADR-0192](0192-a-runs-forge-is-resolved-from-its-work-target-and-the-claim-carries-the-forge-and-a-credential-minted-for-that-run.md) carries the forge.
3. **The worker commits as that account**, author and committer both, in the checkout, the rebase and the yield commit. When the claim carries no committer, it commits as `ploeg-agent <agent@ploeg.invalid>` and logs why; `.invalid` is reserved, so the fallback names no organisation and no forge links it to an account.
4. **Trailers name the Run, the Work Item and the person.** After `Agent-Trace-Id`, every commit ends with `Ploeg-Run: <run id>`, `Work-Item: <reference> <tracker link>` and `Ploeg-Acts-For: <person id> (<proof>)`. The Task Spec carries them as `trailers` and the prompt's delivery contract lists them; the worker writes them on its own yield commit.
5. **No `Co-authored-by` yet.** Ploeg's person id is `oidc-<hash>`, and Ploeg holds no e-mail for a person, so it cannot write a `Co-authored-by` trailer that a forge links to the person. `Ploeg-Acts-For` names what Ploeg can prove. A `Co-authored-by` trailer follows once Ploeg records a verified e-mail for a person.

This amends ADR-0016's "git identity (name + email used for commits)" on the registry entry: the identity is read from the forge, not configured.

### Consequences

* Good, because a GitHub App Run's commits show the App's bot, and a Forgejo or GitLab Run's commits the bot account, with no setting.
* Good, because every commit names its Run, its Work Item and the person it acts for.
* Bad, because a forge account without a readable e-mail, or a credential that cannot read its own account (a GitHub installation token on a runtime GitHub connection), commits as the fallback. ploegd logs it.
* Bad, because the trailers in the agent's own commits are an instruction in the prompt, not enforced: an agent can leave them out. The yield commit always carries them.
* Neutral, because a pull request's author is unchanged: it is still the account that opened it.
* Bad, because v0.2.0-rc.136 shipped the configured identity this record replaces: `PLOEG_<DIALECT>_COMMIT_NAME` and `_COMMIT_EMAIL`, chart `executor.<forge>.commitName` and `commitEmail`, and the claim's `commitName` and `commitEmail`. Removing them is a recorded break against that candidate (`docs/contracts/breaking-changes.yaml`); no stable release carried them. A worker of rc.136 commits as its own fallback against a `ploegd` with this record.

### Confirmation

* `go test ./pkg/forgeidentity/ ./pkg/githubapp/` reads each forge's account and the App's bot from fakes, and caches reads.
* `go test ./pkg/httpapi/ -run TestClaim_` checks the claim carries the forge's committer, the Run's id and the person.
* `go test ./pkg/worker/ -run TestYieldedWorkIsCommittedAsTheForgeAccountAndNamesThePerson` checks the checkout's identity, the Task Spec's trailers and the yield commit's author, committer and trailers, and the fallback; it fails on the code before this record.
* `go test ./internal/ledger/` keeps this record well-formed.

## Pros and Cons of the Options

### A configured commit name and e-mail per forge

* Good, because it works for a credential that cannot read its own account.
* Bad, because it repeats what the forge knows, can drift from the account that pushes, and is one more setting per forge.

### Commit as the person the Run acts for

* Good, because the commit shows the person.
* Bad, because the person did not write or push the commit, and Ploeg holds no e-mail for them to commit as.

## Re-evaluation triggers

* Ploeg records a verified e-mail for a person: add `Co-authored-by`.
* Forgejo Authorized Integrations replace the bot's token, so a Run's credential belongs to a per-Team bot.
* A forge's user endpoint or noreply address scheme changes.
* An installation needs commits attributed to an account other than the one that pushes.

## More Information

* 2026-11-20 — The owner rejected a configured commit identity per forge: the identity is the forge connection's own, and the person is named in trailers.
* 2026-11-20 — Accepted by the owner, with the breaking removal of the rc.136 commit identity settings.
* Related: [ADR-0086](0086-github-is-a-forge-and-a-run-pushes-with-its-own-github-app-installation-token.md) (the GitHub App pushes), [ADR-0156](0156-each-run-carries-a-transaction-token-that-names-its-person-and-a-team-without-a-sponsor-runs-nothing.md) (the person), [ADR-0180](0180-a-call-without-a-live-person-is-attributed-to-its-run-or-a-proven-workload-and-an-administrator-overrides-as-themselves.md) (attribution without a live person), [ADR-0192](0192-a-runs-forge-is-resolved-from-its-work-target-and-the-claim-carries-the-forge-and-a-credential-minted-for-that-run.md) (the claim carries the forge).
