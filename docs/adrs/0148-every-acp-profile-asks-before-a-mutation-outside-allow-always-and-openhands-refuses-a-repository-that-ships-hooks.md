---
status: proposed
date: 2026-10-24
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Every ACP profile asks before a mutation outside `allow_always`, and OpenHands refuses a repository that ships hooks

## Context and Problem Statement

A Run's permission mode (`allow_always`, `allow_read_only`, `deny_all`) is enforced by the ACP adapter's permission policy, which answers each `session/request_permission` and fails closed. It only sees what the agent asks about. The [ACP profiles contract](../contracts/acp-profiles.md) says a profile sets the agent's own approval setting to match the mode, so that outside `allow_always` the agent asks for every tool call. Qwen Code (`tools.approvalMode`) and Goose (`GOOSE_MODE`) do; OpenHands asks for every action by default. The OpenCode profile did not: it ignored the mode, and OpenCode 1.18.30's default ruleset is `"*": "allow"`, asking only for paths outside the working directory, `.env` reads and doom loops. Under `allow_read_only` and `deny_all` an OpenCode Run edited files and ran commands that the policy never saw.

Separately, the OpenHands CLI 1.16.0 (SDK 1.21.0, frozen per [ADR-0118](0118-the-unmaintained-openhands-cli-is-retired-as-a-harness.md)) loads `<working directory>/.openhands/hooks.json` ahead of `~/.openhands/hooks.json`, in both its ACP server and its headless mode. A hook is a shell command OpenHands runs on session start, on each prompt and around each tool call, outside the permission request. A cloned repository could therefore run its own commands in every OpenHands Run under every permission mode, which the [attack drill](0126-a-recurring-attack-drill-runs-every-harness-against-a-hostile-repository.md) is meant to rule out but did not test. How does each profile make the Run's mode decide every mutation, and how do the OpenHands harnesses keep a repository's hooks from running?

## Decision Drivers

* The permission mode is a promise to the person who set it; a mode the agent never asks about is no mode at all.
* The repository is untrusted input ([ADR-0126](0126-a-recurring-attack-drill-runs-every-harness-against-a-hostile-repository.md)); repository-supplied code must not start inside a Run.
* No change may land in the Run's own commit or alter what a reviewer reads in the clone.
* The OpenHands CLI will get no upstream fix, and it has no setting that skips the working directory's hooks file.
* The adapter's permission caps (200 requests per Run, 60 per minute) must not stop a normal read-only Run.

## Considered Options

* OpenCode: write a permission block for the mode into the Run's own config; OpenHands: refuse the Run when the clone ships `.openhands/hooks.json`
* OpenCode: `"*": "deny"` under the non-`allow_always` modes, so OpenCode hides the tools
* OpenHands: neutralise the file in the clone (delete it, empty it, or move it aside around `session/new`)
* OpenHands: point the hooks lookup elsewhere through the environment

## Decision Outcome

Chosen option: "**OpenCode writes a permission block for the mode; OpenHands refuses the Run when the clone ships `.openhands/hooks.json`**", because both keep the decision with the adapter's fail-closed policy and neither touches the clone.

* **OpenCode under `allow_read_only`** gets `"permission": {"*": "ask", "read": "allow", "grep": "allow", "glob": "allow"}`, in that order because OpenCode lets the last matching rule decide. Every edit, command, fetch, subtask and path outside the working directory asks; the three read tools, which the policy grants under this mode anyway, run without a request, so a reviewer's reads do not exhaust the permission caps. It also sets `experimental.continue_loop_on_deny`, so a refused call comes back to the model as a failed tool call and the review continues instead of ending at the first refusal.
* **OpenCode under `deny_all`** gets `"permission": {"*": "ask"}`. Every call asks and is refused, and OpenCode's default ends the turn at the first refusal.
* **OpenCode under `allow_always`** is unchanged: no permission block, OpenCode's own defaults.
* The block replaces any `permission` in a `configJson` override, so an operator's document cannot widen a read-only or deny-all Run. The repository's own `opencode.json` stays switched off by `OPENCODE_DISABLE_PROJECT_CONFIG`.
* **OpenHands**, both the `openhands` adapter and the `acp` profile `openhands`, refuses to prepare a Run whose clone has `.openhands/hooks.json`, following OpenHands' own lookup: a regular file or a symlink that resolves refuses, a dangling symlink or a `.openhands` that is not a directory does not, and any other error to read it refuses. The Run ends with an error naming the file and the two ways forward: remove it, or run the repository with another harness.

### Consequences

