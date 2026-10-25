import copy
import json
import subprocess
import sys
import time

from worker_admission_cases import (
    ALLOWED_RUNTIME_CLASS,
    UNAPPROVED_RUNTIME_CLASS,
    VARIANTS,
    compliant_ephemeral_container,
    hostile_mutations,
    only,
    render,
    worker_pods,
)

ACTIVATION_TIMEOUT_SECONDS = 90


def kubectl(*args, stdin=None, ok=True):
    result = subprocess.run(["kubectl", *args], input=stdin, text=True, capture_output=True, check=False, timeout=120)
    if ok and result.returncode != 0:
        raise AssertionError(f"kubectl {' '.join(args)} failed: {result.stderr.strip()}")
    return result


def refuse_unless_disposable_cluster():
    context = kubectl("config", "current-context").stdout.strip()
    if not context.startswith("kind-"):
        sys.exit(f"refusing to run against context {context!r}: this check creates cluster-scoped admission policies and expects a disposable kind cluster")
    return context


def server_version():
    version = json.loads(kubectl("version", "-o", "json").stdout)["serverVersion"]
    return f"{version['major']}.{version['minor'].rstrip('+')}.0", version["gitVersion"]


def apply(document):
    kubectl("apply", "--server-side", "--field-manager=admission-check", "-f", "-", stdin=json.dumps(document))


def create_dry_run(pod):
    return kubectl("create", "--dry-run=server", "-o", "name", "-f", "-", stdin=json.dumps(pod), ok=False)


def replace_ephemeral_containers_dry_run(pod, container):
    updated = copy.deepcopy(pod)
    updated["spec"]["ephemeralContainers"] = [container]
    path = f"/api/v1/namespaces/{pod['metadata']['namespace']}/pods/{pod['metadata']['name']}/ephemeralcontainers?dryRun=All"
    return kubectl("replace", "--raw", path, "-f", "-", stdin=json.dumps(updated), ok=False)


def denial_text(result):
    return result.stderr + result.stdout


def assert_admitted(result, case):
    assert result.returncode == 0, f"{case}: must be admitted, got {denial_text(result).strip()}"


def assert_denied_by(result, policy, message, case):
    text = denial_text(result)
    assert result.returncode != 0, f"{case}: admitted, expected {message!r}"
    assert f"ValidatingAdmissionPolicy '{policy}'" in text and message in text, f"{case}: refused for another reason: {text.strip()}"


def install_runtime_classes():
    for name in [ALLOWED_RUNTIME_CLASS, UNAPPROVED_RUNTIME_CLASS]:
        apply({"apiVersion": "node.k8s.io/v1", "kind": "RuntimeClass", "metadata": {"name": name}, "handler": "runc"})


def type_check_warnings(policy_name):
    deadline = time.monotonic() + ACTIVATION_TIMEOUT_SECONDS
    while time.monotonic() < deadline:
        live = json.loads(kubectl("get", "validatingadmissionpolicy", policy_name, "-o", "json").stdout)
        status = live.get("status", {})
        if status.get("observedGeneration") == live["metadata"]["generation"] and "typeChecking" in status:
            return status["typeChecking"].get("expressionWarnings", [])
        time.sleep(1)
    raise AssertionError(f"{policy_name}: the API server never reported type checking")


def wait_until_enforced(policy_name, probe):
    deadline = time.monotonic() + ACTIVATION_TIMEOUT_SECONDS
    while time.monotonic() < deadline:
        if f"ValidatingAdmissionPolicy '{policy_name}'" in denial_text(create_dry_run(probe.pod)):
            return
        time.sleep(1)
    raise AssertionError(f"{policy_name}: the binding never started denying")


def create_pending_pod(pod):
    pending = copy.deepcopy(pod)
    pending["metadata"]["name"] += "-pending"
    pending["spec"]["nodeSelector"] = {"admission-check.ploeg.dev/never-schedule": "true"}
    kubectl("create", "-f", "-", stdin=json.dumps(pending))
    return pending


