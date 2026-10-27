import argparse
import json
import os
import re
import sys
from pathlib import Path

import yaml

repository = Path(__file__).resolve().parent.parent
MATRIX = 'docs/reference/support-matrix.yaml'
OUTPUT = 'docs/reference/support-matrix.md'
COMMAND = 'mise run support-matrix'
WORKFLOWS = '.github/workflows'
RESEARCH = 'docs/research/'
RECURRING_TRIGGERS = {'push', 'pull_request', 'schedule'}
SKIP_CALL = re.compile(r'\bt\.Skip(?:f|Now)?\(')
STATUSES = {
    'qualified-live': 'A dated research record shows it working against a live deployment or service, at the named release or commit.',
    'tested-in-ci': 'A CI job that runs on every change or on a schedule exercises it.',
    'supported': 'Inside the supported profile and documented, but no CI job or live record exercises it.',
    'experimental': 'Implemented or plausible, outside what anything tests. It may work; nothing here says it does.',
    'unsupported': 'Outside the supported profile. The chart refuses it under `supportProfile: P1` where it can see it.',
}
REQUIRED_EVIDENCE = {
    'qualified-live': {'live'},
    'tested-in-ci': {'ci'},
    'supported': {'doc'},
    'unsupported': {'doc'},
    'experimental': set(),
}
EVIDENCE_KINDS = ('ci', 'live', 'doc', 'source')


def normalized(text):
    return ' '.join(text.split())


def heading_slug(heading):
    slug = re.sub(r'[^\w\- ]', '', heading.strip().lower())
    return slug.replace(' ', '-')


def anchors_of(text):
    return {heading_slug(match) for match in re.findall(r'^#{1,6}\s+(.*?)\s*#*\s*$', text, re.M)}


class Repository:
    def __init__(self, root):
        self.root = root
        self.texts = {}
        self.workflows = {}

    def text(self, path):
        if path not in self.texts:
            target = self.root / path
            self.texts[path] = target.read_text() if target.is_file() else None
        return self.texts[path]

    def workflow(self, name):
        if name not in self.workflows:
            text = self.text(f'{WORKFLOWS}/{name}')
            self.workflows[name] = yaml.safe_load(text) if text is not None else None
        return self.workflows[name]

    def go_test_body(self, package, name):
        directory = self.root / package
        if not directory.is_dir():
            return None
        for path in sorted(directory.glob('*_test.go')):
            text = path.read_text()
            start = re.search(rf'^func {re.escape(name)}\(', text, re.M)
            if start:
                end = re.search(r'^func ', text[start.end():], re.M)
                return text[start.start(): start.end() + end.start()] if end else text[start.start():]
        return None


def missing_phrases(text, phrases):
    haystack = normalized(text)
    return [phrase for phrase in phrases if normalized(phrase) not in haystack]


def workflow_triggers(workflow):
    triggers = workflow.get(True, workflow.get('on'))
    if isinstance(triggers, str):
        return {triggers}
    if isinstance(triggers, list):
        return set(triggers)
    return set(triggers or {})


def check_ci(repo, evidence):
    name, job_id = evidence.get('workflow'), evidence.get('job')
    workflow = repo.workflow(name) if name else None
    if workflow is None:
        return [f'workflow {WORKFLOWS}/{name} does not exist']
    if not workflow_triggers(workflow) & RECURRING_TRIGGERS:
        return [f'workflow {name} never runs on a push, a pull request or a schedule, so it tests no release']
    job = (workflow.get('jobs') or {}).get(job_id)
    if job is None:
        return [f'workflow {name} has no job {job_id}']
    job_text = json.dumps(job) + json.dumps(workflow.get('env') or {})
    problems = [f'job {name}/{job_id} does not contain {phrase!r}' for phrase in missing_phrases(job_text, evidence.get('contains', []))]
    test = evidence.get('test')
    if test:
        body = repo.go_test_body(test['package'], test['name'])
        if body is None:
            problems.append(f'{test["package"]} has no test {test["name"]}')
        elif SKIP_CALL.search(body):
            enabled_by = test.get('enabledBy')
            if not enabled_by:
                problems.append(f'{test["name"]} skips unless something enables it; name what in {name}/{job_id} does (enabledBy)')
            elif missing_phrases(job_text, [enabled_by]):
                problems.append(f'{test["name"]} skips unless {enabled_by!r}, which job {name}/{job_id} does not contain')
    return problems


