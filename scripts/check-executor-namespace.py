import sys

import yaml

WORKER_KINDS = {"ScaledJob", "CronJob", "TriggerAuthentication", "SandboxTemplate", "SandboxWarmPool"}
EXECUTOR_POD_NAMES = {"ploeg-worker", "ploeg-sandbox-launcher"}
PLOEGD_PORT = 8080


def selector_matches(selector, labels):
    if selector is None:
        return True
    for key, value in (selector.get("matchLabels") or {}).items():
        if labels.get(key) != value:
            return False
    for expression in selector.get("matchExpressions") or []:
        key, operator, values = expression["key"], expression["operator"], expression.get("values") or []
        if operator == "In" and labels.get(key) not in values:
            return False
        if operator == "NotIn" and labels.get(key) in values:
            return False
        if operator == "Exists" and key not in labels:
            return False
        if operator == "DoesNotExist" and key in labels:
            return False
    return True


def peer_admits(peer, policy_namespace, namespace, pod_labels):
    if "ipBlock" in peer:
        return False
    if "namespaceSelector" in peer:
        if not selector_matches(peer["namespaceSelector"], {"kubernetes.io/metadata.name": namespace}):
            return False
    elif namespace != policy_namespace:
        return False
    return selector_matches(peer.get("podSelector"), pod_labels)


def port_admits(rule, port):
    ports = rule.get("ports")
    return not ports or any(int(p.get("port", port)) == port for p in ports)


def policy_admits(policy, direction, policy_namespace, namespace, pod_labels, port):
    peers_key = "from" if direction == "ingress" else "to"
    for rule in policy["spec"].get(direction) or []:
        if not port_admits(rule, port):
            continue
        peers = rule.get(peers_key)
        if peers is None or any(peer_admits(p, policy_namespace, namespace, pod_labels) for p in peers):
            return True
    return False


def pod_templates(document):
    spec = document.get("spec") or {}
    kind = document["kind"]
    if kind == "ScaledJob":
        return [spec["jobTargetRef"]["template"]]
    if kind == "CronJob":
        return [spec["jobTemplate"]["spec"]["template"]]
    if kind == "SandboxTemplate":
        return [spec["podTemplate"]]
    return []


def main(path, release_namespace, executor_namespace):
    documents = [d for d in yaml.safe_load_all(open(path)) if d]
    failures = []

    def namespace_of(document):
        return (document.get("metadata") or {}).get("namespace") or release_namespace

    def where(document):
        return f"{document['kind']} {document['metadata']['name']}"

    executor_accounts = set()
    executor_pods = []
    for document in documents:
        for template in pod_templates(document):
            executor_accounts.add(template["spec"].get("serviceAccountName", "default"))
            executor_pods.append((document, template))

    executor_roles = set()
    for document in documents:
        if document["kind"] == "RoleBinding":
            subjects = document.get("subjects") or []
            if any(s.get("kind") == "ServiceAccount" and s["name"] in executor_accounts for s in subjects):
                executor_roles.add(document["roleRef"]["name"])
                for subject in subjects:
                    if subject.get("namespace") != executor_namespace:
                        failures.append(f"{where(document)} binds ServiceAccount {subject['name']} in namespace {subject.get('namespace')}, not {executor_namespace}")

    def executor_side(document):
        kind = document["kind"]
        name = document["metadata"]["name"]
        if kind in WORKER_KINDS:
            return True
        if kind == "ServiceAccount" and name in executor_accounts:
            return True
        if kind in ("Role", "RoleBinding") and (name in executor_roles or any(
                s.get("name") in executor_accounts for s in document.get("subjects") or [])):
            return True
        if kind == "NetworkPolicy":
            selected = ((document["spec"].get("podSelector") or {}).get("matchLabels") or {}).get("app.kubernetes.io/name")
            return selected in EXECUTOR_POD_NAMES
        return False

    executor_objects = 0
    for document in documents:
        if document["kind"] in ("ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding"):
            continue
        expected = executor_namespace if executor_side(document) else release_namespace
        executor_objects += executor_side(document)
        if namespace_of(document) != expected:
            failures.append(f"{where(document)} renders into namespace {namespace_of(document)}, not {expected}")
    if executor_objects == 0:
        failures.append("the render holds no worker object to place")

    for document in documents:
        if document["kind"] == "ValidatingAdmissionPolicy":
            matched = document["spec"]["matchConstraints"]["namespaceSelector"]
            if not selector_matches(matched, {"kubernetes.io/metadata.name": executor_namespace}):
                failures.append(f"{where(document)} does not match the worker namespace {executor_namespace}")
            if selector_matches(matched, {"kubernetes.io/metadata.name": release_namespace}) and release_namespace != executor_namespace:
                failures.append(f"{where(document)} still matches the release namespace {release_namespace}")

    ploegd_labels = None
    for document in documents:
        if document["kind"] == "Deployment" and document["spec"]["template"]["metadata"]["labels"].get("ploeg.webgrip.dev/component") == "daemon":
            ploegd_labels = document["spec"]["template"]["metadata"]["labels"]
    policies = [d for d in documents if d["kind"] == "NetworkPolicy"]

    def selecting(namespace, labels):
        return [p for p in policies if namespace_of(p) == namespace and selector_matches(p["spec"].get("podSelector"), labels)]

    for document, template in executor_pods:
        labels = template["metadata"]["labels"]
        for policy in selecting(release_namespace, ploegd_labels or {}):
            if "Ingress" in policy["spec"].get("policyTypes", []) and not policy_admits(policy, "ingress", release_namespace, executor_namespace, labels, PLOEGD_PORT):
                failures.append(f"{where(policy)} refuses {labels.get('app.kubernetes.io/name')} pods of {where(document)} from {executor_namespace} on {PLOEGD_PORT}")
        egress = [p for p in selecting(executor_namespace, labels) if "Egress" in p["spec"].get("policyTypes", [])]
        if policies and not egress:
            failures.append(f"no NetworkPolicy in {executor_namespace} limits the egress of the pods of {where(document)}")
        if egress and not any(policy_admits(p, "egress", executor_namespace, release_namespace, ploegd_labels or {}, PLOEGD_PORT) for p in egress):
            failures.append(f"no NetworkPolicy lets the pods of {where(document)} reach ploegd in {release_namespace} on {PLOEGD_PORT}")
        for container in template["spec"].get("containers") or []:
            for env in container.get("env") or []:
                if env["name"] == "PLOEG_API_URL" and release_namespace != executor_namespace and f".{release_namespace}.svc" not in env.get("value", ""):
                    failures.append(f"{where(document)} container {container['name']} calls ploegd at {env.get('value')}, which does not resolve from {executor_namespace}")

    for failure in failures:
        print(f"executor namespace: {failure}")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main(*sys.argv[1:4]))
