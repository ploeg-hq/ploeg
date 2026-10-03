"""Generate Ploeg's configuration reference from its Go source and Helm chart."""

import argparse
import json
import os
import re
import sys
from pathlib import Path
from urllib.parse import quote

import yaml

root = Path(__file__).resolve().parent.parent
APP = '.'
BINARIES = ['ploegd', 'ploeg-worker']
CHART = f'{APP}/ops/helm/ploeg'
OUTPUT = f'{APP}/docs/reference/configuration.md'
DESCRIPTIONS = f'{APP}/docs/reference/configuration-descriptions.yaml'
COMMAND = 'mise run docs-configuration'
READERS = {'Getenv', 'LookupEnv'}
UNITS = {'Nanosecond': 'ns', 'Microsecond': 'us', 'Millisecond': 'ms', 'Second': 's', 'Minute': 'm', 'Hour': 'h'}
EXAMPLE = re.compile(r'^(?:-\s.*|#.*|[A-Za-z_][\w.-]*:\s*(?:\{.*|\[.*|"[^"]*"|\S+)?\s*(?:#.*)?)$')


def go_files(directory):
    return sorted(path for path in directory.glob('*.go') if not path.name.endswith('_test.go'))


def imports(text):
    """Return the import paths of one Go file."""
    found = re.findall(r'^import\s+(?:[\w.]+\s+)?"([^"]+)"', text, re.M)
    for block in re.findall(r'^import\s*\((.*?)^\)', text, re.M | re.S):
        found += re.findall(r'^\s*(?:[\w.]+\s+)?"([^"]+)"', block, re.M)
    return found


def package_dirs(app, binary):
    """Return the module directories a binary compiles, starting at cmd/<binary>."""
    module = re.search(r'^module\s+(\S+)', (app / 'go.mod').read_text(), re.M)[1]
    seen, queue = [], [app / 'cmd' / binary]
    while queue:
        directory = queue.pop(0)
        if directory in seen or not directory.is_dir():
            continue
        seen.append(directory)
        for path in go_files(directory):
            for imported in imports(path.read_text()):
                if imported == module or imported.startswith(module + '/'):
                    queue.append(app / imported[len(module):].lstrip('/'))
    return seen


def call_arguments(text, start):
    """Split the arguments of the call whose opening parenthesis is at start."""
    depth, quote_char, current, arguments = 0, None, '', []
    for index in range(start, len(text)):
        char = text[index]
        if quote_char:
            current += char
            if char == quote_char and text[index - 1] != '\\':
                quote_char = None
            continue
        if char in '"`':
            quote_char = char
        elif char in '([{':
            depth += 1
            if depth == 1:
                continue
        elif char in ')]}':
            depth -= 1
            if depth == 0:
                arguments.append(current.strip())
                return arguments
        elif char == ',' and depth == 1:
            arguments.append(current.strip())
            current = ''
            continue
        current += char
    return arguments


def function_bodies(text):
    """Yield name, parameter names and body of each top-level function."""
    for match in re.finditer(r'^func\s+(\w+)\(([^)]*)\)[^{\n]*\{\n(.*?)^\}', text, re.M | re.S):
        parameters = [part.split()[0] for part in match[2].split(',') if part.strip()]
        yield match[1], parameters, match[3]


def helpers(texts):
    """Return helpers whose first parameter names the variable they read, with their arity and whether they require it."""
    found = {}
    changed = True
    while changed:
        changed = False
        for text in texts:
            for name, parameters, body in function_bodies(text):
                if name in found or not parameters:
                    continue
                key = re.escape(parameters[0])
                readers = '|'.join([r'os\.(?:' + '|'.join(READERS) + ')'] + [re.escape(helper) for helper in found])
                if re.search(rf'(?:{readers})\({key}\s*[,)]', body):
                    found[name] = {'arity': len(parameters), 'required': bool(re.search(r'is required|os\.Exit', body))}
                    changed = True
    return found


def constants(directory):
    """Return the constant and package-level declarations of one package as source expressions."""
    values = {}
    for path in go_files(directory):
        text = path.read_text()
        for block in re.findall(r'^const\s*\((.*?)^\)', text, re.M | re.S):
            values.update(dict(re.findall(r'^\s*(\w+)\s*(?:\w+\s*)?=\s*(.+?)\s*$', block, re.M)))
        values.update(dict(re.findall(r'^const\s+(\w+)\s*(?:\w+\s*)?=\s*(.+?)\s*$', text, re.M)))
    return values


