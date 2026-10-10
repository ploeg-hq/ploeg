---
type: how-to
audience: [operator, owner]
owner: ploeg
last_verified: 2026-10-10
verified_by: "Read pkg/target/{resolver,readiness}.go, pkg/config/{config,resolve}.go, pkg/httpapi/server.go, pkg/provider/vikunja/vikunja.go, pkg/provider/{forgejo,gitlab}/repository.go and cmd/ploegd/routing.go; go test ./pkg/target ./pkg/config ./pkg/httpapi ./pkg/provider/gitlab ./cmd/ploegd. Not checked against a live deployment."
---

# Route a board that serves several repositories

**Goal:** one tracker board holds tickets for more than one repository. A ticket labelled `repo/<target>` goes to that repository, and a ticket without such a label goes to the board's default, as it does today.

**How it works:** you register repositories under `targets:` in ploegd's configuration file, and each board names the targets it may use. A label is only a choice among those registered targets; it can never name a repository of its own ([ADR-0038](../adrs/0038-a-repo-label-selects-among-registered-targets-and-the-board-default-is-the-fallback.md)). Ploeg reads the labels once, when the ticket is assigned, and pins the result on the Work Item. In ClickUp the labels are the task's tags, and the same rules apply.

Read [Before you start](index.md#before-you-start) for names and the database session.

## 1. Register the targets

Add a `targets:` map to the chart's `config:` value, next to `trackers:`. The key is the text after `repo/` in the label, so the label `repo/homelab-cluster` selects the key `homelab-cluster`.

```yaml
config:
  targets:
    app:
      repo: webgrip/app
      branch: development
      forge: forgejo
    homelab-cluster:
      repo: webgrip/homelab-cluster
      branch: main
      forge: forgejo
```

| Field | Meaning |
| --- | --- |
| `repo` | `owner/name`, required |
| `branch` | Base branch. Unset means the repository's default branch, which may be a stale stub, so set it |
| `forge` | Forge instance id. Unset means `PLOEG_TARGET_FORGE`, default `forgejo` |
Two keys may point at one repository, for example an old and a new label name for the same product. The Run card keys `cardStyle`, `release`, `rarity` and `cardShape` are retired ([ADR-0080](../adrs/0080-ploeg-keeps-no-run-card-code-and-removes-it-in-one-release.md)): ploegd still starts with them, logs a warning and ignores them.

## 2. Point the board at the targets

Give the board a `default:` (a target key) instead of `repo`, `branch` and `forge`, and list the other targets its labels may select under `allow:`:

```yaml
config:
  trackers:
    vikunja:
      projects:
        - name: "App"
          id: "10"
          default: app
          allow: [homelab-cluster]
```

The three shapes a board can take:

| Board config | Ticket without a `repo/*` label | Ticket with `repo/<key>` |
| --- | --- | --- |
| `default: <key>` plus `allow:` | goes to the default | goes to `<key>` if it is the default or in `allow` |
| `repo:` (the existing shape), optionally with `allow:` | goes to `repo` | goes to `<key>` if its target is the board's own repository or in `allow` |
| only `allow:` (hint-required) | refused | goes to `<key>` if it is in `allow` |

A board without `allow:` may only select its own default. ploegd refuses to start when a `default` or `allow` entry names a key that is not under `targets:`, when a board sets both `default` and `repo`, or when a board has none of `repo`, `default` and `allow`.

## 3. Label the tickets

Give a ticket that belongs to another repository exactly one `repo/<key>` label. Ploeg compares the whole label title with `repo/` plus each key, character for character. `repo/Homelab-Cluster` does not match `homelab-cluster`, and is refused as an unregistered label. Labels without the `repo/` prefix are ignored.

Ploeg refuses the ticket, queues nothing and comments on it with the reason when:

- it carries two different `repo/*` labels (two labels with the same title count as one),
- a `repo/*` label names no registered key,
- the label names a registered key the board does not allow,
- the board has no default and the ticket has no `repo/*` label,
- the board selects by label and Ploeg could not read the ticket from Vikunja or ClickUp, so the labels are unknown,
- the selected target is not ready (step 4).

None of these falls back to the default or to a worker's own repository. To retry, fix the label and assign the ticket again. Changing a label on a ticket that is already queued changes nothing: the target was pinned when it was assigned.

**Once `targets:` exists, the label rules apply to every board in the file.** A ticket on any routed board that carries a `repo/*` label no target is registered for, such as a label for an archived repository, is refused from then on. Before you add `targets:`, list the `repo/*` labels in use and register or remove each one.

## 4. Check that each target is ready

When ploegd starts, it asks the forge about every registered target and logs `registered target ready` or `registered target not ready` with the reason. A target is not ready when its repository is archived, is a mirror, or has no `AGENTS.md` at the root of its base branch.

- **At assignment:** a ticket that resolves to a target not ready is refused with that reason. Ploeg asks the forge again first, so a repository you fixed after startup is accepted without a restart.
- **At claim:** Ploeg checks again before any Run starts. If the target became unready, the Run ends `stuck` with the reason and the Work Item moves to `needs_human`. If the forge cannot be reached, the Run ends as a retryable infrastructure failure and the Work Item goes back in the queue.

The Forgejo and GitLab forges report readiness. On GitLab, a pull mirror counts as a mirror, and a target's `repo` may name subgroups (`group/subgroup/project`). A target on any other forge is never ready.

## 5. Verify

1. Restart ploegd after the change (the config checksum on the Deployment does this) and read its log: `target map loaded` reports `registered_targets`, followed by one readiness line per target.
2. Assign a labelled ticket and look for `target resolved` with `hint=repo/<key>` and `registered_target=<key>`.
3. Check the pinned route on the Work Item:

   ```sql
   SELECT external_id, route_rule, route_hint, target_owner, target_repo, target_base_branch
   FROM work_items WHERE provider = 'vikunja' ORDER BY id DESC LIMIT 10;
   ```

4. List refusals, which have an audit row and no Work Item. An operator consumer reads them from `GET /api/v1/operator/route-refusals`: the newest refusal of each task from the last 14 days, newest first and at most 50, within the consumer's teams. Each row carries a stable `code` (`label_missing`, `label_not_allowed`, `label_unregistered`, `multiple_labels`, `labels_unread`, `no_board_rule`, `target_not_ready`, or `unclassified` for a row recorded before codes existed), the `reason` Ploeg commented on the task and the `allowedLabels` of the board, for example `["repo/homelab-cluster", "repo/app"]`. A task that was queued after its refusal drops out. [The operator API schema](../contracts/operator-api.v1.schema.json) defines the response. Older history is still in the audit log:

   ```sql
   SELECT at, detail->>'external_id' AS ticket, detail->>'code' AS code, detail->>'reason' AS reason
   FROM audit_log WHERE action = 'work_item.route_refused' ORDER BY id DESC LIMIT 20;
   ```

## Not implemented yet

- An item whose board matches no rule and that carries no `repo/*` label still falls back to the worker's own repository, as before. Strict routing, which would refuse it (backlog #108), is a separate change.
- The registry is kept by hand. Deriving it from the repository configuration in `webgrip/homelab-cluster` is ADR-0038's later step G1.

## Symptoms

| Symptom | Cause | Fix |
| --- | --- | --- |
| ploegd does not start: `target "x" is not in targets` | A board names a key that is not registered | Add the key under `targets:` or remove it from the board |
| Ticket comment: `names no registered target` | The label has no matching key, or differs in case or spacing | Register the key, or fix the label title |
| Ticket comment: `does not allow` | The key is registered but not in this board's `allow` | Add it to `allow`, or move the ticket to the right board |
| Ticket comment: `requires a repository label` | Hint-required board and no `repo/*` label | Add one label and assign again |
| Ticket comment: `labels could not be read` | ploegd has no Vikunja API credentials, or the read failed | Set `PLOEG_VIKUNJA_URL` and `PLOEG_VIKUNJA_TOKEN`, then assign again |
| Ticket comment: `is not ready` | Target archived, a mirror, or no `AGENTS.md` on its base branch | Fix the repository or the target's `branch`, then assign again |
| Work Item in `needs_human` with a run `stuck` on `is not ready` | The target became unready after the ticket was queued | Fix the repository, then assign the ticket again |
