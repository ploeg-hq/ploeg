---
status: proposed
date: 2026-11-17
decision-makers: Ryan Grippeling
supersedes: 0023
review-by: 2027-03-31
---

# A Run's forge is resolved from its Work Target, and the claim carries that forge and a credential minted for that Run

## Context and Problem Statement

[ADR-0023](0023-the-forge-dialect-travels-on-the-work-item.md) put the forge dialect on the Work Item and kept the forge URL and credential deployment-global, because "a worker pod holds exactly one forge credential and one forge URL". It accepted that one release cannot span two forges, and named the events that should supersede it: a third forge, and a deployment that must span two forges from one release. Both have happened. GitHub became a forge ([ADR-0086](0086-github-is-a-forge-and-a-run-pushes-with-its-own-github-app-installation-token.md)), and an operator now runs one Ploeg for repositories on a self-hosted Forgejo and on GitHub.

What exists today:

* **ploegd is already multi-forge.** It configures a Forgejo, a GitLab and a GitHub forge provider side by side, each when its URL is set, in one `forges` map ([`cmd/ploegd/main.go`](../../cmd/ploegd/main.go#L189-L245)). The chart renders each of `executor.forgejo`, `executor.gitlab` and `executor.github` into ploegd's environment when that block has a URL ([`deployment.yaml`](../../ops/helm/ploeg/templates/deployment.yaml#L185-L256)), and ploegd's network policy reaches every one of them ([`_networkpolicy.tpl`](../../ops/helm/ploeg/templates/_networkpolicy.tpl#L81-L98)).
* **The worker is single-forge.** `ploeg-worker` boots with one `FORGE_URL` and one `PLOEG_TARGET_FORGE` ([`cmd/ploeg-worker/main.go`](../../cmd/ploeg-worker/main.go#L128-L129)), and every resolved target gets that one URL whatever forge it names ([`pkg/worker/target.go`](../../pkg/worker/target.go#L48-L58)). The chart picks one block through `executor.forge` (`ploeg.forge` in [`_helpers.tpl`](../../ops/helm/ploeg/templates/_helpers.tpl#L87-L97)) and renders its URL, its shared `tokenSecret` and its `readTokenSecret` into every worker pod ([`_helpers.tpl`](../../ops/helm/ploeg/templates/_helpers.tpl#L893-L923)); the worker's egress reaches only that forge ([`_networkpolicy.tpl`](../../ops/helm/ploeg/templates/_networkpolicy.tpl#L84-L85)). The reference says so: "One active forge per release: a worker pod holds one forge URL and one credential" ([`configuration.md`](../reference/configuration.md#L293)).
* **Per-Run minting exists for two forges, for writers only.** On Forgejo, ploegd signs in as the bot with `PLOEG_FORGEJO_BOT_PASSWORD` and mints a `write:repository` token restricted to the Run's repository ([ADR-0013](0013-push-rights-are-minted-per-run.md) tier 2, [ADR-0172](0172-the-forgejo-broker-runs-only-on-forgejo-15-and-refuses-a-token-the-forge-did-not-restrict-to-the-runs-repository.md), [`cmd/ploegd/forgecreds.go`](../../cmd/ploegd/forgecreds.go#L29-L51)); without the password every Run gets the shared token. On GitHub, the App mints an installation token per Run, and `forgeTokenExpiresAt` lets the worker refresh it ([ADR-0086](0086-github-is-a-forge-and-a-run-pushes-with-its-own-github-app-installation-token.md), [`pkg/httpapi/server.go`](../../pkg/httpapi/server.go#L600-L612)). `forgebroker.Router` already sends a mint to the GitHub App or to the default broker by the target's forge id ([`pkg/forgebroker/github.go`](../../pkg/forgebroker/github.go#L150-L181)). A reading Run gets nothing minted ([`pkg/httpapi/server.go`](../../pkg/httpapi/server.go#L784-L800)); it reads with the static `readTokenSecret` mounted in its pod.
* **GitLab has no per-Run minting.** ADR-0023 recorded why: Forgejo's mint path has no GitLab equivalent, and a project access token is a different escalation. That has not changed.

So a Work Item whose target names GitHub, routed to a release whose workers were rendered for Forgejo, is cloned from the Forgejo URL with the Forgejo token. Serving both forges today takes two releases of the whole dispatch plane, which is ADR-0023's option C under another name.

Where does a Run learn which forge it acts on and with which credential, so that one release serves several forges without any worker pod holding a credential for a forge its Run does not touch?

## Decision Drivers

* **One answer to "which forge".** ADR-0014 put the repository on the Work Item and [ADR-0016](0016-forge-registry-and-per-run-repo-scoped-credentials.md) resolves a target's forge id through the registry. The worker must not keep a second, boot-time answer that can disagree with it.
* **A worker pod holds no standing forge credential** (rule R6, [ADR-0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md)). A pod that can reach every forge with a static token is the blast radius ADR-0023 refused to widen.
* **The reader/writer split stays enforced by the forge** (ADR-0013 tier 1), not only by Ploeg's scheduling.
* **Existing values keep working.** A single-forge release renders and behaves as today.
* **Vendor specifics stay behind ports** ([ADR-0183](0183-ploegs-core-reaches-trackers-and-forges-only-through-ports-and-every-adapter-passes-its-ports-conformance-suite.md)).

## Considered Options

* **A. The forge travels on the claim: dialect, URL and a credential minted for that Run, resolved from `Target.Forge` through the registry**
* B. Keep ADR-0023: one forge per release, a second forge is a second release
* C. Every worker pod mounts a static token and URL for every configured forge
* D. The forge is chosen per Team (ADR-0023's option B)

## Decision Outcome

Chosen option: "**A. The forge travels on the claim**", because it is the only option where one release serves several forges and no worker pod holds a credential for any forge before it claims a Run, or for any forge but its Run's after.

Proposed, not built. Each clause states the contract the implementation must meet.

### 1. The claim carries the forge

When ploegd hands out a Run it resolves the Work Item's `Target.Forge` through the forge registry (ADR-0016); an empty id is the default forge. The claim response gains the forge's dialect and the URL workers use for it, beside the existing `forgeToken`, `forgeTokenPerRun` and `forgeTokenExpiresAt`. The worker builds its `harness.RepoRef` from these and no longer from `FORGE_URL` and `PLOEG_TARGET_FORGE`. A target whose forge id the registry does not know is refused at claim time with a named reason, as an unknown dialect already fails loudly under ADR-0023.

A worker pod holds no forge credential and no fixed forge URL. `AGENT_BUILDER_TOKEN` and `FORGE_URL` stop being boot-required once every active forge mints (section 3); in a single-forge release that falls back to the shared token they stay, as section 3 describes.

### 2. V1: reading Roles get a per-Run read-only credential (chosen A)

A reading Run is minted a credential too, restricted to reading the Run's repository and revoked when the Run settles or its Lease lapses:

* **GitHub:** an installation token for the Run's repository with read permissions only (contents, pull requests, checks, commit statuses and metadata read).
* **Forgejo:** a token minted for the bot with `read:repository`, restricted to the Run's repository, under the same Forgejo 15 check and restriction refusal as ADR-0172.

The forge still enforces the reader/writer split; the credential now also dies with the Run. Consequence: a Forgejo forge in a multi-forge release needs the bot password and Forgejo 15 or later, because there is no other way to mint.

Rejected **B: a static read token per forge mounted in every pod**. It keeps today's mint path for readers, but every reading pod could then read every repository on every configured forge, which is the widening section 1 exists to avoid.

### 3. V2: a multi-forge release refuses to start unless every active forge mints (chosen A)

A forge is **active** when its chart block has a `url` (section 4). With more than one active forge, ploegd refuses to start unless each active forge can mint per-Run credentials for readers and writers: GitHub with an App configured, Forgejo with the bot password on Forgejo 15 or later. The refusal names the forge and what it lacks. GitLab cannot mint (ADR-0023), so GitLab can only be the sole forge of its release.

A release with one active forge keeps today's behaviour, including the shared-token fallback: without minting, workers boot with `FORGE_URL`, `AGENT_BUILDER_TOKEN` and the reader's `readTokenSecret` exactly as now, and the claim's forge must equal that forge.

Rejected **B: allow shared static tokens in multi-forge releases**. It lets GitLab join a multi-forge release, but every pod would again hold standing credentials for several forges.

### 4. V3: the existing chart blocks become active by their `url` (chosen A)

* `executor.forgejo`, `executor.github` and `executor.gitlab` are each active when they set `url`, as they already are for ploegd. `executor.forge` keeps its meaning as the default forge for a target that names none, and stays required to name an active block.
* Worker egress (`networkPolicy`) reaches every active forge. The worker's forge proxy ([`pkg/worker/forgeproxy.go`](../../pkg/worker/forgeproxy.go#L23-L56)) keeps a Run inside its own repository on its own forge, and the claimed credential cannot reach another repository anyway.
* A values file that sets only the `executor.forge` block renders the same workers as today.

Rejected **B: a new `executor.forges[]` list**. It reads cleaner, but it breaks every existing values file and duplicates blocks ploegd already reads.

### Not chosen now

* **Per-Team forge** (ADR-0023's option B) stays rejected for ADR-0023's reasons: it re-couples capability to codebase and gives ploegd and the worker two answers.
* **Execution cells** ([ADR-0107](0107-execution-runs-in-cells-that-pull-from-one-hub.md)) are compatible with this record: a cell claims from the hub and gets the same forge, URL and minted credential on the claim. Cells are not built, so they are not an alternative now.

### Consequences

* Good, because one release serves repositories on several forges, and onboarding a repository on another active forge is a routing entry and a webhook.
* Good, because no worker pod holds a standing forge credential in a multi-forge release, and every credential a Run holds, read or write, is scoped to one repository and dies with the Run.
* Good, because ploegd and the worker read one resolved answer for "which forge", and ADR-0023's "routed to a forge the release is not configured for fails at run time" becomes a claim-time refusal with a reason.
* Good, because a single-forge release, including GitLab and a Forgejo without the bot password, keeps working unchanged.
* Bad, because a Forgejo forge in a multi-forge release now needs the bot password and Forgejo 15, and reading Runs add a mint and a revoke per Run on each forge.
* Bad, because GitLab cannot share a release with another forge until it can mint per Run.
* Bad, because the claim contract and `harness.RepoRef` handling change, and the worker's boot requirements become conditional on how the release is configured.
* Neutral, because worker egress widens to every active forge; the per-Run credential and the forge proxy, not the network policy, keep a Run on its repository.

### Confirmation

Proposed, none of these exist yet. Once built, `mise run verify` (which runs `go test ./...` and `scripts/helm-golden.sh check`) in `.github/workflows/ci.yml` runs them:

* A Helm golden, `ops/helm/ploeg/ci/golden/executor-two-forges.yaml`, rendering Forgejo and GitHub as active forges: worker pods carry no `AGENT_BUILDER_TOKEN`, `readTokenSecret` or `FORGE_URL`, and the worker network policy reaches both forges. The existing goldens, including `executor-gitlab.yaml` and `executor-github.yaml`, stay byte for byte.
* A chart render test that two active forges with a Forgejo block lacking `botPasswordSecret`, or with an active GitLab block, fail the render with a message naming the forge.
* A claim contract test in `pkg/httpapi` and `pkg/worker`: the claim carries the dialect and URL resolved from `Target.Forge`, a reading Run receives a minted read-only credential, and an unknown forge id is refused. The claim's schema in `docs/contracts/` and its Go types change together.
* A `cmd/ploegd` boot test for V2: with two active forges ploegd refuses to start when one cannot mint, and a single active forge without minting starts with the shared token as today.
* `forgebroker` tests with `httptest` fakes: a Forgejo read mint asks for `read:repository` restricted to the Run's repository and refuses an unrestricted token; a GitHub read mint asks for read permissions only; a mint for a forge id is never sent to another forge's broker.
* `go test ./internal/ledger/` keeps this record well-formed, and `go test ./internal/boundary/` keeps it free of consumer names.

## Pros and Cons of the Options

### B. Keep ADR-0023

* Good, because nothing changes.
* Bad, because an operator with repositories on two forges runs two dispatch planes, with two budgets and two sets of Teams, for one body of work.

### C. Every worker pod mounts a static token for every forge

* Good, because it needs no change to minting and lets GitLab join.
* Bad, because every pod can reach every repository the bots can see on every forge, the blast radius R6 and ADR-0023 refused.

### D. The forge is chosen per Team

* Good, because it needs no claim change.
* Bad, because a Team is a capability tier, not a forge, and ploegd would still read `Target.Forge` while the worker read the Team.

## Re-evaluation triggers

* GitLab gains a way to create and revoke a repository-scoped token per Run without a new escalation, so GitLab can join a multi-forge release.
* Execution cells (ADR-0107) are built, or ADR-0184 is accepted and moves forge connections into Ploeg's own secret store.
* Read mints hit a forge's rate limit or token limit in practice, or a Forgejo release below 15 must join a multi-forge release.
* A deployment needs two instances of the same dialect active in one release.

## More Information

* Supersedes [ADR-0023](0023-the-forge-dialect-travels-on-the-work-item.md): its dialect-on-the-Work-Item rule stays, its "URL and credential stay deployment-global" does not. Both of its supersession triggers fired.
* Builds on [ADR-0013](0013-push-rights-are-minted-per-run.md), [ADR-0014](0014-work-target-is-a-work-item-attribute.md), [ADR-0016](0016-forge-registry-and-per-run-repo-scoped-credentials.md), [ADR-0086](0086-github-is-a-forge-and-a-run-pushes-with-its-own-github-app-installation-token.md) and [ADR-0172](0172-the-forgejo-broker-runs-only-on-forgejo-15-and-refuses-a-token-the-forge-did-not-restrict-to-the-runs-repository.md).
* Related: [ADR-0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md), [ADR-0107](0107-execution-runs-in-cells-that-pull-from-one-hub.md), [ADR-0183](0183-ploegs-core-reaches-trackers-and-forges-only-through-ports-and-every-adapter-passes-its-ports-conformance-suite.md), [ADR-0184](0184-tracker-and-forge-connection-secrets-live-in-a-secret-store-ploeg-writes-itself-and-no-person-handles-them.md).
* Found while writing this record: `forgebroker.Forgejo.Mint` ignores `MintRequest.Forge`, and `forgebroker.Router` sends every non-GitHub mint to the default broker ([`pkg/forgebroker/github.go`](../../pkg/forgebroker/github.go#L173-L181)). With Forgejo and GitLab both configured, a GitLab writer is minted a Forgejo token. Section 3 makes that release refuse to start; the implementation must route each mint by forge id.
* Open: how an operator consumer learns that an installation serves several forges. The version route reports contract majors only ([ADR-0147](0147-ploeg-serves-the-major-of-each-contract-and-keeps-its-consumers-two-newest-lines-working.md)), and a consumer must not infer forges from target keys. Recommendation: `GET /api/v1/operator/connections` ([ADR-0129](0129-ploeg-reports-how-each-board-and-repository-it-routes-to-is-wired.md)) reports each active forge instance (id, dialect, base URL) and whether it mints a credential per Run or falls back to the shared token (section 3), so a consumer can offer a repository on that forge and say what credential its Runs will hold. Recorded 2026-11-17 after an independent review.
* 2026-11-17: built on branch `feat/multi-forge-claim`, with every check in the Confirmation list; the status stays proposed until the owner accepts it. Two details the record left open were settled there. A reading Run's credential lives on its Run row (`agent_runs.forge_token_id`), because a reader holds no Lease; settlement, `ExpireRuns`, a withdrawal and the orphan sweep revoke it, and a reader refreshes an expiring GitHub token through the same `forge-credential` route as a writer. ploegd hands workers each forge's `url` through `PLOEG_<DIALECT>_WORKER_URL`, which the chart renders only in a multi-forge release, so a single-forge release keeps its `FORGE_URL` and renders byte for byte as before. The chart also refuses a multi-forge release whose GitHub block has no `tracker.github.appId`.
