# Agent roster source of truth — research verdict

> Surveyed 2026-07-28 via a five-agent fan-out (Authentik, LiteLLM, Backstage, Vikunja
> source dig, local config map) on where dark-factory personas and teams should be
> defined. Migrated 2026-07-28 from assistant memory into version control because it
> carries a decision that changes **this** repo — see §4 and backlog #103.

**Verdict: one git-owned org manifest in `homelab-cluster`, plus a reconciler in the
`agent-builder-provisioner` mould. Every candidate system is a consumer, not a source.**

## 1. Why a source of truth is needed

The roster (personas `jane`/`bob`/…, teams `papyrus`/`bronze`/`silver`/`gold`) is
currently duplicated across five places with no shared origin: Ploeg's HelmRelease
(`PLOEG_TEAM_MAP` and `executor.teams`), the De Vloer `TEAMS` constant, a Kyverno
exception listing `ploeg-worker-bronze`/`ploeg-worker-silver`, Grafana dashboards and
alerts, and the Vikunja user rows themselves (which have no manifest at all).

Prior art anchor: `kubernetes/org` plus peribolos — YAML in git, a reconciler pushes.
[design.md §3](../design.md) already specifies a Team as a declarative manifest and the
DB `teams` table as a mirror of applied manifests, so this direction is consistent with
the existing design rather than new.

## 2. What each candidate system can actually do

- **Vikunja 2.4.0** added native bot users (`PUT /user/bots`; username must start
  `bot-`; owned by a normal user; no password or email), with tokens minted via
  `PUT /tokens {"owner_id": botID}`. Bot and token routes are **JWT-only** — an API
  token cannot create bots — so a reconciler needs a local "factory" user and a
  `POST /login`. The Teams API is open (creator becomes team admin), and assignability
  requires the assignee to have `CanRead` on the project, satisfied by a read-level team
  share. There is no SCIM, the admin user API is Pro-only, and OIDC/LDAP team sync fires
  only at interactive login, which makes it useless for bots. **We run 2.3.0 — the
  upgrade is the gate.**
- **Authentik** stays the human IdP. Blueprints-in-git could hold the roster, but
  Vikunja cannot consume it headlessly (login-gated sync, by maintainer design), so
  Authentik in the bot loop would be a YAML store with extra steps.
- **LiteLLM v1.83.14** keeps teams and users DB-only; there is no config-as-code
  (export/import is open upstream), all ecosystem sync is inbound, and
  Organizations/SCIM/JWT are paywalled. It is a sink: the reconciler creates teams on
  the free tier, and Ploeg mints per-run keys carrying `team_id` for spend rollup.
- **Backstage v1.51+** treats the catalog as a mirror of git and has no outbound
  provisioning at all. Ingesting the same manifest would buy a free org-chart UI,
  nothing more.

## 3. Owner decisions (2026-07-28)

1. **`PLOEG_TEAM_MAP` dies.** ploegd resolves assignee → team from Vikunja team
   membership at runtime using a factory token, cached; an unknown assignee drops the
   event, which also removes the `PLOEG_DEFAULT_TEAM` footgun. Tracked as backlog #103.
2. **Vikunja bot users: adopted** as the provisioning primitive.
3. **Backstage is out of the loop** — a bespoke minimal `org.yaml` schema, not the
   Backstage descriptor format.
4. **A SCIM bridge was evaluated and rejected**: more machinery than a direct reconciler
   for roughly eight identities, and Vikunja SCIM would likely land Pro-gated anyway.
5. **LiteLLM leg**: trial `PalenaAI/litellm-operator` (LiteLLMTeam CRs generated from
   `org.yaml`), with upstream PRs for the gaps found (team/user metadata fields missing
   from the CRD spec; members are email+role only).

## 4. What this means for Ploeg

The only change inside this repo is decision 1 — replacing the static assignee→team
environment map with a runtime lookup against Vikunja team membership. The manifest, the
reconciler, and the identity plumbing all live in `homelab-cluster`.

Re-evaluate on: a Vikunja OSS admin/user API or SCIM landing (shrinks the reconciler);
LiteLLM gaining teams-as-code natively; `PalenaAI/litellm-operator` maturing.