def check_scope(variant, mutation):
    unlabelled = copy.deepcopy(mutation.pod)
    unlabelled["metadata"]["name"] += "-unlabelled"
    unlabelled["metadata"]["labels"].pop("app.kubernetes.io/name")
    assert_admitted(create_dry_run(unlabelled), f"{variant.name}: {mutation.name} without the worker label is outside the policy")
    elsewhere = copy.deepcopy(mutation.pod)
    elsewhere["metadata"]["namespace"] = "default"
    elsewhere["spec"].pop("serviceAccountName", None)
    assert_admitted(create_dry_run(elsewhere), f"{variant.name}: {mutation.name} in another namespace is outside the policy")


def remove(variant, policy_name):
    kubectl("delete", "validatingadmissionpolicybinding", policy_name, "--ignore-not-found", "--wait=true")
    kubectl("delete", "validatingadmissionpolicy", policy_name, "--ignore-not-found", "--wait=true")
    kubectl("delete", "namespace", variant.namespace, "--ignore-not-found", "--wait=true")


def check_variant(variant, kube):
    documents = render(variant.values, variant.namespace, kube=kube)
    policy_name = only(documents, "ValidatingAdmissionPolicy")["metadata"]["name"]
    remove(variant, policy_name)
    try:
        return check_installed_variant(variant, documents, policy_name)
    finally:
        remove(variant, policy_name)


def check_installed_variant(variant, documents, policy_name):
    policy = only(documents, "ValidatingAdmissionPolicy")
    kubectl("create", "namespace", variant.namespace)
    for doc in documents:
        if doc["kind"] == "ServiceAccount":
            doc["metadata"]["namespace"] = variant.namespace
            apply(doc)
    apply(policy)
    apply(only(documents, "ValidatingAdmissionPolicyBinding"))
    warnings = type_check_warnings(policy_name)
    assert warnings == [], f"{policy_name}: API server type checking warned: {warnings}"
    counts = {"admitted": 0, "denied": 0}
    for source, pod in worker_pods(documents, variant.namespace):
        mutations = hostile_mutations(pod, variant.runtime_class_names)
        wait_until_enforced(policy_name, mutations[0])
        assert_admitted(create_dry_run(pod), f"{variant.name}: rendered {source}")
        counts["admitted"] += 1
        pending = create_pending_pod(pod)
        assert_admitted(replace_ephemeral_containers_dry_run(pending, compliant_ephemeral_container(pod)), f"{variant.name}: a restricted ephemeral container on {source}")
        counts["admitted"] += 1
        for mutation in mutations:
            case = f"{variant.name} {source}: {mutation.name}"
            if mutation.ephemeral_container is None:
                result = create_dry_run(mutation.pod)
            else:
                result = replace_ephemeral_containers_dry_run(pending, mutation.ephemeral_container)
            assert_denied_by(result, policy_name, mutation.message, case)
            counts["denied"] += 1
        kata = next(m for m in mutations if m.name == "Kata hypervisor override annotation")
        annotated = {"metadata": {"annotations": kata.pod["metadata"]["annotations"]}}
        result = kubectl("patch", "pod", pending["metadata"]["name"], "-n", variant.namespace, "--dry-run=server", "--type=merge", "-p", json.dumps(annotated), ok=False)
        assert_denied_by(result, policy_name, kata.message, f"{variant.name} {source}: Kata annotation added on UPDATE")
        counts["denied"] += 1
        check_scope(variant, mutations[0])
        counts["admitted"] += 2
    return counts


context = refuse_unless_disposable_cluster()
kube, git_version = server_version()
install_runtime_classes()
totals = {"admitted": 0, "denied": 0}
for variant in VARIANTS:
    counts = check_variant(variant, kube)
    totals = {key: totals[key] + counts[key] for key in totals}
print(f"{context} (Kubernetes {git_version}): API server type-checked every policy without warnings; {totals['admitted']} expected admissions passed and {totals['denied']} hostile requests were refused by the rule that names them.")
