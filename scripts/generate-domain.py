#!/usr/bin/env python3
"""Generate domain documentation from a domain model YAML file.

Usage:
    mise exec -- uv run --frozen python scripts/generate-domain.py docs/domain/model.yaml -o docs/domain/
    mise exec -- uv run --frozen python scripts/generate-domain.py --glossary docs/reference/glossary.md MODEL [MODEL ...]

Produces in the output directory:
    overview.md   - project intro, bounded contexts, ER diagram (Mermaid)
    glossary.md   - alphabetized terms with definitions and cross-references
    entities.md   - attribute tables, relationships, state diagrams (Mermaid)
    rules.md      - business rules grouped by what they apply to
    events.md     - domain events (only if the model defines any)

With --glossary, merges the terms of every model into one page with a context
and owner column and lists words with more than one meaning first. A term
defined by two models, or a synonym that names another model's term, stops
generation. --stdout prints the page instead of writing it.

Validation warnings (dangling references, duplicate names) go to stderr and
never block per-model generation.
"""

import argparse
import os
import re
import sys
import unicodedata
from pathlib import Path

try:
    import yaml
except ImportError:
    sys.exit("PyYAML is required: run this script through the locked environment, mise exec -- uv run --frozen python scripts/generate-domain.py")


def warn(msg):
    print(f"WARNING: {msg}", file=sys.stderr)


def slug(name):
    """Anchor slug matching python-markdown's toc slugify, so intra-page
    links resolve when the docs are rendered by MkDocs."""
    s = unicodedata.normalize("NFKD", str(name)).encode("ascii", "ignore").decode("ascii")
    s = re.sub(r"[^\w\s-]", "", s).strip().lower()
    return re.sub(r"[-\s]+", "-", s)


def mermaid_id(name):
    """Mermaid node ids can't contain spaces/punctuation."""
    return re.sub(r"[^A-Za-z0-9_]", "_", str(name))


def load(path):
    with open(path) as f:
        model = yaml.safe_load(f) or {}
    model["_path"] = Path(path)
    return model


def imported_terms(model):
    names = set()
    for imp in model.get("imports", []) or []:
        source = load(model["_path"].parent / imp["model"])
        defined = {t.get("name") for t in source.get("terms", [])}
        for name in imp.get("terms", []) or []:
            if name not in defined:
                warn(f"import {name!r} is not a term of {imp['model']}")
            names.add(name)
    return names


def owner_of(term, model):
    return term.get("owner") or model.get("owner") or str(model.get("project", "")).lower()


def cell(text):
    return " ".join(str(text).split()).replace("|", "\\|")


def front_matter(owner):
    """Front matter for a generated reference page; `generated_by` stands in for last_verified."""
    return ("---\ntype: reference\naudience: [owner, integrator, contributor, agent]\n"
            f"owner: {owner}\ngenerated_by: \"mise run domain\"\n---\n\n")


def validate(model):
    contexts = {c.get("name") for c in model.get("contexts", [])}
    terms = [t.get("name") for t in model.get("terms", [])]
    entities = [e.get("name") for e in model.get("entities", [])]
    rule_ids = [r.get("id") for r in model.get("rules", [])]

    for label, names in (("term", terms), ("entity", entities), ("rule id", rule_ids)):
        seen = set()
        for n in names:
            if n in seen:
                warn(f"duplicate {label}: {n!r}")
            seen.add(n)

    imported = imported_terms(model) if "_path" in model else set()
    for name in imported & set(terms):
        warn(f"term {name!r} is both defined and imported")
    known = set(terms) | set(entities) | imported

    def check_ctx(obj, kind):
        ctx = obj.get("context")
        if ctx and ctx not in contexts:
            warn(f"{kind} {obj.get('name') or obj.get('id')!r} references unknown context {ctx!r}")

    for t in model.get("terms", []):
        check_ctx(t, "term")
        for ref in t.get("see_also", []) or []:
            if ref not in terms and ref not in imported:
                warn(f"term {t.get('name')!r} see_also references unknown term {ref!r}")
        for entry in t.get("not_to_be_confused_with", []) or []:
            if not entry.get("term") or not entry.get("note"):
                warn(f"term {t.get('name')!r} has a not_to_be_confused_with entry without term and note")

    for e in model.get("entities", []):
        check_ctx(e, "entity")
        for rel in e.get("relationships", []) or []:
            if rel.get("to") not in entities:
                warn(f"entity {e.get('name')!r} relationship targets unknown entity {rel.get('to')!r}")
        froms = {s.get("from") for s in e.get("states", []) or []}
        tos = {s.get("to") for s in e.get("states", []) or []}
        for state in (froms | tos) - {"[start]", None}:
            if state not in froms and state not in tos:
                warn(f"entity {e.get('name')!r} has isolated state {state!r}")

    for r in model.get("rules", []):
        check_ctx(r, "rule")
        for target in r.get("applies_to", []) or []:
            if target not in known:
                warn(f"rule {r.get('id')!r} applies_to unknown term/entity {target!r}")

    for ev in model.get("events", []):
        ent = ev.get("entity")
        if ent and ent not in entities:
            warn(f"event {ev.get('name')!r} references unknown entity {ent!r}")

    for a in model.get("ambiguities", []) or []:
        if a.get("status") == "resolved" and not a.get("resolution"):
            warn(f"ambiguity {a.get('phrase')!r} is resolved but has no resolution text")
        if a.get("status", "open") == "open" and a.get("phrase") in known:
            warn(f"ambiguity {a.get('phrase')!r} is still open but matches a defined term — resolve it or rename one")

    for d in model.get("dialogues", []) or []:
        ctx = d.get("context")
        if ctx and ctx not in contexts:
            warn(f"dialogue {d.get('title')!r} references unknown context {ctx!r}")




