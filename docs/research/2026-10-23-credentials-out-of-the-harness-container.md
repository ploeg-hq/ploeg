# Credentials out of the harness's container: evidence for F-03

Date: 2026-10-23 · Issue: [#224](https://github.com/ploeg-hq/ploeg/issues/224) · Decision record: [ADR-0138](../adrs/0138-a-harness-runs-in-its-own-container-and-no-credential-enters-it.md), proposed. Source read at `development` `060cb06`. This note carries the evidence; the record carries the verdict.

## 1. The finding, line by line

Threat-model finding [F-03](../security/threat-model.md#findings): the shared builder forge token (AS5) and the team bootstrap token (AS3) are present in every writer container even when per-Run tokens are minted, and only an in-process control in the same uid and container keeps them from the harness.

| Claim | Where the code or render shows it |
| --- | --- |
| The harness image is the pod's only container and `ploeg-worker` starts in it | [_helpers.tpl](../../ops/helm/ploeg/templates/_helpers.tpl) lines 457 to 462 (init container copies the binary), [golden executor-isolation.yaml](../../ops/helm/ploeg/ci/golden/executor-isolation.yaml) lines 306 to 309 (`containers: worker`, `image: agent-runner`, `command: /mnt/bin/ploeg-worker`) |
| The harness is a child process of the worker, same uid, same container | [pkg/harness/adapter.go](../../pkg/harness/adapter.go) lines 225 to 228 (`exec.CommandContext`, `cmd.Env = BaseEnv + ExtraEnv`); ACP over stdio at [pkg/harness/adapters/acp/launcher.go](../../pkg/harness/adapters/acp/launcher.go) lines 52 to 61 |
| The bootstrap token is a `secretKeyRef` env var of that container | [_helpers.tpl](../../ops/helm/ploeg/templates/_helpers.tpl) lines 634 to 639; golden lines 335 to 339 |
| The builder (or read-only) token is a `secretKeyRef` env var of that container | [_helpers.tpl](../../ops/helm/ploeg/templates/_helpers.tpl) lines 669 to 678; golden lines 344 to 348 |
| The worker requires the builder token at boot even when a per-Run token will be minted | [cmd/ploeg-worker/main.go](../../cmd/ploeg-worker/main.go) line 119 (`requireEnv("AGENT_BUILDER_TOKEN")`) |
| The shared token is the fallback whenever the claim carries no per-Run token | [pkg/worker/worker.go](../../pkg/worker/worker.go) lines 273 to 275; the claim mints only for a writer with a Work Target: [pkg/httpapi/server.go](../../pkg/httpapi/server.go) lines 712 to 720 |
| A reader never receives a per-Run token, so a reader's forge token is always the shared read-only one | [pkg/httpapi/server.go](../../pkg/httpapi/server.go) lines 709 to 712 |
| The bootstrap token is used for one request, `POST /api/v1/claim`; afterwards the signed control token authenticates | [pkg/worker/api.go](../../pkg/worker/api.go) lines 114 and 127 to 132; [worker-control](../contracts/worker-control.md#authority) |
| The only protection is `PR_SET_DUMPABLE=0` on the worker process plus the ptrace refusal | [pkg/worker/conceal_linux.go](../../pkg/worker/conceal_linux.go) line 15, called at [main.go](../../cmd/ploeg-worker/main.go) line 79; [pkg/worker/capabilities_linux.go](../../pkg/worker/capabilities_linux.go) lines 31 to 36, called at main.go lines 137 to 141; merged as [#63](https://github.com/ploeg-hq/ploeg/pull/63) (`6c82d98`) |
| The harness's own environment is an allowlist plus placeholders | [pkg/worker/environment.go](../../pkg/worker/environment.go) lines 8 to 32; proxies at [worker.go](../../pkg/worker/worker.go) lines 541 to 551 |
| The worker's own git carries the real token in the child's environment | [pkg/worker/git.go](../../pkg/worker/git.go) lines 51 to 57 (`GIT_CONFIG_VALUE_0=Authorization: Basic …`) and 73 to 78 (`cmd.Env`) |
| Such a git runs every two minutes while the harness runs | [pkg/worker/board.go](../../pkg/worker/board.go) line 177, wired with `forgeCred.current` at [worker.go](../../pkg/worker/worker.go) line 513 |
| The root filesystem of the container is writable | `executor.workerSecurityContext` in [values.yaml](../../ops/helm/ploeg/values.yaml) lines 713 to 721 sets no `readOnlyRootFilesystem` |
| Capabilities are dropped, so no second uid is available in one container | [values.yaml](../../ops/helm/ploeg/values.yaml) lines 718 to 719; golden lines 372 to 379 |
| The pod already uses a native sidecar | DinD at [_helpers.tpl](../../ops/helm/ploeg/templates/_helpers.tpl) lines 479 to 481 (`restartPolicy: Always`) |
| The pod mounts no ServiceAccount token | [_helpers.tpl](../../ops/helm/ploeg/templates/_helpers.tpl) line 446 |
| The ploegd image is distroless and carries no git | [ops/docker/ploegd/Dockerfile](../../ops/docker/ploegd/Dockerfile), `distroless/static-debian13:nonroot` |
| Adapters describe a harness as data | `harness.Invocation` at [pkg/harness/adapter.go](../../pkg/harness/adapter.go) lines 178 to 186 |
| `claude-environment` runs a local `claude` CLI with a person's login and no agent in the pod | [pkg/harness/adapters/claudeenv/claudeenv.go](../../pkg/harness/adapters/claudeenv/claudeenv.go) lines 153 to 163; env at [_helpers.tpl](../../ops/helm/ploeg/templates/_helpers.tpl) lines 587 to 597 |

## 2. Experiment: a non-dumpable parent's children are readable

Question: does `PR_SET_DUMPABLE=0` on `ploeg-worker` also hide the environment of the processes it spawns?

Method: a Go program marks itself non-dumpable, then (1) runs a same-uid child that reads the parent's `/proc/<pid>/environ`, and (2) starts `sleep 2` with `CHILD_SECRET` in its environment and runs a same-uid process that reads the child's `/proc/<pid>/environ`. Run on 2026-10-23 as uid 1000 on Linux 6.8.0, `kernel.yama.ptrace_scope=1`, `fs.suid_dumpable=2`, with no capabilities.

```go
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

func main() {
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 0, 0); errno != 0 {
		panic(errno)
	}
	self := exec.Command("cat", fmt.Sprintf("/proc/%d/environ", os.Getpid()))
	out, err := self.CombinedOutput()
	fmt.Printf("parent environ readable by same-uid child: err=%v contains=%v\n", err, strings.Contains(string(out), "PARENT_SECRET"))
	child := exec.Command("sleep", "2")
	child.Env = []string{"PATH=" + os.Getenv("PATH"), "CHILD_SECRET=tok-child"}
	if err := child.Start(); err != nil {
		panic(err)
	}
	time.Sleep(200 * time.Millisecond)
	spy := exec.Command("cat", fmt.Sprintf("/proc/%d/environ", child.Process.Pid))
	out, err = spy.CombinedOutput()
	fmt.Printf("child environ readable by same-uid sibling: err=%v contains=%v\n", err, strings.Contains(string(out), "CHILD_SECRET"))
	child.Wait()
}
```

Output with `PARENT_SECRET=tok-parent` in the parent's environment:

```text
parent environ readable by same-uid child: err=exit status 1 contains=false
child environ readable by same-uid sibling: err=<nil> contains=true
```

Reading: the control in `conceal_linux.go` works for the worker process and for nothing it execs. `execve` sets the dumpable flag back to its default for an ordinary binary, so every `git` the worker runs is readable by the harness for as long as it runs, and [git.go](../../pkg/worker/git.go) puts the forge token in that process's environment. Yama's `ptrace_scope` does not apply: `/proc/<pid>/environ` is checked with `PTRACE_MODE_READ`, which Yama leaves alone. `TestHarnessCannotReadConcealedWorkerEnvironment` ([conceal_linux_test.go](../../pkg/worker/conceal_linux_test.go) lines 53 to 58) reads the parent only, so it cannot see this.

Consequence for F-24: the fix in progress moves token-bearing git out of the harness-writable clone. While worker and harness share a container, the same token is also readable from the git process's environment, and git offers no clean way around it: `-c` lands in `/proc/<pid>/cmdline`, an environment variable in `environ`, and a credential helper over an inherited descriptor in `/proc/<pid>/fd`, all readable from a dumpable same-uid process. A separate container closes the whole set.

## 3. Where documentation and code disagree

| Documentation says | Code shows | Reference |
| --- | --- | --- |
| ADR-0034: the worker is non-dumpable, "so a same-user harness cannot read its environment or memory" | True for the worker process; false for the git processes it spawns, which carry the forge token (section 2) | [ADR-0034](../adrs/0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md); [git.go](../../pkg/worker/git.go) lines 54 and 77 |
| worker-control, Workspace boundary: the bootstrap and builder tokens "stay out of the harness's reach unless the worker hands them over" | Two paragraphs later the same section says a workspace "may inspect same-identity parent resources such as `/proc` and discover its scope-limited bootstrap or forge capability" | [worker-control](../contracts/worker-control.md#workspace-boundary) |
| managed-workers, Operating limits: the worker "still carries a scope-limited bootstrap credential and selected forge access" | The builder token is the bot's read-write token for every repository it can push to (AS5), not scoped to the Run; the bootstrap token has no expiry (AS3) | [managed workers](../ops/managed-workers.md#operating-limits); threat model assets table |
| ADR-0106 `standard` class: credentials are "placeholders, proxies that check the placeholder"; `hardened` and above "move them out of the pod" | There is no rung between "in the harness's container" and "out of the pod"; ADR-0138 adds "out of the harness's container" to `standard` | [ADR-0106](../adrs/0106-a-run-declares-an-isolation-floor-and-a-cell-runs-it-at-the-strictest-class-it-can-honour.md) |
| ADR-0120: the harness "needs no writable image path" | The context is `restricted` without `readOnlyRootFilesystem`, so the image's writable paths stay writable; whether one is on the worker's `PATH` is a property of the harness image | [values.yaml](../../ops/helm/ploeg/values.yaml) lines 713 to 721 |
| Issue #224 names the options "a worker sidecar" and "an init step" | With the worker as the Job's main container and the harness as the native sidecar, the Job still completes when the worker finishes its post-harness work; the inverse would end the pod when the harness exits, before the leak scan and verification | [executor contract](../contracts/executor.md#the-run-protocol) |

## 4. What each option leaves reachable from the harness's container

| | (a) separate container | (b) init exchange | (c) drop shared, exchange bootstrap | (d) harden in place |
| --- | --- | --- | --- | --- |
| Bootstrap token (AS3) | not present | not present | present until exchanged; a projected token is a readable file | present, behind `PR_SET_DUMPABLE` |
| Shared builder token (AS5) | not present | present (fallback at worker.go 273 cannot be exchanged) | absent only if minting is mandatory; readers keep a shared read-only token | present, behind `PR_SET_DUMPABLE` |
| Per-Run key and forge token (AS6, AS7) | not present | in the worker process, one control | same | same |
| Worker's git children with the token | in another PID namespace | readable `/proc/<pid>/environ` | same | same |
| Planted binary on the worker's `PATH` | different root filesystem | possible if the image leaves a `PATH` directory writable | same | same |
| Loopback listeners the harness runs (F-24) | reachable; placeholder check and the F-24 fix still needed | same | same | same |
| Kernel | shared | shared | shared | shared |
