import copy
import subprocess
import tempfile
from dataclasses import dataclass
from pathlib import Path

import yaml

root = Path(__file__).resolve().parent.parent
chart = root / "ops/helm/ploeg"

ALLOWED_RUNTIME_CLASS = "kata"
UNAPPROVED_RUNTIME_CLASS = "runc"
NAMESPACE_PREFIX = "admission"


@dataclass(frozen=True)
class Variant:
    name: str
    values: dict
    runtime_class_names: tuple

    @property
    def namespace(self):
        return f"{NAMESPACE_PREFIX}-{self.name}"


@dataclass(frozen=True)
class Mutation:
    name: str
    pod: dict
    message: str
    ephemeral_container: dict | None = None


def admission(runtime_class_names=()):
    return {"security": {"workerAdmission": {"enabled": True, "runtimeClassNames": list(runtime_class_names)}}}


def team(name, **settings):
    return {"name": name, "model": "litellm_proxy/deepseek-chat", "budget": "1", **settings}


VARIANTS = [
    Variant("keda", {**admission(), "executor": {"enabled": True, "teams": [team("keda")]}}, ()),
    Variant("cronjob", {**admission(), "executor": {"enabled": True, "type": "cronjob", "teams": [team("cronjob")]}}, ()),
    Variant(
        "sandbox",
        {
            **admission([ALLOWED_RUNTIME_CLASS]),
            "supportProfile": "unsupported",
            "executor": {
                "enabled": True,
                "harness": {"name": "exec", "dind": False},
                "teams": [team("sandbox", executorType="sandbox", sandbox={"runtimeClassName": ALLOWED_RUNTIME_CLASS})],
            },
        },
        (ALLOWED_RUNTIME_CLASS,),
    ),
]


def render(values, namespace, kube="1.30.0", ok=True):
    with tempfile.TemporaryDirectory() as directory:
        path = Path(directory) / "values.yaml"
        path.write_text(yaml.safe_dump(values))
        args = ["helm", "template", "admission", str(chart), "--namespace", namespace, "--kube-version", kube]
        args += ["-f", str(chart / "ci/required-values.yaml"), "-f", str(path)]
        result = subprocess.run(args, text=True, capture_output=True, check=False, timeout=60)
    if not ok:
        assert result.returncode != 0, "unsafe or unsupported configuration rendered successfully"
        return result.stderr
    assert result.returncode == 0, result.stderr
    return [doc for doc in yaml.safe_load_all(result.stdout) if doc]


def only(documents, kind):
    matches = [doc for doc in documents if doc["kind"] == kind]
    assert len(matches) == 1, f"expected one {kind}, rendered {len(matches)}"
    return matches[0]


def pod_templates(documents):
    for doc in documents:
        if doc["kind"] == "ScaledJob":
            yield f"ScaledJob/{doc['metadata']['name']}", doc["spec"]["jobTargetRef"]["template"]
        elif doc["kind"] == "CronJob":
            yield f"CronJob/{doc['metadata']['name']}", doc["spec"]["jobTemplate"]["spec"]["template"]
        elif doc["kind"] == "SandboxTemplate":
            yield f"SandboxTemplate/{doc['metadata']['name']}", doc["spec"]["podTemplate"]


def worker_pods(documents, namespace):
    pods = []
    for source, template in pod_templates(documents):
        pod = {"apiVersion": "v1", "kind": "Pod", "metadata": copy.deepcopy(template.get("metadata", {})), "spec": copy.deepcopy(template["spec"])}
        pod["metadata"]["name"] = source.split("/", 1)[1].lower()
        pod["metadata"]["namespace"] = namespace
        if pod["metadata"].get("labels", {}).get("app.kubernetes.io/name") == "ploeg-worker":
            pods.append((source, pod))
    assert pods, "the render produced no worker pod"
    return pods


def attacker(pod, **security_context):
    container = copy.deepcopy(pod["spec"]["containers"][0])
    container["name"] = "attack"
    container.pop("ports", None)
    container.setdefault("securityContext", {}).update(security_context)
    return container


CONTAINER_ATTACKS = [
    ("SYS_PTRACE", {"capabilities": {"drop": ["ALL"], "add": ["SYS_PTRACE"]}}, "Every container must disable privilege escalation, drop ALL capabilities and add none."),
    ("privileged", {"privileged": True, "allowPrivilegeEscalation": True}, "Every container must disable privilege escalation, drop ALL capabilities and add none."),
    ("privilege escalation", {"allowPrivilegeEscalation": True}, "Every container must disable privilege escalation, drop ALL capabilities and add none."),
    ("root", {"runAsUser": 0, "runAsNonRoot": False}, "Every container must run as non-root."),
    ("unconfined seccomp", {"seccompProfile": {"type": "Unconfined"}}, "Every container must use RuntimeDefault or a provisioned Localhost seccomp profile."),
    ("unconfined AppArmor", {"appArmorProfile": {"type": "Unconfined"}}, "Unconfined AppArmor is forbidden."),
]