def gen_overview(model):
    lines = [f"# {model.get('project', 'Domain')} — Domain Overview", ""]
    if model.get("description"):
        lines += [str(model["description"]).strip(), ""]
    if model.get("version"):
        lines += [f"*Model version {model['version']}. Generated from `model.yaml` — do not edit by hand.*", ""]

    ctxs = model.get("contexts", [])
    if ctxs:
        lines += ["## Bounded contexts", ""]
        for c in ctxs:
            lines.append(f"- **{c.get('name')}** — {str(c.get('description', '')).strip()}")
        lines.append("")



    ents_for_map = model.get("entities", [])
    if ctxs and ents_for_map:
        entity_ctx = {e.get("name"): e.get("context") for e in ents_for_map}
        edges = {}
        for e in ents_for_map:
            for r in e.get("relationships", []) or []:
                a, b = e.get("context"), entity_ctx.get(r.get("to"))
                if a and b and a != b:
                    edges.setdefault((a, b), set()).add(r.get("kind", "related"))
        if edges:
            lines += ["## Context map", "",
                      "Arrows point from the context that holds the reference "
                      "to the context it references.", "",
                      "```mermaid", "flowchart LR"]
            for c in ctxs:
                name = c.get("name")
                lines.append(f'    {mermaid_id(name)}["{name}"]')
            for (a, b), kinds in sorted(edges.items()):
                label = ", ".join(sorted(kinds))
                lines.append(f"    {mermaid_id(a)} -->|{label}| {mermaid_id(b)}")
            lines += ["```", ""]

    open_amb = [a for a in model.get("ambiguities", []) or [] if a.get("status", "open") == "open"]
    if open_amb:
        lines += ["## ⚠ Open ambiguities", "",
                  "These terms are contested or vague. Resolve them before writing specs that depend on them.", ""]
        for a in open_amb:
            lines.append(f"- **{a.get('phrase')}** — {str(a.get('issue', '')).strip()}")
            if a.get("options"):
                lines.append(f"  - Options: {', '.join(a['options'])}")
            if a.get("recommendation"):
                lines.append(f"  - Recommendation: {str(a['recommendation']).strip()}")
        lines.append("")

    ents = model.get("entities", [])
    rels = [(e.get("name"), r) for e in ents for r in e.get("relationships", []) or []]
    if ents:
        lines += ["## Entity relationships", "", "```mermaid", "erDiagram"]
        for e in ents:
            lines.append(f"    {mermaid_id(e.get('name'))} {{}}")
        card = {"has_one": "||--||", "has_many": "||--o{", "belongs_to": "}o--||", "references": "}o..o{"}
        for src, r in rels:
            arrow = card.get(r.get("kind"), "||--||")
            label = r.get("kind", "related")
            lines.append(f"    {mermaid_id(src)} {arrow} {mermaid_id(r.get('to'))} : {label}")
        lines += ["```", ""]

    lines += ["## Contents", "",
              "- [Glossary](glossary.md)",
              "- [Entities](entities.md)",
              "- [Business rules](rules.md)"]
    if model.get("events"):
        lines.append("- [Domain events](events.md)")
    lines.append("")
    return "\n".join(lines)


