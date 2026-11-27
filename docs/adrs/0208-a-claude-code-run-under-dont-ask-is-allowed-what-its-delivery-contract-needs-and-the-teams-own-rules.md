---
status: proposed
date: 2026-11-27
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-05-27
---

# A claude-code Run under `dontAsk` is allowed what its delivery contract needs and the Team's own rules

## Context and Problem Statement

The `claude-code` adapter runs `claude -p --permission-mode bypassPermissions` unless `PLOEG_CLAUDE_PERMISSION_MODE` says otherwise, and the chart never renders that variable. Backlog #62 planned `dontAsk` with an explicit allowlist and recorded in July 2026 that `dontAsk` was not a real CLI mode; Claude Code now documents it: every call its permission rules do not allow is refused instead of asked about. Run today under `dontAsk`, a Run gets no rules: Claude Code refused the edit, the commit, the drop box write, the verification script and the notes append, and the session ended as a success with nothing delivered ([research](../research/2026-11-27-claude-code-dont-ask.md)). How does a Team run `claude-code` under `dontAsk`, and who decides what the Run may do?

## Decision Drivers

* The Team's permission policy decides; Ploeg never widens the sandbox, and a call outside the rules is refused, not hung.
* A mode that refuses what the delivery contract requires can never deliver, which is a misconfiguration Ploeg can prevent.
* `bypassPermissions` Teams must render and run exactly as before.
* Claude Code matches a Bash rule against the command text, an output redirect against `Edit` rules, and refuses a redirect to a variable.

## Considered Options

* Ploeg allows what the delivery contract needs, and the Team adds and denies with its own rules
* The Team lists every rule itself
* Map the ACP permission modes (`allow_always`, `allow_read_only`, `deny_all`) onto Claude Code rules

## Decision Outcome

Chosen option: "Ploeg allows what the delivery contract needs, and the Team adds and denies with its own rules", because it is the only one under which a `dontAsk` Run can deliver without each Team rediscovering the contract, while the Team still narrows it.

* The chart's `harness.claudeCode.permissionMode` (`""`, `bypassPermissions`, `dontAsk`) renders `PLOEG_CLAUDE_PERMISSION_MODE` and `harness.claudeCode.permissions` (`allow`, `deny`) renders `PLOEG_CLAUDE_PERMISSIONS`, only into a claude-code pod, most specific level first. The worker refuses an unknown mode or a malformed rule before it claims.
* In every mode but `bypassPermissions` the adapter passes, through `--settings`: `Edit(//<scratch>/**)` and `Bash(printf *)` for every Run; the scripts the prompt names (verification, fast check) by path and by variable, the quoted variable form first; `mcp__<server>__*` for each MCP server Ploeg configured for the Run; and for a writer only, `Edit(//<clone>/**)`, `Bash(git *)` and `Bash(curl *)`. The Team's `allow` rules follow. The scratch directory becomes a working directory, and the prompt names the absolute path of the drop box and of each other file Ploeg names by a variable in the scratch directory.
* The Team's `deny` rules apply in every mode, `bypassPermissions` included, and Claude Code lets a deny rule win over any allow rule, so a Team can take back any rule of the contract.
* Writes to Claude Code's protected paths stay refused under `dontAsk`, whatever the rules say.
* An attended Run ([ADR-0100](0100-an-attended-run-relays-its-harnesss-requests-through-ploegd-and-falls-back-to-its-teams-policy.md)) relays nothing on this harness: `dontAsk` never asks, and only the ACP adapter is to implement the relay.

### Consequences

* Good, because a Team can run `claude-code` under `dontAsk` from the chart, and a call outside its rules ends as a refusal the agent reports.
* Good, because `bypassPermissions` Teams keep their exact invocation.
* Bad, because a `dontAsk` writer cannot edit `.claude/`, `.husky/`, `.npmrc`, `.pre-commit-config.yaml` and the other protected paths with its file tools, so such a change needs another mode or a person.
* Bad, because the variable-rule order relies on undocumented Claude Code behaviour, pinned by a test and re-measured by the live harness test.
* Neutral, because no `claude-code` Run is claimed until the harness is qualified for credential isolation ([ADR-0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md), amendment of 2026-11-18); this record does not change that.

### Confirmation

* `go test ./pkg/harness/adapters/claudecode/`: `TestPrepare_ADontAskWriterIsAllowedWhatTheDeliveryContractNeeds`, `TestPrepare_ADontAskReaderMayNotEditItsCloneOrRunGit`, `TestPrepare_TheTeamsRulesAreAddedAndItsDenyRulesKept`, `TestPrepare_DontAskAllowsTheMCPServersPloegConfigured`, `TestPrepare_ADontAskRunWithPluginsKeepsThePluginHooksAndGetsTheRules`, `TestPrepare_BypassPermissionsKeepsItsArgvAndAppliesOnlyTheTeamsDenyRules`, `TestPrepare_ADontAskRunIsToldTheAbsolutePathOfEachFileItWrites` and `TestPrepare_TheQuotedScriptVariableRuleComesBeforeTheBareOne`.
* `go test ./pkg/worker/`: `TestNewAdapter_RejectsMisconfiguration`, `TestParseClaudePermissions` and `TestTheHarnessIsToldWhetherTheRunWritesAndWhichScriptsItsPromptNames`.
* Live, opt-in like the other live harness tests: `TestLiveClaudeCodeDeliversUnderDontAskAndIsRefusedOutsideItsRules` in `pkg/harness/harnesstest` drives the real binary through the adapter as a writer and requires the edit, the commit, the verification script and the drop box to work and a command outside the rules to be refused.

## Pros and Cons of the Options

### The Team lists every rule itself

* Good, because nothing is allowed that a person did not write down.
* Bad, because every Team must know the drop box, the scratch directory, the variable-rule order and the absolute-path rules, and a Team that misses one gets Runs that end as successes with nothing delivered.

### Map the ACP permission modes onto Claude Code rules

* Good, because one vocabulary would cover every harness.
* Bad, because `allow_always` would have to mean `Bash` for every input, which is `bypassPermissions` without the protected paths, and `deny_all` cannot deliver at all; the Team could not name the commands it does allow, which is what `dontAsk` is for.

## Re-evaluation triggers

* Claude Code documents how rules that differ only in quoting combine, changes how a Bash rule matches a variable, or starts expanding a redirect target.
* Claude Code adds a mode that asks a `--permission-prompt-tool` for calls outside the rules without prompting, which could carry the ADR-0100 relay on this harness.
* `claude-code` is qualified for credential isolation and a Team runs it under `dontAsk` on a cluster.

## More Information

* Evidence: [docs/research/2026-11-27-claude-code-dont-ask.md](../research/2026-11-27-claude-code-dont-ask.md).
* Related: backlog #62, [ADR-0030](0030-target-repository-instructions-rank-below-the-delivery-contract.md), [ADR-0139](0139-a-team-declares-claude-code-plugins-pinned-to-a-commit-whose-hooks-run-guarded-with-only-their-declared-environment.md), [ADR-0148](0148-every-acp-profile-asks-before-a-mutation-outside-allow-always-and-openhands-refuses-a-repository-that-ships-hooks.md).
