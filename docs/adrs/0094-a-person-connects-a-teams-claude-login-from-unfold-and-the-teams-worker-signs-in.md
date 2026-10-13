---
status: proposed
date: 2026-10-13
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A person connects a team's Claude login from Unfold, and the team's own worker signs in

## Context and Problem Statement

A `claude-environment` team (ADR-0084) dispatches with a Team member's claude.ai login, kept on the team's login volume. Today a person creates that login by running a one-off `kubectl run … claude auth login` pod with a page of JSON overrides, Kyverno-shaped probes and security context. Anthropic caps the login at 30 days, so this happens every month, and nothing warns before it lapses: the first sign is a Run that fails at dispatch. That is acceptable for the owner once and unacceptable for a customer. How does a person connect, check and renew a team's Claude login without a terminal?

## Decision Drivers

* A customer admin connects Claude from Unfold, in a browser, in under two minutes, with no `kubectl`.
* No new standing privilege: ploegd gets no right to create pods, and no new component holds the login.
* The login stays where the worker already reads it, the team's login volume, so dispatch does not change.
* Ploeg sees the login's account and expiry, so Unfold warns before it lapses instead of after a failed Run.
* `claude auth login` is interactive only: it prints a URL and reads a code on stdin. There is no flag that takes a code or a refresh token without a browser (2026-10-11: a refresh-token login hung).

## Considered Options

* The team's own worker signs in on request
* ploegd launches a sign-in Job
* ploegd runs the OAuth exchange itself and writes the credential file
* Keep the `kubectl` recipe and document it better

## Decision Outcome

Chosen option: "The team's own worker signs in on request", because it reuses everything the team already has: the worker pod already mounts the login volume, already runs the `claude` CLI, already passes Kyverno and the team's egress policy to claude.ai, and already authenticates to ploegd with its bootstrap token. Nothing gains a privilege it lacked.

How it works:

1. An Unfold admin opens the team's **Claude** connection and clicks **Connect**. Unfold calls `POST /api/v1/operator/teams/{team}/claude-sign-in` with its operator token. ploegd records one pending sign-in for the team, refuses a second while one is open, and refuses a team whose harness is not `claude-environment`.
2. The team's ScaledJob scales on queued Runs *or* a pending sign-in, so a worker starts. On claim, ploegd hands it the sign-in instead of a Run.
3. The worker runs `claude auth login` with `CLAUDE_CONFIG_DIR` on the login volume, reads the sign-in URL from its output, and posts it to ploegd. It never logs the URL or the code.
4. Unfold polls the sign-in, shows the URL as a button, and a field for the code. The person signs in on claude.ai, picks the organisation that owns the environment, and pastes the code.
5. Unfold posts the code; the worker receives it on its next poll and writes it to the CLI's stdin. It then runs `claude auth status`, posts the account, organisation and expiry, and exits. The sign-in expires after 15 minutes without a code.
6. Every `claude-environment` Run also reports the login's account and expiry after dispatch, so the connection page shows "Connected as … · renews by …", and Unfold raises a needs-you item seven days before expiry. A Run that finds no valid login fails with "Claude is not connected for this team" and a link to the page, not a dispatch error.

The `kubectl` recipe stays in the runner README as the operator fallback.

### Consequences

* Good, because a customer admin never sees a terminal, and renewal is the same two clicks.
* Good, because the login never leaves the team's volume and the worker: ploegd and Unfold carry only the one-time URL and code, held in memory and in a row that is cleared when the sign-in ends.
* Good, because expiry becomes visible before it bites.
* Bad, because the sign-in shares the team's one worker slot (`maxReplicaCount: 1`, required by the shared login file): a Run that is executing delays the sign-in until it ends. Accepted: renewal is monthly and the page says what it is waiting for.
* Bad, because the worker scrapes the CLI's output for the URL; a change in Claude Code's wording breaks it. Mitigated by matching the OAuth authorize URL (`https://claude.com/cai/oauth/authorize` in Claude Code 2.1.296) rather than prose, and by a test against the pinned CLI version in the image.

### Confirmation

* Unit tests in `pkg/worker` drive the sign-in against a fake `claude` that prints a URL and expects a code on stdin, and assert the URL and code never reach a log line.
* `pkg/httpapi` tests cover the operator and worker endpoints: one open sign-in per team, team scope on the operator token, a non-`claude-environment` team refused, expiry after 15 minutes.
* The Helm golden for a `claude-environment` team shows the ScaledJob trigger counting pending sign-ins.

## Pros and Cons of the Options

### ploegd launches a sign-in Job

* Good, because the sign-in never waits for a running Run.
* Bad, because ploegd would need RBAC to create pods, a new standing privilege, and the Job would duplicate the worker's pod shape, policies and volume mounts.

### ploegd runs the OAuth exchange itself and writes the credential file

* Good, because no CLI output is scraped.
* Bad, because it reimplements Anthropic's private client flow and credential format, which Anthropic may change without notice, and it puts the login in ploegd.

### Keep the `kubectl` recipe and document it better

* Good, because nothing is built.
* Bad, because it fails the first driver: no customer should run it, and nothing warns before the login lapses.

## Re-evaluation triggers

* Anthropic ships a non-interactive login for self-hosted environments (a code or token flag, or a service identity for dispatch).
* A team needs more than one concurrent worker on one login.

## More Information

* Related: [ADR-0084](0084-a-claude-code-run-can-execute-in-a-teams-self-hosted-claude-environment.md) (the harness and the login volume), [ADR-0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md).
