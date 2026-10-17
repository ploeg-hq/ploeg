---
status: proposed
date: 2026-10-17
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A claude-environment Run uses the Claude login of the person it acts for, and a person's own Claude Code may carry it

## Context and Problem Statement

A `claude-environment` Run (ADR-0084) dispatches its session with a claude.ai login. ADR-0084 chose the owner's own seat, kept on a team's login volume, and ADR-0094 proposed a team-wide login that an admin connects. Either way one person's login sits on a shared volume and every Run of the team acts as that person. Such a login is a personal identity, not a service credential: Claude Code requests scopes for inference, sessions, uploads, connectors and claude.ai Projects, so whoever can read the file acts as that person for up to 30 days. In a company, several people work with Ploeg, each with their own Claude seat, usually behind the company's SSO, and they want the work they start to count against their own seat and the progress to be visible to colleagues. Whose login does a Run use, where is it kept, and how does a person's own Claude Code take part?

## Decision Drivers

* A Run acts with the login of the person it acts for, never a colleague's and never a shared or bot seat.
* No personal login on a shared volume or in plain text. A stored login unlocks only its owner's account, and its owner can remove it.
* A person's own Claude Code can start or carry a Run without Ploeg storing anything.
* Work stays visible and attributed: each Run names its person and is shown to everyone the operator consumer shows Runs to.
* Ploeg knows people only through the identities it already records ([ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md)) and its trackers; it knows no consumer.

## Considered Options

* Each person's own login, stored encrypted per person, and a person's own Claude Code may carry the Run
* Only client-carried Runs; Ploeg never stores a login
* One team login connected by an admin (ADR-0094)
* One dedicated seat per deployment

## Decision Outcome

Chosen option: "Each person's own login, stored encrypted per person, and a person's own Claude Code may carry the Run", because it serves tracker work and attended work alike, keeps every login scoped to its owner, and lets the person's own tool do the work when they are at it.

**Who a Run acts for.** An attended Run acts for its admission's acting person (`X-Ploeg-Acting-User`, ADR-0108). Tracker work acts for the tracker user who handed the Work Item to Ploeg (ADR-0095), matched to a person by the identity provider both share. A Work Item whose person has no Claude connection waits as `needs_human` with "connect your Claude to start this"; Ploeg never falls back to another person's login. The Run records its person and that a plan paid for it.

**Connecting a login.** A person connects their own claude.ai login through an operator consumer, acting as themselves. ploegd asks a worker of a `claude-environment` team to run `claude auth login` in a scratch configuration directory, relays the sign-in URL and the code as ADR-0094 described, and receives the resulting credential file once the CLI reports the account. ploegd stores it encrypted with a key from its environment (`PLOEG_CLAUDE_LOGIN_KEY`), keyed by the person, with the account, organisation and expiry beside it. The person can see, renew and remove their own connection; no one else can read it.

**Using a login.** A Run that dispatches for a person receives that person's credential file over its authenticated channel, writes it to a scratch configuration directory, dispatches, and returns the file afterwards, because the CLI may have refreshed it. ploegd stores the returned file only if it still holds the version it handed out, and lets one dispatch per person run at a time, so two refreshes never race. The scratch directory is removed when the Run ends. Every dispatch reports the login's expiry, and ploegd emits an event seven days before it lapses.

**A person's own Claude Code may carry the Run.** An operator consumer may admit an attended Run as `carrier: client`. ploegd does not queue it for a worker: it returns the Run's prompt, environment and ref to the admitting client, which dispatches with the person's own CLI under the person's own login and records the session on the Run as that person. The runner's hooks then exchange the session token, push and report exactly as for a worker-dispatched Run (ADR-0084). A client may also register a session it already runs as a client-carried Run, and report its transcript (ADR-0090) and outcome itself; Ploeg then pushes nothing, because the person's own tools did. Ploeg stores no login for client-carried Runs.

**The login volume** (`harness.claudeEnvironment.loginVolume`) stays only for a single-person deployment and is deprecated.

### Consequences

* Good, because every login unlocks only its owner, is encrypted at rest and removable by its owner, and a leak of the store without the key reveals nothing.
* Good, because usage lands on the seat of the person who started the work, which is also how Anthropic licenses seats.
* Good, because a person working in their own Claude Code needs no stored login at all.
* Bad, because ploegd now holds personal credentials, encrypted. Accepted: the alternative is the same credential on a shared volume in plain text.
* Bad, because a person who leaves keeps a stored login until it expires or is removed. ploegd deletes a person's connection when the identity provider disables them, if the deployment wires that event; otherwise it lapses within 30 days.
* Bad, because tracker work needs the tracker and the operator consumer to share an identity provider to find the person. A deployment without one names the mapping in configuration.
* Unproven: that a credential file copied into a fresh configuration directory dispatches and refreshes without a browser. A refresh-token login hung on 2026-10-11; a copied file is a different path. The first increment proves it, and this record is revised if it fails.

### Confirmation

* A spike test drives the pinned Claude Code against a copied credential file in a fresh directory, dispatches, and asserts the refreshed file round-trips; it gates the rest.
* `pkg/store` tests: credentials are stored only encrypted, are readable only for their person, and a stale write-back loses to a newer one.
* `pkg/httpapi` tests: one person's connection is never served to a Run acting for another; a Work Item without a connected person becomes `needs_human`; a client-carried Run is never claimed by a worker.
* `pkg/worker` tests: the scratch configuration directory is removed on every exit path, and no credential reaches a log line.

## Pros and Cons of the Options

### Only client-carried Runs; Ploeg never stores a login

* Good, because Ploeg holds no personal credential.
* Bad, because tracker work assigned on a board would need the person's machine to be on and running Claude Code.

### One team login connected by an admin (ADR-0094)

* Good, because one connection serves a whole team.
* Bad, because every Run acts as one person, whose personal account is exposed to whoever reads the store.

### One dedicated seat per deployment

* Good, because a leak exposes a seat with no personal data.
* Bad, because a shared or bot seat is not how Anthropic licenses Team seats, usage lands on no person, and colleagues cannot tell whose work spent it.

## Re-evaluation triggers

* Anthropic offers delegated or service authorization for self-hosted environments (a token scoped to sessions only, or session creation on a person's behalf).
* The copied-credential spike fails.
* A deployment needs a person's Runs to dispatch while that person's seat is suspended.

## More Information

* 2026-10-17 — The owner rejected a shared login: work should count towards the person who started it, also inside a company, several people should use it, and progress should be shareable. This record replaces ADR-0094 and the seat choice in ADR-0084.
* Related: [ADR-0084](0084-a-claude-code-run-can-execute-in-a-teams-self-hosted-claude-environment.md), [ADR-0094](0094-a-person-connects-a-teams-claude-login-from-unfold-and-the-teams-worker-signs-in.md) (rejected), [ADR-0095](0095-work-is-handed-to-ploeg-by-assigning-one-ploeg-user-and-a-team-label-picks-the-team.md), [ADR-0090](0090-a-runs-agent-session-is-kept-as-a-bounded-redacted-transcript.md), [ADR-0100](0100-an-attended-run-relays-its-harnesss-requests-through-ploegd-and-falls-back-to-its-teams-policy.md), [ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md).
