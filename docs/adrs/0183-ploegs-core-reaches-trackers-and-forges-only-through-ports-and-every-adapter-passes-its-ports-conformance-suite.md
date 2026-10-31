---
status: proposed
date: 2026-10-31
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Ploeg's core reaches trackers and forges only through ports, and every adapter passes its port's conformance suite

## Context and Problem Statement

[ADR-0077](0077-ploeg-knows-work-sources-and-change-destinations-never-vendors.md) decides where Ploeg ends up: core knows a Work Source and a Change Destination, and vendor adapters eventually run out of process. Its second migration step, "core depends only on ports and the existing providers become in-process adapters", does not say what a port looks like, how an adapter says what it can do, or how anyone checks that a new adapter behaves like the others.

`pkg/provider` is already half of that. It has two base interfaces (`TrackerProvider`, `ForgeProvider`) and 25 small capability interfaces that core discovers with type assertions (21 assertions outside `pkg/provider`, from `BoardReader` to `CIReRunner`). No core package imports an adapter package; only `cmd/ploegd` does. But the remaining coupling is spread across core, and nothing proves that two adapters of one port behave alike:

**Vendor names and branches in core packages**

* `pkg/work/branch.go:13,24`: Vikunja items keep `agent/vik-<id>` and `VIK-<id>`. Every other tracker gets `<provider>-<id>`.
* `pkg/config/config.go:58-63,247-253,363,387` and `pkg/config/projects.go:43`: one configuration section per vendor. A ClickUp board must carry an id, because "clickup name resolution is not implemented".
* `cmd/ploegd/connections.go:34-37` and `pkg/httpapi/connections.go:223`: webhook wiring is reported for Vikunja only.
* `cmd/ploegd/webhooks.go`: the webhook coverage check and its registration are Vikunja-only.
* `cmd/ploeg-mcp/tools.go:419-423`: Vikunja and ClickUp task URLs are parsed with regular expressions in the MCP server.
* `pkg/apilimit/apilimit.go:63-67`: each vendor's rate limit sits in a shared package, keyed by vendor name.
* `pkg/httpapi/forge_credential.go:123`: core reads `X-Forgejo-Delivery` and `X-Gitea-Delivery`.

**Forge dialects in the worker and the Run contract**

* `pkg/harness/contract.go:306-315`: the dialect constants travel on `taskspec.v1` ([ADR-0023](0023-the-forge-dialect-travels-on-the-work-item.md)).
* `pkg/worker/forge.go:65`, `pkg/worker/forgeproxy.go:346-360,493-495` and `pkg/taskprompt/prompt.go:350-365` switch on the forge dialect.
* `pkg/forgebroker/forgejo.go` and `pkg/forgebroker/github.go` mint forge credentials. `pkg/githubapi` and `pkg/githubapp` sit beside core packages.

**Composition**

* `cmd/ploegd/main.go` reads at least 15 vendor environment variables. `cmd/ploegd/github.go`, `github_forge.go` and `forgecreds.go` read 17 more. Each adapter is built by hand in the composition root.

**Trackers and forges mixed in one concept**

* `pkg/provider` holds both ports, their events and their capabilities in one package. `Comment` means a tracker comment on `TrackerProvider` and a pull request comment on `ForgeProvider`.
* `ExecutionReader` (`pkg/provider/execution.go:17`) is tracker-only but lives next to `ForgeRepositoryLocator` and `RepositoryInspector`.
* The GitHub tracker and the GitHub forge share App credentials through `pkg/githubapp`.

**Behaviour each adapter decides alone**

* The comment body is Markdown, but ClickUp showed it verbatim, so a person read `**Outcome:**` with its asterisks.
* `SetStatus` receives `awaiting_review`, `needs_human` and `done` (`pkg/shiftengine/publish.go:398`, `review.go:250,303`, `dispatch_gate.go:154`). Every tracker writes `done` only, and `publish.go:380-397` asks for "a provider whose tracker HAS an in-review column" to be free to use it.

How should core and adapters be shaped, so that a tracker or forge is added by writing an adapter, and each adapter is proven to keep its port's contract?

