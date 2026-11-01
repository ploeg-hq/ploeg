---
status: proposed
date: 2026-11-01
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A Team is assignable in a tracker as a delegate and reports its work as a session of activities

## Context and Problem Statement

Today a tracker hands work to Ploeg by assignment, and the assignee doubles as the routing key. Vikunja and ClickUp parse the added assignee, look it up in `TeamMap` and fall back to `DefaultTeam` (`pkg/provider/vikunja/vikunja.go`, `pkg/provider/clickup/clickup.go`); [ADR-0095](0095-work-is-handed-to-ploeg-by-assigning-one-ploeg-user-and-a-team-label-picks-the-team.md) added one shared `dispatchUser` plus a team label; [ADR-0186](0186-an-operator-consumer-binds-a-tracker-scope-to-a-team-at-runtime-and-static-routing-wins-on-conflict.md) lets a consumer bind a Scope to a Team at runtime. Three things are wrong with this at once:

1. **Ploeg occupies a person's seat.** Every tracker account Ploeg uses is a member, counted and billed like a person, and the human who wanted the work done has to give up the assignee field to hand it over. The card then says Ploeg is responsible, when a person still is.
2. **Routing happens by container, not by choice.** A List or project is pinned to a Team, so a person who wants another Team on one card has no field to say so, and a misroute is found when the Shift fails.
3. **Ploeg reports back as comments, and only at the end.** The tracker learns that a Shift started, asked a question or opened a pull request through `Comment` and `SetStatus` (`provider.MappableStates`), typically once per state. Whether the work is going, stuck or finished is not visible on the card while it happens, and the transcript [ADR-0090](0090-a-runs-agent-session-is-kept-as-a-bounded-redacted-transcript.md) keeps never reaches the tracker.

Meanwhile trackers are growing a first-class notion of an agent. Linear's Agent Sessions (developer preview, [agents](https://linear.app/developers/agents), [agent interaction](https://linear.app/developers/agent-interaction)) let an OAuth app installed with `actor=app` and the scopes `app:assignable` and `app:mentionable` be a *delegate* on an issue: it is assignable and @mentionable, it is free (not a billable user), the human stays the assignee, and assigning or mentioning it opens a **session** in which the agent emits typed **activities** (`thought`, `action`, `response`, `error`, `elicitation`), sets **external URLs** and honours a **stop**. ADR-0095 named exactly this as a trigger: "a tracker gains a first-class agent or delegate field that is a better start signal than assignment". It has fired. The [2026-11-01 landscape](../research/2026-11-01-tracker-agent-identity-landscape.md) shows no other tracker offers an open path yet, and no cross-tracker standard exists.

How does a Team become something a person assigns, in any tracker, without a seat per Team; how does the card show the work as it happens; and how does a tracker that has no such feature get the same behaviour?

## Decision Drivers

* **One model for every tracker, shaped by the richest one.** Core writes one session; the adapter renders it. Where a tracker has a native agent, Ploeg uses it; where it has none, the adapter emulates it, and core cannot tell the difference ([ADR-0183](0183-ploegs-core-reaches-trackers-and-forges-only-through-ports-and-every-adapter-passes-its-ports-conformance-suite.md)).
* **The human stays the assignee.** Responsibility stays with the person; Ploeg is the delegate.
* **No seat per Team.** A Team is a Ploeg concept and must not cost a tracker account, a login and a share per Team ([ADR-0095](0095-work-is-handed-to-ploeg-by-assigning-one-ploeg-user-and-a-team-label-picks-the-team.md)).
* **Fail at intake, not in the Run.** Whether a Team fits a card is knowable before a sandbox is claimed. Saying so is cheaper than a failed Shift and clearer than a `needs_human` an hour later.
* **Today's deployments keep working.** List pins (`trackers.<kind>.projects[].team`), team labels and runtime bindings stay valid; the new field wins only where it is set.
* **Ploeg names none of its consumers** ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)) and keeps the internal model compatible with A2A so a facade stays possible ([ADR-0007](0007-a2a-adopt-nothing-watchlist-a-facade.md)).
* **No model spend for the answer to "will you take this".** The gate is a check of facts Ploeg already holds, like the dependency hold of [ADR-0123](0123-dispatch-waits-for-tracker-dependencies-and-an-opt-in-free-premise-check.md).

