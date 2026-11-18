import copy
import json
from dataclasses import dataclass

from worker_admission_cases import admission, render, team

NAMESPACE = "admission-claude-runner"
ORCHESTRATOR = "admission-claude-runner-orchestrator"
SESSION_ANNOTATION = "ploeg.webgrip.dev/claude-session"
WORK_ORDER = "PLOEG_CLAUDE_RUNNER_WORK_ORDER"
GATEWAY_BOOTSTRAP = "ploeg-worker-bootstrap"
GATEWAY_ACCOUNT = "admission-credential-gateway"

VALUES = {
    **admission(),
    "supportProfile": "unsupported",
    "claudeRunner": {"enabled": True, "mode": "orchestrator", "runtimeClassName": "kata", "environmentSecret": {"name": "claude-runner-environment"}},
    "executor": {"enabled": True, "credentialGateway": {"enabled": True}, "teams": [team("silver", harness={"dind": False})]},
}

SERVICE_ACCOUNT = "The runner pod runs as the session ServiceAccount without a Kubernetes API token."
VOLUMES = "The runner pod mounts only the chart's emptyDir volumes: no Secret, projected, hostPath or PersistentVolumeClaim volume."
PLACEMENT = "The runner pod shares nothing with its node and is placed like the chart's runner Job."
POD_LABELS = "The runner pod carries exactly the labels of the chart's runner Job."
JOB_ANNOTATIONS = "The runner Job carries no annotation but its order and session."
POD_ANNOTATIONS = "The runner pod carries no annotation but its session and the chart's podAnnotations."
CONTAINERS = "The runner pod runs exactly the chart's containers."
RUNNER = "Container runner is the chart's: its image, command, arguments, mounts and security context, and no environment but the chart's and the work order."
INIT = "Init container claude-code is the chart's: its image, command, mounts, security context and environment."
JOB_SPEC = "The runner Job runs one pod once, for as long as the chart's runner Job."


@dataclass(frozen=True)
class HostileJob:
    name: str
    job: dict
    messages: frozenset


def rendered(kube="1.30.0"):
    documents = render(VALUES, NAMESPACE, kube=kube)
    config = next(d for d in documents if d["kind"] == "ConfigMap" and d["metadata"]["name"].endswith("-orchestrator"))
    policy = next(d for d in documents if d["kind"] == "ValidatingAdmissionPolicy" and d["metadata"]["name"].endswith("-claude-runner-jobs"))
    binding = next(d for d in documents if d["kind"] == "ValidatingAdmissionPolicyBinding" and d["metadata"]["name"] == policy["metadata"]["name"])
    agent = next(d for d in documents if d["kind"] == "ValidatingAdmissionPolicy" and d["metadata"]["name"].endswith("-agent-security"))
    return documents, json.loads(config["data"]["job.json"]), policy, binding, agent


def spawned(template, order="order-1", session="session_01ADMISSION"):
    job = copy.deepcopy(template)
    job["metadata"]["name"] = f"ploeg-claude-runner-{order}"
    job["metadata"].setdefault("annotations", {}).update({"ploeg.webgrip.dev/claude-order": order, SESSION_ANNOTATION: session})
    job["spec"]["template"]["metadata"]["annotations"][SESSION_ANNOTATION] = session
    runner = next(c for c in job["spec"]["template"]["spec"]["containers"] if c["name"] == "runner")
    runner["env"].append({"name": WORK_ORDER, "value": "eyJ.admission-work-order.sig"})
    return job


def as_admitted(job):
    defaulted = copy.deepcopy(job)
    defaulted["spec"]["template"]["metadata"]["labels"].update({"batch.kubernetes.io/job-name": job["metadata"]["name"], "job-name": job["metadata"]["name"],
                                                               "batch.kubernetes.io/controller-uid": "uid", "controller-uid": "uid"})
    return defaulted


def session_pod(job):
    template = job["spec"]["template"]
    return {"apiVersion": "v1", "kind": "Pod", "metadata": copy.deepcopy(template["metadata"]) | {"name": job["metadata"]["name"], "namespace": NAMESPACE},
            "spec": copy.deepcopy(template["spec"])}