def check_live(repo, evidence):
    record = evidence.get('record', '')
    if not record.startswith(RESEARCH):
        return [f'live evidence {record} is not a research record under {RESEARCH}']
    text = repo.text(record)
    if text is None:
        return [f'research record {record} does not exist']
    at = evidence.get('at')
    if not at:
        return [f'live evidence {record} names no release or commit (at)']
    problems = [f'{record} does not name {at!r}, the release it claims to qualify'] if missing_phrases(text, [at]) else []
    problems += [f'{record} does not contain {phrase!r}' for phrase in missing_phrases(text, evidence.get('contains', []))]
    return problems


def check_document(repo, evidence):
    path, _, anchor = evidence.get('path', '').partition('#')
    text = repo.text(path)
    if text is None:
        return [f'{path} does not exist']
    problems = [f'{path} has no heading for #{anchor}'] if anchor and anchor not in anchors_of(text) else []
    problems += [f'{path} does not contain {phrase!r}' for phrase in missing_phrases(text, evidence.get('contains', []))]
    return problems


CHECKS = {'ci': check_ci, 'live': check_live, 'doc': check_document, 'source': check_document}


def evidence_kind(evidence):
    kinds = [kind for kind in EVIDENCE_KINDS if kind in evidence]
    return kinds[0] if len(kinds) == 1 else None


def validate(repo, matrix):
    problems = []
    lines = [line['id'] for line in matrix.get('lines', [])]
    axes = {axis['id'] for axis in matrix.get('axes', [])}
    seen = set()
    if not lines:
        problems.append('the matrix names no release line')
    for row in matrix.get('rows', []):
        row_id = row.get('id', '?')
        where = f'row {row_id}'
        if row_id in seen:
            problems.append(f'{where}: duplicate id')
        seen.add(row_id)
        if row.get('axis') not in axes:
            problems.append(f'{where}: unknown axis {row.get("axis")!r}')
        cells = row.get('cells') or {}
        if set(cells) != set(lines):
            problems.append(f'{where}: cells {sorted(cells)} do not match the release lines {sorted(lines)}')
        kinds = set()
        for evidence in row.get('evidence', []):
            kind = evidence_kind(evidence)
            if kind is None:
                problems.append(f'{where}: evidence must be exactly one of {", ".join(EVIDENCE_KINDS)}: {evidence}')
                continue
            kinds.add(kind)
            problems += [f'{where}: {problem}' for problem in CHECKS[kind](repo, evidence[kind])]
        for line, status in cells.items():
            if status not in STATUSES:
                problems.append(f'{where} {line}: unknown status {status!r}')
                continue
            needed = REQUIRED_EVIDENCE[status]
            if needed and not needed & kinds:
                problems.append(f'{where} {line}: {status} needs {" or ".join(sorted(needed))} evidence, and the row has none')
            if status == 'experimental' and not row.get('limits'):
                problems.append(f'{where} {line}: experimental needs limits saying what is untested')
    return problems


def relative_link(path):
    return os.path.relpath(path, os.path.dirname(OUTPUT))


def evidence_link(evidence):
    kind = evidence_kind(evidence)
    body = evidence[kind]
    if kind == 'ci':
        label = f'CI `{body["workflow"].removesuffix(".yml")}` / `{body["job"]}`'
        return f'[{label}]({relative_link(WORKFLOWS + "/" + body["workflow"])})'
    if kind == 'live':
        name = Path(body['record']).stem
        return f'[live: {name}]({relative_link(body["record"])}) at `{body["at"]}`'
    path, _, anchor = body['path'].partition('#')
    label = body.get('label') or Path(path).name
    target = relative_link(path) + (f'#{anchor}' if anchor else '')
    return f'[{label}]({target})'


def cell_text(status, row):
    if status != 'qualified-live':
        return status
    ats = [evidence['live']['at'] for evidence in row.get('evidence', []) if 'live' in evidence]
    return f'qualified-live ({", ".join(f"`{at}`" for at in ats)})'


def escape(text):
    return normalized(text).replace('|', '\\|')


