import copy
import json
import subprocess
import tempfile
from pathlib import Path

import celpy
from celpy.adapter import json_to_cel
import yaml

root = Path(__file__).resolve().parent.parent
chart = root / "ops/helm/ploeg"
key = "workerAdmission"
namespace = "security-test"


def render(values, kube="1.30.0", ok=True):
    with tempfile.TemporaryDirectory() as directory:
        path = Path(directory) / "values.yaml"
        path.write_text(yaml.safe_dump(values))
        args = ["helm", "template", "security-test", str(chart), "--namespace", "security-test", "--kube-version", kube]
        args += ["-f", str(chart / "ci/required-values.yaml")]
        result = subprocess.run(args + ["-f", str(path)], text=True, capture_output=True, check=False, timeout=60)
        if not ok:
            assert result.returncode != 0, "unsafe or unsupported configuration rendered successfully"
            return result.stderr
        assert result.returncode == 0, result.stderr
        return [doc for doc in yaml.safe_load_all(result.stdout) if doc]


values = {"security": {key: {"enabled": True, "runtimeClassNames": ["kata"]}}}
values["security"]["workerNetworkPolicy"] = {"enabled": True}
values["executor"] = {
    "enabled": True,
    "harness": {"name": "exec", "dind": False},
    "teams": [{"name": "security", "model": "litellm_proxy/deepseek-chat", "budget": "1",
               "executorType": "sandbox", "sandbox": {"runtimeClassName": "kata"}}],
}

documents = render(values)
policy = next(doc for doc in documents if doc["kind"] == "ValidatingAdmissionPolicy")
binding = next(doc for doc in documents if doc["kind"] == "ValidatingAdmissionPolicyBinding")
assert policy["spec"]["failurePolicy"] == "Fail"
assert binding["spec"]["validationActions"] == ["Deny"]
assert binding["spec"]["policyName"] == policy["metadata"]["name"]
assert policy["spec"]["matchConstraints"]["namespaceSelector"]["matchLabels"]["kubernetes.io/metadata.name"] == namespace
assert "pods/ephemeralcontainers" in policy["spec"]["matchConstraints"]["resourceRules"][0]["resources"]
pod = next(doc for doc in documents if doc["kind"] == "SandboxTemplate")["spec"]["podTemplate"]
pod.update({"apiVersion": "v1", "kind": "Pod"})
selector = policy["spec"]["matchConstraints"]["objectSelector"]["matchLabels"]
assert all(pod["metadata"]["labels"].get(k) == v for k, v in selector.items())
network = next(doc for doc in documents if doc["kind"] == "NetworkPolicy" and doc["metadata"]["name"].endswith("-worker-boundary"))
assert network["spec"]["podSelector"]["matchLabels"] == selector
assert network["spec"]["ingress"] == []
assert network["spec"]["egress"][0]["ports"] == [{"protocol": "TCP", "port": 8080}]
assert all(rule.get("to") for rule in network["spec"]["egress"])
unsafe = copy.deepcopy(values)
unsafe["executor"]["harness"]["dind"] = True
assert "forbids privileged DinD" in render(unsafe, ok=False)

environment = celpy.Environment()
programs = [environment.program(environment.compile(item["expression"])) for item in policy["spec"]["validations"]]
variable_programs = [(item["name"], environment.program(environment.compile(item["expression"]))) for item in policy["spec"].get("variables", [])]


def admits(candidate, strict=False):
    activation = {"object": json_to_cel(candidate), "variables": json_to_cel({})}
    variables = {}
    for name, program in variable_programs:
        variables[name] = program.evaluate(activation)
        activation["variables"] = celpy.celtypes.MapType(variables)
    try:
        return all(bool(program.evaluate(activation)) for program in programs)
    except celpy.CELEvalError:
        if strict:
            raise
        return False


assert admits(pod, strict=True), "real rendered/generated agent pod must pass"
mutations = {}
for field in ["hostNetwork", "hostPID", "hostIPC", "shareProcessNamespace", "automountServiceAccountToken"]:
    changed = copy.deepcopy(pod)
    changed["spec"][field] = True
    mutations[field] = changed
changed = copy.deepcopy(pod)
changed["spec"].pop("automountServiceAccountToken")
mutations["implicit API token"] = changed
changed = copy.deepcopy(pod)
changed["spec"].setdefault("volumes", []).append({"name": "host", "hostPath": {"path": "/var/run/docker.sock"}})
mutations["host socket"] = changed
for group in ["containers", "initContainers", "ephemeralContainers"]:
    for label, addition in [
        ("ptrace", {"capabilities": {"drop": ["ALL"], "add": ["SYS_PTRACE"]}}),
        ("privileged", {"privileged": True}),
        ("root", {"runAsUser": 0}),
        ("escalation", {"allowPrivilegeEscalation": True}),
        ("unconfined", {"seccompProfile": {"type": "Unconfined"}}),
    ]:
        changed = copy.deepcopy(pod)
        container = copy.deepcopy(pod["spec"]["containers"][0])
        container["name"] = "attack"
        container.setdefault("securityContext", {}).update(addition)
        changed["spec"].setdefault(group, []).append(container)
        mutations[f"{group}: {label}"] = changed
changed = copy.deepcopy(pod)
changed["spec"]["runtimeClassName"] = "runc"
mutations["unapproved RuntimeClass"] = changed
changed = copy.deepcopy(pod)
changed["spec"].pop("runtimeClassName", None)
mutations["missing RuntimeClass"] = changed
for name, changed in mutations.items():
    assert not admits(changed), f"policy admitted {name}"
assert "Kubernetes >= 1.30" in render(values, kube="1.29.0", ok=False)
defaults = {}
assert not any(doc["kind"].startswith("ValidatingAdmissionPolicy") for doc in render(defaults))
contract = json.loads((chart / "security-contract.json").read_text())
assert contract["contractRevision"] == 1
assert contract["identity"]["delegatedUserAccessToken"] is False
assert (chart / "SECURITY.md").is_file()
print(f"{chart.name}: real pod passes; {len(mutations)} hostile pod mutations denied; namespace, binding, version and network boundaries checked. Kubernetes API-server and live CNI qualification still required.")
