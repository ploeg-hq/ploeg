# Ploeg threat model for the OSS Scanner

This is the short brief for Anthropic's [OSS Scanner](https://github.com/anthropics/oss-scanner). The full model is [docs/security/threat-model.md](../docs/security/threat-model.md): read its *Supported deployment profile*, *Entry points*, *Assets* and *Findings* sections before you report anything. Where this brief and that document disagree, that document wins.

## What Ploeg does and where untrusted input enters

Ploeg is a Go control plane for coding agents. `ploegd` takes Work Items from trackers (Vikunja, ClickUp, GitHub Issues) and from authenticated operator consumers, budgets them, and launches one Kubernetes worker pod per Run. In the pod, `ploeg-worker` clones the target repository, runs an agent harness on it, and talks back to `ploegd` for model keys, forge credentials and the outcome. The agent's model traffic and forge traffic pass through credential proxies in the worker, so the harness only ever holds placeholders.

Treat all of the following as attacker-controlled:

- **Repository content** a Run clones: files, git config, hooks, symlinks, submodules, scripts, agent instruction files.
- **Work Item text** from a tracker: title, description, labels, comments. A valid webhook signature proves the tracker sent it, not who wrote it.
- **Everything the agent harness does after prompt injection.** Assume arbitrary code as uid 1000 in the harness container, with the pod's network. The harness is model-directed, so its output (outcome files, summaries, findings, transcripts, SVG cards, notes, follow-up Work Items) is untrusted when `ploegd` or the worker reads it.
- **Webhook requests** on `/webhooks/tracker/{provider}` and `/webhooks/forge/{provider}` from the internet.
- **Pull request reviews, review comments and CI status text** coming back from the forge.

Partly trusted: operator consumers hold a bearer token scoped to teams and a budget, but the person they name (`X-Ploeg-Actor`) is only a claim unless proven. ploeg-mcp clients hold an OAuth access token.

Trusted: the installer's Helm values and Secrets, the Kubernetes API, PostgreSQL, the model gateway (LiteLLM) and the forge's API responses about itself.

## Components that matter most

1. `pkg/httpapi`: webhook verification, worker claim and Run routes (`worker_auth.go`), operator API (`operator.go`), credential handout, outcome handling.
2. `pkg/worker`: everything between the harness and the credentials. The credential proxies, outcome drop box, git handling of hostile repositories, process cleanup, leak detection.
3. `pkg/forgebroker`, `pkg/githubapp`, `pkg/llmbroker`, `pkg/litellm`: minting and scoping per-Run forge tokens and model keys.
4. `pkg/store`: authorization and accounting state in PostgreSQL, lease and run-token checks, spend settlement. SQL built from input.
5. `cmd/ploeg-mcp`, `pkg/mcpauth`: OAuth token checks and the signed confirmation for actions.
6. `pkg/svgsafe`, `pkg/transcript`, `pkg/taskprompt`, `pkg/notes`, `pkg/followup`: agent or tracker text that is rendered, stored, published or turned into new work.
7. `ops/helm/ploeg`: the chart's RBAC, NetworkPolicies, pod security contexts and the profile guard that refuses unsupported values.

## Out of scope

- Anything only reachable outside **Profile P1** (see the full model). That includes `executor.workerAuth.mode: legacy`, `harness.dind: true`, the `claude-code`, `acp/opencode`, `acp/qwen-code`, `acp/goose` and `acp/custom` harnesses, `claude-environment` and the Claude runner, GitLab as a forge, a sandbox runtime class that is not a VM, and ploeg-mcp `execute` groups without login confirmation. Report a way to reach those code paths from a P1 deployment; do not report their behaviour once there.
- That the agent can do what its Role allows in its own workspace: run code, read the repository, push to its own branch, open its own pull request. That is the product.
- Prompt injection that only changes what the agent writes in its own pull request. A person reviews it before merge. Injection that gets a credential, more budget, another repository, another Run's data, or an unreviewed merge is in scope.
- `docs/`, `openspec/`, `knowledge/`, `scripts/` and every `testdata/` directory, unless CI or a release runs the script on untrusted input.
- Vulnerabilities in dependencies themselves. A Ploeg call pattern that makes a dependency exploitable is in scope.
- Plain HTTP between components (F-06), missing per-client rate limits (F-18) and the other open findings in the full model's *Findings* table, unless you show a new path or a worse impact than the table records.

## How to exercise it

- The image runs as the unprivileged `scanner` user and has the module cache, `helm` and built binaries under `/src/bin` (`ploegd`, `ploeg-worker`, `ploeg-mcp`, `ploeg-okf`). `go test ./...` runs offline, including the PostgreSQL-backed tests, which start an embedded server. The first run is logged at `/src/bin/go-test.log`; `TestRestoreDrillReconcilesToTheBackupPointAndFencesEveryOlderLease` is timing-sensitive and can fail on a loaded machine.
- Tests fake every external service with `net/http/httptest`. The fastest way to build a reproducer is a Go test next to the code it attacks, in the style of `pkg/httpapi/worker_auth_test.go` or `pkg/worker/*_test.go`.
- Render the chart with `helm template ploeg ops/helm/ploeg -f ops/helm/ploeg/ci/required-values.yaml` and add a `ci/*-values.yaml` file to try other values.
- `docs/ops/attack-drill.md` describes the hostile-repository drill the project already runs.

## How we rate severity

We use the likelihood-by-impact matrix in the full model.

- **Critical:** with no foothold beyond what an actor above already has, getting the LiteLLM master key, a forge bot password or GitHub App key, the transaction-token signing key, or a forge credential that outlives one Run; escaping the worker pod onto the node; publishing or merging a change nobody reviewed; spending beyond a Run's authorized budget without limit.
- **High:** a per-Run model key or forge token reaching the harness in clear text; one Run reading or acting on another Run, Team or repository; claiming paid work without a valid bootstrap token; forging a webhook that queues paid work; bypassing the ploeg-mcp confirmation; a hostile repository running code in `ploegd` or the worker (not the harness).
- **Medium:** bounded overspend, replaying a signed webhook, persistent denial of service from one unauthenticated request, stored script injection where Ploeg renders agent text, SQL injection behind operator authentication.
- **Low:** information leaks of aggregate data, error-text echo, denial of service that needs sustained load, hardening gaps with no demonstrated path.

A finding that needs the attacker to already hold an asset it would obtain (for example, the master key to get the master key) is not a finding.

## How we want reports and patches

- Name the entry point (EP-id from the full model when one fits), the actor, the asset and the code path with file and function.
- Give a reproducer as a Go test that fails on the current code. That is also how this project lands every security fix.
- Patches: smallest change that closes the path, plus that regression test. Do not edit an existing file in `pkg/store/migrations/`; add the next migration. Keep schemas in `docs/contracts/` and their Go types in step.
- Deduplicate against the full model's *Findings* table and the [open security issues](https://github.com/ploeg-hq/ploeg/issues). If a finding matches an F-id, say which and what it adds.