## Decision Drivers

* **Core imports ports, never adapters** ([ADR-0077](0077-ploeg-knows-work-sources-and-change-destinations-never-vendors.md), [ADR-0069](0069-ploeg-names-none-of-its-consumers.md)).
* **Adapters differ in what their system can do.** A Kanban board, an issue tracker and a forge do not all have statuses, dependencies or check runs. A port that requires everything forces fake implementations.
* **Every behaviour core relies on is tested once, for every adapter.** Today each adapter has its own tests in its own words.
* **Small, independently mergeable steps.** Other work changes `pkg/provider`, `pkg/shiftengine` and `cmd/ploegd` every day.
* **The gateway already has its contract.** [ADR-0177](0177-ploeg-reaches-models-through-a-gateway-contract-with-litellm-and-envoy-ai-gateway-as-implementations.md) defines it with required operations and a contract suite, and this record does not reopen it.

## Considered Options

* Driven ports with capability sub-interfaces, an explicit adapter table in the composition root, and one conformance suite per port
* One wide interface per port that every adapter implements in full, with "unsupported" errors
* Go out of process at once (ADR-0077 steps 4 to 6) and skip the in-process ports
* Keep `pkg/provider` as it is and fix vendor leaks one at a time

## Decision Outcome

Chosen option: "**Driven ports with capability sub-interfaces, an explicit adapter table in the composition root, and one conformance suite per port**". It is what `pkg/provider` already half does, so each step is small. It lets an adapter say "my system cannot do this" by not implementing an interface. And it makes the suite, not a reviewer's memory, the definition of a working adapter. ADR-0077's out-of-process destination stays open: the suite becomes the test of an out-of-process adapter's in-process shim.

### 1. Ports

There are three driven ports. Core, meaning every package outside `pkg/provider/<adapter>`, the vendor client packages and `cmd/`, depends on them and on nothing vendor-specific.

| Port | Realises (ADR-0077) | Required | Capabilities an adapter may declare |
| --- | --- | --- | --- |
| **Tracker** | Work Source | `Name`, `ParseWebhook` (intake), `FetchItem`, `Comment`, `SetStatus` | `ExecutionReader`, `DeliveryIdentifier`, `BoardReader` (status and comments), `TaskLinker`, `RelationReader` (parents), `DependencyReader`, and proposed: `ScopeNamer` (board name to id), `TaskRefParser` (task URL to id), `ReferenceNamer` (branch and trailer prefix), `WebhookChecker` (list, health, register), `Assigner` |
| **Forge** | Change Destination | `Name`, `ParseWebhook`, `Comment`, `Comments`, `EditComment`, `PullRequestState`, `PullRequestFacts` | the existing `CommitStatusReader`, `CIReRunner`, `WriteAccessReader`, `AncestryReader`, `MergeabilityReader`, `PullRequestCloser`, `CommentDeleter`, `CommentAttacher`, `ChangedPathsReader`, `PullRequestChangeReader`, `PullRequestActivityReader`, `CIHistoryReader`, `PullRequestDiffReader`, `PublishedPullRequestReader`, `PullRequestLister`, `RepositoryInspector`, `ForgeRepositoryLocator`, `DeliveryIdentifier`, and proposed: `CredentialMinter` (today `forgebroker`), `WorkerProfile` (the proxy allowlist and briefing the worker needs) |
| **Gateway** | none | as ADR-0177 decides: every operation is required, with no optional capabilities | none |

A capability is a Go interface with one job. An adapter declares a capability by implementing it, and core finds it with a type assertion through one helper per port: `provider.TrackerCapabilities(t)` names the capabilities, for logs, `/connections` and the suite. A missing capability is a supported answer with a documented effect. For example, without `DependencyReader` nothing is held, and without `TaskLinker` the record has no link. It is never an error at run time.

### 2. Rules the ports carry