## Considered Options

* **An optional Tracker capability, `AgentIdentity`, shaped by native agent sessions; adapters without the feature emulate it over a Team field and comments; one conformance suite covers both**
* One tracker member per Team
* List or board to Team routing in deployment configuration (today's state)
* A2A as the session protocol between tracker and Ploeg

## Decision Outcome

Chosen option: "**An optional Tracker capability, `AgentIdentity`, shaped by native agent sessions; adapters without the feature emulate it; one conformance suite covers both**", because it is the only option that gives every tracker the same card-level behaviour, costs no seats, moves "will this Team take it" to the moment of assignment, and leaves a native adapter free to drop in later without a caller changing. The first implementation is the ClickUp emulation; the Linear native adapter is the second and the proof that the interface was shaped right.

### 1. The capability, in Ploeg's words

`AgentIdentity` is a capability interface of the Tracker port (`pkg/provider`), discovered like every other one through `provider.TrackerCapabilities` and reported as `agent_identity` in `pkg/provider/capabilities.go`. It has one job: let a Team be a delegate on an item and report a Shift to that item as a session. Its shape follows the native model, so that an emulating adapter implements the native vocabulary and a native adapter implements it directly:

| Native notion (Linear) | Ploeg notion | Carried by |
| --- | --- | --- |
| Delegate: an assignable, non-billable app actor | A Team, presented by the adapter as one delegate per Team | `Delegates(ctx) ([]Delegate, error)` names them; a `Delegate` is `{Team, ExternalID, Display}` |
| Session opened by assignment or @mention (`AgentSessionEvent`, type `created`) | A Shift on a Work Item ([ADR-0010](0010-shift-owns-the-item-lease-owns-the-branch.md)) | `ParseWebhook` returns a `TrackerEvent` of the new kind `TrackerSessionOpened` with `Team` set from the delegate and `Session` set to the tracker's session reference |
| A later prompt in the session (`AgentSessionEvent`, type `prompted`) | A steer of the live Shift, or a new Shift when the previous one closed | `TrackerSessionPrompted` with the prompt text under `Item` as untrusted data |
| Stop | Cancel of the Shift: the Work Item goes `withdrawn`, as an unassignment does today (`pkg/work/types.go`) | `TrackerSessionStopped` |
| Activities `thought`, `action`, `response`, `error`, `elicitation` | The transcript entries ([ADR-0090](0090-a-runs-agent-session-is-kept-as-a-bounded-redacted-transcript.md): `thought`, `tool_call`, `tool_output`, `message`, `stage`), the Shift's status events and audit rows, and the `needs_human` question of ADR-0121 and ADR-0123 | `PostActivity(ctx, session SessionRef, a Activity) error`, where `Activity` is `{Kind, Body, At, Ref}` with `Kind` in the five native kinds |
| External URLs (`agentSessionUpdate` with `externalUrls`) | The pull request Ploeg opened, and a consumer's page for the Shift when one is configured | `SetExternalURLs(ctx, session SessionRef, urls []ExternalURL) error` |
| Session state `pending`, `active`, `awaitingInput`, `complete`, `stale`, `error`, derived from the last activity | Derived the same way; Ploeg never sets a state, it posts the activity that implies it | nothing extra |

The mapping from Ploeg's records to activities is fixed in core, once, in `pkg/shiftengine`, and is the same for every adapter:

* **The gate's answer is the first activity** (section 3): a `response` ("starting, estimate …"), an `elicitation` (one question), or an `error` ("not starting, because …" with the Teams that fit).
* A Run that starts posts an `action` naming its Role and harness; a Run's `message` entries become `response`; its `thought` entries become `thought`; its tool calls become `action`, batched so that at most one `action` a minute carries the tool calls since the last one; a Run that fails posts `error`.
* A `needs_human` with a question ([ADR-0121](0121-findings-carry-ids-writers-answer-them-and-a-run-asks-instead-of-guessing.md), [ADR-0123](0123-dispatch-waits-for-tracker-dependencies-and-an-opt-in-free-premise-check.md)) is an `elicitation`; the person's reply arrives as `TrackerSessionPrompted` and answers it.
* The pull request is an external URL the moment it exists, and a `response` when the Shift ends with `awaiting_review` or `done`.

An adapter may declare, in its `Delegate`, that it is **native** or **emulated**, and whether it keeps `thought` and `action`. Core still sends every activity; an emulating adapter that does not keep thoughts drops them, so the person is not flooded, and the full record stays in the transcript. Core never branches on native or emulated; the flag is for `/connections`, logs and the suite.

Timing is part of the contract because the native model enforces it: the webhook is answered within 5 s, and the first activity or an external URL is posted within 10 s of `TrackerSessionOpened`, or the session shows as unresponsive. Ploeg already answers webhooks before it works (the thin-payload rule in `TrackerEvent`), and the gate's answer needs no model call, so the 10 s holds for every adapter.

The internal model stays **A2A-compatible** without adopting A2A: a session is a task, and the derived states map one to one onto A2A v1.0 task states (`pending` → `SUBMITTED`, `active` → `WORKING`, `awaitingInput` → `INPUT_REQUIRED`, `complete` → `COMPLETED`, `error` → `FAILED`, a stop → `CANCELED`, a gate refusal → `REJECTED`). Nothing in the capability names A2A; the facade ADR-0007 watchlists can be an adapter of this same capability later.

### 2. Emulation, for trackers without the feature

ClickUp and Vikunja have no agent actor, no session and no activity. The adapter implements the same interface over the tracker's own primitives. Core calls the same methods and reads the same events; this table is the adapter's business:

| Operation | ClickUp primitive (the first implementation) |
| --- | --- |
| Delegates | A dropdown custom field on the card, named for Ploeg (configured, default `Ploeg Team`), whose options are the Teams the adapter knows. `Delegates` returns the options. **If** the ClickUp API lets Ploeg create the field and write its options, the adapter creates the field on a bound List when it is missing and adds an option for each Team the installation declares; if it does not, the operator creates the field by hand and `/connections` reports a List where it is missing. Ploeg reads the field in every case; writing it is an operator-enabled opt-in like webhook registration (`WebhookRegistrar`). |
| "Who" | The field's selected option names the Team. |
| "Go" | The one Ploeg member (`trackers.clickup.agents`, one member for the whole installation) being assigned. The person who assigns stays an assignee; Ploeg's member is the second one, since ClickUp allows several. |
| Session opened | `taskAssigneeUpdated` adding Ploeg's member, with the field set, becomes `TrackerSessionOpened`. The session reference is the task id plus the assignment's history item id. |
| Prompted | A comment on the card by a person while the session is open becomes `TrackerSessionPrompted`; ClickUp's `taskCommentPosted` event is added to `DispatchEvents` (`pkg/provider/clickup/webhooks.go`). |
| Activities | Comments. The first comment is the gate's answer. An emulated adapter keeps `response`, `elicitation` and `error` and drops `thought`; it keeps the `action` that names a starting Run and drops batched tool-call actions. Bodies are Markdown rendered through the adapter's existing rich-text path (`richtext.go`). |
| External URL | One comment with the pull request link, edited rather than repeated when the URL changes, where the API allows editing a comment; a new comment otherwise. |
| Stop | Ploeg's member being unassigned (`assignee_rem`), or the card moved out of the statuses the List maps for `active` and `needs_human` (`PLOEG_CLICKUP_STATUS_MAP`). Both become `TrackerSessionStopped`. |

What Ploeg owns in ClickUp under this record: it **reads** the field; it **may create and sync** the field's options from the Teams it knows, if the API allows and the operator enabled it; it **never routes by List or label when the field is set**; and it **falls back to the existing List to Team pin** (`trackers.clickup.projects[].team`, ADR-0186's bindings, ADR-0095's labels) **only while the field is absent** on the card, so every deployment that exists today keeps working unchanged. A field that names a Team the installation does not have is the gate's "not starting" with the Teams that fit, not a fallback.

Vikunja emulates the same way over a label group or a project-level dropdown when it has one, and over the assignment of one Ploeg user otherwise; it is not the first implementation and is listed only to show the shape generalises.

### 3. The gate runs at intake, and its answer is the first activity

When `TrackerSessionOpened` arrives, `pkg/shiftengine`'s admission (`admit` in `dispatch_gate.go`) runs a **fit check** before the dependency hold, before the premise check, and before any sandbox or Lease is claimed. It reads only what Ploeg already holds:

1. **Required positions filled.** Every Role of the Team's crew is bound to a model and a harness, and the Team has a sponsor ([ADR-0156](0156-each-run-carries-a-transaction-token-that-names-its-person-and-a-team-without-a-sponsor-runs-nothing.md)).
2. **The repository is listed for the Team.** The card's Work Target resolves to a registered target the Team's intake names ([ADR-0038](0038-a-repo-label-selects-among-registered-targets-and-the-board-default-is-the-fallback.md), R11).
3. **Each Role's harness can enter the repository.** The same checks the worker applies at `Prepare`, run against the repository's tree without a sandbox: an `openhands` ACP profile refuses a repository that ships `.openhands/hooks.json` ([ADR-0148](0148-every-acp-profile-asks-before-a-mutation-outside-allow-always-and-openhands-refuses-a-repository-that-ships-hooks.md)), and a harness the Team's allowlist does not carry is refused ([ADR-0098](0098-an-operator-admission-may-choose-each-runs-harness-from-its-teams-allowlist.md)).
4. **Weekly headroom above the estimate.** The Team's spend for the period, under its cap ([ADR-0162](0162-a-team-spends-at-most-its-cap-per-period-and-a-claim-over-it-starts-nothing.md)), leaves room for this card's estimate: the tracker's estimate when `BoardStatus.EstimateSeconds` has one, else the Team's median settled Shift.

The answer is posted as the session's first activity, within the 10 s:

* **Starting**, a `response` with the estimate and the Roles that will run. The Shift opens.
* **One question**, an `elicitation`, when exactly one fact is missing that a person can supply (which repository, which of two labels). The Work Item is `needs_human`; the reply reopens admission.
* **Not starting**, an `error` with the reasons and the Teams that do fit. Nothing is claimed. The person changes the field or the card and assigns again.
* **Ranked fitting Teams**, a `response`, when the field is empty and no fallback route applies: the Teams that pass checks 1 to 4, best first. Nothing starts until a person picks one.

This replaces "find out when the Run fails". The premise check of ADR-0123 keeps its place after this gate: fit is a matter of record, premise is a matter of judgement, and the free classifier still runs only for a Team that fits.

### 4. Order of adapters

| Order | Adapter | Mode | Why here |
| --- | --- | --- | --- |
| 1 | **ClickUp** | emulated | The owner's decision: it is the tracker in use, it has no open agent path (only a Cursor partner integration), and emulation on it exercises every method of the interface against real primitives: field, assignment, comments, status. Its fixture (`pkg/provider/clickup/conformance_test.go`) gains the capability's cases first. |
| 2 | **Linear** | native | Open and free; the model the interface is shaped by. Landing it second is the test that a native adapter drops in behind the same callers with no change in `pkg/shiftengine`. One Linear OAuth app is one delegate, so with several Teams an installation either installs one app per Team or lets the session's first activity ask which Team; which of the two is an open question below. |
| 3 | **Jira** | native, later | Partner agents are GA in Jira Cloud; a third-party agent through an external MCP server is documented but unverified for an agent Atlassian did not partner with, and Rovo credits apply from 2026-12-03. Not before that path is verified. |
| parked | **GitLab** | native, parked | GitLab Duo Agent Platform assigns a flow to a service account through an Assign trigger, on Premium or Ultimate. Parked by the owner; the GitLab Issues tracker adapter does not exist yet. |

Vikunja gains the emulation when a Vikunja installation asks for it; its adapter keeps the `dispatchUser` path meanwhile.

### 5. Conformance

`pkg/provider/trackertest` gains the capability's cases, run only when the adapter declares `AgentIdentity`, so a native and an emulated adapter pass the same tests. The `World` gains `Delegates`, per task a `Session` with `Activities` and `ExternalURLs`, and `Fixture` gains `Delegation(secret, taskID, team string) *http.Request` (the vendor's request that assigns the delegate for a Team), `Prompt(...)` and `Stop(...)`:

* assigning the delegate for a Team yields one `TrackerSessionOpened` with that Team and a session reference, and the delegate is not the only assignee;
* a delegate for an unknown Team yields `TrackerSessionOpened` with an empty Team, which core answers with ranked fitting Teams;
* the first `PostActivity` after the open reaches the `World` within the fixture's clock bound and is readable as a person would read it, without Markdown markers where the vendor shows text verbatim;
* `thought` posted to an adapter that does not keep thoughts leaves the `World` unchanged, and the same `thought` to one that does keeps it;
* `SetExternalURLs` is readable on the item and, called twice with a changed URL, leaves one current URL;
* the vendor's stop (unassignment, or the native stop) yields `TrackerSessionStopped` for the open session and nothing for a closed one;
* a prompt inside an open session yields `TrackerSessionPrompted` carrying the text as data;
* the unconfigured adapter's writes are no-ops and its `Delegates` is empty.

The fit check has its own tests in `pkg/shiftengine`, one per check, each proving that the refusal is posted as the first activity and that no claim, Lease or pod was made.

### 6. What Ploeg owns around the Team

A delegate is a Team, so what a Team is, which version of it ran, and what spend counts against it are decided here too. Three decisions, each Ploeg's:

1. **Team manifests, their versions and their lineage are Ploeg's.** The manifest of [ADR-0110](0110-a-team-is-ploegs-own-manifest-of-charter-crew-permissions-intake-and-skills.md) is received, versioned and read through the operator API, the same door as ADR-0186's bindings: `GET` and `PUT /api/v1/operator/teams/{team}/manifest`, with `If-Match` on the version. Every accepted `PUT` is a new, immutable `version`; a manifest carries `copiedFrom: {team, version}` when it was made from another Team's manifest, so lineage is a field, not a convention. An operator consumer edits manifests there and keeps no copy of its own; the static file remains a valid source and is loaded as version 1 of each Team. Consequence: a consumer's "build a team" page is a client of this API, and there is one definition of a Team at any moment, in Ploeg.
2. **Every Run and Shift records the manifest version it ran under.** `team_manifest_version` is written on the Shift when it opens and on each Run when it is admitted, and is served in the operator API's Run and delivery facts, so a consumer groups outcomes per Team version from Ploeg's facts and computes what it shows ([ADR-0079](0079-run-cards-belong-to-the-consumer-and-ploeg-supplies-delivery-facts.md)). A Run never changes version mid-flight: a manifest `PUT` applies to the next Shift. Consequence: "did version 3 of this Team do better than version 2" is answerable from Ploeg's records without Ploeg computing it.
3. **An experiment allowance per person is Ploeg's budget concern.** Beside the Team's cap ([ADR-0162](0162-a-team-spends-at-most-its-cap-per-period-and-a-claim-over-it-starts-nothing.md)) and the Ask allowance ([ADR-0082](0082-an-ask-is-a-read-only-run-outside-the-shift-paid-from-a-periodic-allowance.md)), an installation may grant a person `experiment: {amount, currency, period}` in the shape ADR-0110's `spend` uses. A Shift a person starts under it, through the operator API, is tagged `experiment: true` with the person, is paid from that allowance and not from the Team's cap, and carries the tag on every Run and on its delivery facts, so a consumer can leave it out of a Team version's record. Ploeg defines the allowance and the tag, and no user interface. Consequence: trying a Team on one card costs the person's allowance, never the Team's budget or its record.