def gen_glossary(model):
    lines = [f"# Glossary — {model.get('project', 'Domain')}", "",
             "*Generated from `model.yaml` — do not edit by hand.*", ""]
    combined = model.get("combined_glossary")
    if combined:
        lines += [f"The [combined Unfold glossary]({combined}) lists every term of every model once, "
                  "with its owner and the words it must not be confused with.", ""]
    terms = sorted(model.get("terms", []), key=lambda t: str(t.get("name", "")).lower())
    retired = model.get("retired_terms", []) or []
    local = {t.get("name") for t in terms} | {r.get("name") for r in retired}

    def link(name):
        if name in local:
            return f"[{name}](#{slug(name)})"
        if combined:
            return f"[{name}]({combined}#{slug(name)})"
        return name

    if not terms:
        lines += ["_No terms defined yet._", ""]
    for t in terms:
        name = t.get("name")
        lines.append(f"## {name}")
        if t.get("context"):
            owner = f" · Owner: {t['owner'].capitalize()}" if t.get("owner") else ""
            lines.append(f"*Context: {t['context']}{owner}*")
        lines += ["", str(t.get("definition", "")).strip(), ""]
        if t.get("synonyms"):
            lines.append(f"**Also known as:** {', '.join(t['synonyms'])}  ")
        if t.get("avoid"):
            lines.append(f"**Do not use:** {', '.join(t['avoid'])}  ")
        for entry in t.get("not_to_be_confused_with", []) or []:
            lines.append(f"**Not to be confused with** {link(entry['term'])}: {cell(entry['note'])}  ")
        if t.get("examples"):
            lines.append("**Examples:** " + "; ".join(t["examples"]) + "  ")
        if t.get("see_also"):
            links = ", ".join(link(s) for s in t["see_also"])
            lines.append(f"**See also:** {links}  ")
        lines.append("")

    if retired:
        lines += ["---", "", "## Retired terms", "",
                  "Do not use these names as terms.", ""]
        for r in retired:
            lines += [f"### {r['name']}", f"*Use instead: {', '.join(link(u) for u in r['use'])}*", "",
                      str(r.get("because", "")).strip(), ""]

    imports = sorted({n for imp in model.get("imports", []) or [] for n in imp.get("terms", []) or []}, key=str.lower)
    if imports:
        lines += ["## Terms owned by other models", "",
                  "This model uses these terms with their owners' meaning: "
                  + ", ".join(link(n) for n in imports) + ".", ""]

    refs = model.get("references", []) or []
    if refs:
        lines += ["## Decisions cited", ""]
        for r in refs:
            lines.append(f"- [{r['label']}]({r['path']}): {str(r.get('note', '')).strip()}")
        lines.append("")

    dialogues = model.get("dialogues", []) or []
    if dialogues:
        lines += ["---", "", "## Example dialogues", "",
                  "Short exchanges showing the terms used precisely at concept boundaries.", ""]
        for d in dialogues:
            lines.append(f"### {d.get('title', 'Dialogue')}")
            if d.get("context"):
                lines.append(f"*Context: {d['context']}*")
            lines.append("")
            for ln in d.get("lines", []) or []:
                lines.append(f"> **{ln.get('speaker', '?')}:** {str(ln.get('text', '')).strip()}")
            lines.append("")

    ambs = model.get("ambiguities", []) or []
    open_amb = [a for a in ambs if a.get("status", "open") == "open"]
    resolved = [a for a in ambs if a.get("status") == "resolved"]
    if open_amb:
        lines += ["---", "", "## ⚠ Flagged ambiguities", ""]
        for a in open_amb:
            lines.append(f"### {a.get('phrase')}")
            lines += ["", str(a.get("issue", "")).strip(), ""]
            if a.get("options"):
                lines.append(f"**Options:** {', '.join(a['options'])}  ")
            if a.get("recommendation"):
                lines.append(f"**Recommendation:** {str(a['recommendation']).strip()}  ")
            lines.append("")
    if resolved:
        lines += ["## Resolved ambiguities", ""]
        for a in resolved:
            lines.append(f"- **{a.get('phrase')}** — {str(a.get('resolution', '')).strip()}")
        lines.append("")
    return "\n".join(lines)


