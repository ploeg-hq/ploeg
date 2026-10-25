import copy
import json

import celpy
from celpy.adapter import json_to_cel

from worker_admission_cases import VARIANTS, chart, hostile_mutations, only, render, worker_pods


def compile_policy(policy):
    environment = celpy.Environment()
    variables = [(item["name"], environment.program(environment.compile(item["expression"]))) for item in policy["spec"].get("variables", [])]
    validations = [(item["message"], environment.program(environment.compile(item["expression"]))) for item in policy["spec"]["validations"]]
    return variables, validations


def refusals(compiled, pod):
    variables, validations = compiled
    activation = {"object": json_to_cel(pod), "variables": json_to_cel({})}
    values = {}
    for name, program in variables:
        values[name] = program.evaluate(activation)
        activation["variables"] = celpy.celtypes.MapType(values)
    refused = set()
    for message, program in validations:
        result = program.evaluate(activation)
        assert not isinstance(result, celpy.CELEvalError), f"{message!r} errored instead of deciding: {result}"
        if not bool(result):
            refused.add(message)
    return refused


def check_policy_shape(variant, documents):
    policy = only(documents, "ValidatingAdmissionPolicy")
    binding = only(documents, "ValidatingAdmissionPolicyBinding")
    assert policy["spec"]["failurePolicy"] == "Fail"
    assert binding["spec"]["validationActions"] == ["Deny"]
    assert binding["spec"]["policyName"] == policy["metadata"]["name"]
    assert policy["spec"]["matchConstraints"]["namespaceSelector"]["matchLabels"] == {"kubernetes.io/metadata.name": variant.namespace}
    assert policy["spec"]["matchConstraints"]["resourceRules"][0]["resources"] == ["pods", "pods/ephemeralcontainers"]
    return policy


def check_worker_network_policy(documents, pod):
    network = next(doc for doc in documents if doc["kind"] == "NetworkPolicy" and doc["metadata"]["name"].endswith("-worker"))
    assert all(pod["metadata"]["labels"].get(k) == v for k, v in network["spec"]["podSelector"]["matchLabels"].items())
    assert network["spec"]["ingress"] == []
    assert any({"protocol": "TCP", "port": 8080} in rule.get("ports", []) for rule in network["spec"]["egress"])
    assert all(rule.get("to") for rule in network["spec"]["egress"])


def check_variant(variant):
    documents = render(variant.values, variant.namespace)
    policy = check_policy_shape(variant, documents)
    compiled = compile_policy(policy)
    selector = policy["spec"]["matchConstraints"]["objectSelector"]["matchLabels"]
    denied = 0
    for source, pod in worker_pods(documents, variant.namespace):
        assert all(pod["metadata"]["labels"].get(k) == v for k, v in selector.items()), f"{source} escapes the admission selector"
        check_worker_network_policy(documents, pod)
        assert refusals(compiled, pod) == set(), f"{source}: the rendered worker pod must pass, refused {refusals(compiled, pod)}"
        for mutation in hostile_mutations(pod, variant.runtime_class_names):
            refused = refusals(compiled, mutation.pod)
            assert refused == {mutation.message}, f"{variant.name} {mutation.name}: expected only {mutation.message!r}, refused {refused}"
            denied += 1
    return denied


def check_dind_is_refused():
    variant = next(v for v in VARIANTS if v.name == "sandbox")
    unsafe = copy.deepcopy(variant.values)
    unsafe["executor"]["harness"].update({"dind": True, "acknowledgePrivilegedDind": True})
    error = render(unsafe, variant.namespace, ok=False)
    assert "forbids privileged DinD" in error, error


def check_version_floor_and_default():
    variant = VARIANTS[0]
    assert "Kubernetes >= 1.30" in render(variant.values, variant.namespace, kube="1.29.0", ok=False)
    assert not any(doc["kind"].startswith("ValidatingAdmissionPolicy") for doc in render({}, variant.namespace))


def check_contract():
    contract = json.loads((chart / "security-contract.json").read_text())
    assert contract["contractRevision"] == 1
    assert contract["nativeAdmissionDefault"] is False
    assert contract["identity"]["delegatedUserAccessToken"] is False
    assert contract["identity"]["personalForgeAclInheritance"] is False
    assert (chart / "SECURITY.md").is_file()


denied = sum(check_variant(variant) for variant in VARIANTS)
check_dind_is_refused()
check_version_floor_and_default()
check_contract()
print(f"{chart.name}: {len(VARIANTS)} executor variants' worker pods pass; {denied} hostile mutations each refused by exactly the rule that names them. scripts/check-worker-admission-live.py runs the same cases against a real API server.")