* Good, because every profile now meets the contract: outside `allow_always`, every mutation reaches the permission policy.
* Good, because a hostile repository can no longer run commands in an OpenHands Run through a hooks file, and nothing in the clone changes.
* Bad, because a repository that keeps OpenHands hooks for its own developers cannot be worked by either OpenHands harness on Ploeg. Accepted: the CLI is frozen and being retired, and the three ACP agents replacing it are unaffected.
* Bad, because under `allow_read_only` OpenCode now asks for subtasks, web fetches and to-do updates, which count against the permission caps. A read-only Run that calls these more than 60 times a minute or 200 times in all is stopped as stuck. Nothing in the drill reaches the caps; the cost is watched rather than pre-empted.
* Neutral, because OpenHands still asks for every action with ACP tool kind `other`, which `allow_read_only` refuses, so a read-only OpenHands Run cannot read through its tools. That predates this decision and is unchanged by it.

### Confirmation

* Binary-free, on every `mise run verify`: `TestProfile_OpenCodePermissionModeReachesThePolicy` follows each OpenCode tool through the generated rules and the policy with OpenCode's own options and fails on the old profile; `TestProfile_OpenCodeLeavesItsDefaultsUnderAllowAlways`, `TestProfile_OpenCodeConfigOverrideCannotWidenTheRunsPermissions`, `TestHostileConfig_OpenCodeAsksBeforeEveryCallOnAReadOnlyOrDenyAllRun`, `TestHostileConfig_OpenHandsRefusesARepositoryThatShipsHooks`, and `pkg/harness/adapters/openhands` `TestPrepare_RefusesARepositoryThatShipsHooks` and `TestRefuseRepositoryHooks_FollowsOpenHandsLookup`.
* Real binaries, in `.github/workflows/attack-drill.yml` with OpenCode 1.18.30 and OpenHands 1.16.0 installed (`PLOEG_HOSTILE_REPOSITORY=1 go test -run TestHostileRepository ./pkg/harness/adapters/acp/`): `TestHostileRepository_OpenCodeAsksBeforeAMutationOutsideAllowAlways` drives OpenCode through the adapter against a fake gateway that asks for a `bash` call and a `write` call, and requires the call to run under `allow_always` and to be refused under the other two modes; on the old profile it ran under all three. The hostile-repository case for `openhands` requires the profile to refuse, and `TestHostileRepository_OpenHandsRunsARepositoryHookItIsHanded` requires the real binary to run a planted hook when the refusal is bypassed, so the drill cannot pass without seeing the vector.

## Pros and Cons of the Options

### OpenCode: `"*": "deny"` under the non-`allow_always` modes

* Good, because OpenCode would hide the denied tools, so nothing depends on the ACP round trip.
* Bad, because under `allow_read_only` it would also hide fetches and subtasks the policy grants, and it would split the mode's meaning across two places that can drift.
* Bad, because a refusal the policy never sees is not counted or reported.

### OpenHands: neutralise the file in the clone

* Good, because the Run would proceed.
* Bad, because deleting or emptying the file changes the working tree, and a writing Run's `git add --all` would commit the change to the repository's pull request.
* Bad, because moving it aside around `session/new` depends on when OpenHands reads it, which differs between the ACP server and headless mode and repeats on a resumed session; a crash between the move and the restore leaves the clone changed.

### OpenHands: point the hooks lookup elsewhere

* Bad, because 1.16.0 has no such setting: `HookConfig.load` always tries the working directory first, and `OPENHANDS_WORK_DIR` also moves the workspace the agent edits.

## Re-evaluation triggers

* The OpenHands harnesses are retired, or a maintained OpenHands release gains a setting that ignores the working directory's hooks file.
* OpenCode changes how its permission rules are ordered or merged, renames `read`, `grep`, `glob` or `experimental.continue_loop_on_deny`, or its default ruleset stops being `"*": "allow"`.
* A normal read-only or deny-all Run is stopped by the permission caps.
* `TestHostileRepository_OpenHandsRunsARepositoryHookItIsHanded` stops finding its marker, meaning OpenHands no longer reads the working directory's hooks.

## More Information

* Evidence: [docs/research/2026-10-24-acp-permission-mode-and-openhands-hooks.md](../research/2026-10-24-acp-permission-mode-and-openhands-hooks.md).
* Related: [ACP profiles](../contracts/acp-profiles.md), [the attack drill](../ops/attack-drill.md), [ADR-0118](0118-the-unmaintained-openhands-cli-is-retired-as-a-harness.md), [ADR-0126](0126-a-recurring-attack-drill-runs-every-harness-against-a-hostile-repository.md).