def gen_entities(model):
    lines = [f"# Entities — {model.get('project', 'Domain')}", "",
             "*Generated from `model.yaml` — do not edit by hand.*", ""]
    ents = model.get("entities", [])
    if not ents:
        return "\n".join(lines + ["_No entities defined yet._", ""])
    for e in ents:
        lines.append(f"## {e.get('name')}")
        if e.get("context"):
            lines.append(f"*Context: {e['context']}*")
        lines.append("")
        if e.get("description"):
            lines += [str(e["description"]).strip(), ""]

        attrs = e.get("attributes", []) or []
        if attrs:
            lines += ["| Attribute | Type | Required | Description |",
                      "|---|---|---|---|"]
            for a in attrs:
                req = "yes" if a.get("required") else ""
                lines.append(f"| `{a.get('name')}` | `{a.get('type', '')}` | {req} | {str(a.get('description', '')).strip()} |")
            lines.append("")

        rels = e.get("relationships", []) or []
        if rels:
            lines.append("**Relationships**")
            for r in rels:
                desc = str(r.get("description", "")).strip()
                lines.append(f"- {r.get('kind', 'related to')} **{r.get('to')}**" + (f" — {desc}" if desc else ""))
            lines.append("")

        states = e.get("states", []) or []
        if states:
            lines += ["**Lifecycle**", "", "```mermaid", "stateDiagram-v2"]
            for s in states:
                frm = "[*]" if s.get("from") == "[start]" else mermaid_id(s.get("from"))
                to = "[*]" if s.get("to") == "[end]" else mermaid_id(s.get("to"))
                trigger = str(s.get("trigger", "")).strip()
                lines.append(f"    {frm} --> {to}" + (f" : {trigger}" if trigger else ""))
            lines += ["```", ""]
    return "\n".join(lines)


def gen_rules(model):
    lines = [f"# Business Rules — {model.get('project', 'Domain')}", "",
             "*Generated from `model.yaml` — do not edit by hand. Cite rules by id in specs.*", ""]
    rules = model.get("rules", [])
    if not rules:
        return "\n".join(lines + ["_No rules defined yet._", ""])
    groups = {}
    for r in rules:
        targets = r.get("applies_to") or ["General"]
        groups.setdefault(targets[0], []).append(r)
    for target in sorted(groups):
        lines += [f"## {target}", ""]
        for r in groups[target]:
            lines.append(f"### {r.get('id')}")
            if r.get("context"):
                lines.append(f"*Context: {r['context']}*")
            lines += ["", str(r.get("statement", "")).strip(), ""]
            if r.get("rationale"):
                lines += [f"**Why:** {str(r['rationale']).strip()}", ""]
            targets = r.get("applies_to") or []
            if len(targets) > 1:
                lines += [f"**Also applies to:** {', '.join(targets[1:])}", ""]
    return "\n".join(lines)


def gen_events(model):
    lines = [f"# Domain Events — {model.get('project', 'Domain')}", "",
             "*Generated from `model.yaml` — do not edit by hand.*", ""]
    for ev in model.get("events", []):
        lines.append(f"## {ev.get('name')}")
        lines.append("")
        if ev.get("description"):
            lines += [str(ev["description"]).strip(), ""]
        if ev.get("entity"):
            lines.append(f"**Concerns:** {ev['entity']}  ")
        if ev.get("triggers"):
            lines.append(f"**Triggers:** {str(ev['triggers']).strip()}  ")
        lines.append("")
    return "\n".join(lines)


