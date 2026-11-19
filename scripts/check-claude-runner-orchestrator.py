import json
import sys

import yaml

FORBIDDEN_ENV = {
    'CLAUDE_CODE_OAUTH_TOKEN',
    'CLAUDE_CODE_OAUTH_REFRESH_TOKEN',
    'ANTHROPIC_API_KEY',
    'ANTHROPIC_AUTH_TOKEN',
    'SELF_HOSTED_RUNNER_ENVIRONMENT_SECRET',
    'CLAUDE_CONFIG_DIR',
}
RUN_POD_VOLUME_TYPES = {'emptyDir', 'image'}
GATEWAY_NAME = 'ploeg-credential-gateway'
SESSION_PORT = 8084
SESSION_LABEL = {'app.kubernetes.io/name': 'ploeg-claude-runner-session'}
WORKER_LABEL = {'app.kubernetes.io/name': 'ploeg-worker'}
SHARED_NODE_LABEL = 'ploeg.webgrip.dev/credential-gateway-shared-node'
ENVIRONMENT_SECRET = 'claude-runner-environment'


def pod_templates(document):
    kind = document.get('kind')
    spec = document.get('spec') or {}
    if kind in ('Deployment', 'StatefulSet', 'DaemonSet', 'Job'):
        yield f"{kind}/{document['metadata']['name']}", spec.get('template') or {}
    elif kind == 'ScaledJob':
        yield f"{kind}/{document['metadata']['name']}", (spec.get('jobTargetRef') or {}).get('template') or {}
    elif kind == 'CronJob':
        yield f"{kind}/{document['metadata']['name']}", ((spec.get('jobTemplate') or {}).get('spec') or {}).get('template') or {}
    elif kind == 'SandboxTemplate':
        yield f"{kind}/{document['metadata']['name']}", spec.get('podTemplate') or {}
    elif kind == 'ConfigMap':
        for key, value in (document.get('data') or {}).items():
            if key.endswith('.json'):
                inner = json.loads(value)
                for where, template in pod_templates(inner):
                    yield f"ConfigMap/{document['metadata']['name']}[{key}] {where}", template


def mentions_secret(pod, name):
    for volume in pod.get('volumes') or []:
        if (volume.get('secret') or {}).get('secretName') == name:
            return True
        for source in (volume.get('projected') or {}).get('sources') or []:
            if (source.get('secret') or {}).get('name') == name:
                return True
    for container in (pod.get('initContainers') or []) + (pod.get('containers') or []):
        for entry in container.get('env') or []:
            if ((entry.get('valueFrom') or {}).get('secretKeyRef') or {}).get('name') == name:
                return True
        for entry in container.get('envFrom') or []:
            if (entry.get('secretRef') or {}).get('name') == name:
                return True
    return False


def check_runner_pod(where, template, namespace):
    problems = []
    pod = template.get('spec') or {}
    if pod.get('automountServiceAccountToken') is not False:
        problems.append(f"{where}: automountServiceAccountToken is not false")
    for volume in pod.get('volumes') or []:
        types = sorted(set(volume) - {'name'})
        if len(types) != 1 or types[0] not in RUN_POD_VOLUME_TYPES:
            problems.append(f"{where}: volume {volume['name']} is of type {', '.join(types)}, not emptyDir or image")
    for container in (pod.get('initContainers') or []) + (pod.get('containers') or []):
        for entry in container.get('env') or []:
            name = entry['name']
            if 'secretKeyRef' in (entry.get('valueFrom') or {}):
                problems.append(f"{where}: container {container['name']} reads {name} from a Secret")
            if name in FORBIDDEN_ENV:
                problems.append(f"{where}: container {container['name']} renders {name}")
        for entry in container.get('envFrom') or []:
            if 'secretRef' in entry or 'configMapRef' in entry:
                problems.append(f"{where}: container {container['name']} takes envFrom")
    runner = next((c for c in pod.get('containers') or [] if c['name'] == 'runner'), None)
    if runner is None:
        return problems + [f"{where}: no runner container"]
    env = {e['name']: e.get('value') for e in runner.get('env') or []}
    want = f'http://{GATEWAY_NAME}.{namespace}.svc:{SESSION_PORT}'
    if env.get('PLOEG_URL') != want:
        problems.append(f"{where}: PLOEG_URL is {env.get('PLOEG_URL')!r}, want {want!r}")
    if env.get('PLOEG_CREDENTIAL_GATEWAY') != 'on':
        problems.append(f"{where}: PLOEG_CREDENTIAL_GATEWAY is {env.get('PLOEG_CREDENTIAL_GATEWAY')!r}, want 'on' so the hooks clone and push through the gateway")
    if 'PLOEG_CLAUDE_RUNNER_WORK_ORDER' in env:
        problems.append(f"{where}: the template carries a work order; only the spawn-runner hook sets one, per session")
    labels = (template.get('metadata') or {}).get('labels') or {}
    if any(labels.get(k) != v for k, v in SESSION_LABEL.items()):
        problems.append(f"{where}: the runner pod does not carry {SESSION_LABEL}")
    return problems