* **Comment bodies are Markdown.** A tracker or forge adapter renders the body in its system's own format. If the system shows text verbatim, the adapter strips the markup. Core writes one body for every adapter.
* **Status is the operator's word.** `SetStatus` receives only `provider.MappableStates` (`awaiting_review`, `needs_human`, `done`). An adapter whose statuses its operator names maps them from configuration, which `provider.ParseStatusMap` parses and which refuses any other state at start-up. A state without a mapping leaves the item where it is. An adapter with a fixed vocabulary, such as GitHub's open and closed, keeps its own rule. Ploeg never guesses a status name.
* **Webhooks are verified before parsing**, against the raw body, and a missing secret rejects every delivery (today's rule, now tested by the suite).
* **Rate limits are the adapter's.** The adapter declares its `apilimit.Policy`. The shared package keeps the mechanism and loses the vendor table.

### 3. Adapters register in one table

Each adapter package exports `Configure(env func(string) string, cfg config.Adapter) (port, enabled bool, err error)`. `cmd/ploegd/adapters.go` holds the one table of trackers and forges and calls each `Configure`. That table is the only place an adapter is named. Registration stays explicit. No `init()` side effects: a reader can see in one file what a binary contains, and a test can build any subset.

### 4. One conformance suite per port

`pkg/provider/trackertest` and, later, `pkg/provider/forgetest` hold the suites. An adapter's test package supplies a `Fixture`:

* a constructor for the adapter, configured and unconfigured, pointed at a URL;
* an `http.Handler` that fakes the vendor API over a shared, vendor-neutral `World` (tasks with title, Markdown body, scope, open or closed, statuses, dependencies, comments);
* a signer that turns a neutral assignment into the vendor's signed webhook request.

The suite seeds the `World`, calls the port, and checks the `World` and the returned values:

* unsigned, wrongly signed or secretless deliveries are refused;
* an assignment routes to its Team;
* a read returns the title, Markdown body, scope, closed state and, with `TaskLinker`, a link;
* a comment arrives, and the text a person reads carries no Markdown markers;
* only mapped states move an item, and to the mapped status;
* unconfigured write-backs are no-ops;
* dependencies, when declared, name each waited-on item and whether it is done;
* the same delivery yields the same delivery id.

A capability's cases run only when the adapter declares it. From step 2, the suite also fails an adapter that does not declare a capability the support matrix lists for its system.

### 5. Migration, in independently mergeable steps

| Step | Change | Cost | Risk |
| --- | --- | --- | --- |
| 1 | `trackertest` suite, `TrackerCapabilities`, `MappableStates`/`ParseStatusMap` in the port. The ClickUp adapter passes the suite and gains its missing capabilities (`TaskLinker`, `DependencyReader`, mapped statuses, rendered comments). | S | None at run time beyond ClickUp's new write-backs, which are opt-in by configuration. |
| 2 | Vikunja and GitHub Issues fixtures pass the suite. Each gap it finds is fixed in its own commit. | S | Low. It may expose behaviour differences that core relied on. |
| 3 | `internal/boundary` gains an import check: no package outside `cmd/` and the adapter packages imports an adapter or vendor client package. | S | None. |
| 4 | Tracker leaks become capabilities, one pull request each: `ReferenceNamer` (`work/branch.go`, byte-identical for existing items by golden test), `ScopeNamer` (config name resolution for every tracker), `TaskRefParser` (`ploeg-mcp` URL parsing), `WebhookChecker` (generic coverage check and `/connections` wiring), adapter-declared rate policies. | S–M each | Medium for `ReferenceNamer`: a changed branch name orphans open pull requests. A golden test over stored items guards it. |
| 5 | `pkg/provider` splits into `pkg/provider/tracker` and `pkg/provider/forge` port packages. `pkg/provider` keeps type aliases for one minor ([ADR-0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md)). `ExecutionReader` moves to the tracker port. | M (mechanical, wide) | Merge conflicts with parallel work, so it lands in one quiet window. The aliases keep the Go API compatible. |
| 6 | Adapter table and `Configure` per adapter. Vendor environment variables move out of `cmd/ploegd/main.go`. The configuration gains `trackers.<id>.kind`, and the old vendor keys are still read for one minor. | M | Configuration compatibility. Mitigated by reading both shapes and a test per old key. |
| 7 | `forgetest` suite for Forgejo, GitLab and GitHub, and the delivery-header read moves behind `DeliveryIdentifier`. | M | Low. |
| 8 | Worker forge dialects (`worker/forge.go`, `forgeproxy.go`, `taskprompt`, `harness/contract.go`) move behind a `WorkerProfile` capability, or go away under ADR-0077 step 3 when Ploeg opens change requests itself. Its own record supersedes ADR-0023. | L | Security-relevant, because the proxy allowlist is what an agent can reach. Its own record and the attack drill ([ADR-0126](0126-a-recurring-attack-drill-runs-every-harness-against-a-hostile-repository.md)) gate it. |

ADR-0077's steps 4 to 6 (CloudEvents intake, extraction, removal) follow on top. Each of them reuses the suites unchanged.

### Consequences

* Good, because adding a tracker is writing one adapter package, one fixture and one table row. The suite says when it is done.
* Good, because an adapter that cannot do something says so by omission, and the effect of omission is documented once per capability, not rediscovered per vendor.
* Good, because two adapters of one port provably behave alike on what core relies on. Today's per-adapter tests drift.
* Good, because each step is small enough to merge between other work, and step 1 changes no core behaviour.
* Bad, because the fixture's fake vendor API is code to keep in step with the real API. The live qualification tests stay the check against the real system.
* Bad, because two port packages plus aliases make `pkg/provider` larger for one minor before it gets smaller.
* Bad, because capability discovery by type assertion is invisible to the compiler: an adapter that misspells a method silently loses a capability. `TrackerCapabilities` in startup logs and the suite's support-matrix check catch it.

### Confirmation

* `go test ./pkg/provider/...` runs `trackertest` against every tracker adapter that has a fixture. From step 2, every tracker adapter must have one, and a test fails when an adapter package exists without a fixture.
* From step 3, `go test ./internal/boundary/` fails when a core package imports an adapter or vendor client package. It is shown to fail on one injected import.
* Each later step lists, in its pull request, the inventory lines above that it removes. A grep for those vendor names outside adapters, `cmd/` and records then counts down to zero (ADR-0077's boundary test).

## Pros and Cons of the Options

### One wide interface per port, with "unsupported" errors

* Good, because the compiler checks that every method exists.
* Bad, because every adapter writes stubs, core must tell "unsupported" from failure on every call, and the 25 existing capabilities would have to be merged into two interfaces at once.

### Go out of process at once

* Good, because it reaches ADR-0077's destination without an intermediate shape.
* Bad, because it needs the contracts, the CloudEvents intake and adapter images before any coupling is removed, an 8 to 10 week path with nothing mergeable on the way. And without a suite there is nothing to prove the new adapters equal the old.

### Keep `pkg/provider` and fix leaks one by one

* Good, because it costs nothing up front.
* Bad, because nothing stops the next leak. The ClickUp gaps (unrendered comments, done-only status, no dependencies, no link) are exactly the drift a shared suite catches.

## Re-evaluation triggers

* ADR-0077 is accepted or rejected.
* An adapter needs a capability that does not fit one interface with one job.
* The type-assertion discovery hides a lost capability in production once.
* A third tracker or a fourth forge adapter is proposed. That is the point to check that step 6's configuration shape holds.
* Step 5's package split is attempted and conflicts with more than three open pull requests.

## More Information

* Related: [ADR-0015](0015-routing-is-core-policy-over-provider-opaque-scopes.md) (opaque scopes), [ADR-0016](0016-forge-registry-and-per-run-repo-scoped-credentials.md) (forge registry), [ADR-0051](0051-delivery-gates-are-mapped-per-board-from-tracker-statuses.md) (statuses read per board), [ADR-0123](0123-dispatch-waits-for-tracker-dependencies-and-an-opt-in-free-premise-check.md) (dependencies), [ADR-0177](0177-ploeg-reaches-models-through-a-gateway-contract-with-litellm-and-envoy-ai-gateway-as-implementations.md) (gateway contract).
