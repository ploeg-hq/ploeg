# Claude Code's `dontAsk` mode on the claude-code harness

Status: research record, 2026-11-27. Evidence for [ADR-0208](../adrs/0208-a-claude-code-run-under-dont-ask-is-allowed-what-its-delivery-contract-needs-and-the-teams-own-rules.md).

**Question.** What does a `claude-code` Run do when its Team runs Claude Code with `--permission-mode dontAsk`, and which permission rules does it need to deliver?

**Method.** Claude Code 2.1.332 on macOS, `claude -p` with `--model haiku`, `--output-format json`, `--strict-mcp-config` and `--setting-sources project` (or `project,local`), so no user settings reached the session, as in a Run pod whose `HOME` is empty. Each probe ran in a fresh git repository as the working directory with a scratch directory beside it, like a Run's clone and scratch directory. The prompt asked the model to do each step as one tool call and go on after a refusal. A step counts as refused when it appears in the result envelope's `permission_denials`, and as done when its effect is on disk. The documentation read is [Permission modes](https://code.claude.com/docs/en/permission-modes) and [Configure permissions](https://code.claude.com/docs/en/permissions), fetched the same day.

## What the documentation says

* `dontAsk` "auto-denies every tool call that would otherwise prompt". File reads inside the working directories, Claude Code's built-in read-only Bash commands (`ls`, `cat`, `echo`, `grep`, read-only forms of `git` and others) and calls an allow rule covers still run. `AskUserQuestion` and explicit ask rules are always refused.
* Writes to protected paths are refused under `dontAsk` and allowed only under `bypassPermissions`. The protected paths include `.git`, `.claude`, `.vscode`, `.idea`, `.husky`, `.cargo`, `.devcontainer`, `.yarn`, `.mvn`, every `--plugin-dir` and the files `.gitconfig`, `.gitmodules`, shell profiles, `.npmrc`, `.yarnrc*`, `.pre-commit-config.yaml`, `lefthook.yml`, `.mcp.json` and the files passed to `--settings` and `--mcp-config`.
* An `Edit(*)` or `Write(*)` allow rule covers only the working directories. `Edit(//abs/path/**)` is an absolute rule and covers the Write tool too; `Write(path)` rules are not matched. Output redirects (`> file`) are checked against `Edit` rules and protected paths.
* MCP tools need an allow rule; `mcp__<server>__*` allows every tool of one server, and characters such as `.` in a server name become `_`.

## Measurements

| Probe | Settings | Result |
| --- | --- | --- |
| Today's invocation (`--settings '{"disableAllHooks":true}'`) under `dontAsk`: write `hello.txt`, `git add && git commit`, write the drop box in the scratch directory, run `"$PLOEG_VERIFY_SCRIPT"`, append a note with `printf ... >> "$PLOEG_NOTES_FILE"` | none | All five refused. The session ended at once with `subtype: success`: nothing hung, and nothing was delivered. |
| Same steps | `Edit(*)`, `Write(*)`, `Bash(git *)`, the script by path and by variable, `Bash(printf *)`, scratch as `--add-dir` | Four done; the append to `"$PLOEG_NOTES_FILE"` refused. |
| Redirect targets | `Edit(./**)`, `Edit(//<scratch>/**)`, `Bash(echo *)`, `Bash(printf *)` | `echo > out.txt` in the clone and `>> /abs/notes.jsonl` done; `>> "$PLOEG_NOTES_FILE"` refused. With only `Edit(*)`, `echo > out.txt` was refused: `Edit(*)` does not cover a redirect. |
| Script forms with only `Bash(<absolute path>)` | | The absolute path ran; `"$VAR"`, `$VAR` and `sh "$VAR"` refused. |
| Script forms with only `Bash("$VAR")`, `Bash($VAR)`, `Bash(sh "$VAR")` | | All three variable forms ran; the absolute path was refused. A rule matches the command text, not its expansion. |
| Order of the variable rules | `Bash($VAR)` before `Bash("$VAR")` | `"$VAR"` refused (twice, also with other rules beside them). |
| | `Bash("$VAR")` before `Bash($VAR)` | `"$VAR"` and `$VAR` both ran. |
| The adapter's own argv after the change, writer, through `harness.RunCommand` | generated | File written, commit made, verification script ran, drop box written; `python3 -c "open(...)"` outside the rules refused and the Run continued. |
| Same, reader | generated | Write in the clone and `git commit` refused; verification script ran and drop box written; the `python3` call refused. |

Every probe passed its rules with `--settings` while `--setting-sources` left out user settings, and the rules applied, which is what a Run with Team plugins (`--setting-sources user`) relies on.

The model could not find the drop box's path on its own: `printf '%s\n' "$PLOEG_OUTCOME_FILE"` was refused before `Bash(printf *)` was allowed, and a redirect to the variable is refused in any case. Naming the absolute paths in the prompt fixed it.

## Limitations

* One model (Haiku) and one Claude Code version. The rule-order behaviour in particular is undocumented and may change; the adapter test `TestPrepare_TheQuotedScriptVariableRuleComesBeforeTheBareOne` pins the order, and `TestLiveClaudeCodeDeliversUnderDontAskAndIsRefusedOutsideItsRules` re-measures it when the live harness tests run.
* No probe ran inside a Run pod, through the worker's model and forge proxies, or on Linux.
* A `claude-code` Run is not claimed today regardless of its permission mode: since the 2026-11-18 amendment of [ADR-0034](../adrs/0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md), the chart refuses to render and ploegd refuses to claim for a harness that is not qualified for credential isolation, and `claude-code` is not ([qualification record](2026-10-04-isolation-qualification.md)).