def selects(policy, labels):
    wanted = (policy['spec'].get('podSelector') or {}).get('matchLabels') or {}
    return bool(wanted) and all(labels.get(k) == v for k, v in wanted.items())


def is_dns_rule(rule):
    ports = rule.get('ports') or []
    return bool(ports) and all(int(p['port']) == 53 for p in ports)


def is_gateway_rule(rule):
    peers = rule.get('to') or []
    ports = {int(p['port']) for p in rule.get('ports') or []}
    return bool(peers) and ports == {SESSION_PORT} and all(
        set(peer) == {'podSelector'}
        and (peer['podSelector'].get('matchLabels') or {}).get('app.kubernetes.io/name') == GATEWAY_NAME
        for peer in peers)


def is_anthropic_rule(rule):
    peers = rule.get('to') or []
    ports = {int(p['port']) for p in rule.get('ports') or []}
    return bool(peers) and ports == {443} and all(set(peer) == {'ipBlock'} for peer in peers)


def check_session_egress(documents, namespace):
    problems = []
    policies = [d for d in documents if d.get('kind') == 'NetworkPolicy'
                and (d['metadata'].get('namespace') or 'default') == namespace
                and selects(d, SESSION_LABEL) and 'Egress' in (d['spec'].get('policyTypes') or [])]
    if not policies:
        return ['no NetworkPolicy limits the egress of the runner pods']
    for policy in policies:
        name = policy['metadata']['name']
        egress = policy['spec'].get('egress') or []
        for kind, test in (('DNS', is_dns_rule), ('the gateway', is_gateway_rule), ('Anthropic', is_anthropic_rule)):
            if not any(test(rule) for rule in egress):
                problems.append(f"NetworkPolicy {name} does not let runner pods reach {kind}")
        for rule in egress:
            if not (is_dns_rule(rule) or is_gateway_rule(rule) or is_anthropic_rule(rule)):
                problems.append(f"NetworkPolicy {name} lets runner pods reach more than DNS, the gateway and Anthropic: {rule}")
        if policy['spec'].get('ingress'):
            problems.append(f"NetworkPolicy {name} admits ingress to runner pods")
    return problems


def check_gateway_session_port(documents, namespace):
    problems = []
    deployment = next((d for d in documents if d.get('kind') == 'Deployment' and d['metadata']['name'].endswith('-credential-gateway')), None)
    if deployment is None:
        return ['no credential gateway Deployment serves the runner pods']
    container = deployment['spec']['template']['spec']['containers'][0]
    env = {e['name']: e.get('value') for e in container.get('env') or []}
    if env.get('PLOEG_GATEWAY_SESSION_POD_SELECTOR') != 'app.kubernetes.io/name=ploeg-claude-runner-session':
        problems.append(f"the gateway's PLOEG_GATEWAY_SESSION_POD_SELECTOR is {env.get('PLOEG_GATEWAY_SESSION_POD_SELECTOR')!r}")
    if not any(int(p['containerPort']) == SESSION_PORT for p in container.get('ports') or []):
        problems.append(f"the gateway does not listen on its session port {SESSION_PORT}")
    policies = [d for d in documents if d.get('kind') == 'NetworkPolicy' and (d['metadata'].get('namespace') or 'default') == namespace
                and ((d['spec'].get('podSelector') or {}).get('matchLabels') or {}).get('app.kubernetes.io/name') == GATEWAY_NAME]
    admitted = [rule for p in policies for rule in p['spec'].get('ingress') or []
                if {int(port['port']) for port in rule.get('ports') or []} == {SESSION_PORT}
                and all((peer.get('podSelector') or {}).get('matchLabels') == SESSION_LABEL and set(peer) == {'podSelector'} for peer in rule.get('from') or [{}])]
    if not admitted:
        problems.append(f"no NetworkPolicy admits the runner pods to the gateway's session port {SESSION_PORT}, and only them")
    for p in policies:
        for rule in p['spec'].get('ingress') or []:
            if SESSION_PORT in {int(port['port']) for port in rule.get('ports') or []} and rule not in admitted:
                problems.append(f"NetworkPolicy {p['metadata']['name']} admits more than the runner pods to the session port: {rule}")
    return problems