### Consequences

* Good, because a person hands work over by naming a Team on the card and keeps the assignee field, in every tracker, and the card shows the work as it happens.
* Good, because a Team costs no tracker seat and no login; one Ploeg member or one app serves every Team.
* Good, because a misfit is answered in seconds at assignment, with reasons and alternatives, instead of a failed Shift and a `needs_human` later.
* Good, because the first adapter is the one in use, and the second proves the interface: if Linear lands without a change in `pkg/shiftengine`, the shape held.
* Good, because the model stays A2A-compatible, so ADR-0007's facade is one more adapter of this capability and not a second model.
* Bad, because emulation on ClickUp is comments, and comments are noisy: the adapter must drop thoughts and batch actions, and a person who wants the full record opens the consumer's page or the transcript. Accepted: the alternative is a card no one reads.
* Bad, because the ClickUp custom field is a contract with the installation: renaming the field or an option silently breaks "who". `/connections` reports a bound List without the field, and the gate answers with ranked Teams rather than guessing.
* Bad, because the fit check's harness entry test (check 3) needs the repository's tree before a sandbox exists. It runs a shallow read through the forge's `RepositoryInspector`, which is a forge call per assignment; it is cached per repository and commit.
* Bad, because the first implementation is an emulation, so the interface could carry an emulation's assumptions. Mitigated by shaping every method from the native documentation first and by the Linear adapter being the second step, not a later one.
* Neutral, because whether ClickUp's API lets Ploeg create the field and its options is not verified in this record. The design works either way; only the operator's setup differs.
* Bad, because manifest versions, the version on every Run and Shift, and the experiment tag are three more columns and one more allowance in the store, and the static file becomes one source of a Team among two. Accepted: the alternative is a consumer keeping its own copy of a Team, which ADR-0110 and ADR-0079 both refuse.