def render_default(expression, package, app, module):
    """Render a Go default expression as the value an operator would write."""
    expression = expression.strip()
    if re.fullmatch(r'"(?:[^"\\]|\\.)*"', expression):
        return json.loads(expression)
    duration = re.fullmatch(r'(\d+)\s*\*\s*time\.(\w+)', expression)
    if duration and duration[2] in UNITS:
        return duration[1] + UNITS[duration[2]]
    reference = re.fullmatch(r'os\.Getenv\("(\w+)"\)', expression)
    if reference:
        return f'${reference[1]}'
    qualified = re.fullmatch(r'(?:(\w+)\.)?(\w+)', expression)
    if qualified:
        directory = package
        if qualified[1]:
            path = next((imported for imported in imports(''.join(p.read_text() for p in go_files(package))) if imported.split('/')[-1] == qualified[1]), None)
            directory = app / path[len(module):].lstrip('/') if path and path.startswith(module) else None
        value = constants(directory).get(qualified[2]) if directory else None
        if value is not None:
            return render_default(value, directory, app, module)
    return f'`{expression}`'


def scan(base=root):
    """Return every environment variable read by each binary, keyed by (name, binary)."""
    app = base / APP
    module = re.search(r'^module\s+(\S+)', (app / 'go.mod').read_text(), re.M)[1]
    variables = {}
    for binary in BINARIES:
        directories = package_dirs(app, binary)
        texts = {path: path.read_text() for directory in directories for path in go_files(directory)}
        known = helpers(texts.values())
        pattern = re.compile(r'\b(?:os\.(' + '|'.join(READERS) + r')|(' + '|'.join(map(re.escape, known)) + r'))\(' if known else r'\bos\.(' + '|'.join(READERS) + r')\(()')
        for path, text in texts.items():
            reads = []
            for match in pattern.finditer(text):
                arguments = call_arguments(text, match.end() - 1)
                if not arguments:
                    continue
                helper = known.get(match[2]) if match[2] else None
                reads.append((arguments, helper))
            for loop in re.finditer(r'^(\s*)for\s+_,\s*(\w+)\s*:=\s*range\s+\[\]string\{([^}]*)\}\s*\{\n(.*?)^\1\}', text, re.M | re.S):
                if re.search(rf'(?:os\.(?:{"|".join(READERS)})|{"|".join(map(re.escape, known)) or "x^"})\({loop[2]}\)', loop[4]):
                    reads += [([json.dumps(name)], None) for name in re.findall(r'"(\w+)"', loop[3])]
            for arguments, helper in reads:
                literal = re.fullmatch(r'"(\w+)"', arguments[0])
                if not literal:
                    continue
                name = literal[1]
                entry = variables.setdefault((name, binary), {'name': name, 'binary': binary, 'default': '', 'files': []})
                default = ''
                if helper and helper['arity'] > 1 and len(arguments) > 1:
                    default = render_default(arguments[1], path.parent, app, module)
                elif (helper and helper['required']) or f'"{name} is required"' in text:
                    default = 'required'
                if default and not entry['default']:
                    entry['default'] = default
                relative = path.relative_to(base).as_posix()
                if relative not in entry['files']:
                    entry['files'].append(relative)
    return [variables[key] for key in sorted(variables)]


def values_comments(text):
    """Return the comment above or beside each key of a values.yaml, keyed by dotted path."""
    comments, stack, pending = {}, [], []
    for line in text.splitlines():
        stripped = line.strip()
        if not stripped:
            pending = []
            continue
        if stripped.startswith('#'):
            content = stripped[1:].strip()
            if content and not EXAMPLE.match(content):
                pending.append(content)
            continue
        match = re.match(r'^(\s*)([\w.-]+):(?:\s+(.*))?$', line)
        if not match:
            pending = []
            continue
        indent = len(match[1])
        while stack and stack[-1][0] >= indent:
            stack.pop()
        path = '.'.join([key for _, key in stack] + [match[2]])
        inline = re.search(r'^(?:[^"\'#]|"[^"]*"|\'[^\']*\')*?\s#\s?(.*)$', match[3] or '')
        text_parts = pending + ([inline[1].strip()] if inline and inline[1].strip() else [])
        if text_parts:
            comments[path] = ' '.join(text_parts)
        stack.append((indent, match[2]))
        pending = []
    return comments


def values_leaves(values, prefix='', shapes=frozenset(), comments=None):
    """Yield dotted path and default of each leaf in the values tree; an empty map, a list or a shared shape with no commented key is a leaf."""
    comments = comments or {}
    for key, value in values.items():
        path = f'{prefix}.{key}' if prefix else key
        opaque = path in shapes and not any(name.startswith(path + '.') for name in comments)
        if isinstance(value, dict) and value and not opaque:
            yield path, None
            yield from values_leaves(value, path, shapes, comments)
        else:
            yield path, value