def check_orchestrator(documents, namespace, single_node):
    problems = []
    holders = [(where, template) for d in documents for where, template in pod_templates(d)
               if mentions_secret(template.get('spec') or {}, ENVIRONMENT_SECRET)]
    orchestrators = [(w, t) for w, t in holders if w.startswith('Deployment/') and w.endswith('-orchestrator')]
    if len(orchestrators) != 1:
        problems.append(f"want one orchestrator Deployment holding the environment secret, found {len(orchestrators)}")
    for where, _ in holders:
        if (where, _) not in orchestrators:
            problems.append(f"{where} holds the environment secret; only the orchestrator may")
    for where, template in orchestrators:
        deployment = next(d for d in documents if d.get('kind') == 'Deployment' and where == f"Deployment/{d['metadata']['name']}")
        if deployment['metadata'].get('namespace') != namespace:
            problems.append(f"{where} renders into {deployment['metadata'].get('namespace')}, not {namespace}")
        pod = template['spec']
        args = (pod.get('containers') or [{}])[0].get('args') or []
        if args[:2] != ['self-hosted-runner', 'orchestrator']:
            problems.append(f"{where} does not run claude self-hosted-runner orchestrator: {args[:2]}")
        if '--min-idle' in args:
            problems.append(f"{where} pre-warms unbound runners with --min-idle")
        anti = (pod.get('affinity') or {}).get('podAntiAffinity') or {}
        required = anti.get('requiredDuringSchedulingIgnoredDuringExecution') or []
        preferred = [t.get('podAffinityTerm') or {} for t in anti.get('preferredDuringSchedulingIgnoredDuringExecution') or []]
        terms = preferred if single_node else required
        for label in (WORKER_LABEL, SESSION_LABEL):
            if not any(t.get('topologyKey') == 'kubernetes.io/hostname' and (t.get('labelSelector') or {}).get('matchLabels') == label for t in terms):
                problems.append(f"{where} carries no {'preferred' if single_node else 'required'} anti-affinity against {label} by node")
        labels = (template.get('metadata') or {}).get('labels') or {}
        if single_node:
            if required:
                problems.append(f"{where} keeps a required anti-affinity under acknowledgeSingleNode")
            if labels.get(SHARED_NODE_LABEL) != 'true':
                problems.append(f"{where} does not carry {SHARED_NODE_LABEL} under acknowledgeSingleNode")
        elif SHARED_NODE_LABEL in labels:
            problems.append(f"{where} carries {SHARED_NODE_LABEL} without acknowledgeSingleNode")
    return problems


def main(path, namespace, mode=''):
    documents = [d for d in yaml.safe_load_all(open(path)) if d]
    single_node = mode == 'single-node'
    problems = []
    runners = [(w, t) for d in documents for w, t in pod_templates(d)
               if w.startswith('ConfigMap/') and ((t.get('metadata') or {}).get('labels') or {}).get('app.kubernetes.io/name') == SESSION_LABEL['app.kubernetes.io/name']]
    if len(runners) != 1:
        problems.append(f"want one runner Job template, found {len(runners)}")
    for where, template in runners:
        problems += check_runner_pod(where, template, namespace)
        shared = ((template.get('metadata') or {}).get('labels') or {}).get(SHARED_NODE_LABEL)
        if single_node and shared != 'true':
            problems.append(f"{where}: the runner pod does not carry {SHARED_NODE_LABEL} under acknowledgeSingleNode")
    problems += check_session_egress(documents, namespace)
    problems += check_gateway_session_port(documents, namespace)
    problems += check_orchestrator(documents, namespace, single_node)
    for problem in problems:
        print(f"{path}: {problem}")
    return 1 if problems else 0


if __name__ == '__main__':
    sys.exit(main(*sys.argv[1:4]))
