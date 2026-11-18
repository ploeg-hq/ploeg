import sys

import yaml

FORBIDDEN_ENV = {
    'PLOEG_WORKER_BOOTSTRAP_TOKEN',
    'AGENT_BUILDER_TOKEN',
    'FORGE_URL',
    'LLM_API_KEY',
    'PLOEG_LLM_KEY_ISOLATION',
    'PLOEG_FORGE_TOKEN_ISOLATION',
    'PLOEG_CREDENTIAL_ISOLATION_OFF_REASON',
}
FORBIDDEN_ENV_PREFIX = 'PLOEG_CLAUDE_PLUGIN_ENV_'
DOCKER_ENV = {'DOCKER_HOST', 'DOCKER_CERT_PATH', 'DOCKER_TLS_VERIFY'}
HOST_NAMESPACES = ('hostNetwork', 'hostPID', 'hostIPC')
RUN_POD_VOLUME_TYPES = {'emptyDir', 'image'}
GATEWAY_NAME = 'ploeg-credential-gateway'
GATEWAY_PORTS = {8080, 8081, 8082, 8083}
RUN_POD_LABEL = {'app.kubernetes.io/name': 'ploeg-worker'}


def run_pods(document):
    kind = document.get('kind')
    spec = document.get('spec') or {}
    if kind == 'ScaledJob':
        template = (spec.get('jobTargetRef') or {}).get('template') or {}
    elif kind == 'CronJob':
        template = ((spec.get('jobTemplate') or {}).get('spec') or {}).get('template') or {}
    elif kind == 'SandboxTemplate':
        template = spec.get('podTemplate') or {}
    else:
        return
    if (template.get('metadata') or {}).get('labels', {}).get('app.kubernetes.io/name') == 'ploeg-worker':
        yield f"{kind}/{document['metadata']['name']}", template['spec']


def check_run_pod(where, pod, host):
    problems = []
    if pod.get('automountServiceAccountToken') is not False:
        problems.append(f"{where}: automountServiceAccountToken is not false")
    for field in HOST_NAMESPACES:
        if pod.get(field):
            problems.append(f"{where}: the pod sets {field}")
    for volume in pod.get('volumes') or []:
        if 'secret' in volume or 'projected' in volume:
            problems.append(f"{where}: volume {volume['name']} is a Secret or a projection")
        types = sorted(set(volume) - {'name'})
        if len(types) != 1 or types[0] not in RUN_POD_VOLUME_TYPES:
            problems.append(f"{where}: volume {volume['name']} is of type {', '.join(types)}, not emptyDir or image")
        if volume['name'].startswith('docker-'):
            problems.append(f"{where}: volume {volume['name']} is a Docker daemon's")
    containers = (pod.get('initContainers') or []) + (pod.get('containers') or [])
    for container in containers:
        if (container.get('securityContext') or {}).get('privileged'):
            problems.append(f"{where}: container {container['name']} is privileged")
        for mount in container.get('volumeMounts') or []:
            if mount['mountPath'] == '/certs' or mount['mountPath'].startswith('/certs/'):
                problems.append(f"{where}: container {container['name']} mounts {mount['name']} at {mount['mountPath']}, a Docker daemon's client certificates")
        for entry in container.get('env') or []:
            name = entry['name']
            if 'secretKeyRef' in (entry.get('valueFrom') or {}):
                problems.append(f"{where}: container {container['name']} reads {name} from a Secret")
            if name in FORBIDDEN_ENV or name in DOCKER_ENV or name.startswith(FORBIDDEN_ENV_PREFIX):
                problems.append(f"{where}: container {container['name']} renders {name}")
        for entry in container.get('envFrom') or []:
            if 'secretRef' in entry:
                problems.append(f"{where}: container {container['name']} takes envFrom a Secret")
    worker = next((c for c in pod.get('containers') or [] if c['name'] == 'worker'), None)
    if worker is None:
        return problems + [f"{where}: no worker container"]
    env = {e['name']: e.get('value') for e in worker.get('env') or []}
    expected = {
        'PLOEG_CREDENTIAL_GATEWAY': 'on',
        'PLOEG_API_URL': f'http://{host}:8080',
        'PLOEG_CREDENTIAL_GATEWAY_FORGE_URL': f'http://{host}:8081',
        'PLOEG_CREDENTIAL_GATEWAY_MCP_URL': f'http://{host}:8083',
    }
    for name, value in expected.items():
        if env.get(name) != value:
            problems.append(f"{where}: {name} is {env.get(name)!r}, want {value!r}")
    if not (env.get('LLM_BASE_URL') or '').startswith(f'http://{host}:8082'):
        problems.append(f"{where}: LLM_BASE_URL {env.get('LLM_BASE_URL')!r} does not name the gateway's model port")
    return problems


def selects(policy, labels):
    wanted = ((policy['spec'].get('podSelector') or {}).get('matchLabels') or {})
    return all(labels.get(k) == v for k, v in wanted.items())


def is_dns_rule(rule):
    ports = rule.get('ports') or []
    return bool(ports) and all(int(p['port']) == 53 for p in ports)


def is_gateway_rule(rule):
    peers = rule.get('to') or []
    ports = {int(p['port']) for p in rule.get('ports') or []}
    if not peers or ports != GATEWAY_PORTS:
        return False
    return all(
        set(peer) == {'podSelector'}
        and (peer['podSelector'].get('matchLabels') or {}).get('app.kubernetes.io/name') == GATEWAY_NAME
        for peer in peers
    )