def schema_type(node):
    if '$ref' in node:
        name = node['$ref'].rsplit('/', 1)[-1]
        return f'[{name}](#{name.lower()})'
    if 'enum' in node:
        return 'one of ' + ', '.join(f'`{json.dumps(value)}`' for value in node['enum'])
    if 'oneOf' in node:
        return ' or '.join(schema_type(option) for option in node['oneOf'])
    kind = node.get('type', '')
    return ' or '.join(kind) if isinstance(kind, list) else kind


def schema_nodes(node, prefix=''):
    """Yield dotted path and schema node for each property, with [] marking array items."""
    for key, child in (node.get('properties') or {}).items():
        path = f'{prefix}.{key}' if prefix else key
        yield path, child
        yield from schema_nodes(child, path)
        items = child.get('items')
        if isinstance(items, dict) and items.get('properties'):
            yield from schema_nodes(items, f'{path}[]')


def schema_description(node):
    return ' '.join(part for part in [node.get('description'), node.get('//')] if part)


def render_value(value):
    if value is None:
        return ''
    if isinstance(value, str):
        return f'`{value}`' if value else '`""`'
    return f'`{json.dumps(value, separators=(", ", ": "))}`'


def helm_rows(base=root, notes=None):
    """Return the chart values rows and the shared schema definition rows."""
    chart = base / CHART
    values_text = (chart / 'values.yaml').read_text()
    values = yaml.safe_load(values_text) or {}
    schema = json.loads((chart / 'values.schema.json').read_text())
    comments = values_comments(values_text)
    notes = notes or {}
    rows = {}
    nodes = dict(schema_nodes(schema))
    for path, default in values_leaves(values, shapes={path for path, node in nodes.items() if '$ref' in node}, comments=comments):
        rows[path] = {'key': path, 'type': '', 'default': render_value(default), 'description': comments.get(path, ''), 'sources': ['values.yaml'], 'parent': default is None}
    for path, node in nodes.items():
        row = rows.setdefault(path, {'key': path, 'type': '', 'default': '', 'description': '', 'sources': [], 'parent': bool(node.get('properties'))})
        row['type'] = schema_type(node)
        row['sources'].append('values.schema.json')
        row['description'] = schema_description(node) or row['description']
    for path, text in notes.items():
        if path in rows and not rows[path]['description']:
            rows[path]['description'] = text
    kept = [row for row in rows.values() if not row['parent'] or row['description']]
    shapes = {}
    for name, definition in (schema.get('definitions') or {}).items():
        shapes[name] = {'description': schema_description(definition), 'rows': [{'key': path, 'type': schema_type(node), 'description': schema_description(node) or notes.get(f'{name}.{path}', '')} for path, node in schema_nodes(definition)]}
    return sorted(kept, key=lambda row: row['key']), shapes


def load_descriptions(base=root):
    path = base / DESCRIPTIONS
    data = yaml.safe_load(path.read_text()) if path.exists() else None
    return data or {}


def helm_notes(descriptions):
    """Return chart value descriptions, letting a plain `env.<NAME>` value borrow ploegd's description of that variable."""
    notes = {}
    for name, entry in (descriptions.get('env') or {}).items():
        text = entry.get('ploegd') if isinstance(entry, dict) else entry
        if text:
            notes[f'env.{name}'] = text
    notes.update(descriptions.get('helm') or {})
    return notes


def env_description(descriptions, variable):
    entry = (descriptions.get('env') or {}).get(variable['name'])
    if isinstance(entry, dict):
        entry = entry.get(variable['binary'])
    return entry or ''


def unknown_descriptions(base=root):
    """Return description entries that name no variable, binary or chart value."""
    descriptions = load_descriptions(base)
    variables = {(variable['name'], variable['binary']) for variable in scan(base)}
    names = {name for name, _ in variables}
    rows, shapes = helm_rows(base, helm_notes(descriptions))
    keys = {row['key'] for row in rows} | {f'{name}.{row["key"]}' for name, shape in shapes.items() for row in shape['rows']}
    problems = []
    for name, entry in (descriptions.get('env') or {}).items():
        if name not in names:
            problems.append(f'env {name}: no binary reads it')
        elif isinstance(entry, dict):
            problems += [f'env {name}: {binary} does not read it' for binary in entry if (name, binary) not in variables]
    problems += [f'helm {key}: not a chart value' for key in (descriptions.get('helm') or {}) if key not in keys]
    return problems