### Confirmation

Proposed, none of these exist yet:

* `go test ./pkg/provider/...` runs `trackertest`'s `AgentIdentity` cases against every adapter that declares the capability; the ClickUp fixture passes them first, the Linear fixture second, with no change to `pkg/shiftengine` between the two.
* `pkg/shiftengine` tests: each fit check refuses with the right first activity and makes no claim; a passing check opens the Shift and the first activity names the estimate; a card with the field set is never routed by its List; a card without the field routes by its List pin as before (a golden test over today's ClickUp fixture payloads).
* A timing test with the `httptest` ClickUp fake: webhook answered under 5 s and the first activity posted under 10 s, with the gateway unreachable, so no model call sits on the path.
* `provider.TrackerCapabilities` reports `agent_identity` for the ClickUp adapter in start-up logs and `/connections` ([ADR-0129](0129-ploeg-reports-how-each-board-and-repository-it-routes-to-is-wired.md)), with `native: false`.
* `pkg/httpapi` tests against `operator-api.v1.schema.json`: a manifest `PUT` makes a new version and a stale `If-Match` is `412`; `copiedFrom` is served as stored; every Run and Shift the API returns carries `teamManifestVersion`; a Shift started under an experiment allowance carries `experiment` on itself, its Runs and its delivery facts, and its settlement reaches the person's allowance and not the Team's cap (`pkg/store` holds test).
* `go test ./internal/ledger/` keeps this record well-formed.

## Pros and Cons of the Options

### One tracker member per Team

* Good, because it works today with no new code: assign the Team's user.
* Bad, because every Team costs a seat on a paid plan and an account to create, log in, share and rotate, which is the cost ADR-0095 already refused once; and the human loses the assignee field.

### List or board to Team routing in deployment configuration (today's state)

* Good, because it is what exists, and ADR-0186 makes it a runtime call.
* Bad, because a person cannot choose a Team on one card, the choice is invisible on the card, and a misroute fails late. It stays as the fallback while the field is absent.

### A2A as the session protocol

* Good, because A2A v1.0's task states (`SUBMITTED`, `WORKING`, `INPUT_REQUIRED`, `AUTH_REQUIRED`, `COMPLETED`, `FAILED`, `CANCELED`, `REJECTED`) match the session half well, and it has foundation governance.
* Bad, because no tracker speaks A2A, so adopting it as the wire protocol would mean building both ends; and it has no notion of a delegate on an item. Kept as the shape the internal model stays compatible with, per ADR-0007.

## Re-evaluation triggers

* Linear's Agent Sessions leave developer preview, change the activity kinds or the 5 s / 10 s bounds, or add a per-session delegate choice that would let one app serve several Teams.
* Atlassian documents and Ploeg verifies the third-party agent path through an external MCP server, or Rovo credit pricing from 2026-12-03 makes a Jira agent a paid actor.
* ClickUp opens an agent path beyond the Cursor partner integration, or its API gains or confirms custom-field creation, which decides the emulation's setup.
* A cross-tracker standard for agent assignment or sessions appears (A2A gains a tracker binding, the W3C AI Agent Protocol CG or an IETF draft covers more than identity and delegation).
* The Linear adapter cannot land without a change to `pkg/shiftengine`'s activity mapping: the interface carried an emulation's assumption.
* A fit check refuses a card that would have succeeded more than once a week on one installation.

## More Information

* Evidence trail: [research/2026-11-01-tracker-agent-identity-landscape.md](../research/2026-11-01-tracker-agent-identity-landscape.md). Sources: [Linear agents](https://linear.app/developers/agents), [Linear agent interaction](https://linear.app/developers/agent-interaction), [A2A specification](https://a2a-protocol.org/latest/specification/).
* Open questions for the owner: with several Teams on Linear, one app per Team or one app that asks (section 4); the name and default options of the ClickUp field; whether a `prompted` comment after a Shift closed opens a new Shift or needs a fresh assignment; whether the consumer's Shift page is the second external URL.
* Amends: [ADR-0183](0183-ploegs-core-reaches-trackers-and-forges-only-through-ports-and-every-adapter-passes-its-ports-conformance-suite.md) (a new capability and new suite cases), [ADR-0186](0186-an-operator-consumer-binds-a-tracker-scope-to-a-team-at-runtime-and-static-routing-wins-on-conflict.md) (a binding is the fallback when the field is absent), [ADR-0110](0110-a-team-is-ploegs-own-manifest-of-charter-crew-permissions-intake-and-skills.md) (intake gains the delegate), [ADR-0095](0095-work-is-handed-to-ploeg-by-assigning-one-ploeg-user-and-a-team-label-picks-the-team.md) (its trigger fired).
* Related: [ADR-0010](0010-shift-owns-the-item-lease-owns-the-branch.md), [ADR-0011](0011-the-pull-request-is-the-blackboard.md), [ADR-0090](0090-a-runs-agent-session-is-kept-as-a-bounded-redacted-transcript.md), [ADR-0121](0121-findings-carry-ids-writers-answer-them-and-a-run-asks-instead-of-guessing.md), [ADR-0123](0123-dispatch-waits-for-tracker-dependencies-and-an-opt-in-free-premise-check.md), [ADR-0148](0148-every-acp-profile-asks-before-a-mutation-outside-allow-always-and-openhands-refuses-a-repository-that-ships-hooks.md), [ADR-0162](0162-a-team-spends-at-most-its-cap-per-period-and-a-claim-over-it-starts-nothing.md).