def hostile_jobs(job):
    def change(name, messages, edit):
        changed = copy.deepcopy(job)
        edit(changed, changed["spec"]["template"]["spec"], next(c for c in changed["spec"]["template"]["spec"]["containers"] if c["name"] == "runner"))
        return HostileJob(name, changed, frozenset(messages))

    def mount(container, name, path):
        container["volumeMounts"].append({"name": name, "mountPath": path})

    def bootstrap(_, pod, runner):
        pod["volumes"].append({"name": "bootstraps", "secret": {"secretName": GATEWAY_BOOTSTRAP}})
        mount(runner, "bootstraps", "/var/run/secrets/ploeg/bootstraps")

    def gateway_token(_, pod, runner):
        pod["serviceAccountName"] = GATEWAY_ACCOUNT
        pod["volumes"].append({"name": "gateway-token", "projected": {"sources": [{"serviceAccountToken": {"audience": "ploegd", "path": "token"}}]}})
        mount(runner, "gateway-token", "/var/run/secrets/ploeg/gateway")

    return [
        change("the gateway's bootstrap Secret mounted", {VOLUMES, RUNNER}, bootstrap),
        change("the gateway's ServiceAccount with a ploegd-audience token", {SERVICE_ACCOUNT, VOLUMES, RUNNER}, gateway_token),
        change("another ServiceAccount", {SERVICE_ACCOUNT}, lambda _, pod, __: pod.__setitem__("serviceAccountName", GATEWAY_ACCOUNT)),
        change("the API token mounted", {SERVICE_ACCOUNT}, lambda _, pod, __: pod.__setitem__("automountServiceAccountToken", True)),
        change("a hostPath volume", {VOLUMES}, lambda _, pod, __: pod["volumes"].append({"name": "host", "hostPath": {"path": "/"}})),
        change("a PersistentVolumeClaim", {VOLUMES}, lambda _, pod, __: pod["volumes"].append({"name": "login", "persistentVolumeClaim": {"claimName": "pewter-claude-login"}})),
        change("an env var from a Secret", {RUNNER}, lambda _, __, runner: runner["env"].append(
            {"name": "TOKEN", "valueFrom": {"secretKeyRef": {"name": GATEWAY_BOOTSTRAP, "key": "silver--default"}}})),
        change("envFrom a Secret", {RUNNER}, lambda _, __, runner: runner.__setitem__("envFrom", [{"secretRef": {"name": GATEWAY_BOOTSTRAP}}])),
        change("another image", {RUNNER}, lambda _, __, runner: runner.__setitem__("image", "registry.example/attacker:1")),
        change("another command", {RUNNER}, lambda _, __, runner: runner.__setitem__("command", ["sh", "-c", "cat /var/run/secrets/*"])),
        change("another PLOEG_URL", {RUNNER}, lambda _, __, runner: next(e for e in runner["env"] if e["name"] == "PLOEG_URL").__setitem__("value", "http://attacker.example")),
        change("a privileged runner", {RUNNER}, lambda _, __, runner: runner["securityContext"].update({"privileged": True, "allowPrivilegeEscalation": True})),
        change("a second container", {CONTAINERS}, lambda _, pod, runner: pod["containers"].append(copy.deepcopy(runner) | {"name": "sidecar"})),
        change("the Run pod label", {POD_LABELS}, lambda job, _, __: job["spec"]["template"]["metadata"]["labels"].__setitem__("app.kubernetes.io/name", "ploeg-worker")),
        change("an extra Job annotation", {JOB_ANNOTATIONS}, lambda job, _, __: job["metadata"]["annotations"].__setitem__("io.katacontainers.config.hypervisor.kernel_params", "init=/bin/sh")),
        change("an extra pod annotation", {POD_ANNOTATIONS}, lambda job, _, __: job["spec"]["template"]["metadata"]["annotations"].__setitem__("io.katacontainers.config.hypervisor.kernel_params", "init=/bin/sh")),
        change("pinned to a node", {PLACEMENT}, lambda _, pod, __: pod.__setitem__("nodeName", "gateway-node")),
        change("host networking", {PLACEMENT}, lambda _, pod, __: pod.__setitem__("hostNetwork", True)),
        change("a toleration", {PLACEMENT}, lambda _, pod, __: pod.__setitem__("tolerations", [{"operator": "Exists"}])),
        change("another runtime class", {PLACEMENT}, lambda _, pod, __: pod.__setitem__("runtimeClassName", "runc")),
        change("many pods", {JOB_SPEC}, lambda job, _, __: job["spec"].__setitem__("parallelism", 5)),
        change("an init container that reads the environment", {INIT}, lambda _, pod, __: pod["initContainers"][0].__setitem__("command", ["sh", "-c", "env"])),
    ]