def cell(text):
    return str(text).replace('|', '\\|').replace('\n', ' ')


def link(target, output):
    return f'[{Path(target).name}]({quote(os.path.relpath(target, output.parent))})'


def render(base=root):
    output = base / OUTPUT
    descriptions = load_descriptions(base)
    variables = scan(base)
    rows, shapes = helm_rows(base, helm_notes(descriptions))
    chart = base / CHART
    lines = [
        '---',
        'type: reference',
        'audience: [operator, contributor, agent]',
        'owner: ploeg',
        f'generated_by: "{COMMAND}"',
        '---',
        '',
        '# Configuration reference',
        '',
        f'*Generated by `scripts/docs-configuration.py` from the Go source of `ploegd` and `ploeg-worker`, the chart\'s `values.yaml` and `values.schema.json`, and `{Path(DESCRIPTIONS).name}`. Do not edit by hand; regenerate with `{COMMAND}`.*',
        '',
        'This page lists every environment variable the two Ploeg binaries read and every value the Helm chart accepts. The source stays authoritative: `mise run docs-check` fails when this page drifts from it.',
        '',
        '## Environment variables',
        '',
        f'The generator follows each binary\'s imports inside the module and records every literal name passed to `os.Getenv`, `os.LookupEnv` or a helper that wraps them. "required" means the binary refuses to start without it. An empty description has no current documentation; add one to [{Path(DESCRIPTIONS).name}]({quote(os.path.relpath(base / DESCRIPTIONS, output.parent))}).',
        '',
        '| Name | Binary | Default | Description | Source |',
        '| --- | --- | --- | --- | --- |',
    ]
    for variable in variables:
        default = variable['default']
        shown = default if default == 'required' or default.startswith('`') else (f'`{default}`' if default else '')
        files = ', '.join(link(base / name, output) for name in variable['files'])
        lines.append(f"| `{variable['name']}` | {variable['binary']} | {cell(shown)} | {cell(env_description(descriptions, variable))} | {files} |")
    lines += [
        '',
        '## Helm values',
        '',
        f'Keys come from [values.yaml]({quote(os.path.relpath(chart / "values.yaml", output.parent))}) and [values.schema.json]({quote(os.path.relpath(chart / "values.schema.json", output.parent))}). `[]` marks the items of a list. A description comes from the schema, then from the comment beside the key in `values.yaml`.',
        '',
        '| Key | Type | Default | Description | Source |',
        '| --- | --- | --- | --- | --- |',
    ]
    for row in rows:
        lines.append(f"| `{row['key']}` | {cell(row['type'])} | {cell(row['default'])} | {cell(row['description'])} | {', '.join(row['sources'])} |")
    lines += ['', '## Shared value shapes', '', 'Several keys above share one schema definition.']
    for name, shape in shapes.items():
        lines += ['', f'### {name}', '']
        if shape['description']:
            lines += [cell(shape['description']), '']
        lines += ['| Key | Type | Description |', '| --- | --- | --- |']
        lines += [f"| `{row['key']}` | {cell(row['type'])} | {cell(row['description'])} |" for row in shape['rows']]
    return '\n'.join(lines) + '\n'


def gaps(base=root):
    """Return the variables and chart values that have no description."""
    descriptions = load_descriptions(base)
    missing = [f"{variable['name']} ({variable['binary']})" for variable in scan(base) if not env_description(descriptions, variable)]
    rows, shapes = helm_rows(base, helm_notes(descriptions))
    missing += [row['key'] for row in rows if not row['description']]
    missing += [f"{name}.{row['key']}" for name, shape in shapes.items() for row in shape['rows'] if not row['description']]
    return missing


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description='Generate the Ploeg configuration reference.')
    parser.add_argument('--check', action='store_true', help='fail when the committed page is stale')
    parser.add_argument('--gaps', action='store_true', help='list variables and values without a description')
    args = parser.parse_args()
    problems = unknown_descriptions()
    if problems:
        sys.exit('Configuration description problems:\n' + '\n'.join(problems))
    if args.gaps:
        print('\n'.join(gaps()))
        sys.exit()
    expected = render()
    target = root / OUTPUT
    if args.check:
        if not target.exists() or target.read_text() != expected:
            sys.exit(f'Stale configuration reference: {OUTPUT}; regenerate with `{COMMAND}`')
    else:
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(expected)
        print(f'Wrote {OUTPUT}')