def gen_combined(models, path):
    out_dir = Path(path).parent

    def rel(target):
        return Path(os.path.relpath(target, out_dir)).as_posix()

    entries, retired, errors = [], [], []
    for m in models:
        for t in m.get("terms", []):
            entries.append((t, m))
        for r in m.get("retired_terms", []) or []:
            retired.append((r, m))

    names = {}
    for t, m in entries + retired:
        key = str(t["name"]).lower()
        if key in names:
            errors.append(f"{t['name']!r} is defined by {names[key]} and {m['_path']}")
        names[key] = m["_path"]
    for t, m in entries:
        for syn in t.get("synonyms", []) or []:
            if str(syn).lower() in names:
                errors.append(f"synonym {syn!r} of {t['name']!r} names another term")
        refs = list(t.get("see_also", []) or []) + [e["term"] for e in t.get("not_to_be_confused_with", []) or []]
        for ref in refs:
            if str(ref).lower() not in names:
                errors.append(f"{t['name']!r} refers to {ref!r}, which no model defines")
    for r, m in retired:
        for use in r.get("use", []):
            if str(use).lower() not in names:
                errors.append(f"retired {r['name']!r} points to {use!r}, which no model defines")
    if errors:
        sys.exit("Combined glossary conflicts:\n" + "\n".join(f"  - {e}" for e in errors))

    canonical = {str(t["name"]).lower(): t["name"] for t, _ in entries + retired}
    owner = {str(t["name"]).lower(): owner_of(t, m) for t, m in entries + retired}

    def link(name):
        return f"[{canonical[str(name).lower()]}](#{slug(name)})"

    def who(name):
        return owner[str(name).lower()].capitalize()

    pairs, seen = [], set()
    for t, m in sorted(entries, key=lambda e: str(e[0]["name"]).lower()):
        found = [(e["term"], cell(e["note"])) for e in t.get("not_to_be_confused_with", []) or []]
        for word in t.get("avoid", []) or []:
            bare = re.sub(r"\s*\(.*\)$", "", str(word)).strip().lower()
            if bare in canonical and bare != str(t["name"]).lower():
                found.append((canonical[bare], f'Do not call a {t["name"]} "{word}".'))
        for other, note in found:
            key = frozenset((str(t["name"]).lower(), str(other).lower()))
            if key not in seen:
                seen.add(key)
                pairs.append((t["name"], other, note))

    sources = ", ".join(f"[{m.get('project')}]({rel(m['_path'])})" for m in models)
    lines = ["# Glossary", "",
             f"*Generated from the {sources} domain models by `mise run domain`. Do not edit by hand; "
             "change a `model.yaml` and regenerate.*", "",
             "Each term appears once and names the context and application that own it. "
             "Ploeg owns the execution terms, and Vloer uses them with Ploeg's meaning.", ""]
    refs = {}
    for m in models:
        for r in m.get("references", []) or []:
            refs.setdefault((m["_path"].parent / r["path"]).resolve(), r)
    for target, r in refs.items():
        lines += [f"[{r['label']}]({rel(target)}): {str(r.get('note', '')).strip()}", ""]

    lines += ["## Words with more than one meaning", "",
              "| Word | Owner | Not to be confused with | Owner | Difference |",
              "|---|---|---|---|---|"]
    for word, other, note in pairs:
        lines.append(f"| {link(word)} | {who(word)} | {link(other)} | {who(other)} | {note} |")
    lines.append("")

    lines += ["## Terms", "",
              "| Term | Meaning | Context | Owner | Related |",
              "|---|---|---|---|---|"]
    for t, m in sorted(entries, key=lambda e: str(e[0]["name"]).lower()):
        related = []
        if t.get("synonyms"):
            related.append("Also: " + ", ".join(t["synonyms"]))
        if t.get("avoid"):
            related.append("Do not use: " + ", ".join(t["avoid"]))
        for e in t.get("not_to_be_confused_with", []) or []:
            related.append(f"Not to be confused with {link(e['term'])}")
        if t.get("see_also"):
            related.append("See also: " + ", ".join(link(s) for s in t["see_also"]))
        lines.append(f'| <a id="{slug(t["name"])}"></a>**{cell(t["name"])}** | {cell(t.get("definition", ""))} '
                     f'| {cell(t.get("context", ""))} | {who(t["name"])} | {cell("<br>".join(related))} |')
    lines.append("")

    if retired:
        lines += ["## Retired terms", "",
                  "| Term | Use instead | Why |", "|---|---|---|"]
        for r, m in retired:
            uses = ", ".join(link(u) for u in r["use"])
            lines.append(f'| <a id="{slug(r["name"])}"></a>**{cell(r["name"])}** | {uses} | {cell(r.get("because", ""))} |')
        lines.append("")
    return "\n".join(lines)


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("model", nargs="+", help="Path to domain model YAML (e.g. docs/domain/model.yaml)")
    ap.add_argument("-o", "--out", default="docs/domain", help="Output directory (default: docs/domain)")
    ap.add_argument("--glossary", help="Write one combined glossary of every model to this path")
    ap.add_argument("--stdout", action="store_true", help="With --glossary, print instead of writing")
    args = ap.parse_args()

    if args.glossary:
        content = front_matter("unfold") + gen_combined([load(p) for p in args.model], args.glossary)
        if args.stdout:
            sys.stdout.write(content)
        else:
            Path(args.glossary).parent.mkdir(parents=True, exist_ok=True)
            Path(args.glossary).write_text(content)
            print(f"wrote {args.glossary}")
        return
    if len(args.model) != 1:
        ap.error("give one model, or use --glossary to combine several")

    model = load(args.model[0])
    if "project" not in model:
        warn("model has no 'project' field")

    validate(model)

    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    files = {
        "overview.md": gen_overview(model),
        "glossary.md": gen_glossary(model),
        "entities.md": gen_entities(model),
        "rules.md": gen_rules(model),
    }
    if model.get("events"):
        files["events.md"] = gen_events(model)
    owner = str(model.get("owner") or model.get("project", "unfold")).lower()
    for name, content in files.items():
        (out / name).write_text(front_matter(owner) + content)
        print(f"wrote {out / name}")


if __name__ == "__main__":
    main()
