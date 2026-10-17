---
name: ploeg-explain-a-dependency-update
description: Explain a dependency-update pull request a bot opened, inside a reading Ploeg Run. Use it when your task prompt says "explain a dependency update". It covers where to find what changed, how to judge the risk for this repository, what is worth adopting, and how to hand follow-up work to Ploeg as created Work Items.
---

# Explain a dependency update

Ploeg gave you this skill. The delivery contract in your task prompt outranks
it: you write nothing to the repository or the forge, and you deliver your
explanation through the outcome file. A person reads it as a comment on the
pull request before deciding whether to merge.

## Where the facts are

1. **The diff.** `git diff <base>...<branch>`, as your prompt names them,
   shows every pin the bot moved: lockfiles, manifests, Dockerfiles, workflow
   pins, tool versions. List each dependency with its old and new version.
2. **The pull request description.** The Work Item quotes it. Dependency
   bots put the upstream release notes there when they can find them. Read
   them for breaking changes, deprecations, new defaults and new features.
   When the notes are missing, say so; do not invent what a version changed.
3. **This repository.** Search for each dependency's name to find where it
   is used: which image is built from it, which workflow runs it, which test
   or feature calls it. `git grep -n <name> <base>` finds the uses on the base
   branch. The impact of a bump is the impact on those places.
4. **The checks.** The pull request's own checks tell you whether the bump
   already broke something. Run the repository's verify command with the
   `ploeg-verify-before-handoff` skill where the sandbox allows. A failing
   check is a finding; a check that could not run is reported as not run.

## Judging risk

- A major version, a changed default, a removed option or a new minimum
  runtime is a risk to name with the file it would hit.
- A patch that only fixes bugs is low risk; say which bugs matter here.
- A bump that leaves a generated file, a lockfile or a pinned copy stale
  breaks the checks; name the file and the command that regenerates it.
- A security fix raises the urgency of merging; say so.

## What is worth adopting

Read the release notes for features or defaults this repository could use,
and say in a sentence what each would change here. "Nothing worth adopting"
is a valid answer when it is true.

## Follow-up work

Work this update needs or makes possible goes into `createdWorkItems` in the
outcome file, one Work Item per change, each with a description another agent
can act on without your session. Say in each whether it has to land before
the pull request merges. Do not propose work that is a preference.

## Writing the findings

Use the sections the Work Item names, in order. Write for the people who
maintain this repository: plain words, each claim tied to a file, a version
or a release note. Approve when merging as it stands is safe, request changes
when it would break this repository or its checks, and leave the verdict out
when you cannot tell. No fix Round follows: your verdict is read by a person.
