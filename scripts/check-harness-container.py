import sys
from pathlib import Path

import yaml

SEPARATE = {
    'executor-default-worker',
    'executor-cronjob',
    'executor-sandbox-team',
}
SHARED = {
    'executor-harness-container-shared',
    'executor-acknowledged-dind',
    'executor-isolation-disabled',
}
POD_SHAPES = {
    'executor-isolation': {
        'ScaledJob/ploeg-worker-qualified': True,
        'ScaledJob/ploeg-worker-acp-openhands': True,
        'ScaledJob/ploeg-worker-override-optin': True,
        'ScaledJob/ploeg-worker-dind': False,
        'ScaledJob/ploeg-worker-unqualified': False,
        'ScaledJob/ploeg-worker-acp-opencode': False,
        'ScaledJob/ploeg-worker-override-builder': False,
    },
}


def pod_specs(document):
    if not isinstance(document, dict):
        return
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
    pod = template.get('spec') or {}
    names = [c.get('name') for c in pod.get('containers') or []]
    if 'worker' in names:
        yield f"{kind}/{document['metadata']['name']}", pod


def secret_volumes(pod):
    names = set()
    for volume in pod.get('volumes') or []:
        if 'secret' in volume:
            names.add(volume['name'])
        for source in (volume.get('projected') or {}).get('sources') or []:
            if 'secret' in source:
                names.add(volume['name'])
    return names


def credential_sources(container, secrets):
    found = []
    for entry in container.get('env') or []:
        reference = (entry.get('valueFrom') or {})
        if 'secretKeyRef' in reference:
            found.append(f"env {entry['name']} from a Secret")
    for entry in container.get('envFrom') or []:
        if 'secretRef' in entry:
            found.append('envFrom a Secret')
    for mount in container.get('volumeMounts') or []:
        if mount.get('name') in secrets:
            found.append(f"Secret volume {mount['name']}")
    return found


def check_pod(where, pod):
    problems = []
    secrets = secret_volumes(pod)
    init = {c['name']: c for c in pod.get('initContainers') or []}
    main = {c['name']: c for c in pod.get('containers') or []}
    for name, container in {**init, **main}.items():
        if name == 'worker':
            continue
        for source in credential_sources(container, secrets):
            problems.append(f"{where}: container {name} receives {source}")
    harness = init.get('harness')
    if harness is None:
        return problems, False
    if harness.get('restartPolicy') != 'Always':
        problems.append(f"{where}: the harness container is not a native sidecar (restartPolicy: Always)")
    extra = [e['name'] for e in harness.get('env') or [] if e.get('name') != 'PATH']
    if extra or harness.get('envFrom'):
        problems.append(f"{where}: the harness container's environment is not PATH alone: {extra}")
    worker = main['worker']
    if worker.get('image') == harness.get('image'):
        problems.append(f"{where}: the worker container runs the harness image")
    launcher = [e for e in worker.get('env') or [] if e.get('name') == 'PLOEG_HARNESS_LAUNCHER']
    if not launcher:
        problems.append(f"{where}: the worker container does not listen for the launcher")
    if not credential_sources(worker, secrets):
        problems.append(f"{where}: the worker container holds no credential, so the check proves nothing")
    return problems, True


def main(golden):
    problems = []
    for path in sorted(Path(golden).glob('*.yaml')):
        name = path.stem
        pods = [pod for document in yaml.safe_load_all(path.read_text()) for pod in pod_specs(document)]
        separate = []
        for where, pod in pods:
            found, split = check_pod(f"{name}: {where}", pod)
            problems += found
            separate.append(split)
            expected = POD_SHAPES.get(name, {}).get(where)
            if expected is not None and expected != split:
                problems.append(f"{name}: {where} renders harnessContainer {'separate' if split else 'shared'}, want {'separate' if expected else 'shared'}")
        if name in SEPARATE and not (separate and all(separate)):
            problems.append(f"{name}: expected every worker pod to run its harness in its own container")
        if name in SHARED and any(separate):
            problems.append(f"{name}: expected the harness in the worker's container")
    for problem in problems:
        print(problem)
    return 1 if problems else 0


if __name__ == '__main__':
    sys.exit(main(sys.argv[1]))
