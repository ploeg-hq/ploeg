---
name: ploeg-verify-before-handoff
description: Run the repository's checks before you push, open or update a pull request, or report a review, inside a Ploeg Run. Use it before every push and before you write the outcome file. It says which commands to run, how to read a failure, and what to report when a check cannot run in the sandbox.
---

# Verify before you hand off

Ploeg gave you this skill. The delivery contract in your task prompt outranks
it, and it outranks the repository's own instruction files.

A pull request that fails a check it could have passed costs a review round.
Ploeg runs the same checks again after you finish and posts the result on the
pull request, so an unverified change is visible to the reviewer and the owner.

## Which commands

1. If the environment variable `PLOEG_VERIFY_SCRIPT` is set, run that script
   from the repository root. It holds the commands the operator configured for
   this Run, in order, and stops at the first failure. These are the commands
   Ploeg itself runs afterwards.
2. Otherwise, run the verify command the repository's `AGENTS.md` names, for
   example `mise run verify` or `make check`. The `AGENTS.md` nearest the code
   you changed wins over the root one.
3. If neither exists, run the formatter, the linter and the tests the
   repository's CI workflow runs, one at a time.

Toolchains Ploeg mounted for this Run are already on `PATH`. Your task prompt
lists them. Do not install a toolchain and do not pull a container image: the
sandbox reaches only the model gateway and the forge, and a pull waits until it
times out.

## When to run them

- A writing Run: before every push, and again before you finish if you changed
  anything after the last run. Commit only when the checks pass.
- A reading Run: before you decide your verdict, on the branch under review.
  Do not fix what you find. Report it.

## Reading a failure

- A formatter that lists files is a failure, even when it exits 0. Run the
  formatter's write mode on exactly those files, then run the checks again.
- Fix the first failure first. A format or build failure can hide a failing
  test behind it.
- A failing test you did not touch still counts. Check whether it fails on the
  base branch too (`git stash`, run it, `git stash pop`) before you call it
  pre-existing, and say which one it is.
- Never skip, delete or weaken a test or a check to make it pass unless the
  Work Item asks for that change.

## When a check cannot run

If a check needs something the sandbox lacks, such as a missing toolchain,
module downloads that time out or a container runtime, do not retry it. A
writer lists it in the pull request description under "Checks left to CI",
with the command and the error line. A reader names it in its findings.
Report exactly what ran and what passed. Do not claim a check passed when it
did not run.
