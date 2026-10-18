---
type: reference
audience: [owner, integrator, contributor, agent]
owner: ploeg
generated_by: "mise run domain"
---

# Glossary — Ploeg

*Generated from `model.yaml` — do not edit by hand.*

## Admission
*Context: Dispatch*

Ploeg's decision to accept work for execution and record it. Operator work is admitted when an authenticated Operator Consumer asks: as a manual-origin Work Item that enters its Team Queue and ploeg-worker executes, or, until they are retired, as an Operator Execution. Tracker work enters through an Assignment and the Routing Rules. No Run starts before its work is admitted.

**See also:** [Authority](#authority), [Operator Execution](#operator-execution), [Operator Consumer](#operator-consumer), [Assignment](#assignment), [Work Item](#work-item)  

## Agent Container
*Context: Harness*

The container a Run executes: a Harness plus its Adapter, invoked with a Task Spec and obligated to write an Outcome Report before exit. Credentials arrive through scoped runtime configuration, never in the spec.

**See also:** [Run](#run), [Task Spec](#task-spec), [Outcome Report](#outcome-report)  

## Ask
*Context: Integration*

A read-only Run with the Role ask about an existing Work Item, admitted for an Operator Consumer outside any Shift, Round or Lease. Ploeg authorizes it against an Ask Allowance and returns a key capped at the per-Ask Budget; the consumer makes the model call. Ask spend is reported apart from what delivering the Work Item cost.

**Not to be confused with** [Run](#run): An Ask is a Run, but not a delivery Run; no worker claims it and it never writes.  
**See also:** [Ask Allowance](#ask-allowance), [Run](#run), [Work Item](#work-item), [Inference Account](#inference-account)  

## Ask Allowance
*Context: Integration*

The amount of Ask spend a scope may authorize in one period, a Team per UTC calendar month in phase 1. An Ask is admitted only when the limit less settled and held Ask spend covers the per-Ask Budget.

**Not to be confused with** [Shift](#shift): A Shift's Budget pays for delivering one Work Item. An Ask never spends it.  
**See also:** [Ask](#ask), [Team](#team)  

## Assignment
*Context: Dispatch*

The normalized tracker event that offers an ingested Work Item to agents, transitioning it to queued. Assigning in the tracker is the sole human gesture that puts work in front of agents; which Team and which Work Target it resolves to is decided by the Routing Rules, never carried in the event.

**See also:** [Tracker Event](#tracker-event), [Routing Rule](#routing-rule), [Team Queue](#team-queue)  

## Authority
*Context: Dispatch*

The one party entitled to approve a Run, set its budget and grant or revoke its credentials. Ploeg is the Authority for every Run it executes. A Run never switches to another authority because a connection fails.

**See also:** [Admission](#admission), [Inference Account](#inference-account), [Push Credential](#push-credential), [Run](#run)  

## Chat
*Context: Integration*

One chat message: a read-only Run with the Role chat about no Work Item, admitted for an Operator Consumer outside any Shift, Round or Lease. Ploeg authorizes it against the Team's Chat Allowance under the Team's ask inference policy and returns a key capped at the per-chat Budget; the consumer makes the model calls. Chat spend is the Team's and never appears in a Work Item's facts.

**Not to be confused with** [Ask](#ask): An Ask is about one Work Item and is paid from the Ask Allowance; a chat message is about none and is paid from the Chat Allowance.  
**See also:** [Chat Allowance](#chat-allowance), [Ask](#ask), [Run](#run), [Inference Account](#inference-account)  

## Chat Allowance
*Context: Integration*

The amount of chat spend a Team may authorize in one UTC calendar month, kept apart from its Ask Allowance. A chat message is admitted only when the limit less settled and held chat spend covers the per-chat Budget.

**Not to be confused with** [Ask Allowance](#ask-allowance): Asks and chat messages are capped apart; spending one never refuses the other.  
**See also:** [Chat](#chat), [Ask Allowance](#ask-allowance), [Team](#team)  

## Checkpoint
*Context: Dispatch*

The small durable progress record (phase, branch, PR URL) a Run writes via the report API. The Task Spec can carry one, but the current unattended worker does not inject the last stored Checkpoint automatically. Retained repository changes and evidence support recovery; native harness state is not a portable Checkpoint.

**See also:** [Run](#run), [Task Spec](#task-spec)  

## Client
*Context: Dispatch*

A customer inside a Tenant that owns some of its tracker sources and repositories. A Work Item takes its Client from them and may have none. Proposed; Ploeg's store does not record it yet.

**See also:** [Tenant](#tenant), [Work Item](#work-item), [Knowledge Scope](#knowledge-scope)  

## Cost Status
*Context: Execution*

How far a cost has settled, one value set for every cost Ploeg reports. observed is spend seen so far that can still move; provisional is a cost settled once and still inside its correction window; settled is final; unknown means Ploeg has no trustworthy figure, which is never zero. A place that cannot reach a value says which subset it uses.

**Examples:** observed; provisional; settled; unknown  
**See also:** [Run](#run), [Ask](#ask), [Inference Account](#inference-account), [Hold](#hold)  

## Delivery Candidate
*Context: Dispatch*

An immutable proposed repository tree with a canonical commit on an approved base, artifact digests and a verification policy digest, bound to one Operator Execution generation. Worker assertions do not verify it.

**See also:** [Operator Execution](#operator-execution), [Verification Receipt](#verification-receipt), [Publication Operation](#publication-operation)  

## Executor
*Context: Execution*

The component that performs admitted Runs and reports progress and outcomes. Current unattended execution uses Kubernetes workers; an Operator Consumer performs delegated operator execution and reports it to Ploeg. The controller recovers missing reports through expiry and reconciliation, without a Kubernetes Job watcher.

**See also:** [Run](#run)  

## Follow-Up
*Context: Dispatch*

A Work Item created by other work rather than by a person: from a Forge Event (review submitted, check failed, merge-state dirty), or from a Run that splits work or records work it discovered (Product R12). It references its source Work Item and carries that Work Item's Work Target. From a Forge Event it also references the source PR, is routed to the Team owning the source branch, and enters the lifecycle directly at queued; today only a failed check creates one, and only for a Team that enabled forgeFollowUps.repairFailedChecks: one open repair per pull request, capped per pull request. From a Run it also references the source Run, says whether it is Ready, exists only in Ploeg, sits one level deeper than its source, is bounded by its Team's created-work limits, and enters the lifecycle at proposed unless the Team sets autoDispatch (ADR-0031).

**See also:** [Work Item](#work-item), [Forge Event](#forge-event), [Team](#team), [Work Target](#work-target)  

## Forge
*Context: Integration*

One registered git forge instance: id, endpoint, dialect, git identity, credential source. A Work Target names a Forge by id and the registry resolves the rest, so no Work Item ever carries a URL or a token. Distinct from a Forge Provider, which is the adapter speaking one Forge's dialect.

**Examples:** the in-cluster Forgejo instance; a second Forgejo; github.com  
**See also:** [Forge Provider](#forge-provider), [Work Target](#work-target)  

## Forge Event
*Context: Integration*

The normalized result of parsing a forge webhook — review_submitted, check_failed, or merge_state_dirty — with the Work Target, PR, branch, and the feedback body for classification. Source of every Follow-Up; carrying a Work Target is what lets the follow-up path and the assignment path converge on one type.

**See also:** [Follow-Up](#follow-up), [Forge Provider](#forge-provider), [Work Target](#work-target)  

## Forge Provider
*Context: Integration*

The SPI adapter for one git forge: verify and parse webhooks into normalized Forge Events and write back PR comments. Reference implementation: Forgejo. The adapter, not the instance it speaks to — that is a Forge.

**See also:** [Tracker Provider](#tracker-provider), [Forge Event](#forge-event), [Forge](#forge)  

## Harness
*Context: Harness*

A concrete agent tool (Claude Code, opencode, …): the program that manages an agent's conversation with a model and runs the tools it is permitted to use. Ploeg never talks to a Harness directly — only through a Harness Adapter — because this boundary churns fastest of any in the system.

**See also:** [Harness Adapter](#harness-adapter), [Agent Container](#agent-container), [Model](#model)  

## Harness Adapter
*Context: Harness*

The thin wrapper that makes one Harness satisfy the harness contract: accept a Task Spec, drive the tool, emit an Outcome Report. Adapters are the isolation layer for harness churn; ACP is tracked as a candidate standard to adopt instead of inventing more.

**See also:** [Harness](#harness), [Task Spec](#task-spec), [Outcome Report](#outcome-report)  

## Hold
*Context: Dispatch*

Budget kept back against a Shift pool, an Ask Allowance or a Chat Allowance for work that started and has not settled. A Run's Hold starts at what it was authorized and ends when its spend settles. A Hold is derived from the Runs, never stored as a counter. The verb and adjective are "held". Authorized names only the ceiling a budget allows a Run to spend; the Hold is how much of it is still kept back.

**Also known as:** held budget  
**Do not use:** reservation, reserved (for money), authorization (for money kept back)  
**See also:** [Shift](#shift), [Run](#run), [Ask Allowance](#ask-allowance), [Chat Allowance](#chat-allowance), [Inference Account](#inference-account), [Cost Status](#cost-status)  

## Inference Account
*Context: Execution*

The durable authorization and accounting record for a Run's scoped inference capability. Uncertain issuance, blocking, expiry and observed spend keep its Hold until trusted final evidence reconciles it. An observed debit is not settlement.

**See also:** [Run](#run), [Operator Execution](#operator-execution), [Hold](#hold), [Cost Status](#cost-status)  

## Knowledge Scope
*Context: Dispatch*

Which Runs a piece of knowledge reaches: the whole Tenant, one Client, one repository, or the Work Items under one root Work Item. Ploeg derives a Run's scopes from its Work Item's records, never from what an agent or request says.

**See also:** [Work Item Note](#work-item-note), [Promotion](#promotion), [Tenant](#tenant)  

## Lease
*Context: Dispatch*

The exclusive right to WRITE a Shift's branch, unique per Shift. It is a capability rather than a note: the holder's push credential is minted with it and revoked when it lapses, so holding a Lease and being able to push are one fact. Only a writing Run takes one — reading Runs take none, which is what lets any number of them run at once. A Lease governs exclusion only; liveness belongs to the Run, because a Run is what dies and a reader has no Lease to expire.

**Do not use:** claim (as a noun), lock  
**See also:** [Shift](#shift), [Run](#run), [Push Credential](#push-credential)  

## Model
*Context: Harness*

The trained system a Harness sends requests to through the model gateway. Ploeg authorizes and accounts for its use per Run through the Run's Inference Account; it never runs a model itself.

**See also:** [Harness](#harness), [Inference Account](#inference-account)  

## Operator Consumer
*Context: Integration*

A named service identity with explicit Team scope and separate read and execution permissions. It authenticates the human actor and asks Ploeg to admit work: a manual-origin Work Item that ploeg-worker executes from its Team Queue, or, until they are retired, an Operator Execution it executes itself.

**See also:** [Team](#team), [Operator Execution](#operator-execution)  

## Operator Execution
*Context: Dispatch*

A Ploeg record admitted for an authenticated Operator Consumer and linked to its session. A consumer requests admission even when a person will steer the work live. It links one Work Item, Shift and Run and retains its identity through changes in supervision. It is being retired (ADR-0102): an Operator Consumer now admits a manual-origin Work Item that ploeg-worker executes, and Operator Executions remain only until no consumer admits one and none is open.

**Not to be confused with** [Shift](#shift): The whole attempt on a Work Item. An Operator Execution is the admitted record that links one.  
**See also:** [Work Item](#work-item), [Shift](#shift), [Run](#run), [Operator Consumer](#operator-consumer), [Inference Account](#inference-account)  

## Outcome
*Context: Dispatch*

The terminal result of a Run, one of: pr_opened, pr_updated, issue_updated, follow_up_created, stuck, failed, no_change_needed. A stuck Outcome carries a mandatory reason and moves the Work Item to needs_human on the tracker path. A failed Outcome follows the applicable tracker or operator recovery policy; it does not universally authorize retry.

**Not to be confused with** [Verdict](#verdict): A reading Run's opinion of the work; an Outcome classifies how the Run itself ended.  
**See also:** [Outcome Report](#outcome-report), [Run](#run)  

## Outcome Report
*Context: Harness*

The output contract of an Agent Container: Outcome, summary, links, and optionally a new Checkpoint, written before exit. An absent report is not a successful outcome; expiry and reconciliation record the recovery state.

**See also:** [Task Spec](#task-spec), [Outcome](#outcome), [Verdict](#verdict)  

## Plan
*Context: Dispatch*

A Team's configured order of work for a Shift: the budget pool, the ordered Rounds and the Roles in each with their writes flag and per-Run cap, and the bound on fix Rounds. Configuration read at boot, never state. Each Run records a hash of the Plan it was claimed under. A Team without a Plan follows the single-writer path.

**Do not use:** pipeline, workflow  
**See also:** [Team](#team), [Round](#round), [Role](#role), [Shift](#shift)  

## Promotion
*Context: Dispatch*

A request to turn a Work Item Note into reviewed knowledge for a wider Knowledge Scope. It reaches other work only after a Tenant admin accepts it.

**Do not use:** approval  
**See also:** [Work Item Note](#work-item-note), [Knowledge Scope](#knowledge-scope)  

## Publication Operation
*Context: Integration*

A durable, explicitly approved operation to publish one verified Delivery Candidate. An in-flight or uncertain external effect retains a barrier against a replacement execution or duplicate publication.

**See also:** [Delivery Candidate](#delivery-candidate), [Verification Receipt](#verification-receipt), [Operator Execution](#operator-execution)  

## Pull Request Reading
*Context: Dispatch*

A Work Item of origin pull_request: Ploeg's record that a Team reads one pull request someone else opened and reports on it there (Ploeg ADR-0109). A registered Work Target routes the pull requests of listed authors to a Team with an instruction in the operator's words; Ploeg itself knows nothing about the authors or the subject. Its Shift runs only the Team's reading Roles on the pull request's own branch, never a writer; the reader's findings are posted as a comment on the pull request, its Verdict opens no fix Round, and the follow-up work it proposes becomes Follow-Ups under the Team's created-work limits. One per pull request, bound to it in Ploeg's own record; read again when the pull request's title changes, up to a cap. It waits as awaiting_review until the pull request merges or closes, or parks as needs_human when the reader says a merge would break the repository.

**See also:** [Work Item](#work-item), [Follow-Up](#follow-up), [Verdict](#verdict), [Work Target](#work-target), [Forge Event](#forge-event)  

## Push Credential
*Context: Execution*

The repository-scoped forge token minted for one writing Run and revoked when its Lease settles or lapses. What makes the Lease enforceable rather than advisory, and the reason a reading Run cannot write the branch it is reviewing. The model-provider equivalent is the per-Run LiteLLM key. It has no expiry of its own, because a Forgejo access token lives until it is deleted, so revocation is its only bound. The forge orphan sweep revokes every token whose Run holds no Lease.

**Do not use:** builder token, deploy key  
**See also:** [Lease](#lease), [Run](#run)  

## Role
*Context: Dispatch*

A named specialist function within a Team (implementer, reviewer, tester), bound to a harness image and model. A Role is a slot in the manifest; a Run is one execution of that slot. Every Role is either a writer (mutates the tree, so its Runs take the Shift's Lease) or a reader (reads and opines, so its Runs take no Lease and may run beside others).

**Also known as:** specialist, specialist role  
**See also:** [Team](#team), [Run](#run), [Lease](#lease)  

## Round
*Context: Dispatch*

A set of Runs within a Shift that start together. Runs in one Round never observe each other — they receive the same injected state and their findings land afterwards; every later Round sees everything from every earlier one. A Round is either a fan-out of reading Runs or exactly one writing Run, never both, and that rule is the whole of the concurrency control.

**Do not use:** turn, iteration  
**See also:** [Shift](#shift), [Run](#run), [Plan](#plan)  

## Routing Rule
*Context: Dispatch*

The operator-declared mapping from (provider, Scope, actor, hint) to a Team and a Work Target. Rules are ordered and first-match-wins, evaluated once at ingest; an event that matches no rule is never dispatched. The set of Work Targets reachable through the rules is closed and operator-declared, so a hint selects among registered routes and can never construct one (R11).

**Do not use:** team map  
**See also:** [Scope](#scope), [Work Target](#work-target), [Team](#team), [Tracker Event](#tracker-event)  

## Run
*Context: Execution*

One execution of one Role against a Work Item, realized by an Executor as a Kubernetes Job or as an execution delegated to an Operator Consumer. A Lease may accumulate several Runs (roles, retries, resumes). The runner reports its outcome; controller expiry and reconciliation recover missing reports while preserving operator stop intent. "Job" is reserved for the Kubernetes object and is never a domain term.

**Do not use:** job (as a domain term), role run  
**Not to be confused with** [Shift](#shift): The whole attempt on a Work Item, which contains one or more Runs.  
**See also:** [Role](#role), [Outcome](#outcome), [Outcome Report](#outcome-report), [Executor](#executor), [Lease](#lease)  

## Scope
*Context: Integration*

An opaque, provider-scoped container id for a body of work (a Vikunja project, a Jira project, a GitHub repository), carried on every Tracker Event. Ploeg compares a Scope for equality against Routing Rule keys and never parses, splits, or otherwise interprets it — equality-only is the test that keeps a vendor concept out of the core (R7).

**Also known as:** container id  
**See also:** [Tracker Event](#tracker-event), [Routing Rule](#routing-rule), [Tracker Provider](#tracker-provider)  

## Shadow Role
*Context: Dispatch*

A reading Role a Team's plan marks `shadow`. Its Runs read beside a reading Role that acts, in the same Round and with the same briefing, and their Verdict and findings are recorded and served but never act: no fix Round, no close reason, no pull request comment, no briefing for a later Run, no created Work Items or Work Item Notes. Its cost counts in the Shift like any Run's. It compares a reviewer model on real work (Ploeg ADR-0096).

**Also known as:** shadow reader, shadow reviewer  
**See also:** [Role](#role), [Verdict](#verdict), [Round](#round)  

## Shift
*Context: Dispatch*

One Team's engagement with one Work Item: the container that owns the branch, the budget pool, the roster of Runs and the Round counter. Opens when the first Run starts, closes when the work reaches a terminal state. A Shift is what makes several Runs on one item coherent without any of them needing to remember the others. Named for the crew sense — Ploeg is Dutch for a crew, and ploegendienst is shift work.

**Do not use:** claim (as a noun), engagement, session, execution  
**Not to be confused with** [Execution](#execution): A retired term for the same attempt. The word still names a bounded context and is part of Operator Execution.  
**See also:** [Lease](#lease), [Run](#run), [Round](#round), [Team](#team), [Work Item](#work-item)  

## Task Spec
*Context: Harness*

The input contract of an Agent Container: Work Item snapshot, Role, optional Checkpoint, and the Work Item's Work Target together with the forge endpoint its Forge id resolves to. Injected as a file mount or environment; deliberately credential-free (R8).

**See also:** [Outcome Report](#outcome-report), [Agent Container](#agent-container), [Work Target](#work-target), [Forge](#forge)  

## Team
*Context: Dispatch*

A declarative manifest — name, Roles, harness image and model per Role, run strategy (sequential or parallel), resource/token budget, concurrency cap — that is the unit of claiming. Two Teams never hold a Shift on the same Work Item; any number of Roles work within one Team's Shift. A Team never names a repository, forge, or credential: capacity and codebase are independent axes (R11) — those coordinates are the Work Item's Work Target, not the Team's.

**Do not use:** crew  
**Examples:** implementer + reviewer-on-a-different-model-family + tester  
**See also:** [Role](#role), [Shift](#shift), [Work Target](#work-target)  

## Team Queue
*Context: Dispatch*

The ordered set of queued Work Items for one Team — a derived view, not a stored entity. Order mirrors the tracker's priority/rank, falling back to oldest-first; Ploeg never owns prioritization, the board does.

**Also known as:** pick-up queue  
**See also:** [Assignment](#assignment), [Work Item](#work-item)  

## Tenant
*Context: Dispatch*

The access boundary above Teams: the users, sources, repositories, budgets and knowledge that belong together (one organisation in a hosted deployment, one default Tenant self-hosted). Proposed; Ploeg's store does not record it yet.

**See also:** [Team](#team), [Client](#client), [Knowledge Scope](#knowledge-scope)  

## Tracker Event
*Context: Integration*

The normalized result of parsing a tracker webhook — assigned, updated, or unassigned — carrying the external id, the provider's Scope, the actor, and normalized hints. It never carries a Ploeg Team or a repository: choosing either is core policy, decided by the Routing Rules (R7). The core only ever sees normalized events, never raw vendor payloads.

**See also:** [Assignment](#assignment), [Tracker Provider](#tracker-provider), [Scope](#scope), [Routing Rule](#routing-rule)  

## Tracker Item
*Context: Integration*

The authoritative item in the external tracker (Vikunja, Jira, GitHub Issues, …). Ploeg reads it via a Tracker Provider and mirrors it into a Work Item; all content edits happen in the tracker, never in Ploeg.

**Also known as:** issue  
**See also:** [Work Item](#work-item), [Tracker Provider](#tracker-provider)  

## Tracker Provider
*Context: Integration*

The SPI adapter for one task-management system: verify and parse webhooks into normalized Tracker Events, fetch items for mirroring, and write back comments and status. Reference implementation: Vikunja.

**See also:** [Forge Provider](#forge-provider), [Tracker Event](#tracker-event)  

## Verdict
*Context: Harness*

A reading Run's answer to "is this done?", reported in its Outcome Report: approve or request_changes, or empty for no opinion. A request_changes Verdict can open a capped fix Round (Ploeg ADR-0017), unless a Shadow Role gave it.

**See also:** [Outcome Report](#outcome-report), [Round](#round), [Role](#role), [Shadow Role](#shadow-role)  

## Verification Receipt
*Context: Execution*

Evidence reported by an explicitly authorized verifier for one Delivery Candidate and policy digest. It records the real check exit and discovery count from an independent disposable environment.

**See also:** [Delivery Candidate](#delivery-candidate), [Operator Consumer](#operator-consumer)  

## Work Item
*Context: Dispatch*

A unit of work: something we have decided to do, or a problem described well enough that a solution can be formulated or at least conceived. It comes from a Tracker Item, from an Admission for an Operator Consumer (a manual-origin Work Item, or the one an Operator Execution links), from other work (a Follow-Up), or from a pull request someone else opened that a Team reads (a Pull Request Reading). Ploeg keeps one record per Work Item and runs Shifts against it; a failed Shift leaves it in place. Tracker-originated content remains owned by the Tracker Item. Manual-origin content is registered by an authenticated Operator Consumer and never silently creates a Tracker Item.

**Do not use:** task, ticket  
**See also:** [Tracker Item](#tracker-item), [Lease](#lease), [Follow-Up](#follow-up), [Work Target](#work-target)  

## Work Item Note
*Context: Dispatch*

Knowledge a Run, or a person, wrote about one piece of work, stored without review. Only later Runs of Work Items under the same root read it.

**Do not use:** memory  
**See also:** [Knowledge Scope](#knowledge-scope), [Promotion](#promotion)  

## Work Target
*Context: Dispatch*

The forge coordinates a Work Item's Runs act on: forge, owner, repository, base branch. Resolved at ingest from the Scope the item arrived in, pinned on the Work Item, and independent of the Team that claims it — a Team never names one. A coordinate, not a connection: it carries a forge id, never a URL and never a credential (R8).

**Also known as:** target  
**Do not use:** team repo, repo_url  
**See also:** [Work Item](#work-item), [Forge](#forge), [Routing Rule](#routing-rule), [Team](#team)  

---

## Retired terms

Do not use these names as terms.

### Execution
*Use instead: [Shift](#shift), [Run](#run)*

The attempt it named is a Shift, and one Role's part of it is a Run. A second name for a Shift would make every execution statement ambiguous. The word remains a bounded-context name and part of Operator Execution.

### Leased
*Use instead: [Work Item](#work-item)*

The Work Item state in which a Team works the item is active. It was called leased after the Lease it used to imply, but a Work Item in it has a Shift and may hold no Lease, since a Round of readers takes none (ADR-0093). Lease still names the right to write a Shift's branch.

### Reservation
*Use instead: [Hold](#hold)*

Money kept back for unsettled work is a Hold, and the API says held. The inference account state reserved names a lifecycle step, not money.

---

## Example dialogues

Short exchanges showing the terms used precisely at concept boundaries.

### Research can recommend stopping

> **Developer:** The research says this product should not be built. Must the agent still build a prototype?
> **Product owner:** No. Convincing evidence, a business case and a clear conclusion to stop can finish that ticket.
> **Developer:** And if the evidence supports building it?
> **Product owner:** I expect a usable design document, recorded decisions, graphs, documentation, a business case and a final conclusion. A proof of concept may also help.

### Lease vs claim, and what a crash does
*Context: Dispatch*

> **Dev:** The implementer pod got OOM-killed halfway. Who cleans up its claim?
> **Domain expert:** The controller detects the expired **Lease** and applies the recovery policy (**R2**). Recovery does not depend on the dead pod running cleanup.
> **Dev:** So the item is lost?
> **Domain expert:** The retained record remains queryable. Unattended tracker work may retry within **R5**; operator-owned work keeps its stop intent. A stored **Checkpoint** does not guarantee automatic restoration of the harness session.
> **Dev:** And "claim" — is that a table?
> **Domain expert:** A verb. A **Team** claims an item, which means it acquires a **Lease**. If you're writing SQL, the noun is always Lease.

### stuck vs failed vs needs_human
*Context: Dispatch*

> **Dev:** The agent container exited zero but never wrote anything. Is that stuck?
> **Domain expert:** A missing **Outcome Report** needs expiry or reconciliation (**R3**). Legacy tracker work may requeue, but operator-owned work must preserve stop intent and uncertain effects.
> **Dev:** Then what's stuck?
> **Domain expert:** stuck is the agent saying "I understand the task and I cannot proceed" — it must give a reason (**R4**), and the **Work Item** goes to needs_human, not back to the queue.
> **Dev:** And stale?
> **Domain expert:** stale means the machinery gave up — repeated **Lease** expiries with no Outcome at all. needs_human is the agent asking for help; stale is Ploeg refusing to retry blindly.

---

## ⚠ Flagged ambiguities

### who decides that delegated work is finished

Successful checks, another agent's review and a person's approval are different possible conditions for finishing work; the product has no agreed rule for choosing between them.

**Options:** Choose the condition for each task, Always require a person's approval, Let the agent finish when its checks pass  
**Recommendation:** Choose the condition for each task so research, coding and other work can use appropriate checks and approval.  

### when a Shift closes

ADR-0010 introduces the Shift but leaves its terminal rule to the implementation. A Shift plainly closes on a terminal Outcome; less plainly when an item goes needs_human and a human re-queues it — does the old Shift resume with its remaining budget and Round counter, or does a fresh one open?

**Options:** Re-queue always opens a new Shift (budget resets, rounds restart), Re-queue resumes the existing Shift (budget and rounds carry over), Human chooses per re-queue  
**Recommendation:** Resume the existing Shift. A re-queue after needs_human is usually a human unblocking work already done, and restarting the budget silently doubles what the item may cost. Confirm against the first real needs_human Shift.  

### follow-up mirroring

Follow-Ups enter at queued with no Tracker Item, which tensions with "the tracker is the source of truth for what to do" — work now exists that the board cannot see.

**Options:** Keep Follow-Ups tracker-invisible (current), Asynchronously write a Tracker Item back for every Follow-Up, Write back only Follow-Ups that survive longer than one Run  
**Recommendation:** Revisit in roadmap phase 2 (PR-feedback ingestion); async write-back is the likely answer so the board regains full visibility without blocking dispatch.  

### groomer run

The design says Ploeg "can schedule a groomer run" but grooming semantics belong to the operator — it is unclear whether Groomer is Ploeg vocabulary at all, and what distinguishes a groomer run from a normal Run.

**Options:** Keep Groomer out of the core language (operator concern), Define it as a Team with a single grooming Role, First-class GroomerRun concept  
**Recommendation:** Keep it out of the core language for now; if it lands in phase 2, model it as an ordinary Team whose single Role grooms — no new concepts.  
