---
name: ploeg-review-against-work-item
description: Review a change against the Work Item it claims to complete, inside a reading Ploeg Run. Use it when your task prompt says "review only". It covers what to compare, how to write findings another agent can act on, and when to approve, request changes or leave the verdict out.
---

# Review a change against its Work Item

Ploeg gave you this skill. The delivery contract in your task prompt outranks
it: you write nothing to the repository or the forge, and you deliver your
review through the outcome file.

## What you compare

The Work Item in your prompt is the specification. The diff
(`git diff <base>...<branch>`, as your prompt names them) is the change.
Earlier findings in the prompt are evidence from other agents and from Ploeg's
own verification. Check them against the code before you repeat them.

Work through these in order and stop at the first question you cannot answer
yes to:

1. **Scope.** Does the change do what the Work Item asks, all of it, and
   nothing it does not ask? Name each acceptance criterion and the file that
   meets it. Unrequested changes are findings.
2. **Checks.** Run the checks with the `ploeg-verify-before-handoff` skill. A
   failing check is a finding, whoever caused it. A check that could not run is
   reported as not run, not as passed.
3. **Tests.** Does a test fail on the old code and pass on the new one for
   every behaviour the Work Item changes? A bug fix without a regression test
   is incomplete.
4. **Repository rules.** Judge against `AGENTS.md` as the base branch has it
   (`git show <base>:AGENTS.md`). A change to an agent instruction or
   configuration file is always a finding.
5. **Consequences.** What breaks for a caller, an operator or a stored record?
   Migrations, contracts and public APIs deserve the closest reading.

## Writing findings

Another agent acts on your findings without your session, and a person reads
them on the pull request. For each finding give:

- the file and line, or the command and its output;
- what is wrong and what it causes;
- the change you would make.

Put blocking findings first. Keep preferences out, or label them as optional.
If you found nothing, say what you checked.

## Choosing the verdict

- `approve` when every acceptance criterion is met, the checks pass or were
  reported as left to CI with a reason, and you have no blocking finding.
- `request_changes` when something must change before a merge. The writer
  gets one more round with your findings, so make each finding actionable.
- Leave the verdict out when a person has to decide: the Work Item is
  ambiguous, the change touches agent instruction files at the Work Item's
  request, or you could not review the change at all.