def check_run_pod_egress(documents, namespace):
    problems = []
    policies = [d for d in documents if d.get('kind') == 'NetworkPolicy'
                and (d['metadata'].get('namespace') or 'default') == namespace
                and selects(d, RUN_POD_LABEL) and 'Egress' in (d['spec'].get('policyTypes') or [])]
    if not policies:
        return ['no NetworkPolicy limits the egress of the Run pods']
    for policy in policies:
        egress = policy['spec'].get('egress') or []
        if not any(is_gateway_rule(rule) for rule in egress):
            problems.append(f"NetworkPolicy {policy['metadata']['name']} does not let Run pods reach the gateway")
        for rule in egress:
            if not (is_dns_rule(rule) or is_gateway_rule(rule)):
                problems.append(f"NetworkPolicy {policy['metadata']['name']} lets Run pods reach more than DNS and the gateway: {rule}")
        if policy['spec'].get('ingress'):
            problems.append(f"NetworkPolicy {policy['metadata']['name']} admits ingress to Run pods")
    for template in documents:
        if template.get('kind') == 'SandboxTemplate' and (template['spec'].get('networkPolicy') or template['spec'].get('networkPolicyManagement') == 'Managed'):
            problems.append(f"SandboxTemplate {template['metadata']['name']} adds a managed NetworkPolicy to the Run pods' egress")
    return problems


def check_gateway(documents, namespace, bootstrap_keys):
    problems = []
    deployments = [d for d in documents if d.get('kind') == 'Deployment' and d['metadata']['name'].endswith('-credential-gateway')]
    if len(deployments) != 1:
        return [f"want one credential gateway Deployment, found {len(deployments)}"]
    deployment = deployments[0]
    if deployment['metadata'].get('namespace') != namespace:
        problems.append(f"the gateway Deployment renders into {deployment['metadata'].get('namespace')}, not {namespace}")
    pod = deployment['spec']['template']['spec']
    anti = ((pod.get('affinity') or {}).get('podAntiAffinity') or {}).get('requiredDuringSchedulingIgnoredDuringExecution') or []
    if not any(term.get('topologyKey') == 'kubernetes.io/hostname'
               and (term.get('labelSelector') or {}).get('matchLabels') == RUN_POD_LABEL for term in anti):
        problems.append('the gateway carries no required anti-affinity against Run pods by node')
    secrets = {v['name']: v['secret'] for v in pod.get('volumes') or [] if 'secret' in v}
    bootstrap = [s for s in secrets.values() if s.get('secretName') == 'ploeg-worker-bootstrap']
    if not bootstrap:
        problems.append('the gateway does not mount the bootstrap Secret')
    else:
        keys = sorted(item['key'] for item in bootstrap[0].get('items') or [])
        if keys != sorted(bootstrap_keys):
            problems.append(f"the gateway mounts bootstrap keys {keys}, want {sorted(bootstrap_keys)}")
    mounted = {m['name'] for c in pod['containers'] for m in c.get('volumeMounts') or []}
    for name in secrets:
        if name not in mounted:
            problems.append(f"the gateway does not mount its volume {name}")
    tokens = [s['serviceAccountToken'] for v in pod.get('volumes') or []
              for s in (v.get('projected') or {}).get('sources') or [] if 'serviceAccountToken' in s]
    if not any(t.get('audience') == 'ploegd' for t in tokens):
        problems.append('the gateway mounts no ServiceAccount token for the audience ploegd')
    return problems


ADAPTER_LABEL = {'app.kubernetes.io/name': 'ploeg-mcp-adapter'}


def check_mcp_adapters(documents):
    problems = []
    for d in documents:
        if d.get('kind') != 'Deployment' or d['spec']['template']['metadata'].get('labels', {}).get('app.kubernetes.io/name') != 'ploeg-mcp-adapter':
            continue
        name = d['metadata']['name']
        pod = d['spec']['template']['spec']
        if pod.get('automountServiceAccountToken') is not False:
            problems.append(f"MCP adapter {name} mounts a ServiceAccount token")
        for c in pod.get('containers') or []:
            if '@sha256:' not in c.get('image', ''):
                problems.append(f"MCP adapter {name} runs an image not pinned by digest")
            if not (c.get('securityContext') or {}).get('readOnlyRootFilesystem'):
                problems.append(f"MCP adapter {name} has a writable root filesystem")
        labels = d['spec']['template']['metadata']['labels']
        policies = [p for p in documents if p.get('kind') == 'NetworkPolicy' and selects(p, labels) and 'Ingress' in (p['spec'].get('policyTypes') or [])]
        if not policies:
            problems.append(f"no NetworkPolicy limits who reaches MCP adapter {name}")
        for policy in policies:
            for rule in policy['spec'].get('ingress') or []:
                peers = rule.get('from') or []
                if not peers or any((peer.get('podSelector') or {}).get('matchLabels', {}).get('app.kubernetes.io/name') != GATEWAY_NAME for peer in peers):
                    problems.append(f"NetworkPolicy {policy['metadata']['name']} lets more than the credential gateway reach MCP adapter {name}")
    return problems


def main(path, namespace, bootstrap_keys):
    documents = [d for d in yaml.safe_load_all(open(path)) if d]
    host = f'{GATEWAY_NAME}.{namespace}.svc'
    problems = []
    pods = [found for d in documents for found in run_pods(d)]
    if not pods:
        problems.append('the render holds no Run pod to check')
    for where, pod in pods:
        problems += check_run_pod(where, pod, host)
    problems += check_run_pod_egress(documents, namespace)
    problems += check_gateway(documents, namespace, [k for k in bootstrap_keys.split(',') if k])
    problems += check_mcp_adapters(documents)
    for problem in problems:
        print(f"{path}: {problem}")
    return 1 if problems else 0


if __name__ == '__main__':
    sys.exit(main(*sys.argv[1:4]))
