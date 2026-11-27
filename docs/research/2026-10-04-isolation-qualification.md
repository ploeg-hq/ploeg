# Credential isolation: per-harness qualification

Status: research record, 2026-10-04. It qualifies harnesses for both credential proxies of [ADR-0034](../adrs/0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md) and backs the chart default `executor.litellm.keyIsolation: proxy` and `executor.forgeTokenIsolation: proxy` (VIK-576).

**Question.** For each harness the chart offers, has a real Run worked with both proxies on (`PLOEG_LLM_KEY_ISOLATION=proxy`, `PLOEG_FORGE_TOKEN_ISOLATION=proxy`)?

**Method.** The lead read Runs from the webgrip homelab's Ploeg database on 2026-10-04. Both proxies have been on for every homelab team since homelab-cluster commit `da9a1917` (2026-09-28 06:31 CEST). Every homelab worker runs `dind: false` (homelab ADR-0053). A harness counts as qualified when at least one Run with both proxies on reached its normal outcome: a pull request for a writer, an outcome report for the rest.

**Limitations.**

* The Runs below used the proxies as they were before Ploeg #64 (merge `b2530a4`), which made both proxies refuse a request that does not present the Run's placeholder. A harness that sends the placeholders it was given, which is what these Runs did, does not notice that check. No Run has been made since #64 shipped to the homelab.
* Every qualified Run ran without DinD. A harness that calls the model or the forge from inside a DinD container cannot reach the worker's loopback, so the chart turns both proxies off whenever a workload has `dind: true`.
* Evidence is per harness, not per executor. OpenHands ran under the KEDA ScaledJob executor, and the `acp` profile `openhands` under the sandbox executor with `runtimeClassName: kata`. The proxies live on the worker's loopback, so the executor does not change the path a request takes.

## Results

| Harness (chart name) | Qualified | Evidence |
| --- | --- | --- |
| `openhands` | yes | Team silver, KEDA ScaledJob executor. Writer Run 215 (2026-09-30) opened [webgrip/glide#50](https://forgejo.webgrip.dev/webgrip/glide/pulls/50); Run 184 opened [webgrip/glide#15](https://forgejo.webgrip.dev/webgrip/glide/pulls/15). |
| `acp`, profile `openhands` | yes | Team bronze, sandbox executor under `runtimeClassName: kata`. Writer Run 222 (2026-10-01 16:40) opened [webgrip/glide#108](https://forgejo.webgrip.dev/webgrip/glide/pulls/108); Run 220 opened [#88](https://forgejo.webgrip.dev/webgrip/glide/pulls/88), Run 208 [#45](https://forgejo.webgrip.dev/webgrip/glide/pulls/45) and Run 194 [#16](https://forgejo.webgrip.dev/webgrip/glide/pulls/16). Bronze's reviewer Role runs the same harness. |
| `exec` | yes, plumbing only | Team copper (`/bin/cat {taskspec}`), sandbox executor under kata. Runs since 2026-09-28 ended `no_change_needed`, so no pull request exists. `exec` never calls the model or the forge, so these Runs exercise the placeholder plumbing only: the worker starts with both proxies on, hands over placeholders, and reports an outcome. The Run IDs were not copied out of the database for this record. A custom `exec` command that calls the model must read `LLM_BASE_URL` and `LLM_API_KEY` from its environment, as every adapter does. |
| `claude-code` | no | No Run with both proxies on yet. |
| `acp`, profile `opencode` (the `acp` default) | no | No Run with both proxies on yet. |
| `acp`, profile `qwen-code` | no | No Run with both proxies on yet. |
| `acp`, profile `goose` | no | No Run with both proxies on yet. |
| `acp`, profile `custom` | no | No Run with both proxies on yet. The agent depends on the argv, so a qualification would hold only for one agent. |

## What the chart does with this

* The qualified set is the helper `ploeg.isolationQualifiedHarnesses` in [_helpers.tpl](../../ops/helm/ploeg/templates/_helpers.tpl): `openhands`, `acp/openhands` and `exec`. An `acp` workload is matched as `acp/<profile>`, with `opencode` when no profile is set.
* A workload with `dind: true` renders both proxies off with `PLOEG_CREDENTIAL_ISOLATION_OFF_REASON=dind`. A harness outside the set renders them off with `unqualified`. Global values set to `""` render `disabled`.
* `harness.credentialIsolation` on a team or Role (`proxy` or `""`) wins over all of the above for that workload.
* `ploeg-worker` logs one WARN at boot naming the harness and the reason whenever either proxy is off.
* Goldens: `executor-isolation` (default on, DinD fallback, unqualified fallback, `acp` profiles, explicit overrides) and `executor-isolation-disabled`.

## Qualifying another harness

Run one team on the harness with `harness.credentialIsolation: proxy` and `dind: false`. When a Run reaches its normal outcome, add a row here citing the Run and its pull request, and add the harness name to `ploeg.isolationQualifiedHarnesses` in the same change.

A cluster now refuses a Run on a harness outside the set (ADR-0034, amendment of 2026-11-18), so a harness that is not qualified yet cannot get the cluster Run this procedure asks for. For `claude-code` the owner chose on 2026-11-27 to qualify it with a live opt-in test instead.

## 2026-11-27: the `claude-code` qualification test

[`TestLiveClaudeCodeCredentialIsolation`](../../pkg/worker/live_isolation_qualification_test.go) runs one writer Run through the worker's own execute path with both proxies on, the real `claude` binary, a model gateway test key and a test forge repository. Only ploegd is faked. It fails unless:

* `claude`'s argv and environment carry neither the real model key nor the real forge token, and `ANTHROPIC_API_KEY`, `LLM_API_KEY` and `AGENT_BUILDER_TOKEN` hold the Run's placeholders;
* `ANTHROPIC_BASE_URL` and `LLM_BASE_URL` point at the worker's model proxy and `claude`'s git is rewritten to the worker's forge proxy;
* no file under the worker's work directory (the clone, the Run's home with Claude Code's own session files, the scratch directory), `claude`'s raw output, or the transcript sent to ploegd carries either credential, plain, base64 or URL-encoded;
* every model call reached the gateway with the real key the worker's proxy swapped in, at least one did, and no proxy refused a request for a missing or wrong placeholder;
* the Run ended with a pull request the worker observed on the forge.

Run it with:

```sh
PLOEG_HARNESS_CONFORMANCE=1 \
PLOEG_CONFORMANCE_LLM_BASE_URL=... PLOEG_CONFORMANCE_LLM_API_KEY=... PLOEG_CONFORMANCE_LLM_MODEL=... \
PLOEG_CONFORMANCE_FORGE_URL=... PLOEG_CONFORMANCE_FORGE_TOKEN=... PLOEG_CONFORMANCE_FORGE_REPO=owner/name \
mise exec -- go test ./pkg/worker/ -run TestLiveClaudeCodeCredentialIsolation -v -timeout 30m
```

`PLOEG_CONFORMANCE_FORGE` (default `forgejo`) and `PLOEG_CONFORMANCE_FORGE_BASE_BRANCH` (default `main`) are optional. The base URL is the gateway's OpenAI-compatible `/v1` URL, the key a LiteLLM key limited to the cheapest model, and the forge token one that may push a branch and open a pull request in a throwaway repository. The test leaves the pull request open as the evidence to cite.

**Result.** Not run yet: no model gateway test key or test forge was reachable from the machine that wrote the test. The `claude-code` row above stays `no` and `claude-code` stays out of both qualified lists until a passing run is recorded here.
