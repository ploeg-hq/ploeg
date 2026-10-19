# ACP initialize waits on the agent's cold start

Evidence for the 5 minute default of `PLOEG_ACP_INIT_TIMEOUT`, recorded on 2026-10-20 against agent-runner 1.4.1 (`sha256:3649001200acbc7708f628403183ce1af93f90a2de339e55dfc70abb2f51f57b`, OpenHands CLI 1.16.0).

## Symptom

Runs 300 to 303 (2026-10-17), 357 and 358 (2026-10-18) and the reviewers 361 and 363 stopped with `acp agent did not start` and `initialize: {"code":-32603,"message":"Internal error","data":{"error":"context deadline exceeded"}}`. The adapter gave `initialize` a fixed 30 seconds. Other Runs on the same days started.

## What the 30 seconds are spent on

`openhands acp` reads its first JSON-RPC line only after `openhands_cli.entrypoint` has imported the whole agent stack. `python -X importtime` on the image at one CPU attributes 3.5 s of a 3.9 s start to that import: `openhands.sdk` pulls in `litellm` (1.8 s on its own), the tool registry and the event models, about 4,000 modules. The work is CPU-bound and runs on one thread, so it scales with the container's CPU limit.

The answer to `initialize` itself is immediate once the import is done; `session/new` follows 0.1 to 0.2 s later. Neither makes a network call before `session/new`, which fetches `<base URL>/v1/model/info` from the gateway.

## Measurements

The image was run under Docker Desktop on an Apple silicon laptop (amd64 under emulation), as uid 1000 with the environment the worker gives a harness, driven by a client that sends `initialize` and `session/new` and times each answer.

| CPU limit | `openhands --version` | answer to `initialize` | same, every module precompiled |
| --- | --- | --- | --- |
| 1 | 4.2 s | 4.7 s, 6.3 s, 7.0 s | 3.6 s, 3.9 s |
| 0.5 | 9.6 s | 9.0 s, 9.4 s, 11.7 s | 8.4 s, 8.8 s |
| 0.25 | | 20.1 s, 22.4 s | 24.6 s, 28.2 s |

The cluster is slower than the laptop. The homelab measured `openhands --version` at 2 min 20 s under a 500m limit and about 19 s under a full core (helmrelease notes, 2026-07-27 and 2026-08-26). The laptop takes 9.6 s and 4.2 s for the same command, so the cluster ran it 4.5 times slower at one CPU and 14 times slower at 500m. Applying those factors to the `initialize` times gives 21 to 32 s for a builder with one CPU, on either side of the old deadline, and 40 s to over 2 minutes for a reviewer with 500m, which misses it almost every time. That matches the failures: builders some of the time, reviewers nearly always. Kata adds a VM and the node is oversubscribed (CPU requests of 100m), so neither factor is a constant.

## Bytecode

The image installs OpenHands without precompiling it. The build's `openhands --version` runs as root and writes bytecode for 4,284 of the 10,774 source files; a Run executes as uid 1000, cannot write to `/opt/uv/tools`, and recompiles the 222 files the ACP start imports that the build did not reach. Precompiling everything saved 1 to 2.5 s at one CPU and nothing measurable at 500m or below. Bytecode is a minor share of the start, not its cause.

## Decision

The deadline is a guard against an agent that never answers, not a performance budget. Five minutes is twice the slowest cold start the cluster has recorded, and costs a wedged agent four and a half extra minutes of a lease that is renewed anyway. The failure now says how long the adapter waited and that the agent was still starting, so a slow start is not read as a protocol fault.
