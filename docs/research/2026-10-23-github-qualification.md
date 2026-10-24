# GitHub qualification, 2026-10-23

The question, from issue #44: do the GitHub Issues tracker ([ADR-0085](../adrs/0085-a-team-label-on-an-issue-in-a-configured-github-repository-dispatches-it.md)) and the GitHub forge ([ADR-0086](../adrs/0086-github-is-a-forge-and-a-run-pushes-with-its-own-github-app-installation-token.md)) work against real GitHub, run through their opt-in qualifications against a dedicated test repository?

The answer: the tracker's read and write-back passed against github.com with a personal token. The forge qualification and the GitHub App path did not run, because no installed App holds the permissions they need.

## Setup

- Ploeg `development` at `c37de7fe09725f7cd580b279ad07dccabdfd51d4`, Go 1.27.3 through `mise exec`.
- Test repository [`ploeg-hq/ploeg-qualification`](https://github.com/ploeg-hq/ploeg-qualification): public, created for this run. It holds only a README that says it is a fixture.
- github.com, the default API URL, so `PLOEG_GITHUB_E2E_API_URL` was unset.
- Credentials: `PLOEG_GITHUB_E2E_TOKEN` set to the maintainer's `gh auth token`, passed in the environment and never written to a file or to output. `PLOEG_GITHUB_E2E_APP_ID` was unset.

### Why not the GitHub App

ADR-0086 says a Run pushes with its own App installation token, so the App path is the one that counts. It could not run:

- The organisation's App installations are `ploeg-hq-gitops`, `ploeg-hq-renovate`, `claude` and `chatgpt-codex-connector`.
- The only Ploeg-owned App with a key held for this, `ploeg-hq-gitops`, has `administration`, `secrets`, `actions_variables` and `metadata`. It has no `issues`, `contents`, `pull_requests`, `checks` or `statuses`, so it cannot mint the tokens either qualification asks for (`issues: write` for the tracker, the five permissions of `githubapp.ForgePermissions()` for the forge).
- The key was not reachable from the qualifying workstation anyway: its OpenBao token was refused (`403` on `auth/token/lookup-self`).

The same `go test` run executed the forge test, which skipped with its own message:

```text
=== RUN   TestQualificationAGitHubRunDeliversAPullRequest
    qualification_test.go:23: set PLOEG_GITHUB_E2E_REPO (owner/repo of a dedicated test repository), PLOEG_GITHUB_E2E_APP_ID and PLOEG_GITHUB_E2E_APP_PRIVATE_KEY_FILE to run the GitHub forge qualification
--- SKIP: TestQualificationAGitHubRunDeliversAPullRequest (0.00s)
```

## Command and result

```sh
PLOEG_GITHUB_E2E_REPO=ploeg-hq/ploeg-qualification \
PLOEG_GITHUB_E2E_TOKEN="$(gh auth token)" \
mise exec -- go test ./pkg/provider/githubissues/ ./pkg/provider/github/ -run Qualification -v -count=1
```

| Test | Credential | Result |
| --- | --- | --- |
| `TestQualificationGitHubIssuesReadAndWriteBack` | personal token | PASS (4.34s) |
| `TestQualificationAGitHubRunDeliversAPullRequest` | none (needs an App) | SKIP |

```text
=== RUN   TestQualificationGitHubIssuesReadAndWriteBack
--- PASS: TestQualificationGitHubIssuesReadAndWriteBack (4.34s)
ok  	github.com/ploeg-hq/ploeg/pkg/provider/githubissues	4.346s
```

## What the tracker run exercised

The test opened issue [#1](https://github.com/ploeg-hq/ploeg-qualification/issues/1). Its live state afterwards, read back through the API:

- Title `Ploeg qualification ploeg-qualification-1792733571016263095`, one comment from the provider with the same marker, state `closed`.
- `FetchExecutionItem` read the issue as open, with the title, the scope `ploeg-hq:ploeg-qualification`, and the description `Visible text.`: the body's HTML comment was stripped.
- `Comment` wrote a comment, and `BoardComments` read it back.
- `SetStatus(done)` closed the issue, and a second `FetchExecutionItem` read it as closed.
- The timeline shows two `closed` events one second apart: Ploeg's close, which sent `state_reason: completed` and which the timeline records with a null reason, then the test cleanup's close with `not_planned`. The cleanup therefore overwrites the reason Ploeg set, so this run does not prove from the issue's final state that Ploeg closes as `completed`.

## What it did not exercise

The live qualification covers reads and write-backs only. The following have fixture tests (`httptest`) that pass at the same commit (`go test ./pkg/provider/githubissues/ ./pkg/provider/github/ ./pkg/githubapp/ ./pkg/githubapi/ ./pkg/forgebroker/`), but nothing on github.com tested them:

| Behaviour | Fixture test |
| --- | --- |
| `X-Hub-Signature-256` check | `TestSignatureIsRequiredAndChecked`, `TestParseWebhookRejectsWhatItCannotAuthenticate` |
| Delivery-id deduplication and redelivery | `TestDeliveryIDIsTheGitHubDeliveryHeader`, `pkg/httpapi` GitHub tests |
| Edit, close and reopen events | `TestIssueActionsMapToTrackerEvents`, `TestIgnoredDeliveriesYieldNoEvents` |
| Pagination | `TestBoardCommentsFollowsPagination`, `TestNextLinkOutsideTheAPIRootIsRefused`, `TestCommentsFollowsTheLinkHeaderToTheLastPage` |
| Rate limits | `TestSecondaryRateLimitIsWaitedOutWithinTheBound`, `TestLongRateLimitReturnsRateLimitError`, `TestReadsWaitOutASecondaryRateLimit` |
| App token minting, caching, re-mint on 401 | `pkg/githubapp` tests, `TestRejectedTokenIsForgottenAndRetriedOnce` |
| Per-Run token, branch push, workflow refusal, pull request open/read/comment/close, changed paths, commit status, revocation | `pkg/forgebroker` and `pkg/provider/github` tests; the live forge qualification skipped |
| Reviews, check runs, re-run CI | `TestCommitStatusCombinesStatusesAndCheckRuns`, `TestReRunCIReRunsTheFailedWorkflowRunsOfTheCommit` |

A live webhook check needs a ploegd endpoint that github.com can reach. No qualification test does this yet.

## To finish the qualification

1. Register a Ploeg App owned by `ploeg-hq` with the permissions in [Connect GitHub as a forge](../how-to/connect-github-as-a-forge.md). Install it on `ploeg-hq/ploeg-qualification` only.
2. Run both qualifications with `PLOEG_GITHUB_E2E_APP_ID` and `PLOEG_GITHUB_E2E_APP_PRIVATE_KEY_FILE` in place of the token, and append the result to this note.