def render(matrix):
    lines = matrix['lines']
    out = [
        '---',
        'type: reference',
        'audience: [owner, operator, integrator, contributor, agent]',
        'owner: ploeg',
        f'generated_by: "{COMMAND}"',
        '---',
        '',
        '# Support and qualification matrix',
        '',
        f'*Generated by `scripts/support-matrix.py` from [support-matrix.yaml](support-matrix.yaml). Do not edit by hand; edit the YAML and run `{COMMAND}`. `mise run docs-check` fails when this page drifts from the YAML, or when a cell claims more than its evidence shows.*',
        '',
        escape(matrix['summary']),
        '',
        '## What a cell means',
        '',
        '| Status | Meaning | The check requires |',
        '| --- | --- | --- |',
    ]
    requirement = {
        'qualified-live': 'a research record under `docs/research/` that names the release or commit (`at`) and contains the quoted result',
        'tested-in-ci': 'a job that exists in a workflow triggered by push, pull request or schedule, whose text contains the quoted versions; a named Go test must exist and, if it skips by default, the job must contain what enables it',
        'supported': 'a page that states the requirement, containing the quoted text',
        'experimental': 'a limits note saying what is untested',
        'unsupported': 'a page that states the exclusion, containing the quoted text',
    }
    for status, meaning in STATUSES.items():
        out.append(f'| `{status}` | {meaning} | {requirement[status]} |')
    out += ['', '## Release lines', '', '| Line | State | What it covers |', '| --- | --- | --- |']
    for line in lines:
        out.append(f'| {line["title"]} | {line["state"]} | {escape(line["summary"])} |')
    header = '| Item | ' + ' | '.join(line['title'] for line in lines) + ' | Evidence | Limits |'
    divider = '| --- | ' + ' | '.join('---' for _ in lines) + ' | --- | --- |'
    for axis in matrix['axes']:
        rows = [row for row in matrix['rows'] if row['axis'] == axis['id']]
        out += ['', f'## {axis["title"]}', '', escape(axis['summary']), '', header, divider]
        for row in rows:
            cells = ' | '.join(cell_text(row['cells'][line['id']], row) for line in lines)
            evidence = '<br>'.join(evidence_link(evidence) for evidence in row.get('evidence', [])) or 'none'
            out.append(f'| {escape(row["item"])} | {cells} | {evidence} | {escape(row.get("limits", ""))} |')
    out += ['', '## In release notes', '', escape(matrix['releaseNotes']), '']
    return '\n'.join(out)


def notes(matrix, line_id, commit):
    line = next((line for line in matrix['lines'] if line['id'] == line_id), None)
    if line is None:
        sys.exit(f'No release line {line_id!r}; the lines are {", ".join(entry["id"] for entry in matrix["lines"])}')
    reference = commit or 'development'
    base = f'https://github.com/ploeg-hq/ploeg/blob/{reference}/'
    out = ['### Qualification', '', escape(matrix['notesIntro']), '']
    for status in STATUSES:
        groups = []
        for axis in matrix['axes']:
            items = [escape(row['item']) for row in matrix['rows'] if row['axis'] == axis['id'] and row['cells'][line_id] == status]
            if items:
                groups.append(f'- {axis["title"]}: {"; ".join(items)}')
        if groups:
            out += [f'**{status}**', '', *groups, '']
    out.append(f'Every cell, with its evidence and limits: [support and qualification matrix]({base}{OUTPUT}).')
    return '\n'.join(out) + '\n'


def main():
    parser = argparse.ArgumentParser(description='Validate and render the support and qualification matrix')
    parser.add_argument('--check', action='store_true', help='fail when the committed page is stale or a cell claims more than its evidence')
    parser.add_argument('--matrix', default=None, help='the YAML source, by default docs/reference/support-matrix.yaml')
    parser.add_argument('--root', default=None, help='the repository the evidence paths resolve against')
    parser.add_argument('--output', default=None, help='the page to write or compare, by default docs/reference/support-matrix.md')
    parser.add_argument('--notes', metavar='LINE', help="print the release notes' Qualification section for a release line")
    parser.add_argument('--commit', help='the commit the release notes link the matrix at')
    args = parser.parse_args()
    root = Path(args.root).resolve() if args.root else repository
    matrix_path = Path(args.matrix) if args.matrix else root / MATRIX
    output = Path(args.output) if args.output else root / OUTPUT
    matrix = yaml.safe_load(matrix_path.read_text())
    problems = validate(Repository(root), matrix)
    if problems:
        sys.exit('Support matrix claims without matching evidence:\n' + '\n'.join(f'- {problem}' for problem in problems))
    if args.notes:
        sys.stdout.write(notes(matrix, args.notes, args.commit))
        return
    expected = render(matrix)
    if args.check:
        if not output.exists() or output.read_text() != expected:
            sys.exit(f'Stale support matrix: {OUTPUT}; regenerate with `{COMMAND}`')
        return
    output.write_text(expected)
    print(f'Wrote {output.relative_to(root) if output.is_relative_to(root) else output}')


if __name__ == '__main__':
    main()