def with_change(pod, change):
    changed = copy.deepcopy(pod)
    change(changed)
    return changed


def hostile_mutations(pod, runtime_class_names):
    def set_spec(field, value):
        return lambda changed: changed["spec"].__setitem__(field, value)

    def annotate(key, value):
        return lambda changed: changed["metadata"].setdefault("annotations", {}).__setitem__(key, value)

    mutations = [
        Mutation("host network", with_change(pod, set_spec("hostNetwork", True)), "Host networking is forbidden."),
        Mutation("host PID", with_change(pod, set_spec("hostPID", True)), "Host PID sharing is forbidden."),
        Mutation("host IPC", with_change(pod, set_spec("hostIPC", True)), "Host IPC sharing is forbidden."),
        Mutation("shared process namespace", with_change(pod, set_spec("shareProcessNamespace", True)), "Sharing the pod process namespace defeats worker credential isolation."),
        Mutation("API token mounted", with_change(pod, set_spec("automountServiceAccountToken", True)), "Agent pods must explicitly disable automatic Kubernetes API credentials."),
        Mutation("API token implicit", with_change(pod, lambda changed: changed["spec"].pop("automountServiceAccountToken")), "Agent pods must explicitly disable automatic Kubernetes API credentials."),
        Mutation(
            "host container-runtime socket",
            with_change(pod, lambda changed: changed["spec"].setdefault("volumes", []).append({"name": "host", "hostPath": {"path": "/run/containerd/containerd.sock"}})),
            "Host paths, including host container-runtime sockets, are forbidden.",
        ),
        Mutation(
            "host port",
            with_change(pod, lambda changed: changed["spec"]["containers"][0].setdefault("ports", []).append({"containerPort": 2222, "hostPort": 2222})),
            "Host ports are forbidden.",
        ),
        Mutation(
            "pod sysctl",
            with_change(pod, lambda changed: changed["spec"].setdefault("securityContext", {}).__setitem__("sysctls", [{"name": "net.ipv4.ip_unprivileged_port_start", "value": "0"}])),
            "Pod sysctls are forbidden.",
        ),
        Mutation(
            "pod runs as root",
            with_change(pod, lambda changed: changed["spec"].setdefault("securityContext", {}).__setitem__("runAsUser", 0)),
            "Every container must run as non-root.",
        ),
        Mutation(
            "legacy unconfined AppArmor annotation",
            with_change(pod, annotate(f"container.apparmor.security.beta.kubernetes.io/{pod['spec']['containers'][0]['name']}", "unconfined")),
            "Unconfined AppArmor is forbidden.",
        ),
        Mutation(
            "Kata hypervisor override annotation",
            with_change(pod, annotate("io.katacontainers.config.hypervisor.kernel_params", "init=/bin/sh")),
            "Kata runtime override annotations (io.katacontainers.*) are forbidden.",
        ),
    ]
    for group in ["containers", "initContainers"]:
        for label, security_context, message in CONTAINER_ATTACKS:
            changed = with_change(pod, lambda changed: changed["spec"].setdefault(group, []).append(attacker(pod, **security_context)))
            mutations.append(Mutation(f"{group}: {label}", changed, message))
    for label, security_context, message in CONTAINER_ATTACKS:
        ephemeral = attacker(pod, **security_context)
        for field in ["resources", "lifecycle", "livenessProbe", "readinessProbe", "startupProbe", "restartPolicy"]:
            ephemeral.pop(field, None)
        changed = with_change(pod, lambda changed: changed["spec"].setdefault("ephemeralContainers", []).append(ephemeral))
        mutations.append(Mutation(f"ephemeralContainers: {label}", changed, message, ephemeral_container=ephemeral))
    if runtime_class_names:
        message = "The pod must use an explicitly allowed, installed sandbox RuntimeClass."
        mutations.append(Mutation("unapproved RuntimeClass", with_change(pod, set_spec("runtimeClassName", UNAPPROVED_RUNTIME_CLASS)), message))
        mutations.append(Mutation("missing RuntimeClass", with_change(pod, lambda changed: changed["spec"].pop("runtimeClassName", None)), message))
    return mutations


def compliant_ephemeral_container(pod):
    ephemeral = attacker(pod)
    ephemeral["name"] = "debug"
    for field in ["resources", "lifecycle", "livenessProbe", "readinessProbe", "startupProbe", "restartPolicy"]:
        ephemeral.pop(field, None)
    return ephemeral
