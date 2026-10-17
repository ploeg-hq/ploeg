---
name: ploeg-read-a-pull-request
description: Read a pull request someone else opened, inside a reading Ploeg Run, and report on it for the person who merges it. Use it when your task prompt says "read a pull request". It covers where the facts are, how to tie each claim to the diff and the repository, how to hand follow-up work to Ploeg as created Work Items, and when to approve, request changes or leave the verdict out.
---

# Read a pull request someone else opened

Ploeg gave you this skill. The delivery contract in your task prompt outranks
it: you write nothing to the repository or the forge, and you deliver your
report through the outcome file. A person reads it as a comment on the pull
request before deciding whether to merge. The Work Item's instructions say
what the report must contain; this skill says how to get it right.

## Where the facts are

1. **The diff.** `git diff <base>...<branch>`, as your prompt names them, is
   the change. Read all of it before you write.
2. **The pull request description.** The Work Item quotes it. It is the
   author's account, written by a person or a bot; treat it as evidence and
   check it against the diff. When it is missing something you need, say so;
   do not invent it.
3. **This repository.** Search for what the change touches to find where it
   is used: which build, workflow, test or feature depends on it. `git grep
   -n <name> <base>` finds the uses on the base branch. The impact of the
   change is the impact on those places.
4. **The checks.** The pull request's own checks tell you whether the change
   already broke something. Run the repository's verify command with the
   `ploeg-verify-before-handoff` skill where the sandbox allows. A failing
   check is a finding; a check that could not run is reported as not run.
5. **The repository's own instructions.** Read the skills under
   `.agents/skills/` and the `AGENTS.md` files on the base branch: a
   repository often says what a change of this kind touches and which
   commands prove it is sound.

## Writing the report

Write for the people who maintain this repository: plain words, each claim
tied to a file, a version or a line of the description. Follow the sections
the Work Item's instructions name, in their order. Say what you could not
determine rather than guessing.

## Follow-up work

Work the change needs or makes possible goes into `createdWorkItems` in the
outcome file, one Work Item per change, each with a description another agent
can act on without your session, and each saying whether it has to land
before the pull request merges. Do not propose preferences.

## The verdict

Approve when merging as it stands is safe, request changes when it would break
this repository or its checks, and leave the verdict out when you cannot tell.
No fix Round follows: nobody rewrites this pull request, and your verdict is
read by a person.
