---
status: proposed
date: 2026-10-18
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A claude-environment Run is started by the person's own Claude Code, and Ploeg stores no Claude login

## Context and Problem Statement

A `claude-environment` Run ([ADR-0084](0084-a-claude-code-run-can-execute-in-a-teams-self-hosted-claude-environment.md)) dispatches its session with a claude.ai login. ADR-0084 used the owner's own seat, kept on a team's login volume. ADR-0094 proposed a team-wide login that an admin connects; it was rejected. Either way, one person's login sits on a shared volume and every Run of the team acts as that person.

Such a login is a personal identity, not a service credential. Claude Code requests scopes for inference, sessions, uploads, connectors and claude.ai Projects, so whoever can read the file acts as that person for up to 30 days.

In a company, several people work with Ploeg, each on their own Claude seat, usually behind the company's SSO. They want the work they start to count against their own seat, and the progress to be visible to colleagues.

Whose login does a Run use, where is it kept, and how does a person's own Claude Code take part?

The first version of this record, on 2026-10-17, chose to store each person's login encrypted in ploegd. Two things then changed the picture:

- **Anthropic's terms.** Anthropic's [legal and compliance terms](https://code.claude.com/docs/en/legal-and-compliance) say developers "may not collect, store, or intermediate Claude.ai credentials or session tokens". The same terms do not prevent "an end user from signing in to the unmodified Claude Code binary with their own Claude subscription, including where a platform hosts Claude Code". Whether a per-person store falls under the first sentence or the second is not settled. The owner asked Anthropic on 2026-10-18 and has no answer yet.
- **The 2026-10-18 spike** ([research](../research/2026-10-18-copied-claude-login-spike.md)). It proved that a copied login dispatches and refreshes without a browser. It also showed that every refresh replaces the refresh token, while the 30-day limit stays where it was.

## Decision Drivers

* Ploeg collects, stores and relays no Claude login, so every reading of Anthropic's terms is met.
* A Run acts with the login of the person it acts for, never a colleague's, and never a shared or bot seat.
* A person's own Claude Code starts or carries their Run.
* Work stays visible and attributed. Each Run names its person and is shown to everyone the operator consumer shows Runs to.
* Work that no person starts still has a path, and that path is paid with API keys.
* Ploeg knows people only through the identities it already records ([ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md)) and through its trackers. It knows no consumer.

## Considered Options

* Only client-carried Runs; Ploeg never stores a login; work nobody starts runs on API keys
* Each person's own login, stored encrypted per person, and a person's own Claude Code may carry the Run
* One team login connected by an admin (ADR-0094)
* One dedicated seat per deployment

## Decision Outcome

Chosen option: "Only client-carried Runs; Ploeg never stores a login; work nobody starts runs on API keys". It is the only option that holds under every reading of Anthropic's terms. It still puts usage on the seat of the person who started the work, and it lets that work run on the team's self-hosted environment after one action from the person.

**A Run that waits for its person.** A Work Item routed to a `claude-environment` team does not queue for a worker. It waits as `needs_human` with the reason "start this from your Claude Code", naming the person it acts for.

- An attended Run acts for its admission's acting person (`X-Ploeg-Acting-User`, ADR-0108).
- Tracker work acts for the tracker user who handed the Work Item to Ploeg (ADR-0095), matched to a person by the identity provider both share, or by a mapping in configuration.

**The person's own Claude Code starts it** (`carrier: client`). An operator consumer, acting as the person, claims the Run. ploegd returns the Run's prompt, environment id and ref. The client then:

1. dispatches with the person's own CLI and login, `claude --environment <id> --ref <ref>`;
2. records the returned session on the Run, as that person, through the existing claude-session route.

From there the runner's hooks exchange the session token, push and report exactly as for a worker-dispatched Run (ADR-0084). The session runs on the team's environment, so the person can close their machine.

A client may also register a session it already runs as a client-carried Run. It then reports the transcript (ADR-0090) and the outcome itself, and Ploeg pushes nothing, because the person's own tools did.

A worker never claims a client-carried Run.

**Starting several at once.** A client may claim every Run waiting for its person in one action, still one dispatch per Run, under the person's own login. Whether a command that drains a queue on a timer stays within ordinary individual use of a seat is one of the questions put to Anthropic. Until it is answered, a person starts the batch themselves.

**Work nobody starts.** A team may name `unattended: api` with an API-key harness (ADR-0008). A `claude-environment` Run that has waited longer than `unattended.after` (default off) then moves to that harness, metered like any other Run. Without that setting, it keeps waiting.

**No stored login.** ploegd keeps no claude.ai credential: no per-person store, no relay of a sign-in, no credential file sent to a worker.

The login volume (`harness.claudeEnvironment.loginVolume`) is deprecated. Its last use is a single-person deployment that dispatches its owner's own work, and its documentation says that this sits in the grey zone of the terms.

### Consequences

* Good, because Ploeg holds no personal credential, so neither a breach of ploegd nor a change in Anthropic's reading of its terms touches anyone's login.
* Good, because usage lands on the seat of the person who started the work, which is also how Anthropic licenses seats.
* Good, because the session still runs on the team's isolated environment, unattended once started.
* Bad, because tracker work on a `claude-environment` team waits until its person starts it, or until the team's API fallback takes it.
* Bad, because the runner's post-session hook still sends the session's own token to ploegd for the push exchange (ADR-0084). Whether that counts as "intermediating" a session token is the second question put to Anthropic. ploegd only verifies it against Anthropic's JWKS and never uses it to call Anthropic.
* Neutral: storing logins per person stays possible later. Its design is kept under "Pros and Cons of the Options" in case Anthropic permits it.

### Confirmation

* `pkg/httpapi` tests:
  * a client-carried Run is never claimed by a worker;
  * only the person it acts for can claim it;
  * a `claude-environment` Work Item without a client waits as `needs_human` with its person named.
* `pkg/store` test: no column, table or migration holds a claude.ai credential. `go test ./internal/ledger/` keeps this record's Confirmation present.
* `pkg/worker` test: a team with `unattended: api` moves a Run that waited past `unattended.after` to its API-key harness, and a team without it never does.
* The 2026-10-18 spike evidence is kept in [research](../research/2026-10-18-copied-claude-login-spike.md). It applies only if the stored-login option returns.

## Pros and Cons of the Options

### Each person's own login, stored encrypted per person

How it would work:

- A person connects their claude.ai login through an operator consumer. A worker runs `claude auth login` in a scratch directory and relays the URL and code.
- ploegd stores the resulting file encrypted with `PLOEG_CLAUDE_LOGIN_KEY`, keyed by the person.
- A Run receives the file, dispatches, and returns the refreshed file. ploegd keeps the returned file only if it still holds the version it handed out, with one dispatch per person at a time.

The 2026-10-18 spike showed that every refresh replaces the refresh token, so a stale copy is dead, and a write-back race could revoke the person's login.

* Good, because tracker work runs without the person present.
* Bad, because Anthropic's terms say developers may not "collect, store, or intermediate Claude.ai credentials", and it is unsettled whether a self-hosted store for an organisation's own members falls under that.
* Bad, because ploegd would hold personal credentials, and a person who leaves keeps a stored login until it lapses, within 30 days.

### One team login connected by an admin (ADR-0094)

* Good, because one connection serves a whole team.
* Bad, because every Run acts as one person, whose personal account is exposed to whoever reads the store.
* Bad, because one login would serve several people's work, which the terms rule out.

### One dedicated seat per deployment

* Good, because a leak exposes a seat with no personal data.
* Bad, because a shared or bot seat is not how Anthropic licenses Team seats, usage lands on no person, and colleagues cannot tell whose work spent it.

## Re-evaluation triggers

* Anthropic answers the 2026-10-18 question: whether a per-person stored login, the session-token exchange, and a person's batch start are permitted.
* Anthropic offers delegated or service authorization for self-hosted environments, such as a token scoped to sessions only, or session creation on a person's behalf.
* Waiting for a person proves too slow for tracker work, and the API fallback is not the right answer.

## More Information

* 2026-10-17 — The owner rejected a shared login. Work should count towards the person who started it, several people should be able to use it inside a company, and progress should be shareable. This record replaces ADR-0094 and the seat choice in ADR-0084.
* 2026-10-18 — The owner asked whether storing logins is allowed. It is not settled, so this record now stores none: client-carried Runs only, with an API fallback. The spike passed, and its findings are kept with the parked option. The question went to Anthropic's sales team.
* Related:
  * [ADR-0084](0084-a-claude-code-run-can-execute-in-a-teams-self-hosted-claude-environment.md)
  * [ADR-0094](0094-a-person-connects-a-teams-claude-login-from-unfold-and-the-teams-worker-signs-in.md) (rejected)
  * [ADR-0095](0095-work-is-handed-to-ploeg-by-assigning-one-ploeg-user-and-a-team-label-picks-the-team.md)
  * [ADR-0090](0090-a-runs-agent-session-is-kept-as-a-bounded-redacted-transcript.md)
  * [ADR-0100](0100-an-attended-run-relays-its-harnesss-requests-through-ploegd-and-falls-back-to-its-teams-policy.md)
  * [ADR-0108](0108-every-operator-action-names-its-person-and-channel-and-an-mcp-action-needs-the-persons-confirmation.md)
  * [ADR-0008](0008-litellm-is-the-credential-and-metering-seam.md)
